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

// Package check provides periodic health checks (HTTP, TCP, exec).
package check

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Pre-allocated error for the no-check-type case (should never happen in practice).
var errNoCheckType = fmt.Errorf("no check type configured")

// HTTP defines an HTTP health check.
type HTTP struct {
	URL    string
	Socket string // optional unix socket path
}

// TCP defines a TCP health check.
type TCP struct {
	Host string
	Port int
}

// Exec defines a command-based health check.
type Exec struct {
	Command string
	Args    []string
}

// Config defines a single health check from config.
type Config struct {
	HTTP         *HTTP
	TCP          *TCP
	Exec         *Exec
	Period       string
	Timeout      string
	InitialDelay string // delay before first check (default: 1x period)
	Level        string // "alive" or "ready"
	Threshold    int
}

// Checker runs a single health check on a periodic loop.
type Checker struct {
	stopCh       chan struct{}
	onFailureFn  func(checkName string)          // called when threshold breached
	metricsFn    func(checkName string, ok bool) // called after every check
	credential   *syscall.Credential             // optional: run exec checks as this user
	httpClient   *http.Client                    // cached HTTP client (created once)
	httpReq      *http.Request                   // cached base request (cloned per-check)
	name         string
	tcpAddr      string // cached "host:port" for TCP checks
	cfg          Config
	period       time.Duration
	timeout      time.Duration
	initialDelay time.Duration
	threshold    int

	failures int

	mu      sync.Mutex
	healthy bool
}

// New creates a new Checker from config.
func New(name string, cfg Config, onFailure func(string), metricsFn func(string, bool)) (*Checker, error) {
	period, err := time.ParseDuration(cfg.Period)
	if err != nil || period <= 0 {
		if cfg.Period == "" {
			period = 10 * time.Second
		} else {
			return nil, fmt.Errorf("check %s: invalid period %q: %v", name, cfg.Period, err)
		}
	}

	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil || timeout <= 0 {
		if cfg.Timeout == "" {
			timeout = 3 * time.Second
		} else {
			return nil, fmt.Errorf("check %s: invalid timeout %q: %v", name, cfg.Timeout, err)
		}
	}

	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = 3
	}

	count := 0
	if cfg.HTTP != nil {
		count++
	}
	if cfg.TCP != nil {
		count++
	}
	if cfg.Exec != nil {
		count++
	}
	if count != 1 {
		return nil, fmt.Errorf("check %s: must define exactly one of http, tcp, or exec", name)
	}

	initialDelay := period // default: wait one period before first check
	if cfg.InitialDelay != "" {
		initialDelay, err = time.ParseDuration(cfg.InitialDelay)
		if err != nil || initialDelay < 0 {
			return nil, fmt.Errorf("check %s: invalid initial-delay %q: %v", name, cfg.InitialDelay, err)
		}
	}

	c := &Checker{
		name:         name,
		cfg:          cfg,
		period:       period,
		timeout:      timeout,
		initialDelay: initialDelay,
		threshold:    threshold,
		healthy:      true,
		stopCh:       make(chan struct{}),
		onFailureFn:  onFailure,
		metricsFn:    metricsFn,
	}
	if cfg.HTTP != nil {
		c.httpClient = c.buildHTTPClient()
		// Pre-build the base request to avoid parsing the URL on every check.
		req, err := http.NewRequest(http.MethodGet, cfg.HTTP.URL, nil)
		if err != nil {
			return nil, fmt.Errorf("check %s: invalid http url %q: %v", name, cfg.HTTP.URL, err)
		}
		c.httpReq = req
	}
	if cfg.TCP != nil {
		if cfg.TCP.Port <= 0 || cfg.TCP.Port > 65535 {
			return nil, fmt.Errorf("check %s: tcp port %d is not in valid range [1, 65535]", name, cfg.TCP.Port)
		}
		host := cfg.TCP.Host
		if host == "" {
			host = "localhost"
		}
		c.tcpAddr = fmt.Sprintf("%s:%d", host, cfg.TCP.Port)
	}
	return c, nil
}

