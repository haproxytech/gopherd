// Package service manages individual process lifecycles including start, stop, signal, and restart.
package service

import (
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/haproxytech/go-init/backoff"
	"github.com/haproxytech/go-init/logger"
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
func ParseExitAction(s string, defaultAction ExitAction) ExitAction {
	switch ExitAction(s) {
	case ActionRestart, ActionShutdown, ActionSuccessShutdown, ActionFailureShutdown, ActionIgnore:
		return ExitAction(s)
	case "":
		return defaultAction
	default:
		log.Fatalf("unknown exit action: %q", s)
		return defaultAction
	}
}

// Process holds the configuration for a single process.
type Process struct {
	UserID         *int             
	GroupID        *int             
	Environment    map[string]string
	OnCheckFailure map[string]string
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
	ExtraArgs      string
	Args           []string
	After          []string         
	Before         []string         
	Requires       []string         
	BackoffFactor  float64
	Prefix         string
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
	Name      string
	OnSuccess ExitAction
	OnFailure ExitAction

	Proc Process

	stopSignal syscall.Signal
	killDelay  time.Duration
	Pid        int

	mu      sync.Mutex
	Enabled bool
	Oneshot bool

	running bool
	stopped bool // true if Stop() was called (we initiated the exit)
}

// New creates a new Service from a Process config.
// globalPrefix is the top-level prefix setting; per-process Prefix overrides it.
func New(p Process, globalPrefix string) *Service {
	name := p.Name
	if name == "" {
		name = p.Command
	}

	enabled := p.Startup != "disabled"
	oneshot := p.Startup == "oneshot"

	stopSig, err := ParseSignal(p.StopSignal)
	if err != nil {
		log.Fatalf("process %s: %v", name, err)
	}

	killDelay := DefaultKillDelay
	if p.KillDelay != "" {
		killDelay, err = time.ParseDuration(p.KillDelay)
		if err != nil {
			log.Fatalf("process %s: invalid kill-delay %q: %v", name, p.KillDelay, err)
		}
	}

	var backoffDelay time.Duration
	if p.BackoffDelay != "" {
		backoffDelay, err = time.ParseDuration(p.BackoffDelay)
		if err != nil {
			log.Fatalf("process %s: invalid backoff-delay %q: %v", name, p.BackoffDelay, err)
		}
	}

	var backoffLimit time.Duration
	if p.BackoffLimit != "" {
		backoffLimit, err = time.ParseDuration(p.BackoffLimit)
		if err != nil {
			log.Fatalf("process %s: invalid backoff-limit %q: %v", name, p.BackoffLimit, err)
		}
	}

	reqSet := make(map[string]bool)
	for _, r := range p.Requires {
		reqSet[r] = true
	}

	checkFailMap := make(map[string]ExitAction)
	for checkName, actionStr := range p.OnCheckFailure {
		checkFailMap[checkName] = ParseExitAction(actionStr, ActionRestart)
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
		OnSuccess:      ParseExitAction(p.OnSuccess, ActionShutdown),
		OnFailure:      ParseExitAction(p.OnFailure, ActionShutdown),
		Backoff:        backoff.New(backoffDelay, p.BackoffFactor, backoffLimit),
		Requires:       reqSet,
		OnCheckFailure: checkFailMap,
		Stdout:         logger.NewPrefixWriter(os.Stdout, name, prefix),
		Stderr:         logger.NewPrefixWriter(os.Stderr, name, prefix),
	}
}

// Start launches the process. Returns the PID on success.
func (s *Service) Start() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cmd := exec.Command(s.Proc.Command, s.Proc.Args...)
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if s.Proc.WorkingDir != "" {
		cmd.Dir = s.Proc.WorkingDir
	}

	if len(s.Proc.Environment) > 0 {
		cmd.Env = os.Environ()
		for k, v := range s.Proc.Environment {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	cred, err := ResolveCredential(s.Proc.User, s.Proc.Group, s.Proc.UserID, s.Proc.GroupID)
	if err != nil {
		return 0, err
	}
	if cred != nil {
		cmd.SysProcAttr.Credential = cred
	}

	if err := cmd.Start(); err != nil {
		return 0, err
	}

	s.cmd = cmd
	s.Pid = cmd.Process.Pid
	s.running = true
	s.stopped = false
	s.startedAt = time.Now()
	return s.Pid, nil
}

// Stop sends the configured stop signal and schedules SIGKILL after kill-delay.
// The stopped flag is set so the reap loop knows this was an intentional exit.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	s.stopped = true
	_ = s.cmd.Process.Signal(s.stopSignal)
	pid := s.Pid
	delay := s.killDelay
	if delay > 0 {
		time.AfterFunc(delay, func() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		})
	}
}

// Signal sends an arbitrary signal to the process.
func (s *Service) Signal(sig os.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Signal(sig)
}

// MarkExited marks the service as no longer running and returns how long it ran.
func (s *Service) MarkExited() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	return time.Since(s.startedAt)
}

// WasStopped returns true if the service exited because we called Stop()
// (as opposed to exiting on its own). This distinguishes intentional signal-death
// from unexpected exits for the purpose of exit code propagation.
func (s *Service) WasStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

// IsRunning returns whether the service is currently running.
func (s *Service) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}
