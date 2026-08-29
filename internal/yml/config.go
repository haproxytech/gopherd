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

package yml

import (
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/haproxytech/gopherd/check"
	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/internal/cron"
	"github.com/haproxytech/gopherd/internal/logger"
	"github.com/haproxytech/gopherd/service"
)

// envFromOS returns the current process environment as a map.
// A var so tests can stub the environment source.
var envFromOS = func() map[string]string {
	env := os.Environ()
	m := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

// Shutdown order modes.
const (
	ShutdownReverseDep   = "reverse-dep"
	ShutdownDep          = "dep"
	ShutdownSimultaneous = "simultaneous"
)

// Config is the top-level gopherd configuration.
type Config struct {
	Checks     map[string]check.Config
	LogTargets map[string]logger.TargetConfig
	Prefix     string
	// PassEnv is the global default for the per-service pass-env flag.
	// nil means "not set", treated as false: child processes do not inherit
	// gopherd's environment, so operator secrets cannot silently leak into
	// every child. Explicit true opts in to inheritance.
	PassEnv *bool
	// LogCapture is the global default for the per-service log-capture flag.
	// nil means "not set", treated as false: children write directly to
	// gopherd's stdout/stderr FDs with zero supervision overhead. Explicit
	// true opts in to capture (prefixes, logs command, log-targets).
	LogCapture *bool
	// ExportSocket is the global default for the per-service export-socket
	// flag. nil means "not set", treated as false: children do not receive
	// GOPHERD_SOCKET. Explicit true exposes the control socket path so client
	// commands work from inside services.
	ExportSocket *bool

	ShutdownOrder string
	// InitStopSignal lists signals that trigger gopherd's own graceful
	// shutdown. Defaults to [SIGTERM, SIGINT]. SIGKILL/SIGSTOP are rejected
	// at load since they cannot be caught.
	InitStopSignal []string
	Control        control.Config
	Processes      []service.Process
	NoLogo         bool
	// Subreaper enables PR_SET_CHILD_SUBREAPER so orphaned descendants are
	// re-parented to gopherd (and reaped by its Wait4 loop) instead of the
	// real PID 1. Needed when gopherd is not PID 1 (docker exec, k8s
	// sidecars, nested init).
	Subreaper bool
}

// ShutdownSignals returns the signals that trigger gopherd's graceful
// shutdown: init-stop-signal when set, else {SIGTERM, SIGINT}.
//
// Names are validated at parse time, so a parse failure here is an internal
// bug; such entries are skipped rather than erroring so PID 1 never crashes.
func (c *Config) ShutdownSignals() map[syscall.Signal]bool {
	out := make(map[syscall.Signal]bool)
	if len(c.InitStopSignal) == 0 {
		out[syscall.SIGTERM] = true
		out[syscall.SIGINT] = true
		return out
	}
	for _, name := range c.InitStopSignal {
		if sig, err := service.ParseSignal(name); err == nil {
			out[sig] = true
		}
	}
	return out
}

// Load reads and parses a YAML config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	return Unmarshal(data)
}