// Run starts the periodic check loop in a goroutine.
func (c *Checker) Run() {
	go func() {
		// Initial delay before first check (default: 1x period, configurable via initial-delay).
		select {
		case <-time.After(c.initialDelay):
		case <-c.stopCh:
			return
		}

		ticker := time.NewTicker(c.period)
		defer ticker.Stop()

		for {
			err := c.Execute()

			c.mu.Lock()
			var callFailure bool
			if err != nil {
				c.failures++
				if c.metricsFn != nil {
					c.metricsFn(c.name, false)
				}
				if c.failures >= c.threshold && c.healthy {
					c.healthy = false
					log.Printf("check %s: unhealthy (%d consecutive failures): %v", c.name, c.failures, err)
					callFailure = true
				}
			} else {
				if c.metricsFn != nil {
					c.metricsFn(c.name, true)
				}
				if !c.healthy {
					log.Printf("check %s: healthy again", c.name)
				}
				c.failures = 0
				c.healthy = true
			}
			c.mu.Unlock()
			// Call onFailureFn outside the lock to avoid a lock-order inversion:
			// onFailureFn acquires d.mu, and d.mu is also held when stopChecks()
			// calls c.Stop(). Releasing c.mu first keeps the ordering consistent.
			if callFailure && c.onFailureFn != nil {
				c.onFailureFn(c.name)
			}

			select {
			case <-ticker.C:
			case <-c.stopCh:
				return
			}
		}
	}()
}

// WaitReady runs the check in a loop until it passes once or ctx is cancelled.
// The polling interval is capped at 1 second so a check with a long period
// (e.g., 30s) does not stall service startup for an entire period between tries.
func (c *Checker) WaitReady(ctx context.Context) error {
	if err := c.Execute(); err == nil {
		return nil
	}
	ticker := time.NewTicker(min(c.period, time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.Execute(); err == nil {
				return nil
			}
		}
	}
}

// Stop stops the periodic check loop.
func (c *Checker) Stop() {
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
}

// Execute runs a single check and returns nil on success.
func (c *Checker) Execute() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	switch {
	case c.cfg.HTTP != nil:
		return c.checkHTTP(ctx)
	case c.cfg.TCP != nil:
		return c.checkTCP(ctx)
	case c.cfg.Exec != nil:
		return c.checkExec(ctx)
	default:
		return errNoCheckType
	}
}

func (c *Checker) buildHTTPClient() *http.Client {
	client := &http.Client{
		Timeout: c.timeout,
		// Disable redirect following to prevent SSRF via redirect chains
		// (e.g., redirecting to cloud metadata endpoints).
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if c.cfg.HTTP.Socket != "" {
		client.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", c.cfg.HTTP.Socket)
			},
		}
	}
	return client
}

func (c *Checker) checkHTTP(ctx context.Context) error {
	// Reuse cached request with the check's context. WithContext does a
	// shallow copy but avoids re-parsing the URL on every check.
	req := c.httpReq.WithContext(ctx)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http check: %w", err)
	}
	// Drain the body before closing so the transport can reuse the TCP connection.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http check: status %d", resp.StatusCode)
	}
	return nil
}

func (c *Checker) checkTCP(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.tcpAddr)
	if err != nil {
		return fmt.Errorf("tcp check %s: %w", c.tcpAddr, err)
	}
	conn.Close()
	return nil
}

func (c *Checker) checkExec(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, c.cfg.Exec.Command, c.cfg.Exec.Args...)
	if c.credential != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: c.credential,
			Setpgid:    true,
		}
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exec check: %w", err)
	}
	return nil
}

// SetCredential sets the credential for exec health checks so they run
// as the associated service's user instead of as root.
func (c *Checker) SetCredential(cred *syscall.Credential) {
	c.credential = cred
}
