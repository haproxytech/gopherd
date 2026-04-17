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
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/haproxytech/gopherd/backoff"
	"github.com/haproxytech/gopherd/cpu"
	"github.com/haproxytech/gopherd/logger"
	"github.com/haproxytech/gopherd/memory"
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
	UserID            *int
	GroupID           *int
	Environment       map[string]string
	OnCheckFailure    map[string]string
	CleanEnv          *bool
	Name              string
	Command           string
	WorkingDir        string
	User              string
	Group             string
	Startup           string
	StopSignal        string
	KillDelay         string
	OnSuccess         string
	OnFailure         string
	BackoffDelay      string
	BackoffLimit      string
	ReadyCheck        string
	ReadyTimeout      string
	StartupTimeout    string
	DotEnv            string
	Prefix            string
	Args              []string
	After             []string
	Before            []string
	Requires          []string
	BackoffFactor     float64
	UseEntrypointArgs bool
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
// prevents shell-unsafe values from leaking into cmd.Env (M6).
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

// parseDotEnv reads a dotenv file and returns key-value pairs.
// Lines are in the format KEY=value. Empty lines and lines starting with # are skipped.
// Uses O_NOFOLLOW to reject symlinks atomically, matching the protection applied to
// the main config file.
func parseDotEnv(path string) (map[string]string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
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
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("dotenv %s: %w", path, err)
	}
	// Strip UTF-8 BOM if present so the first key is not prefixed with it (M6).
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
// If cleanEnv is true, the parent's environment is not inherited — only dotenv
// and per-process vars are used. This prevents secrets from leaking to children.
// The returned userKeys set identifies keys set by dotenv or procEnv; only
// values at those keys are eligible for {{...}} template expansion, so
// inherited OS env values that happen to contain "{{" are passed through
// verbatim.
func buildEnvMap(dotenvPath string, procEnv map[string]string, cleanEnv bool) (map[string]string, map[string]bool, error) {
	env := make(map[string]string)
	if !cleanEnv {
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
var cpuRe = regexp.MustCompile(`\{\{\s*cpu\s*(.*?)\s*\}\}`)

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
				cpus, err := cpu.Eval(s[loc[2]:loc[3]], totalCPUs)
				if err != nil {
					return nil, err
				}
				b.WriteString(strconv.Itoa(cpus))
				prev = loc[1]
			}
			b.WriteString(s[prev:])
			s = b.String()
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
func (s *Service) Start() (int, error) {
	// Build environment, resolve credentials, and expand templates before
	// acquiring the lock to minimize time spent in the critical section.
	cleanEnv := s.Proc.CleanEnv != nil && *s.Proc.CleanEnv
	env, userKeys, err := buildEnvMap(s.Proc.DotEnv, s.Proc.Environment, cleanEnv)
	if err != nil {
		return 0, err
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

	if s.Proc.WorkingDir != "" {
		cmd.Dir = s.Proc.WorkingDir
	}

	// Set the child's environment explicitly when dotenv, per-process vars,
	// or clean-env is used. When cmd.Env is nil, Go inherits the parent env.
	if cleanEnv || s.Proc.DotEnv != "" || len(s.Proc.Environment) > 0 {
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
			// MarkExited calls killTimer.Stop() under s.mu, but Stop() returns
			// false and does not wait if the callback is already running. That
			// leaves a narrow window in which this callback races MarkExited:
			// the service could have exited, been reaped, and had its PID
			// reused by an unrelated process group by the time we signal.
			//
			// Close the window by re-taking s.mu and verifying under the lock
			// that the service is still running with the original PID. If it
			// is not, someone else now owns that PID and we must not touch it.
			s.mu.Lock()
			stillOurs := s.running.Load() && int(s.Pid.Load()) == kpid
			s.mu.Unlock()
			if !stillOurs {
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
func (s *Service) MarkExited() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running.Store(false)
	if s.done != nil {
		close(s.done)
	}
	if s.killTimer != nil {
		s.killTimer.Stop()
		s.killTimer = nil
	}
	return time.Since(s.startedAt)
}

// WasStopped returns true if the service exited because we called Stop()
// (as opposed to exiting on its own). This distinguishes intentional signal-death
// from unexpected exits for the purpose of exit code propagation.
func (s *Service) WasStopped() bool {
	return s.stopped.Load()
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
