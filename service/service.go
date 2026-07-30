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
// An unknown action returns the default plus a non-nil error so callers can
// recover (e.g. revert a reload) rather than crashing PID 1.
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
	// LogCapture pipes child stdout/stderr through gopherd (enables prefixes,
	// the logs command, and log-targets). nil/false: the child inherits
	// gopherd's stdout/stderr FDs and gopherd never touches its output.
	LogCapture     *bool
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
	// SDNotifyTimeout is the max wait for a READY=1 datagram on $NOTIFY_SOCKET
	// after start. Empty = 60s default. Only meaningful when SDNotify is true.
	SDNotifyTimeout string
	// ParentDeathSignal is the signal name the kernel delivers to the child
	// when its parent thread dies (via PR_SET_PDEATHSIG, set after fork before
	// exec). Empty = unset. Linux-only; ignored on non-Linux builds.
	ParentDeathSignal string
	// ExitCodeMap remaps observed child exit codes before OnSuccess/OnFailure
	// dispatch and before they become gopherd's own exit code. Typical use:
	// neutralise SIGTERM→143 and SIGKILL→137 so a cleanly-stopped service is
	// not reported as a failure. Nil/empty passes codes through unchanged.
	ExitCodeMap map[int]int
	// SignalRewrite opts this service into signal forwarding and optionally
	// rewrites the signal on the way to the child. Keys and values are signal
	// names ("SIGUSR1", "USR1", ...). Forwarding is strictly opt-in: when
	// nil/empty, received signals are not forwarded. Does not affect gopherd's
	// own reactions to SIGTERM/SIGINT/SIGHUP.
	SignalRewrite map[string]string
	Args          []string
	After         []string
	Before        []string
	Requires      []string
	// RemoveEnv lists env keys to delete from the child's final environment
	// after merging OS env, dotenv, and per-process environment. Used to drop
	// a shared dotenv key one service must not see, or to strip OS env vars
	// when opting in to pass-env.
	RemoveEnv         []string
	BackoffFactor     float64
	UseEntrypointArgs bool
	// SDNotify enables the sd_notify readiness protocol: gopherd allocates a
	// per-service abstract unix datagram socket, exposes it via $NOTIFY_SOCKET,
	// and blocks dependents until the child writes "READY=1" to it.
	SDNotify bool
	// StrictGroups drops a named user's supplementary groups when an explicit
	// group is set. Default false keeps the user's full membership.
	StrictGroups bool
	// DotEnvFollow permits symlinks when opening the dotenv file, confined to the
	// file's directory (os.Root). Default false rejects any symlinked leaf or
	// ancestor. Enable for K8s ..data/ mounts or paths under /var/run; mirrors
	// the {{file}} follow modifier.
	DotEnvFollow bool
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

	// sdNotifyListener owns the sd_notify abstract socket when Proc.SDNotify
	// is set. Created in Start, closed in MarkExited; nil otherwise. Guarded
	// by svc.mu.
	sdNotifyListener *sdnotify.Listener

	Name      string
	OnSuccess ExitAction
	OnFailure ExitAction

	Proc Process

	stopSignal syscall.Signal
	killDelay  time.Duration
	// Pid is atomic so control-socket callbacks can read it without holding
	// svc.mu, while Start() writes it under svc.mu.
	Pid atomic.Int64

	mu      sync.Mutex
	running atomic.Bool
	stopped atomic.Bool // true if Stop() was called (we initiated the exit)
	Enabled bool
	Oneshot bool
	// LogCapture resolved from Proc.LogCapture; false = direct FD passthrough.
	LogCapture bool
}

// New creates a new Service from a Process config. globalPrefix is the
// top-level prefix; per-process Prefix overrides it. Returns an error rather
// than fatal-logging so a malformed reload config does not crash PID 1.
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
		// A negative delay would pass ParseDuration but silently disable Stop()'s
		// SIGKILL escalation (its killDelay > 0 guard), hanging shutdown. 0 is the
		// explicit "never escalate" value.
		if killDelay < 0 {
			return nil, fmt.Errorf("process %s: invalid kill-delay %q: must not be negative (use 0 to never escalate to SIGKILL)", name, p.KillDelay)
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
		LogCapture:     p.LogCapture != nil && *p.LogCapture,
		Stdout:         logger.NewPrefixWriter(os.Stdout, name, prefix),
		Stderr:         logger.NewPrefixWriter(os.Stderr, name, prefix),
	}, nil
}

