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

// Package metrics tracks internal service and health check statistics.
package metrics

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Metrics tracks service and check statistics in memory.
type Metrics struct {
	services map[string]*serviceStats
	checks   map[string]*checkStats
	mu       sync.RWMutex
}

type serviceStats struct {
	StartedAt time.Time
	Pid       int
	Restarts  int
	Exits     int
	Failures  int
	Successes int
	Up        bool
	Enabled   bool
	// Pending means the service is enabled and configured but the startup
	// loop has not attempted to fork it yet. Set by RegisterService for
	// enabled services and cleared by ServiceStarted. Surfaces "pending"
	// in stats output during the brief startup window.
	Pending bool
}

// ServiceSnapshot is a machine-readable view of one service's stats, intended
// for JSON output of `gopherd status -o json`.
type ServiceSnapshot struct {
	Name      string `json:"name"`
	State     string `json:"state"` // up, stopped, disabled, pending
	Pid       int    `json:"pid,omitempty"`
	Uptime    int64  `json:"uptime_seconds,omitempty"`
	Enabled   bool   `json:"enabled"`
	Exits     int    `json:"exits"`
	Restarts  int    `json:"restarts"`
	Successes int    `json:"successes"`
	Failures  int    `json:"failures"`
}

// CheckSnapshot mirrors ServiceSnapshot for health checks.
type CheckSnapshot struct {
	Name     string `json:"name"`
	Healthy  bool   `json:"healthy"`
	Failures int    `json:"failures"`
}

// Snapshot is the full view returned by Metrics.Snapshot. Empty slices
// marshal to "[]" rather than null so consumers can iterate unconditionally.
type Snapshot struct {
	Services []ServiceSnapshot `json:"services"`
	Checks   []CheckSnapshot   `json:"checks"`
}

type checkStats struct {
	Failures int
	Healthy  bool
}

// New creates a new Metrics instance.
func New() *Metrics {
	return &Metrics{
		services: make(map[string]*serviceStats),
		checks:   make(map[string]*checkStats),
	}
}

// svcRegister returns the entry for name, creating it if absent. Used only
// by RegisterService — event methods (Started/Exited/Restarted) must not
// create entries, otherwise a service unregistered by reload would be
// resurrected by its own late exit event.
func (m *Metrics) svcRegister(name string) *serviceStats {
	s, ok := m.services[name]
	if !ok {
		s = &serviceStats{Enabled: true}
		m.services[name] = s
	}
	return s
}

func (m *Metrics) chk(name string) *checkStats {
	c, ok := m.checks[name]
	if !ok {
		c = &checkStats{Healthy: true}
		m.checks[name] = c
	}
	return c
}

// RegisterService seeds a service in the metrics map so it appears in stats
// before its first run. Idempotent: re-registering updates Enabled (so a
// reload that toggles startup:disabled is reflected) but preserves counters.
func (m *Metrics) RegisterService(name string, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.svcRegister(name)
	s.Enabled = enabled
	// Re-registering an already-started service (reload path) must not
	// resurrect Pending — only mark pending if the service has never run.
	if enabled && s.StartedAt.IsZero() && s.Exits == 0 {
		s.Pending = true
	}
}

// IsPending reports whether the service is registered, enabled, and has not
// yet been started — i.e. waiting for the startup loop or a control-socket
// start command.
func (m *Metrics) IsPending(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.services[name]
	return ok && s.Pending
}

// UnregisterService removes a service entry, e.g. after a reload drops it
// from the config. Counters for a service that no longer exists would be
// misleading in stats output.
func (m *Metrics) UnregisterService(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.services, name)
}

// ServiceStarted records that a service has started. A no-op if name was
// never registered (e.g. unregistered by a reload before the event landed).
func (m *Metrics) ServiceStarted(name string, pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[name]
	if !ok {
		return
	}
	s.Up = true
	s.Pid = pid
	s.Pending = false
	s.StartedAt = time.Now()
}

// ServiceExited records that a service has exited. A no-op if name was
// never registered.
func (m *Metrics) ServiceExited(name string, exitCode int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[name]
	if !ok {
		return
	}
	s.Up = false
	s.Pid = 0
	s.Pending = false
	s.Exits++
	if exitCode == 0 {
		s.Successes++
	} else {
		s.Failures++
	}
}

