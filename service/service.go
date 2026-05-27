// Copyright 2026 HAProxy Technologies LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package service manages individual process lifecycles including start, stop, signal, and restart.
package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/haproxytech/gopherd/internal/backoff"
	"github.com/haproxytech/gopherd/internal/cpu"
	"github.com/haproxytech/gopherd/internal/logger"
	"github.com/haproxytech/gopherd/internal/memory"
	"github.com/haproxytech/gopherd/internal/sdnotify"
)

// ExitAction defines what to do when a process exits.
type ExitAction string

// Exit action constants.
const (
	ActionRestart         ExitAction = "restart"
	ActionShutdown        ExitAction = "shutdown"
	ActionSuccessShutdown ExitAction = "success-shutdown"
	ActionFailureShutdown ExitAction = "failure-shutdown"
	ActionIgnore          ExitAction = "ignore"
)

// DefaultKillDelay is the default grace period before sending SIGKILL.
const DefaultKillDelay = 5 * time.Second

// ParseExitAction parses an exit action string, returning the default if empty.
// An unknown action returns the default together with a non-nil error. This
// lets callers recover (e.g. reload() logging the error and reverting) rather
// than crashing PID 1, which a log.Fatalf from library code would do.
func ParseExitAction(s string, defaultAction ExitAction) (ExitAction, error) {
	switch ExitAction(s) {
	case ActionRestart, ActionShutdown, ActionSuccessShutdown, ActionFailureShutdown, ActionIgnore:
		return ExitAction(s), nil
	case "":
		return defaultAction, nil
	default:
		return defaultAction, fmt.Errorf("unknown exit action: %q", s)
	}
}

// ValidateExitAction returns an error if s is not a recognised exit action.
// Empty string is accepted (callers substitute a default).
func ValidateExitAction(s string) error {
	switch ExitAction(s) {
	case ActionRestart, ActionShutdown, ActionSuccessShutdown, ActionFailureShutdown, ActionIgnore, "":
		return nil
	default:
		return fmt.Errorf("unknown exit action %q (valid: restart, shutdown, success-shutdown, failure-shutdown, ignore)", s)
	}
}

// Process holds the configuration for a single process.
type Process struct {
	UserID         *int
	GroupID        *int
	Environment    map[string]string
	OnCheckFailure map[string]string
	PassEnv        *bool
	Name           string
	Command        string
	WorkingDir     string
	User           string
	Group          string
	Startup        string
	StopSignal     string
	KillDelay      string
	OnSuccess      string
	OnFailure      string
	BackoffDelay   string
	BackoffLimit   string
	ReadyCheck     string
	ReadyTimeout   string
	StartupTimeout string
	DotEnv         string
	Prefix         string
	// SDNotifyTimeout is the maximum duration to wait for a READY=1 datagram
	// on $NOTIFY_SOCKET after the service has started. Empty = 60s default.
	// Only meaningful when SDNotify is true.
	SDNotifyTimeout string
	// ParentDeathSignal is the signal name (e.g. "SIGTERM", "SIGKILL") that
	// the kernel will deliver to the child when its parent thread dies. Set
	// via prctl(PR_SET_PDEATHSIG) after fork, before exec. Empty = unset.
	// Linux-only: silently ignored on non-Linux builds.
	ParentDeathSignal string
	// ExitCodeMap remaps observed child exit codes before they feed into
	// OnSuccess/OnFailure dispatch and before they become gopherd's own
	// exit code. Typical use: neutralise SIGTERM→143 and SIGKILL→137 so a
	// cleanly-stopped service is not reported as a failure. A nil or empty
	// map means "pass the code through unchanged".
	ExitCodeMap map[int]int
	// SignalRewrite opts this service into signal forwarding and optionally
	// rewrites the signal on the way to the child. Keys and values are
	// signal names ("SIGUSR1", "USR1", "HUP", ...). When nil/empty, gopherd
	// does NOT forward arbitrary received signals to the child; forwarding
	// is strictly opt-in. Does not affect the shutdown/reload signal paths
	// — gopherd's own reactions to SIGTERM/SIGINT/SIGHUP still run.
	SignalRewrite map[string]string
	Args          []string
	After         []string
	Before        []string
	Requires      []string
	// RemoveEnv lists env keys to delete from the child's final environment
	// after merging OS env (if pass-env is true), dotenv, and per-process
	// environment. Used to drop shared dotenv keys that one service must
	// not see, or to strip specific OS env vars when opting in to pass-env
	// for other reasons.
	RemoveEnv         []string
	BackoffFactor     float64
	UseEntrypointArgs bool
	// SDNotify enables the sd_notify-compatible readiness protocol: gopherd
	// allocates a per-service abstract unix datagram socket, exposes it via
	// $NOTIFY_SOCKET in the child env, and blocks the start of dependents
	// until the child writes "READY=1" to that socket.
	SDNotify bool
}

