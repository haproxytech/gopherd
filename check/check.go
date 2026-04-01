// Package check provides periodic health checks (HTTP, TCP, exec).
package check

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

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
	Level        string         // "alive" or "ready"
	Threshold    int   
}

// Checker runs a single health check on a periodic loop.
type Checker struct {
	stopCh       chan struct{}
	onFailureFn  func(checkName string)          // called when threshold breached
	metricsFn    func(checkName string, ok bool) // called after every check
	credential   *syscall.Credential             // optional: run exec checks as this user
	name         string
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

	return &Checker{
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
	}, nil
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
			if err != nil {
				c.failures++
				if c.metricsFn != nil {
					c.metricsFn(c.name, false)
				}
				if c.failures >= c.threshold && c.healthy {
					c.healthy = false
					log.Printf("check %s: unhealthy (%d consecutive failures): %v", c.name, c.failures, err)
					if c.onFailureFn != nil {
						c.onFailureFn(c.name)
					}
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

			select {
			case <-ticker.C:
			case <-c.stopCh:
				return
			}
		}
	}()
}

// WaitReady runs the check in a loop until it passes once or ctx is cancelled.
func (c *Checker) WaitReady(ctx context.Context) error {
	if err := c.Execute(); err == nil {
		return nil
	}
	ticker := time.NewTicker(c.period)
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
		return fmt.Errorf("no check type configured")
	}
}

func (c *Checker) checkHTTP(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.HTTP.URL, nil)
	if err != nil {
		return fmt.Errorf("http check: %w", err)
	}
	client := &http.Client{Timeout: c.timeout}
	if c.cfg.HTTP.Socket != "" {
		client.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", c.cfg.HTTP.Socket)
			},
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http check: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http check: status %d", resp.StatusCode)
	}
	return nil
}

func (c *Checker) checkTCP(ctx context.Context) error {
	host := c.cfg.TCP.Host
	if host == "" {
		host = "localhost"
	}
	addr := fmt.Sprintf("%s:%d", host, c.cfg.TCP.Port)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("tcp check %s: %w", addr, err)
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