// Unmarshal parses YAML bytes into a Config.
func Unmarshal(data []byte) (*Config, error) {
	root, err := Parse(data)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Checks:     make(map[string]check.Config),
		LogTargets: make(map[string]logger.TargetConfig),
	}

	if n := root.Get("prefix"); n != nil {
		cfg.Prefix = n.String()
	}
	if n := root.Get("no-logo"); n != nil {
		cfg.NoLogo = n.Bool()
	}
	if n := root.Get("subreaper"); n != nil {
		cfg.Subreaper = n.Bool()
	}
	if n := root.Get("pass-env"); n != nil {
		cfg.PassEnv = n.BoolPtr()
	}
	if n := root.Get("log-capture"); n != nil {
		cfg.LogCapture = n.BoolPtr()
	}
	if n := root.Get("export-socket"); n != nil {
		cfg.ExportSocket = n.BoolPtr()
	}
	if n := root.Get("init-stop-signal"); n != nil {
		cfg.InitStopSignal = n.Strings()
		// Reject SIGKILL/SIGSTOP: the kernel never delivers them to a
		// userspace handler, so listing them would be dead config.
		for _, name := range cfg.InitStopSignal {
			sig, err := service.ParseSignal(name)
			if err != nil {
				return nil, fmt.Errorf("invalid init-stop-signal %q: %w", name, err)
			}
			if sig == syscall.SIGKILL || sig == syscall.SIGSTOP {
				return nil, fmt.Errorf("invalid init-stop-signal %q: %s cannot be caught by a userspace handler", name, service.SignalName(sig))
			}
		}
	}
	if n := root.Get("shutdown-order"); n != nil {
		cfg.ShutdownOrder = n.String()
	}
	switch cfg.ShutdownOrder {
	case "", ShutdownReverseDep, ShutdownDep, ShutdownSimultaneous:
		// valid
	default:
		return nil, fmt.Errorf("invalid shutdown-order %q: must be %q, %q, or %q",
			cfg.ShutdownOrder, ShutdownReverseDep, ShutdownDep, ShutdownSimultaneous)
	}

	if n := root.Get("control"); n != nil {
		cfg.Control.SocketPath = n.Get("socket").String()
		if modeStr := n.Get("socket-mode").String(); modeStr != "" {
			parsed, err := strconv.ParseUint(modeStr, 8, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid control.socket-mode %q: %w", modeStr, err)
			}
			cfg.Control.SocketMode = os.FileMode(parsed)
		}
	}

	// Snapshot env once per load so all processes see a consistent view and
	// the map is not rebuilt per process. Hot reload re-parses the whole
	// config, so new values are still picked up.
	env := envFromOS()

	for _, item := range root.Get("processes").Items() {
		p, err := parseProcess(item, env)
		if err != nil {
			return nil, err
		}
		// Unset per-service pass-env inherits the global default. If both
		// stay nil, the consumer treats it as false.
		if p.PassEnv == nil && cfg.PassEnv != nil {
			v := *cfg.PassEnv
			p.PassEnv = &v
		}
		if p.LogCapture == nil && cfg.LogCapture != nil {
			v := *cfg.LogCapture
			p.LogCapture = &v
		}
		if p.ExportSocket == nil && cfg.ExportSocket != nil {
			v := *cfg.ExportSocket
			p.ExportSocket = &v
		}
		cfg.Processes = append(cfg.Processes, p)
	}

	for _, e := range root.Get("checks").Entries() {
		c, err := parseCheck(e.Val)
		if err != nil {
			return nil, fmt.Errorf("check %q: %w", e.Key, err)
		}
		cfg.Checks[e.Key] = c
	}

	for _, e := range root.Get("log-targets").Entries() {
		cfg.LogTargets[e.Key] = parseLogTarget(e.Val)
	}

	if len(cfg.Processes) == 0 {
		return nil, fmt.Errorf("no processes defined")
	}

	// Computed once: independent of the per-process loop below.
	shutdownSet := cfg.ShutdownSignals()
	// Scheduled services never run during startup layers, so nothing may order
	// against them (and they may not order against anything).
	scheduledSet := map[string]bool{}
	for _, p := range cfg.Processes {
		if p.Startup == "scheduled" || (p.Startup == "disabled" && p.Schedule != "") {
			name := p.Name
			if name == "" {
				name = p.Command
			}
			scheduledSet[name] = true
		}
	}
	for _, p := range cfg.Processes {
		name := p.Name
		if name == "" {
			name = p.Command
		}
		if p.Command == "" {
			if name == "" {
				name = "(unnamed)"
			}
			return nil, fmt.Errorf("process %q: command is required", name)
		}
		// The control protocol is space-delimited, so a whitespace name would be
		// unaddressable (start/stop/status unreachable for it).
		if strings.ContainsAny(name, " \t\r\n\v\f") {
			return nil, fmt.Errorf("process %q: name must not contain whitespace (used in the space-delimited control protocol); set an explicit name", name)
		}
		if err := service.ValidateExitAction(p.OnSuccess); err != nil {
			return nil, fmt.Errorf("process %q on-success: %w", name, err)
		}
		if err := service.ValidateExitAction(p.OnFailure); err != nil {
			return nil, fmt.Errorf("process %q on-failure: %w", name, err)
		}
		if p.StopSignal != "" {
			if _, err := service.ParseSignal(p.StopSignal); err != nil {
				return nil, fmt.Errorf("process %q stop-signal: %w", name, err)
			}
		}
		for checkName, action := range p.OnCheckFailure {
			if err := service.ValidateExitAction(action); err != nil {
				return nil, fmt.Errorf("process %q on-check-failure[%s]: %w", name, checkName, err)
			}
		}
		if err := validateSignalRewrite(name, p.SignalRewrite, shutdownSet); err != nil {
			return nil, err
		}
		if err := validateScheduled(name, p, scheduledSet); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

// validateScheduled enforces the startup=scheduled contract: a cron schedule
// is required and must parse; exit actions and backoff are meaningless (each
// run is oneshot-style, the next tick is the retry); and scheduled services
// take no part in startup ordering, in either direction.
func validateScheduled(name string, p service.Process, scheduledSet map[string]bool) error {
	// A disabled service may carry a schedule (an env-gated startup flips it
	// on); the full scheduled contract is enforced even while disabled, so
	// flipping the gate can never surface a new config error.
	if p.Startup == "scheduled" || (p.Startup == "disabled" && p.Schedule != "") {
		if p.Schedule == "" {
			return fmt.Errorf("process %q: schedule is required when startup is scheduled", name)
		}
		if _, err := cron.Parse(p.Schedule); err != nil {
			return fmt.Errorf("process %q schedule: %w", name, err)
		}
		if p.OnSuccess != "" {
			return fmt.Errorf("process %q: on-success is not allowed with startup: scheduled (each run's exit is logged; the next tick is the retry)", name)
		}
		if p.OnFailure != "" {
			return fmt.Errorf("process %q: on-failure is not allowed with startup: scheduled (each run's exit is logged; the next tick is the retry)", name)
		}
		if p.BackoffDelay != "" || p.BackoffLimit != "" {
			return fmt.Errorf("process %q: backoff settings are not allowed with startup: scheduled", name)
		}
		if len(p.After) > 0 || len(p.Before) > 0 || len(p.Requires) > 0 {
			return fmt.Errorf("process %q: after/before/requires are not allowed with startup: scheduled (scheduled services do not participate in startup ordering)", name)
		}
		// startup-timeout bounds each run at tick time (not via waitOneshot),
		// so validate here rather than failing silently mid-flight.
		if p.StartupTimeout != "" {
			if _, err := time.ParseDuration(p.StartupTimeout); err != nil {
				return fmt.Errorf("process %q: invalid startup-timeout %q: %w", name, p.StartupTimeout, err)
			}
		}
	} else if p.Schedule != "" {
		return fmt.Errorf("process %q: schedule is only valid with startup: scheduled or disabled", name)
	}
	for _, deps := range [][]string{p.After, p.Before, p.Requires} {
		for _, dep := range deps {
			if scheduledSet[dep] {
				return fmt.Errorf("process %q: cannot order against scheduled service %q (scheduled services do not run at startup)", name, dep)
			}
		}
	}
	return nil
}

// validateSignalRewrite rejects signal-rewrite keys that collide with
// signals gopherd handles itself (SIGHUP = reload, or any signal in the
// shutdown set). The switch in run.go dispatches those before reaching the
// forward branch, so such entries would be dead code.
func validateSignalRewrite(procName string, rewrite map[string]string, shutdownSet map[syscall.Signal]bool) error {
	if len(rewrite) == 0 {
		return nil
	}
	reserved := map[string]string{
		"SIGHUP": "triggers gopherd reload",
	}
	for sig := range shutdownSet {
		reserved[service.SignalName(sig)] = "triggers gopherd shutdown"
	}
	for key := range rewrite {
		if why, clash := reserved[key]; clash {
			return fmt.Errorf("process %q: signal-rewrite key %s is reserved (%s); choose a different signal", procName, key, why)
		}
	}
	return nil
}

func parseProcess(n *Node, env map[string]string) (service.Process, error) {
	rawStartup := n.Get("startup").String()
	// {{file}} expands before {{.VAR}}: file contents that look like a
	// template must not be re-expanded.
	fileExpanded, err := service.ExpandFileRefs(rawStartup)
	if err != nil {
		name := n.Get("name").String()
		if name == "" {
			name = n.Get("command").String()
		}
		if name == "" {
			name = "(unnamed)"
		}
		return service.Process{}, fmt.Errorf("process %q startup: %w", name, err)
	}
	startup := strings.TrimSpace(service.ExpandEnvRefs(fileExpanded, env))
	// An env-var reference resolving to empty means "disabled", so
	// `startup: "{{.START_X}}"` gates the service on whether $START_X is set.
	// A literal empty string (no reference) falls through to "enabled".
	if startup == "" && strings.Contains(rawStartup, "{{") {
		startup = "disabled"
	}
	// Validate here, not in Unmarshal, so a template-expanded value can be
	// redacted: `startup: "{{.DB_PASS}}"` must not echo the secret to the log.
	switch startup {
	case "", "enabled", "disabled", "oneshot", "scheduled":
		// valid
	default:
		name := n.Get("name").String()
		if name == "" {
			name = n.Get("command").String()
		}
		if name == "" {
			name = "(unnamed)"
		}
		if rawStartup != startup {
			return service.Process{}, fmt.Errorf("process %q: startup template %q expanded to a disallowed value (valid: enabled, disabled, oneshot, scheduled)", name, rawStartup)
		}
		return service.Process{}, fmt.Errorf("process %q: invalid startup %q (valid: enabled, disabled, oneshot, scheduled)", name, startup)
	}

	p := service.Process{
		Name:              n.Get("name").String(),
		Command:           n.Get("command").String(),
		Args:              n.Get("args").Strings(),
		WorkingDir:        n.Get("working-dir").String(),
		User:              n.Get("user").String(),
		Group:             n.Get("group").String(),
		Startup:           startup,
		StopSignal:        n.Get("stop-signal").String(),
		KillDelay:         n.Get("kill-delay").String(),
		OnSuccess:         n.Get("on-success").String(),
		OnFailure:         n.Get("on-failure").String(),
		BackoffDelay:      n.Get("backoff-delay").String(),
		BackoffLimit:      n.Get("backoff-limit").String(),
		ReadyCheck:        n.Get("ready-check").String(),
		ReadyTimeout:      n.Get("ready-timeout").String(),
		StartupTimeout:    n.Get("startup-timeout").String(),
		Schedule:          n.Get("schedule").String(),
		UseEntrypointArgs: n.Get("use-entrypoint-args").Bool(),
		PassEnv:           n.Get("pass-env").BoolPtr(),
		LogCapture:        n.Get("log-capture").BoolPtr(),
		ExportSocket:      n.Get("export-socket").BoolPtr(),
		DotEnv:            n.Get("dotenv").String(),
		DotEnvFollow:      n.Get("dotenv-follow").Bool(),
		After:             n.Get("after").Strings(),
		Before:            n.Get("before").Strings(),
		Requires:          n.Get("requires").Strings(),
		RemoveEnv:         n.Get("remove-env").Strings(),
		Environment:       n.Get("environment").StringMap(),
		OnCheckFailure:    n.Get("on-check-failure").StringMap(),
		UserID:            n.Get("user-id").IntPtr(),
		GroupID:           n.Get("group-id").IntPtr(),
		StrictGroups:      n.Get("strict-groups").Bool(),
		SDNotify:          n.Get("sd-notify").Bool(),
		SDNotifyTimeout:   n.Get("sd-notify-timeout").String(),
		ParentDeathSignal: n.Get("parent-death-signal").String(),
		SignalRewrite:     n.Get("signal-rewrite").StringMap(),

		ConditionFileExists:  n.Get("condition-file-exists").String(),
		ConditionFileMissing: n.Get("condition-file-missing").String(),
	}
	// Absolute-only: a relative condition path would silently depend on the
	// daemon's cwd. Same path in both conditions can never be satisfied.
	for key, path := range map[string]string{
		"condition-file-exists":  p.ConditionFileExists,
		"condition-file-missing": p.ConditionFileMissing,
	} {
		if path != "" && !filepath.IsAbs(path) {
			name := p.Name
			if name == "" {
				name = p.Command
			}
			return p, fmt.Errorf("process %q: %s %q must be an absolute path", name, key, path)
		}
	}
	if p.ConditionFileExists != "" && p.ConditionFileExists == p.ConditionFileMissing {
		name := p.Name
		if name == "" {
			name = p.Command
		}
		return p, fmt.Errorf("process %q: condition-file-exists and condition-file-missing name the same path %q; the conditions can never both hold", name, p.ConditionFileExists)
	}
	// exit-code-map: YAML keys parse as strings; convert to an int-keyed map.
	// Both sides accept a raw integer ("143") or a signal name ("SIGTERM" /
	// "TERM"); signal names map to the shell convention 128+signum, matching
	// what waitStatusCode reports for signal-terminated children. Mixed forms
	// are fine.
	if err := requireMapping(n, "exit-code-map", p.Name, p.Command); err != nil {
		return p, err
	}
	if raw := n.Get("exit-code-map").StringMap(); len(raw) > 0 {
		p.ExitCodeMap = make(map[int]int, len(raw))
		for k, v := range raw {
			keyInt, err := parseExitCode(k)
			if err != nil {
				name := p.Name
				if name == "" {
					name = p.Command
				}
				return p, fmt.Errorf("process %q: exit-code-map key %q: %w", name, k, err)
			}
			valInt, err := parseExitCode(v)
			if err != nil {
				name := p.Name
				if name == "" {
					name = p.Command
				}
				return p, fmt.Errorf("process %q: exit-code-map[%s] = %q: %w", name, k, v, err)
			}
			p.ExitCodeMap[keyInt] = valInt
		}
	}
	// signal-rewrite: validate key and value as known signal names, and
	// canonicalise to "SIGFOO" form so the runtime lookup need not re-parse
	// on every event.
	if len(p.SignalRewrite) > 0 {
		canon := make(map[string]string, len(p.SignalRewrite))
		for k, v := range p.SignalRewrite {
			fromSig, err := service.ParseSignal(k)
			if err != nil {
				name := p.Name
				if name == "" {
					name = p.Command
				}
				return p, fmt.Errorf("process %q: signal-rewrite source %q: %w", name, k, err)
			}
			toSig, err := service.ParseSignal(v)
			if err != nil {
				name := p.Name
				if name == "" {
					name = p.Command
				}
				return p, fmt.Errorf("process %q: signal-rewrite target %q: %w", name, v, err)
			}
			canon[service.SignalName(fromSig)] = service.SignalName(toSig)
		}
		p.SignalRewrite = canon
	}
	// Validate parent-death-signal at parse time so a typo surfaces before spawn.
	if p.ParentDeathSignal != "" {
		if _, err := service.ParseSignal(p.ParentDeathSignal); err != nil {
			name := p.Name
			if name == "" {
				name = p.Command
			}
			return p, fmt.Errorf("process %q: invalid parent-death-signal %q: %w", name, p.ParentDeathSignal, err)
		}
	}
	// Validate sd-notify-timeout at parse time so a typo surfaces before spawn.
	if p.SDNotifyTimeout != "" {
		if _, err := time.ParseDuration(p.SDNotifyTimeout); err != nil {
			name := p.Name
			if name == "" {
				name = p.Command
			}
			return p, fmt.Errorf("process %q: invalid sd-notify-timeout %q: %w", name, p.SDNotifyTimeout, err)
		}
	}
	// Surface unparseable numeric fields rather than silently defaulting.
	if raw := n.Get("backoff-factor").String(); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		// ParseFloat accepts "NaN"/"Inf". A NaN factor slips past backoff.New's
		// factor <= 0 guard and collapses later delays to zero (crash loop → fork
		// storm), so reject non-finite/non-positive factors at load.
		if err == nil && (math.IsNaN(v) || math.IsInf(v, 0) || v <= 0) {
			err = fmt.Errorf("must be a finite number greater than 0")
		}
		if err != nil {
			name := p.Name
			if name == "" {
				name = p.Command
			}
			return p, fmt.Errorf("process %q: invalid backoff-factor %q: %w", name, raw, err)
		}
		p.BackoffFactor = v
	}
	p.Prefix = n.Get("prefix").String()
	// {{file}}/{{.VAR}} in args land in argv, world-readable via
	// /proc/<pid>/cmdline; warn so secrets move to environment:.
	if slices.ContainsFunc(p.Args, argSecretTemplateRe.MatchString) {
		name := p.Name
		if name == "" {
			name = p.Command
		}
		log.Printf("warning: process %q expands {{file}}/{{.VAR}} in args; argv is world-readable via /proc/<pid>/cmdline — use environment: for secrets", name)
	}
	return p, nil
}