// Service wraps a Process config with runtime state for lifecycle management.
type Service struct {
	startedAt      time.Time
	Backoff        *backoff.Backoff
	Requires       map[string]bool       // hard dependencies
	OnCheckFailure map[string]ExitAction // check name -> action

	Stdout *logger.PrefixWriter
	Stderr *logger.PrefixWriter

	cmd       *exec.Cmd
	killTimer *time.Timer // deferred SIGKILL; cancelled on exit to prevent PID reuse race
	done      chan struct{}

	// sdNotifyListener owns the abstract unix datagram socket for sd_notify
	// readiness signalling when Proc.SDNotify is set. Created in Start() and
	// closed in MarkExited(); nil otherwise. Guarded by svc.mu.
	sdNotifyListener *sdnotify.Listener

	Name      string
	OnSuccess ExitAction
	OnFailure ExitAction

	Proc Process

	stopSignal syscall.Signal
	killDelay  time.Duration
	// Pid is stored atomically so control-socket callbacks can read it
	// without holding svc.mu, while Start() writes it under svc.mu.
	Pid atomic.Int64

	mu      sync.Mutex
	running atomic.Bool
	stopped atomic.Bool // true if Stop() was called (we initiated the exit)
	Enabled bool
	Oneshot bool
}

// New creates a new Service from a Process config.
// globalPrefix is the top-level prefix setting; per-process Prefix overrides it.
// Returns an error rather than fatal-logging so a malformed reload config does
// not crash PID 1.
func New(p Process, globalPrefix string) (*Service, error) {
	name := p.Name
	if name == "" {
		name = p.Command
	}

	enabled := p.Startup != "disabled"
	oneshot := p.Startup == "oneshot"

	stopSig, err := ParseSignal(p.StopSignal)
	if err != nil {
		return nil, fmt.Errorf("process %s: %w", name, err)
	}

	killDelay := DefaultKillDelay
	if p.KillDelay != "" {
		killDelay, err = time.ParseDuration(p.KillDelay)
		if err != nil {
			return nil, fmt.Errorf("process %s: invalid kill-delay %q: %w", name, p.KillDelay, err)
		}
	}

	var backoffDelay time.Duration
	if p.BackoffDelay != "" {
		backoffDelay, err = time.ParseDuration(p.BackoffDelay)
		if err != nil {
			return nil, fmt.Errorf("process %s: invalid backoff-delay %q: %w", name, p.BackoffDelay, err)
		}
	}

	var backoffLimit time.Duration
	if p.BackoffLimit != "" {
		backoffLimit, err = time.ParseDuration(p.BackoffLimit)
		if err != nil {
			return nil, fmt.Errorf("process %s: invalid backoff-limit %q: %w", name, p.BackoffLimit, err)
		}
	}

	reqSet := make(map[string]bool)
	for _, r := range p.Requires {
		reqSet[r] = true
	}

	checkFailMap := make(map[string]ExitAction)
	for checkName, actionStr := range p.OnCheckFailure {
		act, err := ParseExitAction(actionStr, ActionRestart)
		if err != nil {
			return nil, fmt.Errorf("process %s on-check-failure[%s]: %w", name, checkName, err)
		}
		checkFailMap[checkName] = act
	}

	onSuccess, err := ParseExitAction(p.OnSuccess, ActionShutdown)
	if err != nil {
		return nil, fmt.Errorf("process %s on-success: %w", name, err)
	}
	onFailure, err := ParseExitAction(p.OnFailure, ActionShutdown)
	if err != nil {
		return nil, fmt.Errorf("process %s on-failure: %w", name, err)
	}

	prefix := globalPrefix
	if p.Prefix != "" {
		prefix = p.Prefix
	}
	if prefix == "" {
		prefix = logger.DefaultPrefix
	}

	return &Service{
		Proc:           p,
		Name:           name,
		Enabled:        enabled,
		Oneshot:        oneshot,
		stopSignal:     stopSig,
		killDelay:      killDelay,
		OnSuccess:      onSuccess,
		OnFailure:      onFailure,
		Backoff:        backoff.New(backoffDelay, p.BackoffFactor, backoffLimit),
		Requires:       reqSet,
		OnCheckFailure: checkFailMap,
		Stdout:         logger.NewPrefixWriter(os.Stdout, name, prefix),
		Stderr:         logger.NewPrefixWriter(os.Stderr, name, prefix),
	}, nil
}

