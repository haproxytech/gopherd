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

	"github.com/haproxytech/gopherd/check"
	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/logger"
	"github.com/haproxytech/gopherd/service"
)

// Config is the top-level gopherd configuration.
type Config struct {
	Checks     map[string]check.Config
	LogTargets map[string]logger.TargetConfig
	Prefix     string
	Control    control.Config
	Processes  []service.Process
	NoLogo     bool
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

	for _, item := range root.Get("processes").Items() {
		cfg.Processes = append(cfg.Processes, parseProcess(item))
	}

	for _, e := range root.Get("checks").Entries() {
		cfg.Checks[e.Key] = parseCheck(e.Val)
	}

	for _, e := range root.Get("log-targets").Entries() {
		cfg.LogTargets[e.Key] = parseLogTarget(e.Val)
	}

	if len(cfg.Processes) == 0 {
		return nil, fmt.Errorf("no processes defined")
	}

	return cfg, nil
}

func parseProcess(n *Node) service.Process {
	p := service.Process{
		Name:              n.Get("name").String(),
		Command:           n.Get("command").String(),
		Args:              n.Get("args").Strings(),
		WorkingDir:        n.Get("working-dir").String(),
		User:              n.Get("user").String(),
		Group:             n.Get("group").String(),
		Startup:           n.Get("startup").String(),
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
		CleanEnv:          n.Get("clean-env").Bool(),
		DotEnv:            n.Get("dotenv").String(),
		After:             n.Get("after").Strings(),
		Before:            n.Get("before").Strings(),
		Requires:          n.Get("requires").Strings(),
		Environment:       n.Get("environment").StringMap(),
		OnCheckFailure:    n.Get("on-check-failure").StringMap(),
		UserID:            n.Get("user-id").IntPtr(),
		GroupID:           n.Get("group-id").IntPtr(),
	}
	if v, ok := n.Get("backoff-factor").Float(); ok {
		p.BackoffFactor = v
	}
	p.Prefix = n.Get("prefix").String()
	return p
}

func parseCheck(n *Node) check.Config {
	c := check.Config{
		Period:       n.Get("period").String(),
		Timeout:      n.Get("timeout").String(),
		InitialDelay: n.Get("initial-delay").String(),
		Level:        n.Get("level").String(),
	}
	if v, ok := n.Get("threshold").Int(); ok {
		c.Threshold = v
	}
	if h := n.Get("http"); h != nil {
		c.HTTP = &check.HTTP{
			URL:    h.Get("url").String(),
			Socket: h.Get("socket").String(),
		}
	}
	if t := n.Get("tcp"); t != nil {
		port, _ := t.Get("port").Int()
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
	return c
}

func parseLogTarget(n *Node) logger.TargetConfig {
	return logger.TargetConfig{
		Type:     n.Get("type").String(),
		Location: n.Get("location").String(),
		Services: n.Get("services").Strings(),
		Labels:   n.Get("labels").StringMap(),
	}
}