// stripDotEnvComment removes an unquoted inline comment from a .env value.
// An inline comment starts with " #" outside any quoted region; quotes protect
// hash characters, matching Docker's --env-file and most .env parsers.
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
			i++ // skip escaped char inside double quotes
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

// dotenvUnescapeDouble processes backslash escapes (\n, \t, \r, \\, \") in a
// .env double-quoted value.
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

// isValidEnvKey reports whether k is a valid POSIX environment variable name:
// [A-Za-z_][A-Za-z0-9_]*. Rejecting malformed keys prevents shell-unsafe
// values from leaking into cmd.Env.
func isValidEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9': // digits only after the first char
		default:
			return false
		}
	}
	return true
}

// checkAncestorsNotSymlinked rejects any ancestor of path (from "/" to the
// immediate parent) that is a symlink or not a directory. path must be absolute
// and Clean'd. Duplicated from logger/ so each package stays self-contained.
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

// maxDotEnvSize caps parseDotEnv's read so a huge or swapped-out file cannot
// OOM-kill PID 1. Real dotenv files are typically under 10 KiB.
const maxDotEnvSize = 1 << 20

// parseDotEnv reads a dotenv file into KEY=value pairs (empty and #-comment
// lines skipped). Without follow, a symlinked leaf or ancestor is rejected —
// else an attacker with write access to a path directory could redirect the
// open. With follow, os.Root confines resolution to the file's directory (K8s
// ..data/ symlinks, /var/run -> /run), like the {{file}} follow modifier.
func parseDotEnv(path string, follow bool) (map[string]string, error) {
	// Absolute path so the ancestor walker has a stable start even for a
	// relative configured path.
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("dotenv %s: %w", path, err)
	}
	abs = filepath.Clean(abs)
	f, err := openConfined(abs, follow)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, fmt.Errorf("dotenv %s is a symlink; refusing to open (set dotenv-follow: true to permit)", path)
		}
		return nil, fmt.Errorf("dotenv %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("dotenv %s: stat: %w", path, err)
	}
	// Reject non-regular files: a FIFO would block io.ReadAll forever.
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
	data, err := io.ReadAll(io.LimitReader(f, maxDotEnvSize+1))
	if err != nil {
		return nil, fmt.Errorf("dotenv %s: %w", path, err)
	}
	if int64(len(data)) > maxDotEnvSize {
		return nil, fmt.Errorf("dotenv %s exceeds %d-byte size cap", path, maxDotEnvSize)
	}
	// Strip UTF-8 BOM so the first key is not prefixed with it.
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
			// Skip "=value" lines; an empty key yields an invalid cmd.Env entry (B5).
			continue
		}
		if !isValidEnvKey(k) {
			return nil, fmt.Errorf("dotenv %s: invalid env key %q (must match [A-Za-z_][A-Za-z0-9_]*)", path, k)
		}
		v = strings.TrimSpace(v)
		v = stripDotEnvComment(v)
		// Strip matching outer quotes. Double-quoted values get escape
		// sequences processed (matching YAML); single-quoted are literal.
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = dotenvUnescapeDouble(v[1 : len(v)-1])
		} else if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = v[1 : len(v)-1]
		}
		env[k] = v
	}
	return env, nil
}