// stripDotEnvComment removes an unquoted inline comment from a .env value.
// Inline comments start with " #" (space followed by hash) outside of any
// quoted region. Single and double quotes protect hash characters, matching
// the same convention used by Docker's --env-file and most .env parsers.
//
// Examples:
//
//	value # comment      → value
//	"value # not"        → "value # not"   (hash inside double quotes)
//	'value # not'        → 'value # not'   (hash inside single quotes)
//	#tag                 → #tag            (no preceding space, kept literal)
func stripDotEnvComment(v string) string {
	inSingle := false
	inDouble := false
	for i := 0; i < len(v); i++ {
		switch {
		case v[i] == '\\' && inDouble && i+1 < len(v):
			i++ // skip escaped character inside double-quoted string
		case v[i] == '\'' && !inDouble:
			inSingle = !inSingle
		case v[i] == '"' && !inSingle:
			inDouble = !inDouble
		case v[i] == '#' && !inSingle && !inDouble && i > 0 && v[i-1] == ' ':
			return strings.TrimRight(v[:i], " ")
		}
	}
	return v
}

// dotenvUnescapeDouble processes backslash escape sequences in a .env
// double-quoted value. Handles the common sequences: \n, \t, \r, \\, \".
func dotenvUnescapeDouble(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i+1])
		}
		i += 2
	}
	return b.String()
}

// isValidEnvKey reports whether k is a valid POSIX environment variable
// name: [A-Za-z_][A-Za-z0-9_]*. Rejecting malformed keys in dotenv files
// prevents shell-unsafe values from leaking into cmd.Env.
func isValidEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r == '_':
			// always allowed
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			// always allowed
		case i > 0 && r >= '0' && r <= '9':
			// digits only after the first character
		default:
			return false
		}
	}
	return true
}