// argSecretTemplateRe matches arg templates whose value lands in argv
// ({{file}}, {{.VAR}}). {{cpu}}/{{mem}} expand to integers, so excluded.
var argSecretTemplateRe = regexp.MustCompile(`\{\{\s*(?:file\b|\.)`)

// requireMapping rejects a value that cannot hold a key-value table. A scalar
// or a list parses to an empty map, so without this the whole setting would be
// silently ignored until a child exits. An absent key, or a bare "key:" with no
// entries, stays legal.
func requireMapping(n *Node, key, procName, command string) error {
	v := n.Get(key)
	if v == nil || v.kind == kindMapping {
		return nil
	}
	if procName == "" {
		procName = command
	}
	got := fmt.Sprintf("%q", v.String())
	if v.kind == kindSequence {
		got = "a list"
	}
	return fmt.Errorf("process %q: %s must be a key-value map, either indented or inline as {SIGTERM: 0}; got %s", procName, key, got)
}

// parseExitCode accepts a decimal exit code ("143") or a signal name
// ("SIGTERM", "TERM"), returning the numeric exit status. Signal names use
// the shell convention 128+signum, matching what waitStatusCode reports for
// signal-terminated children, so `SIGTERM` and `143` are interchangeable.
func parseExitCode(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty exit code")
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v, nil
	}
	sig, err := service.ParseSignal(s)
	if err != nil {
		return 0, fmt.Errorf("not an integer and not a known signal name: %w", err)
	}
	return 128 + int(sig), nil
}

