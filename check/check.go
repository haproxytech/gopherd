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
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/haproxytech/gopherd/internal/reaper"
)

var errNoCheckType = fmt.Errorf("no check type configured")

// ErrInconclusive marks a probe whose result was lost (e.g. the reap loop
// stole the exec child's exit status). Carries no health data: the run loop
// logs it without touching the failure streak.
var ErrInconclusive = fmt.Errorf("probe result lost")

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
	Threshold    int
}

// Checker runs a single health check on a periodic loop.
type Checker struct {
	stopCh       chan struct{}
	onFailureFn  func(checkName string)          // called when threshold breached
	metricsFn    func(checkName string, ok bool) // called after every check
	credential   *syscall.Credential             // optional: run exec checks as this user
	reaper       *reaper.Registry                // optional: reap loop delivers exec exit statuses
	httpClient   *http.Client
	httpReq      *http.Request // cached base request, cloned per-check
	name         string
	tcpAddr      string // cached "host:port" for TCP checks
	cfg          Config
	period       time.Duration
	timeout      time.Duration
	initialDelay time.Duration
	threshold    int

	failures int
	stopped  atomic.Bool // set by Stop(); guards against stale callbacks after shutdown

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
		// Pre-build the base request to avoid re-parsing the URL on every check.
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
		// Use a timer (not time.After) so it is stopped and GC-eligible if Stop()
		// fires during the initial delay (B1).
		timer := time.NewTimer(c.initialDelay)
		select {
		case <-timer.C:
		case <-c.stopCh:
			timer.Stop()
			return
		}
		timer.Stop()

		ticker := time.NewTicker(c.period)
		defer ticker.Stop()

		for {
			callFailure := c.observe(c.Execute())
			// Call onFailureFn outside c.mu to avoid lock-order inversion:
			// onFailureFn acquires d.mu, which is also held when stopChecks()
			// calls c.Stop(). The stopped guard prevents a callback queued just
			// before Stop() from firing after shutdown (B3).
			//
			// Residual window: Stop() can set stopped between this load and the
			// call. Not closed by joining the goroutine in Stop() — Stop() runs
			// under d.mu (reload's stopChecks) while this goroutine may block on
			// d.mu inside onFailureFn, so joining would deadlock. handleCheckFailure
			// re-validates under d.mu, so a stale post-Stop call is harmless.
			if callFailure && c.onFailureFn != nil && !c.stopped.Load() {
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

// observe applies one probe result to the health state and reports whether
// the failure threshold was just crossed.
func (c *Checker) observe(err error) bool {
	// A lost result is not a failed probe: counting it toward the threshold
	// would restart healthy services (the bug this guards against).
	if err != nil && errors.Is(err, ErrInconclusive) {
		log.Printf("check %s: inconclusive probe (not counted): %v", c.name, err)
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
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
	return callFailure
}

// WaitReady runs the check in a loop until it passes once or ctx is cancelled.
// The poll interval is capped at 1s so a long period (e.g. 30s) does not stall
// startup for a full period between tries.
func (c *Checker) WaitReady(ctx context.Context) error {
	if err := c.Execute(); err != nil {
		log.Printf("check %s: waiting (initial probe failed: %v)", c.name, err)
	} else {
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
	c.stopped.Store(true)
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
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
	} else {
		// Own transport so each checker has an isolated idle-conn pool; otherwise
		// one checker's Stop() (CloseIdleConnections) evicts conns still in use by
		// other checkers.
		client.Transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	return client
}

func (c *Checker) checkHTTP(ctx context.Context) error {
	req := c.httpReq.WithContext(ctx)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http check: %w", err)
	}
	// Drain the body before closing so the transport can reuse the TCP connection.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
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
	_ = conn.Close()
	return nil
}

func (c *Checker) checkExec(ctx context.Context) error {
	// The reap loop's Wait4(-1) steals exit statuses from cmd.Wait (ECHILD),
	// so once it runs, statuses must come from it. Before Activate (startup
	// sequence) there is no competing waiter and cmd.Run is safe.
	if c.reaper != nil && c.reaper.Active() {
		return c.checkExecReaped(ctx)
	}
	cmd := exec.CommandContext(ctx, c.cfg.Exec.Command, c.cfg.Exec.Args...)
	// Setsid gives the check its own process group (so Kill(-pid) on cancel
	// reaches all descendants, I2) and no controlling TTY (blocks TIOCSTI
	// injection).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if c.credential != nil {
		cmd.SysProcAttr.Credential = c.credential
	}
	// Signal the whole process group on cancel so forked grandchildren die too;
	// the default Cancel only kills the leader, leaving descendants reparented to
	// PID 1 with side effects still in flight. WaitDelay bounds the grace period
	// before SIGKILL is forced.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		// ESRCH means the process already exited (a probe that passed right at the
		// deadline). Report ErrProcessDone, like the stdlib default Cancel, so
		// os/exec keeps the real exit status instead of failing the probe.
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = c.timeout
	if err := cmd.Run(); err != nil {
		// ECHILD means a concurrent Wait4(-1) (the reap loop) stole the exit
		// status: the probe's real outcome is unknowable, not a failure.
		if errors.Is(err, syscall.ECHILD) {
			return fmt.Errorf("exec check: %w (%v)", ErrInconclusive, err)
		}
		return fmt.Errorf("exec check: %w", err)
	}
	return nil
}

// checkExecReaped forks the probe and takes its exit status from the reap
// loop via the registry, never waiting on the pid itself.
func (c *Checker) checkExecReaped(ctx context.Context) error {
	cmd := exec.Command(c.cfg.Exec.Command, c.cfg.Exec.Args...)
	// Same containment as the standalone path: own process group, no TTY.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if c.credential != nil {
		cmd.SysProcAttr.Credential = c.credential
	}
	pid, status, err := c.reaper.Start(func() (int, error) {
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		return cmd.Process.Pid, nil
	})
	if err != nil {
		return fmt.Errorf("exec check: %w", err)
	}
	defer c.reaper.Forget(pid)
	// The reap loop owns the wait; just close the process handle.
	defer func() { _ = cmd.Process.Release() }()

	select {
	case code := <-status:
		if code != 0 {
			return fmt.Errorf("exec check: exit status %d", code)
		}
		return nil
	case <-ctx.Done():
		// Kill the whole group so forked grandchildren die too. ESRCH means
		// the leader already exited; its status is still in flight below.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		timer := time.NewTimer(c.timeout)
		defer timer.Stop()
		select {
		case code := <-status:
			if code == 0 {
				// Finished cleanly right at the deadline: keep the real result.
				return nil
			}
			return fmt.Errorf("exec check: %w", ctx.Err())
		case <-timer.C:
			// SIGKILLed but never reaped: status lost, health unknowable.
			return fmt.Errorf("exec check: %w (child not reaped after kill)", ErrInconclusive)
		}
	}
}

// SetReaper routes exec probe exit statuses through the daemon reap loop,
// the sole Wait4(-1) owner; a targeted cmd.Wait would race it and lose.
func (c *Checker) SetReaper(r *reaper.Registry) {
	c.reaper = r
}

// SetCredential sets the credential for exec health checks so they run
// as the associated service's user instead of as root.
func (c *Checker) SetCredential(cred *syscall.Credential) {
	c.credential = cred
}