// checkAncestorsNotSymlinked walks every ancestor of path from "/" down to
// the immediate parent and rejects any that is a symlink or not a directory.
// path must be absolute and already filepath.Clean'd. Duplicated deliberately
// from logger/ so each package stays self-contained.
func checkAncestorsNotSymlinked(path string) error {
	parent := filepath.Dir(path)
	cur := "/"
	rel := strings.TrimPrefix(parent, "/")
	if rel == "" {
		return nil
	}
	for comp := range strings.SplitSeq(rel, "/") {
		cur = filepath.Join(cur, comp)
		info, err := os.Lstat(cur)
		if err != nil {
			return fmt.Errorf("ancestor %s: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("ancestor %s is a symlink", cur)
		}
		if !info.IsDir() {
			return fmt.Errorf("ancestor %s is not a directory", cur)
		}
	}
	return nil
}

// maxDotEnvSize caps how many bytes parseDotEnv will consume from a
// dotenv file. Real dotenv files are typically under 10 KiB; 1 MiB
// leaves plenty of headroom while preventing an operator-pointed or
// swapped-out huge file from OOM-killing PID 1.
const maxDotEnvSize = 1 << 20

// parseDotEnv reads a dotenv file and returns key-value pairs.
// Lines are in the format KEY=value. Empty lines and lines starting with # are skipped.
// Uses O_NOFOLLOW to reject symlinks atomically on the leaf and walks the
// ancestor directories with Lstat to reject any symlink above the leaf. Without
// the ancestor walk, an attacker with write access to a directory on the
// dotenv path could swap an intermediate component for a symlink and redirect
// the open to a file of their choice; O_NOFOLLOW only guards the final component.
func parseDotEnv(path string) (map[string]string, error) {
	// Resolve to an absolute path so the walker has a stable starting point
	// even when the operator configured a relative dotenv path.
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("dotenv %s: %w", path, err)
	}
	abs = filepath.Clean(abs)
	if err := checkAncestorsNotSymlinked(abs); err != nil {
		return nil, fmt.Errorf("dotenv %s: %w", path, err)
	}
	fd, err := syscall.Open(abs, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, fmt.Errorf("dotenv %s is a symlink; refusing to open", path)
		}
		return nil, fmt.Errorf("dotenv %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("dotenv %s: stat: %w", path, err)
	}
	// Reject non-regular files — a FIFO here would block io.ReadAll forever,
	// matching the guard used for the main config file.
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("dotenv %s is not a regular file (mode %s); refusing to open", path, info.Mode())
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		mode := info.Mode()
		if mode&0o002 != 0 {
			return nil, fmt.Errorf("dotenv %s is world-writable (mode %04o, owner uid=%d); refusing to open", path, mode.Perm(), stat.Uid)
		}
		euid := uint32(os.Geteuid())
		if stat.Uid != 0 && stat.Uid != euid {
			return nil, fmt.Errorf("dotenv %s is owned by uid %d (expected root or uid %d); refusing to open", path, stat.Uid, euid)
		}
		if mode&0o020 != 0 {
			log.Printf("warning: dotenv %s is group-writable (mode %04o, owner uid=%d)", path, mode.Perm(), stat.Uid)
		}
	}
	// Cap the dotenv read so a huge or swapped-out file cannot drive
	// PID 1 into an unbounded allocation. Dotenv files are typically
	// under 10 KiB; 1 MiB is generous while still bounded.
	data, err := io.ReadAll(io.LimitReader(f, maxDotEnvSize+1))
	if err != nil {
		return nil, fmt.Errorf("dotenv %s: %w", path, err)
	}
	if int64(len(data)) > maxDotEnvSize {
		return nil, fmt.Errorf("dotenv %s exceeds %d-byte size cap", path, maxDotEnvSize)
	}
	// Strip UTF-8 BOM if present so the first key is not prefixed with it.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	env := make(map[string]string)
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			// Skip lines like "=value" that produce an empty key; an empty key
			// would create an invalid "=value" entry in cmd.Env (B5).
			continue
		}
		if !isValidEnvKey(k) {
			return nil, fmt.Errorf("dotenv %s: invalid env key %q (must match [A-Za-z_][A-Za-z0-9_]*)", path, k)
		}
		v = strings.TrimSpace(v)
		// Strip unquoted inline comments (e.g. "value # comment" → "value").
		v = stripDotEnvComment(v)
		// Strip matching outer quotes. Double-quoted values have their
		// backslash escape sequences processed (e.g. \n, \t) consistent
		// with how the YAML parser handles double-quoted strings.
		// Single-quoted values are taken literally.
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = dotenvUnescapeDouble(v[1 : len(v)-1])
		} else if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = v[1 : len(v)-1]
		}
		env[k] = v
	}
	return env, nil
}

// buildEnvMap builds a merged environment map from OS env, dotenv file, and per-process overrides.
// Priority (highest last): OS env < dotenv < per-process environment.
// If passEnv is false (the default), the parent's environment is not inherited — only dotenv
// and per-process vars are used. This prevents secrets from leaking to children.
// Set passEnv: true explicitly to opt in to inheritance of gopherd's OS env.
// The returned userKeys set identifies keys set by dotenv or procEnv; only
// values at those keys are eligible for {{...}} template expansion, so
// inherited OS env values that happen to contain "{{" are passed through
// verbatim.
func buildEnvMap(dotenvPath string, procEnv map[string]string, passEnv bool) (map[string]string, map[string]bool, error) {
	env := make(map[string]string)
	if passEnv {
		for _, e := range os.Environ() {
			if k, v, ok := strings.Cut(e, "="); ok && k != "" {
				env[k] = v
			}
		}
	}
	userKeys := make(map[string]bool)
	if dotenvPath != "" {
		dotenv, err := parseDotEnv(dotenvPath)
		if err != nil {
			return nil, nil, err
		}
		for k, v := range dotenv {
			env[k] = v
			userKeys[k] = true
		}
	}
	for k, v := range procEnv {
		env[k] = v
		userKeys[k] = true
	}
	return env, userKeys, nil
}