func parseCheck(n *Node) (check.Config, error) {
	c := check.Config{
		Period:       n.Get("period").String(),
		Timeout:      n.Get("timeout").String(),
		InitialDelay: n.Get("initial-delay").String(),
	}
	if raw := n.Get("threshold").String(); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return c, fmt.Errorf("invalid threshold %q: %w", raw, err)
		}
		c.Threshold = v
	}
	if h := n.Get("http"); h != nil {
		c.HTTP = &check.HTTP{
			URL:    h.Get("url").String(),
			Socket: h.Get("socket").String(),
		}
	}
	if t := n.Get("tcp"); t != nil {
		var port int
		if raw := t.Get("port").String(); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil {
				return c, fmt.Errorf("invalid tcp.port %q: %w", raw, err)
			}
			port = v
		}
		c.TCP = &check.TCP{
			Host: t.Get("host").String(),
			Port: port,
		}
	}
	if e := n.Get("exec"); e != nil {
		c.Exec = &check.Exec{
			Command: e.Get("command").String(),
			Args:    e.Get("args").Strings(),
		}
	}
	return c, nil
}

func parseLogTarget(n *Node) logger.TargetConfig {
	cfg := logger.TargetConfig{
		Type:     n.Get("type").String(),
		Location: n.Get("location").String(),
		Services: n.Get("services").Strings(),
		Labels:   n.Get("labels").StringMap(),
		MaxSize:  n.Get("max-size").String(),
		Compress: n.Get("compress").Bool(),
	}
	if v, ok := n.Get("max-files").Int(); ok {
		cfg.MaxFiles = v
	}
	return cfg
}
