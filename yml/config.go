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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/haproxytech/gopherd/check"
	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/logger"
	"github.com/haproxytech/gopherd/service"
)

// envFromOS returns the current process environment as a map.
// Exposed as a var so tests can stub the environment source.
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

// Config is the top-level gopherd configuration.
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
	// A nil pointer means "not set in config", which the consumer treats as
	// false (do not inherit gopherd's environment). This default prevents
	// operator secrets in gopherd's own env from silently leaking into
	// every child. Explicit true opts in to inheritance.
	PassEnv *bool

	ShutdownOrder string
	StopSignal    string // signal that triggers graceful shutdown (default: SIGTERM)
	Control       control.Config
	Processes     []service.Process
	NoLogo        bool
	// Subreaper enables PR_SET_CHILD_SUBREAPER at startup so orphaned
	// descendants are re-parented to gopherd (and reaped by its Wait4 loop)
	// instead of the real PID 1. Useful when gopherd itself is not PID 1
	// (e.g. inside docker exec, k8s sidecars, nested init).
	Subreaper bool
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
	if n := root.Get("stop-signal"); n != nil {
		cfg.StopSignal = n.String()
		// Surface unknown signal names at config load rather than at shutdown.
		if cfg.StopSignal != "" {
			if _, err := service.ParseSignal(cfg.StopSignal); err != nil {
				return nil, fmt.Errorf("invalid stop-signal %q: %w", cfg.StopSignal, err)
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

	// Env is snapshotted once per load so all processes see a consistent view
	// and we avoid rebuilding the map per process. Hot reload picks up new
	// values because the whole config is re-parsed.
	env := envFromOS()

	for _, item := range root.Get("processes").Items() {
		p, err := parseProcess(item, env)
		if err != nil {
			return nil, err
		}
		// If per-service pass-env is unset, inherit the global default
		// when explicitly set. Either side left nil falls through to the
		// consumer, which treats nil as false (do not inherit gopherd's env).
		if p.PassEnv == nil && cfg.PassEnv != nil {
			v := *cfg.PassEnv
			p.PassEnv = &v
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
	}

	return cfg, nil
}

func parseProcess(n *Node, env map[string]string) (service.Process, error) {
	rawStartup := n.Get("startup").String()
	startup := strings.TrimSpace(service.ExpandEnvRefs(rawStartup, env))
	// An env-var reference that resolves to empty means "disabled", so the
	// common `startup: "{{.START_X}}"` pattern gates the service on whether
	// $START_X is set. A literal empty string (no reference) keeps today's
	// behavior of falling through to the default "enabled" branch.
	if startup == "" && strings.Contains(rawStartup, "{{") {
		startup = "disabled"
	}
	// Validate here rather than in Unmarshal so we can redact the expanded
	// value when it came from a template — a misconfigured `startup:
	// "{{.DB_PASS}}"` would otherwise echo the secret into the daemon log.
	switch startup {
	case "", "enabled", "disabled", "oneshot":
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
			return service.Process{}, fmt.Errorf("process %q: startup template %q expanded to a disallowed value (valid: enabled, disabled, oneshot)", name, rawStartup)
		}
		return service.Process{}, fmt.Errorf("process %q: invalid startup %q (valid: enabled, disabled, oneshot)", name, startup)
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
		UseEntrypointArgs: n.Get("use-entrypoint-args").Bool(),
		PassEnv:           n.Get("pass-env").BoolPtr(),
		DotEnv:            n.Get("dotenv").String(),
		After:             n.Get("after").Strings(),
		Before:            n.Get("before").Strings(),
		Requires:          n.Get("requires").Strings(),
		RemoveEnv:         n.Get("remove-env").Strings(),
		Environment:       n.Get("environment").StringMap(),
		OnCheckFailure:    n.Get("on-check-failure").StringMap(),
		UserID:            n.Get("user-id").IntPtr(),
		GroupID:           n.Get("group-id").IntPtr(),
		SDNotify:          n.Get("sd-notify").Bool(),
		SDNotifyTimeout:   n.Get("sd-notify-timeout").String(),
		ParentDeathSignal: n.Get("parent-death-signal").String(),
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
	return p, nil
}

func parseCheck(n *Node) (check.Config, error) {
	c := check.Config{
		Period:       n.Get("period").String(),
		Timeout:      n.Get("timeout").String(),
		InitialDelay: n.Get("initial-delay").String(),
		Level:        n.Get("level").String(),
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