// templateRe matches {{.VAR_NAME}} and {{.VAR_NAME:-default}} placeholders.
// Submatches: (1) var name, (2) default text (may be empty; capture group is
// absent when no ":-default" suffix is present). The default is matched
// permissively as anything up to the first "}"; "}}" cannot appear inside by
// construction, so nesting is not supported.
var templateRe = regexp.MustCompile(`\{\{\s*\.(\w+)\s*(?::-([^}]*))?\s*\}\}`)

// memRe matches {{mem EXPR}} placeholders for memory expressions.
var memRe = regexp.MustCompile(`\{\{\s*mem\s+(.+?)\s*\}\}`)

// cpuRe matches {{cpu}} and {{cpu EXPR}} placeholders for CPU expressions.
// Bare {{cpu}} (no expression) expands to the available CPU count directly.
// The `cpu` token must be followed by whitespace or `}}` so identifiers like
// `{{cpus 50%}}` or `{{cpu_x}}` do not match and are left as literal text
// instead of producing a confusing "invalid cpu expression" error.
var cpuRe = regexp.MustCompile(`\{\{\s*cpu(?:\s+(.+?))?\s*\}\}`)

// expandTemplates resolves {{.VAR}}, {{.VAR:-default}}, {{mem EXPR}}, and
// {{cpu EXPR}} placeholders in a string slice. Environment lookups use env;
// memory expressions use totalMiB; CPU expressions use totalCPUs. When an env
// var is unset or empty, {{.VAR:-default}} expands to the literal default
// text, while {{.VAR}} expands to "" and emits a warning — a silent empty
// substitution of e.g. a password template has historically caused outages.
//
// Expansion is single-pass: if a variable's value itself contains placeholders
// they are not re-expanded. Variables defined in the environment: block therefore
// cannot reference each other.
//
// Uses FindAllStringSubmatchIndex for a single-pass replacement, avoiding the
// double-regex overhead of ReplaceAllStringFunc + FindStringSubmatch.
func expandTemplates(values []string, env map[string]string, totalMiB int64, totalCPUs int) ([]string, error) {
	out := make([]string, len(values))
	// warned tracks keys that already produced a "not set" warning during this
	// expansion call, so a restart loop with the same misconfigured argv does
	// not flood the log on every iteration.
	warned := make(map[string]struct{})
	for i, s := range values {
		if !strings.Contains(s, "{{") {
			out[i] = s
			continue
		}
		// Expand {{mem EXPR}} placeholders first.
		if locs := memRe.FindAllStringSubmatchIndex(s, -1); locs != nil {
			var b strings.Builder
			prev := 0
			for _, loc := range locs {
				b.WriteString(s[prev:loc[0]])
				mib, err := memory.Eval(s[loc[2]:loc[3]], totalMiB)
				if err != nil {
					return nil, err
				}
				b.WriteString(strconv.FormatInt(mib, 10))
				prev = loc[1]
			}
			b.WriteString(s[prev:])
			s = b.String()
		}
		// Expand {{cpu EXPR}} placeholders.
		if locs := cpuRe.FindAllStringSubmatchIndex(s, -1); locs != nil {
			var b strings.Builder
			prev := 0
			for _, loc := range locs {
				b.WriteString(s[prev:loc[0]])
				// Optional capture: bare {{cpu}} has loc[2] == -1.
				expr := ""
				if loc[2] >= 0 {
					expr = s[loc[2]:loc[3]]
				}
				cpus, err := cpu.Eval(expr, totalCPUs)
				if err != nil {
					return nil, err
				}
				b.WriteString(strconv.Itoa(cpus))
				prev = loc[1]
			}
			b.WriteString(s[prev:])
			s = b.String()
		}
		// Expand {{file "/path"}} placeholders. Runs before {{.VAR}} so file
		// contents containing template-like text (e.g. an example config
		// snippet committed as a secret) are not re-expanded.
		if strings.Contains(s, "{{") && strings.Contains(s, "file") {
			expanded, err := ExpandFileRefs(s)
			if err != nil {
				return nil, err
			}
			s = expanded
		}
		// Expand {{.VAR}} and {{.VAR:-default}} placeholders.
		if locs := templateRe.FindAllStringSubmatchIndex(s, -1); locs != nil {
			var b strings.Builder
			prev := 0
			for _, loc := range locs {
				b.WriteString(s[prev:loc[0]])
				name := s[loc[2]:loc[3]]
				val, ok := env[name]
				hasDefault := loc[4] >= 0
				if (!ok || val == "") && hasDefault {
					val = s[loc[4]:loc[5]]
				} else if !ok {
					// Empty substitution of a missing variable can silently
					// corrupt arguments (e.g. an empty password flag). Warn
					// once per missing key per call so a service restart loop
					// does not flood the log with the same warning line.
					if _, already := warned[name]; !already {
						warned[name] = struct{}{}
						log.Printf("warning: template variable {{.%s}} is not set in environment; expanding to empty string", name)
					}
				}
				b.WriteString(val)
				prev = loc[1]
			}
			b.WriteString(s[prev:])
			s = b.String()
		}
		out[i] = s
	}
	return out, nil
}