// ServiceRestarted records a service restart. A no-op if name was never
// registered.
func (m *Metrics) ServiceRestarted(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.services[name]; ok {
		s.Restarts++
	}
}

// RegisterCheck seeds a check in the metrics map so it appears in stats
// before its first probe runs. Healthy=true and failures=0 are the natural
// initial state; the first CheckResult call updates them.
func (m *Metrics) RegisterCheck(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chk(name)
}

// CheckResult records a health check result.
func (m *Metrics) CheckResult(name string, healthy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.chk(name)
	c.Healthy = healthy
	if !healthy {
		c.Failures++
	}
}

// Snapshot returns a structured view of all services and checks, sorted by
// name. Counters are copied so callers can use the result without holding
// any lock.
func (m *Metrics) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	svcNames := make([]string, 0, len(m.services))
	for name := range m.services {
		svcNames = append(svcNames, name)
	}
	slices.Sort(svcNames)
	svcs := make([]ServiceSnapshot, 0, len(svcNames))
	for _, name := range svcNames {
		svcs = append(svcs, m.serviceSnapshotLocked(name))
	}

	chkNames := make([]string, 0, len(m.checks))
	for name := range m.checks {
		chkNames = append(chkNames, name)
	}
	slices.Sort(chkNames)
	chks := make([]CheckSnapshot, 0, len(chkNames))
	for _, name := range chkNames {
		c := m.checks[name]
		chks = append(chks, CheckSnapshot{Name: name, Healthy: c.Healthy, Failures: c.Failures})
	}

	return Snapshot{Services: svcs, Checks: chks}
}

// ServiceSnapshot returns a structured view of one service. The second return
// is false if no service with that name has been registered.
func (m *Metrics) ServiceSnapshot(name string) (ServiceSnapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.services[name]; !ok {
		return ServiceSnapshot{}, false
	}
	return m.serviceSnapshotLocked(name), true
}

// serviceSnapshotLocked must be called with m.mu held.
func (m *Metrics) serviceSnapshotLocked(name string) ServiceSnapshot {
	s := m.services[name]
	snap := ServiceSnapshot{
		Name:      name,
		Enabled:   s.Enabled,
		Exits:     s.Exits,
		Restarts:  s.Restarts,
		Successes: s.Successes,
		Failures:  s.Failures,
	}
	switch {
	case s.Up:
		snap.State = "up"
		snap.Pid = s.Pid
		snap.Uptime = int64(time.Since(s.StartedAt).Truncate(time.Second).Seconds())
	case !s.Enabled:
		snap.State = "disabled"
	case s.Pending:
		snap.State = "pending"
	default:
		snap.State = "stopped"
	}
	return snap
}

// Format returns a human-readable stats summary.
func (m *Metrics) Format() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var b strings.Builder

	if len(m.services) > 0 {
		b.WriteString("services:\n")
		svcNames := make([]string, 0, len(m.services))
		for name := range m.services {
			svcNames = append(svcNames, name)
		}
		slices.Sort(svcNames)
		for _, name := range svcNames {
			s := m.services[name]
			var state string
			switch {
			case s.Up:
				uptime := time.Since(s.StartedAt).Truncate(time.Second)
				state = fmt.Sprintf("up %s pid=%d", uptime, s.Pid)
			case !s.Enabled:
				state = "disabled"
			case s.Pending:
				state = "pending"
			default:
				state = "stopped"
			}
			fmt.Fprintf(&b, "  %-20s %s  exits=%d restarts=%d ok=%d fail=%d\n",
				name, state, s.Exits, s.Restarts, s.Successes, s.Failures)
		}
	}

	if len(m.checks) > 0 {
		b.WriteString("checks:\n")
		chkNames := make([]string, 0, len(m.checks))
		for name := range m.checks {
			chkNames = append(chkNames, name)
		}
		slices.Sort(chkNames)
		for _, name := range chkNames {
			c := m.checks[name]
			state := "healthy"
			if !c.Healthy {
				state = "unhealthy"
			}
			fmt.Fprintf(&b, "  %-20s %s  failures=%d\n", name, state, c.Failures)
		}
	}

	if b.Len() == 0 {
		return "no stats"
	}
	return strings.TrimRight(b.String(), "\n")
}