// buildEnvMap merges OS env, dotenv, and per-process overrides, with priority
// (highest last) OS env < dotenv < per-process. When passEnv is false (the
// default) the parent env is not inherited, preventing secrets from leaking to
// children. The returned userKeys set marks keys from dotenv or procEnv; only
// those values are eligible for {{...}} expansion, so inherited OS env values
// containing "{{" pass through verbatim.
func buildEnvMap(dotenvPath string, dotenvFollow bool, procEnv map[string]string, passEnv bool) (map[string]string, map[string]bool, error) {
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
		dotenv, err := parseDotEnv(dotenvPath, dotenvFollow)
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

// templateRe matches {{.VAR}} and {{.VAR:-default}} placeholders. Submatches:
// (1) var name, (2) default text (absent when no ":-default"). The default is
// matched up to the first "}", so nesting is not supported.
var templateRe = regexp.MustCompile(`\{\{\s*\.(\w+)\s*(?::-([^}]*))?\s*\}\}`)

// memRe matches {{mem EXPR}} placeholders for memory expressions.
var memRe = regexp.MustCompile(`\{\{\s*mem\s+(.+?)\s*\}\}`)

// cpuRe matches {{cpu}} and {{cpu EXPR}}; bare {{cpu}} expands to the available
// CPU count. The `cpu` token must be followed by whitespace or `}}` so e.g.
// `{{cpus 50%}}` or `{{cpu_x}}` stay literal instead of erroring.
var cpuRe = regexp.MustCompile(`\{\{\s*cpu(?:\s+(.+?))?\s*\}\}`)

// expandTemplates resolves {{.VAR}}, {{.VAR:-default}}, {{mem EXPR}}, and
// {{cpu EXPR}} placeholders in a string slice (env lookups via env, memory via
// totalMiB, CPU via totalCPUs). For an unset/empty var, {{.VAR:-default}}
// expands to the default while {{.VAR}} expands to "" and warns — a silent
// empty substitution (e.g. a password template) has historically caused outages.
//
// Expansion is single-pass: a value's own placeholders are not re-expanded, so
// environment: entries cannot reference each other.
func expandTemplates(values []string, env map[string]string, totalMiB int64, totalCPUs int) ([]string, error) {
	out := make([]string, len(values))
	// Track keys already warned about this call so a restart loop with the same
	// misconfigured argv does not flood the log.
	warned := make(map[string]struct{})
	for i, s := range values {
		if !strings.Contains(s, "{{") {
			out[i] = s
			continue
		}
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
		// Expand {{file "/path"}} before {{.VAR}} so file contents containing
		// template-like text are not re-expanded.
		if strings.Contains(s, "{{") && strings.Contains(s, "file") {
			expanded, err := ExpandFileRefs(s)
			if err != nil {
				return nil, err
			}
			s = expanded
		}
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
					// A silent empty substitution can corrupt arguments (e.g.
					// an empty password flag). Warn once per missing key.
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

// StartPlan is the output of PrepareStart, consumed by FinishStart. It owns no
// OS resources (nothing forked, no listener yet), so an unused plan needs no
// cleanup.
type StartPlan struct {
	cmd        *exec.Cmd
	allowedUID int
	sdNotify   bool
}

// PrepareStart does the pre-fork work that must NOT hold the daemon lock:
// dotenv disk reads, credential resolution (NSS lookups can block on LDAP/SSSD),
// and template expansion. It takes no lock and mutates no service state, so a
// hanging lookup here can't stall the reap loop, control handlers, or shutdown.
// FinishStart spawns the returned plan.
func (s *Service) PrepareStart() (*StartPlan, error) {
	// PassEnv nil/false: don't forward gopherd's OS env, so operator secrets
	// don't leak into children unless opted in.
	passEnv := s.Proc.PassEnv != nil && *s.Proc.PassEnv
	env, userKeys, err := buildEnvMap(s.Proc.DotEnv, s.Proc.DotEnvFollow, s.Proc.Environment, passEnv)
	if err != nil {
		return nil, err
	}
	// Drop remove-env keys after the merge so a shared dotenv key can be
	// suppressed for one service without editing the dotenv file.
	for _, k := range s.Proc.RemoveEnv {
		delete(env, k)
		delete(userKeys, k)
	}

	// Resolved up front: the sd_notify listener needs the child's uid to
	// authenticate READY datagrams. Trust only the child's resolved uid (or
	// gopherd's euid when no privilege drop applies) or root for READY=1.
	cred, err := ResolveCredential(s.Proc.User, s.Proc.Group, s.Proc.UserID, s.Proc.GroupID, s.Proc.StrictGroups)
	if err != nil {
		return nil, err
	}
	allowedUID := os.Geteuid()
	if cred != nil {
		allowedUID = int(cred.Uid)
	}

	totalMiB, _ := memory.Available()
	totalCPUs := cpu.Available()

	args, err := expandTemplates(s.Proc.Args, env, totalMiB, totalCPUs)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(s.Proc.Command, args...)
	if s.LogCapture {
		cmd.Stdout = s.Stdout
		cmd.Stderr = s.Stderr
	} else {
		// *os.File makes os/exec pass the FD to the child directly — no pipe,
		// no copy goroutine, gopherd stays out of the output path.
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	cmd.Stdin = nil // each child gets /dev/null as stdin (exec.Cmd default)
	// Setsid: own process group (pgid == pid) keeps Kill(-pid) working; no
	// controlling TTY stops a dropped-priv child TIOCSTI-injecting under -t.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	// Parent-death signal: the kernel delivers sig to the child when gopherd's
	// thread dies, so children do not linger after an abrupt kill. Validated at
	// config load. Non-Linux builds skip via setPdeathsig.
	if s.Proc.ParentDeathSignal != "" {
		pdeathSig, perr := ParseSignal(s.Proc.ParentDeathSignal)
		if perr != nil {
			return nil, fmt.Errorf("parent-death-signal: %w", perr)
		}
		setPdeathsig(cmd.SysProcAttr, pdeathSig)
	}

	if s.Proc.WorkingDir != "" {
		cmd.Dir = s.Proc.WorkingDir
	}

	// Set cmd.Env explicitly unless pass-env is on with no other env config:
	// a nil cmd.Env makes Go inherit the parent env, which is only safe then.
	// SDNotify forces an explicit env so FinishStart can append NOTIFY_SOCKET.
	if !passEnv || s.Proc.DotEnv != "" || len(s.Proc.Environment) > 0 || len(s.Proc.RemoveEnv) > 0 || s.Proc.SDNotify {
		// Expand templates only in user-defined values (dotenv + procEnv).
		// Inherited OS env passes through verbatim so incidental "{{" does not
		// trigger expansion failures.
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
			return nil, err
		}
		for i, idx := range userIdx {
			envVals[idx] = expanded[i]
		}
		// Build "key=value" strings reusing kvBuf to avoid one make per entry;
		// reallocated only when the next entry does not fit.
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

	if cred != nil {
		cmd.SysProcAttr.Credential = cred
	}

	return &StartPlan{cmd: cmd, allowedUID: allowedUID, sdNotify: s.Proc.SDNotify}, nil
}

// FinishStart forks/execs a prepared plan and records running state, holding
// svc.mu only for the listener swap, spawn, and state update. The daemon calls
// it under d.mu so the fork stays serialized with shutdown.
func (s *Service) FinishStart(plan *StartPlan) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Create the listener under svc.mu, serialized with MarkExited (which closes
	// it on exit), so on restart the abstract socket name is already free.
	// Replacing any lingering listener drops stale READY so dependents re-wait.
	if plan.sdNotify {
		if s.sdNotifyListener != nil {
			_ = s.sdNotifyListener.Close()
			s.sdNotifyListener = nil
		}
		l, err := sdnotify.Listen(s.Name, os.Getpid(), plan.allowedUID)
		if err != nil {
			return 0, fmt.Errorf("sd_notify: %w", err)
		}
		s.sdNotifyListener = l
		// Appended here, not in the prepared env, so listener creation stays
		// under svc.mu. Literal path — never template-expanded.
		plan.cmd.Env = append(plan.cmd.Env, "NOTIFY_SOCKET="+l.Path())
	}

	if err := plan.cmd.Start(); err != nil {
		// Don't leak the abstract socket name on a failed spawn.
		if plan.sdNotify && s.sdNotifyListener != nil {
			_ = s.sdNotifyListener.Close()
			s.sdNotifyListener = nil
		}
		return 0, err
	}

	s.cmd = plan.cmd
	s.Pid.Store(int64(plan.cmd.Process.Pid))
	s.done = make(chan struct{})
	s.running.Store(true)
	s.stopped.Store(false)
	s.startedAt = time.Now()
	return plan.cmd.Process.Pid, nil
}

// Start is PrepareStart followed by FinishStart, for callers with no lock to
// keep off the blocking prep (the daemon splits the two halves instead).
func (s *Service) Start() (int, error) {
	plan, err := s.PrepareStart()
	if err != nil {
		return 0, err
	}
	return s.FinishStart(plan)
}

// Stop signals the process group (negative PID, so forked children are hit too)
// with the configured stop signal and schedules SIGKILL after kill-delay. Sets
// the stopped flag so the reap loop knows the exit was intentional. MarkExited
// cancels the deferred SIGKILL to avoid signalling a recycled PID.
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
		// Cancel any previously scheduled SIGKILL first. A second Stop() (e.g.
		// concurrent control-socket client and check-failure handler) would
		// otherwise overwrite s.killTimer, leaving the first timer uncancellable
		// by MarkExited and risking a SIGKILL to a recycled PID.
		if s.killTimer != nil {
			s.killTimer.Stop()
		}
		kpid := int(s.Pid.Load())
		s.killTimer = time.AfterFunc(s.killDelay, func() {
			if kpid <= 0 {
				return
			}
			// Hold s.mu across the Kill to serialize with MarkExited:
			// timer.Stop() does not wait if this callback is already running,
			// so without the lock we could observe a still-running service and
			// signal a PID the kernel has recycled. Under the lock, either
			// MarkExited ran first (running=false, we return) or the Kill
			// completes before it acquires the lock. MarkExited's caller has
			// already reaped the PID via Wait4, so no recycling race remains.
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.running.Load() || int(s.Pid.Load()) != kpid {
				return
			}
			_ = syscall.Kill(-kpid, syscall.SIGKILL)
		})
	}
}

// Signal sends sig to the entire process group. Setsid makes the service lead
// its own group (pgid == pid), so -pid addresses all members, like Stop().
func (s *Service) Signal(sig os.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running.Load() || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	// comma-ok avoids a panic if sig is not a syscall.Signal (B6).
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

// MarkExited marks the service as no longer running, cancels any pending
// SIGKILL, and returns how long it ran.
//
// Clears s.running and s.Pid atomically BEFORE acquiring s.mu so concurrent
// Stop, Signal, and killTimer callers (which re-read these atomics after
// locking) trip their `pid <= 0` / `Pid.Load() != kpid` guards. Since Wait4 has
// already freed the pid for reuse, this bounds the window in which a concurrent
// signaller could Kill a just-recycled pid. The residual window cannot be
// closed without pidfd_send_signal, which is not in the syscall stdlib.
func (s *Service) MarkExited() time.Duration {
	s.running.Store(false)
	s.Pid.Store(0)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		close(s.done)
		s.done = nil
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

// WaitSDNotifyReady blocks until the service writes "READY=1" to $NOTIFY_SOCKET
// or ctx is done. Returns an error if SDNotify was not enabled or ctx expires
// first. Safe to call from outside the service goroutine.
func (s *Service) WaitSDNotifyReady(ctx context.Context) error {
	s.mu.Lock()
	l := s.sdNotifyListener
	s.mu.Unlock()
	if l == nil {
		return fmt.Errorf("service %s: sd_notify not enabled", s.Name)
	}
	return l.WaitReady(ctx)
}

// WasStopped reports whether the service exited because Stop() was called rather
// than on its own, distinguishing intentional signal-death from unexpected exits
// for exit-code propagation.
func (s *Service) WasStopped() bool {
	return s.stopped.Load()
}

// RemapExitCode applies the service's exit-code-map to code, returning it
// unchanged when the map is empty or has no entry. Used by the reap loop before
// OnSuccess/OnFailure dispatch and before propagation as gopherd's own exit.
func (s *Service) RemapExitCode(code int) int {
	if len(s.Proc.ExitCodeMap) == 0 {
		return code
	}
	if mapped, ok := s.Proc.ExitCodeMap[code]; ok {
		return mapped
	}
	return code
}

// RewriteSignal returns (rewritten, true) if sig has an entry in the
// signal-rewrite map, or (0, false) when the service did not opt in to
// forwarding it. An unparseable target falls back to the original signal;
// targets are pre-validated at config load, so this is defensive.
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

// SignalName returns the canonical "SIGFOO" name for sig, or its numeric form
// when unknown. Used by RewriteSignal and the yml package to canonicalise
// signal-rewrite keys; mirrors ParseSignal accepting both "SIGUSR1" and "USR1".
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

// Done returns a channel closed when the service exits, so callers can select
// instead of polling IsRunning().
func (s *Service) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		// Never started or already exited: return a closed channel.
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return s.done
}