// Start launches the process. Returns the PID on success.
func (s *Service) Start() (pid int, err error) {
	// Build environment, resolve credentials, and expand templates before
	// acquiring the lock to minimize time spent in the critical section.
	// The default when PassEnv is unset (nil) is FALSE: gopherd does not
	// forward its own OS env to children unless the operator opts in with
	// pass-env: true. This prevents operator secrets in gopherd's env
	// from silently leaking into every child.
	passEnv := s.Proc.PassEnv != nil && *s.Proc.PassEnv
	env, userKeys, err := buildEnvMap(s.Proc.DotEnv, s.Proc.Environment, passEnv)
	if err != nil {
		return 0, err
	}
	// Drop any keys the operator listed in remove-env. Runs after the OS /
	// dotenv / procEnv merge so a shared dotenv key can be suppressed for
	// one service without modifying the dotenv file.
	for _, k := range s.Proc.RemoveEnv {
		delete(env, k)
		delete(userKeys, k)
	}

	// Allocate the sd_notify listener before exec so NOTIFY_SOCKET is set
	// in the child env. A pre-existing listener (restart path) is replaced
	// because the old socket may still hold stale READY state from the
	// prior run — dependents of a restarting service must wait for the new
	// instance to re-notify readiness on its own terms.
	if s.Proc.SDNotify {
		if s.sdNotifyListener != nil {
			_ = s.sdNotifyListener.Close()
			s.sdNotifyListener = nil
		}
		l, lerr := sdnotify.Listen(s.Name, os.Getpid())
		if lerr != nil {
			return 0, fmt.Errorf("sd_notify: %w", lerr)
		}
		s.sdNotifyListener = l
		env["NOTIFY_SOCKET"] = l.Path()
		// Not marked as a userKey: the value is a literal socket path, never
		// contains "{{", and must not be subject to template expansion.
		// Release the listener on any subsequent error so the abstract
		// socket name is not leaked; MarkExited handles the success path.
		defer func() {
			if err != nil && s.sdNotifyListener != nil {
				_ = s.sdNotifyListener.Close()
				s.sdNotifyListener = nil
			}
		}()
	}

	totalMiB, _ := memory.Available()
	totalCPUs := cpu.Available()

	args, err := expandTemplates(s.Proc.Args, env, totalMiB, totalCPUs)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(s.Proc.Command, args...)
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	cmd.Stdin = nil // each child gets /dev/null as stdin (exec.Cmd default)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Parent-death signal: the kernel delivers sig to the child when its
	// parent thread terminates, so children do not linger after gopherd is
	// killed abruptly (e.g. SIGKILL). Validated at config load, so we
	// surface any residual parse error here as an internal bug rather than
	// a user-facing one. Non-Linux builds silently skip via setPdeathsig.
	if s.Proc.ParentDeathSignal != "" {
		pdeathSig, perr := ParseSignal(s.Proc.ParentDeathSignal)
		if perr != nil {
			return 0, fmt.Errorf("parent-death-signal: %w", perr)
		}
		setPdeathsig(cmd.SysProcAttr, pdeathSig)
	}

	if s.Proc.WorkingDir != "" {
		cmd.Dir = s.Proc.WorkingDir
	}

	// Set the child's environment explicitly when pass-env is off (default),
	// dotenv / per-process vars are supplied, remove-env lists any keys
	// to strip, or a notify listener injected NOTIFY_SOCKET. When cmd.Env
	// is nil, Go inherits the parent env — only safe when pass-env is on
	// and no other env configuration applies.
	if !passEnv || s.Proc.DotEnv != "" || len(s.Proc.Environment) > 0 || len(s.Proc.RemoveEnv) > 0 || s.Proc.SDNotify {
		// Expand {{mem}}, {{cpu}}, and {{.VAR}} only in user-defined env
		// values (dotenv + procEnv). Inherited OS env values are passed
		// through verbatim so that incidental "{{" sequences (e.g. a CI
		// variable containing template-like text from a commit message)
		// do not trigger expansion failures.
		envVals := make([]string, 0, len(env))
		envKeys := make([]string, 0, len(env))
		userVals := make([]string, 0, len(userKeys))
		userIdx := make([]int, 0, len(userKeys))
		for k, v := range env {
			if userKeys[k] && strings.Contains(v, "{{") {
				userIdx = append(userIdx, len(envKeys))
				userVals = append(userVals, v)
			}
			envKeys = append(envKeys, k)
			envVals = append(envVals, v)
		}
		expanded, err := expandTemplates(userVals, env, totalMiB, totalCPUs)
		if err != nil {
			return 0, err
		}
		for i, idx := range userIdx {
			envVals[idx] = expanded[i]
		}
		// Build "key=value" strings reusing a scratch buffer to avoid one
		// make-per-entry. kvBuf is reallocated only when the next entry does
		// not fit in the existing capacity; otherwise the same backing array
		// is reused and string(kvBuf) copies just the used portion.
		cmd.Env = make([]string, len(env))
		var kvBuf []byte
		for i, k := range envKeys {
			need := len(k) + 1 + len(envVals[i])
			if cap(kvBuf) < need {
				kvBuf = make([]byte, 0, need)
			}
			kvBuf = append(kvBuf[:0], k...)
			kvBuf = append(kvBuf, '=')
			kvBuf = append(kvBuf, envVals[i]...)
			cmd.Env[i] = string(kvBuf)
		}
	}

	cred, err := ResolveCredential(s.Proc.User, s.Proc.Group, s.Proc.UserID, s.Proc.GroupID)
	if err != nil {
		return 0, err
	}
	if cred != nil {
		cmd.SysProcAttr.Credential = cred
	}

	// Lock only for the fork/exec and state update.
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return 0, err
	}

	s.cmd = cmd
	s.Pid.Store(int64(cmd.Process.Pid))
	s.done = make(chan struct{})
	s.running.Store(true)
	s.stopped.Store(false)
	s.startedAt = time.Now()
	return cmd.Process.Pid, nil
}

