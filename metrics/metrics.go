// Package metrics tracks internal service and health check statistics.
package metrics

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Metrics tracks service and check statistics in memory.
type Metrics struct {
	services map[string]*serviceStats
	checks   map[string]*checkStats
	mu       sync.Mutex
}

type serviceStats struct {
	StartedAt time.Time
	Restarts  int
	Exits     int
	Failures  int
	Successes int
	Up        bool
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

func (m *Metrics) svc(name string) *serviceStats {
	s, ok := m.services[name]
	if !ok {
		s = &serviceStats{}
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

// ServiceStarted records that a service has started.
func (m *Metrics) ServiceStarted(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.svc(name)
	s.Up = true
	s.StartedAt = time.Now()
}

// ServiceExited records that a service has exited.
func (m *Metrics) ServiceExited(name string, exitCode int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.svc(name)
	s.Up = false
	s.Exits++
	if exitCode == 0 {
		s.Successes++
	} else {
		s.Failures++
	}
}

// ServiceRestarted records a service restart.
func (m *Metrics) ServiceRestarted(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.svc(name).Restarts++
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

// Format returns a human-readable stats summary.
func (m *Metrics) Format() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder

	if len(m.services) > 0 {
		b.WriteString("services:\n")
		for name, s := range m.services {
			state := "stopped"
			if s.Up {
				uptime := time.Since(s.StartedAt).Truncate(time.Second)
				state = fmt.Sprintf("up %s", uptime)
			}
			b.WriteString(fmt.Sprintf("  %-20s %s  exits=%d restarts=%d ok=%d fail=%d\n",
				name, state, s.Exits, s.Restarts, s.Successes, s.Failures))
		}
	}

	if len(m.checks) > 0 {
		b.WriteString("checks:\n")
		for name, c := range m.checks {
			state := "healthy"
			if !c.Healthy {
				state = "unhealthy"
			}
			b.WriteString(fmt.Sprintf("  %-20s %s  failures=%d\n", name, state, c.Failures))
		}
	}

	if b.Len() == 0 {
		return "no stats"
	}
	return strings.TrimRight(b.String(), "\n")
}