// Stop sends the configured stop signal to the process group and schedules
// SIGKILL after kill-delay. Signaling the group (negative PID) ensures that
// children forked by the service also receive the signal.
// The stopped flag is set so the reap loop knows this was an intentional exit.
// The deferred SIGKILL timer is cancelled by MarkExited to prevent sending
// SIGKILL to a recycled PID.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running.Load() || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	s.stopped.Store(true)
	pid := int(s.Pid.Load())
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, s.stopSignal)
	if s.killDelay > 0 {
		// Cancel any previously scheduled SIGKILL before creating a new one.
		// Without this, a second Stop() call (e.g. from a concurrent control
		// socket client and a check-failure handler) would overwrite s.killTimer
		// leaving the first timer unreachable and unable to be cancelled by
		// MarkExited, risking a SIGKILL to a recycled PID.
		if s.killTimer != nil {
			s.killTimer.Stop()
		}
		kpid := int(s.Pid.Load())
		s.killTimer = time.AfterFunc(s.killDelay, func() {
			if kpid <= 0 {
				return
			}
			// MarkExited calls killTimer.Stop() under s.mu, but Stop()
			// returns false and does not wait if the callback is already
			// running. Without a lock the callback could observe a
			// still-running service, then race MarkExited and signal a
			// PID that the kernel has since recycled.
			//
			// Hold s.mu across the Kill so the callback is serialized
			// with MarkExited: either MarkExited runs first (we see
			// running=false and return) or the Kill completes before
			// MarkExited can acquire the lock. The caller of MarkExited
			// has already reaped the PID via Wait4 before taking s.mu,
			// so the check-then-kill sequence inside the lock still
			// cannot race with PID recycling for that service.
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.running.Load() || int(s.Pid.Load()) != kpid {
				return
			}
			_ = syscall.Kill(-kpid, syscall.SIGKILL)
		})
	}
}

// Signal sends an arbitrary signal to the entire process group. The service is
// started with Setpgid=true so -pid addresses all processes in the group, not
// just the process leader. This matches the behaviour of Stop().
func (s *Service) Signal(sig os.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running.Load() || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	// Use comma-ok to avoid a panic if sig is not a syscall.Signal (B6).
	sysSig, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	pid := int(s.Pid.Load())
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, sysSig)
}

// MarkExited marks the service as no longer running and returns how long it ran.
// It cancels any pending deferred SIGKILL to prevent sending signals to a
// recycled PID.
//
// Atomically clears s.running and s.Pid BEFORE acquiring s.mu so that
// concurrent Stop, Signal, and killTimer callers — which all re-read these
// atomics after locking s.mu — see the invalidated values and their
// `pid <= 0` / `Pid.Load() != kpid` guards trip. By the time Wait4 has
// returned and the reap loop reaches this function, the kernel has already
// freed the pid for reuse; the atomic stores bound the window in which a
// concurrent signaller could issue syscall.Kill against a just-recycled
// pid. The residual window cannot be closed without pidfd_send_signal,
// which is not in syscall stdlib.
func (s *Service) MarkExited() time.Duration {
	s.running.Store(false)
	s.Pid.Store(0)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		close(s.done)
	}
	if s.killTimer != nil {
		s.killTimer.Stop()
		s.killTimer = nil
	}
	if s.sdNotifyListener != nil {
		_ = s.sdNotifyListener.Close()
		s.sdNotifyListener = nil
	}
	return time.Since(s.startedAt)
}

// WaitSDNotifyReady blocks until the service writes "READY=1" to
// $NOTIFY_SOCKET or ctx is done. Returns an error if the service was not
// started with SDNotify enabled, or if ctx expires first. Safe to call
// from outside the service goroutine; the listener itself is
// concurrency-safe.
func (s *Service) WaitSDNotifyReady(ctx context.Context) error {
	s.mu.Lock()
	l := s.sdNotifyListener
	s.mu.Unlock()
	if l == nil {
		return fmt.Errorf("service %s: sd_notify not enabled", s.Name)
	}
	return l.WaitReady(ctx)
}

// WasStopped returns true if the service exited because we called Stop()
// (as opposed to exiting on its own). This distinguishes intentional signal-death
// from unexpected exits for the purpose of exit code propagation.
func (s *Service) WasStopped() bool {
	return s.stopped.Load()
}

// RemapExitCode applies the service's exit-code-map to the observed code.
// When the map is empty or has no entry for code, the original value is
// returned unchanged. Used by the reap loop before OnSuccess/OnFailure
// dispatch and before the code is propagated as gopherd's own exit.
func (s *Service) RemapExitCode(code int) int {
	if len(s.Proc.ExitCodeMap) == 0 {
		return code
	}
	if mapped, ok := s.Proc.ExitCodeMap[code]; ok {
		return mapped
	}
	return code
}

// RewriteSignal looks up sig in the service's signal-rewrite map and
// returns (rewritten, true) if an entry exists, or (0, false) when the
// service did not opt in to forwarding for this signal. Unrecognised
// target names fall back to the original signal rather than failing
// silently; names are pre-validated at config load so this is defensive.
func (s *Service) RewriteSignal(sig syscall.Signal) (syscall.Signal, bool) {
	if len(s.Proc.SignalRewrite) == 0 {
		return 0, false
	}
	from := SignalName(sig)
	to, ok := s.Proc.SignalRewrite[from]
	if !ok {
		return 0, false
	}
	parsed, err := ParseSignal(to)
	if err != nil {
		log.Printf("%s: signal-rewrite target %q invalid, using %s: %v", s.Name, to, from, err)
		return sig, true
	}
	return parsed, true
}

// SignalName returns the canonical "SIGFOO" name for a syscall.Signal,
// or its numeric form when no name is known. Used by RewriteSignal for
// map lookup and by the yml package to canonicalise signal-rewrite keys.
// Mirrors how ParseSignal accepts both "SIGUSR1" and "USR1".
func SignalName(sig syscall.Signal) string {
	if name, ok := sigNames[sig]; ok {
		return name
	}
	return fmt.Sprintf("%d", int(sig))
}

// IsRunning returns whether the service is currently running.
func (s *Service) IsRunning() bool {
	return s.running.Load()
}

// Done returns a channel that is closed when the service exits.
// Callers can select on this instead of polling IsRunning().
func (s *Service) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		// Service was never started or already exited — return a closed channel.
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return s.done
}
