// Package main implements go-init, a minimal PID 1 init process and service supervisor.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	goSignal "os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/haproxytech/go-init/check"
	"github.com/haproxytech/go-init/control"
	"github.com/haproxytech/go-init/logger"
	"github.com/haproxytech/go-init/metrics"
	"github.com/haproxytech/go-init/order"
	"github.com/haproxytech/go-init/service"
	"github.com/haproxytech/go-init/version"
	"github.com/haproxytech/go-init/yml"
)

const defaultConfigPath = "/etc/go-init.yml"

// daemon holds all mutable daemon state so reload can update it.
type daemon struct {
	cfg       *yml.Config
	services  map[string]*service.Service
	m         *metrics.Metrics
	pidMap    map[int]*service.Service
	restartCh chan restartReq

	configPath string
	checkers   []*check.Checker
	logTargets []*logger.Target
	exitCode   int

	mu           sync.Mutex
	shuttingDown bool
}

type restartReq struct {
	svc   *service.Service
	delay time.Duration
}

func (d *daemon) startService(svc *service.Service) error {
	pid, err := svc.Start()
	if err != nil {
		return err
	}
	log.Printf("started %s (pid %d)", svc.Name, pid)
	d.m.ServiceStarted(svc.Name)
	d.mu.Lock()
	d.pidMap[pid] = svc
	d.mu.Unlock()
	return nil
}

func (d *daemon) stopAll() {
	for _, svc := range d.services {
		svc.Stop()
	}
}

func (d *daemon) initiateShutdown(code int) {
	d.shuttingDown = true
	d.exitCode = code
	d.stopAll()
}

func (d *daemon) handleCheckFailure(checkName string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.shuttingDown {
		return
	}
	for _, svc := range d.services {
		action, ok := svc.OnCheckFailure[checkName]
		if !ok {
			continue
		}
		log.Printf("check %s failed: %s %s", checkName, action, svc.Name)
		switch action {
		case service.ActionRestart:
			svc.Stop()
		case service.ActionShutdown:
			d.initiateShutdown(1)
		case service.ActionSuccessShutdown:
			d.initiateShutdown(0)
		case service.ActionFailureShutdown:
			d.initiateShutdown(1)
		case service.ActionIgnore:
			// do nothing
		}
	}
}

func (d *daemon) buildLogTargets() {
	for name, ltCfg := range d.cfg.LogTargets {
		lt, err := logger.NewTarget(name, ltCfg)
		if err != nil {
			log.Printf("warning: log-target %s: %v", name, err)
			continue
		}
		d.logTargets = append(d.logTargets, lt)
		log.Printf("configured log-target %s (%s)", name, ltCfg.Type)
	}
}

func (d *daemon) buildServices() {
	d.services = make(map[string]*service.Service)
	for _, p := range d.cfg.Processes {
		svc := service.New(p, d.cfg.Prefix)
		d.services[svc.Name] = svc
		for _, lt := range d.logTargets {
			if lt.AppliesTo(svc.Name) {
				svc.Stdout.AddTarget(lt.Writer)
				svc.Stderr.AddTarget(lt.Writer)
			}
		}
	}
}

func (d *daemon) startOrder() ([]string, error) {
	orderServices := make([]order.Service, len(d.cfg.Processes))
	for i, p := range d.cfg.Processes {
		name := p.Name
		if name == "" {
			name = p.Command
		}
		orderServices[i] = order.Service{
			Name:     name,
			After:    p.After,
			Before:   p.Before,
			Requires: p.Requires,
		}
	}
	return order.TopoSort(orderServices)
}

func (d *daemon) startChecks() {
	for name, checkCfg := range d.cfg.Checks {
		c, err := check.New(name, checkCfg, d.handleCheckFailure, d.m.CheckResult)
		if err != nil {
			log.Printf("warning: check %s: %v", name, err)
			continue
		}
		d.checkers = append(d.checkers, c)
		c.Run()
		log.Printf("started check %s", name)
	}
}

func (d *daemon) stopChecks() {
	for _, c := range d.checkers {
		c.Stop()
	}
	d.checkers = nil
}

func (d *daemon) closeLogTargets() {
	for _, svc := range d.services {
		svc.Stdout.Flush()
		svc.Stderr.Flush()
	}
	for _, lt := range d.logTargets {
		lt.Close()
	}
	d.logTargets = nil
}

// reload re-reads the config and reconciles services, checks, and log targets.
func (d *daemon) reload() (string, error) {
	newCfg, err := yml.Load(d.configPath)
	if err != nil {
		return "", fmt.Errorf("reload config: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.shuttingDown {
		return "", fmt.Errorf("shutting down, reload not possible")
	}

	// Build new service set from config.
	newNames := make(map[string]bool)
	for _, p := range newCfg.Processes {
		name := p.Name
		if name == "" {
			name = p.Command
		}
		newNames[name] = true
	}

	// Stop and remove services that are no longer in config.
	for name, svc := range d.services {
		if !newNames[name] {
			log.Printf("reload: removing service %s", name)
			if svc.IsRunning() {
				svc.Stop()
			}
			delete(d.services, name)
		}
	}

	// Stop old checks, rebuild from new config.
	d.stopChecks()

	// Update config and rebuild services.
	oldServices := d.services
	d.cfg = newCfg
	d.buildServices()

	// Preserve running state: if a service was running and still exists with same command, keep it.
	for name, oldSvc := range oldServices {
		newSvc, exists := d.services[name]
		if !exists {
			continue
		}
		if oldSvc.IsRunning() && oldSvc.Proc.Command == newSvc.Proc.Command {
			// Same service still running — keep the old one, transfer pidMap entries.
			d.services[name] = oldSvc
		} else if oldSvc.IsRunning() {
			// Command changed — stop old, will start new below.
			log.Printf("reload: restarting changed service %s", name)
			oldSvc.Stop()
		}
	}

	// Start new or changed services.
	startOrd, err := d.startOrder()
	if err != nil {
		return "", fmt.Errorf("reload dependencies: %w", err)
	}

	for _, name := range startOrd {
		svc := d.services[name]
		if !svc.Enabled || svc.Oneshot {
			continue
		}
		if svc.IsRunning() {
			continue // already running (preserved from old config)
		}
		if err := d.startService(svc); err != nil {
			log.Printf("reload: start %s failed: %v", name, err)
		}
	}

	// Restart checks with new config.
	d.startChecks()

	return "reload: ok", nil
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("go-init: ")

	_ = version.Set()

	// CLI client mode or passthrough exec.
	if len(os.Args) > 1 {
		first := os.Args[1]
		if control.ClientCommands[first] {
			control.RunClient(os.Args[1:])
			return
		}
		if first == "version" {
			fmt.Println("go-init", version.Version)
			fmt.Println("built from:", version.Repo)
			fmt.Println("commit date:", version.CommitDate)
			return
		}
		if first == "tag" {
			fmt.Println(version.Tag)
			return
		}
		// Passthrough: exec the command directly, replacing this process.
		path, err := exec.LookPath(first)
		if err != nil {
			fmt.Fprintf(os.Stderr, "go-init: %q not found (not a client command and not on PATH)\n", first)
			fmt.Fprintf(os.Stderr, "Client commands: %s\n", strings.Join(control.ClientCommandList(), ", "))
			os.Exit(1)
		}
		if err := syscall.Exec(path, os.Args[1:], os.Environ()); err != nil {
			log.Fatalf("exec %s: %v", path, err)
		}
	}

	configPath := defaultConfigPath
	if v := os.Getenv("GO_INIT_CONFIG"); v != "" {
		configPath = v
	}

	cfg, err := yml.Load(configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	d := &daemon{
		configPath: configPath,
		cfg:        cfg,
		pidMap:     make(map[int]*service.Service),
		restartCh:  make(chan restartReq, 64),
	}

	// Initialize stats tracking.
	d.m = metrics.New()

	// Compute start order from dependencies.
	startOrd, err := d.startOrder()
	if err != nil {
		log.Fatalf("dependencies: %v", err)
	}

	// Build log targets and services.
	d.buildLogTargets()
	d.buildServices()

	// Start enabled services in dependency order.
	for _, name := range startOrd {
		svc := d.services[name]
		if !svc.Enabled {
			log.Printf("skipping disabled service %s", svc.Name)
			continue
		}

		if svc.Oneshot {
			pid, err := svc.Start()
			if err != nil {
				log.Fatalf("oneshot %s: %v", svc.Name, err)
			}
			log.Printf("started oneshot %s (pid %d)", svc.Name, pid)
			var ws syscall.WaitStatus
			syscall.Wait4(pid, &ws, 0, nil)
			code := waitStatusCode(ws)
			svc.MarkExited()
			if code != 0 {
				if svc.OnFailure == service.ActionIgnore {
					log.Printf("oneshot %s exited with status %d (ignored)", svc.Name, code)
					continue
				}
				log.Fatalf("oneshot %s failed (status %d)", svc.Name, code)
			}
			log.Printf("oneshot %s completed", svc.Name)
			continue
		}

		if err := d.startService(svc); err != nil {
			log.Fatalf("start %s: %v", svc.Name, err)
		}

		if svc.Proc.ReadyCheck != "" {
			checkCfg, ok := cfg.Checks[svc.Proc.ReadyCheck]
			if !ok {
				log.Fatalf("%s: ready-check %q not found in [checks]", svc.Name, svc.Proc.ReadyCheck)
			}
			c, err := check.New(svc.Proc.ReadyCheck, checkCfg, nil, nil)
			if err != nil {
				log.Fatalf("%s: ready check: %v", svc.Name, err)
			}
			readyTimeout := 60 * time.Second
			if svc.Proc.ReadyTimeout != "" {
				readyTimeout, err = time.ParseDuration(svc.Proc.ReadyTimeout)
				if err != nil {
					log.Fatalf("%s: invalid ready-timeout %q: %v", svc.Name, svc.Proc.ReadyTimeout, err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
			err = c.WaitReady(ctx)
			cancel()
			if err != nil {
				log.Fatalf("%s: ready-check %q did not pass within %s", svc.Name, svc.Proc.ReadyCheck, readyTimeout)
			}
			log.Printf("%s: ready (check %s passed)", svc.Name, svc.Proc.ReadyCheck)
		}
	}

	// Start health checks.
	d.startChecks()

	// Start the control socket server.
	ctrlServer := control.NewServer(cfg.Control)
	ctrlServer.StatsFn = func() string {
		return d.m.Format()
	}
	ctrlServer.ListFn = func() string {
		d.mu.Lock()
		defer d.mu.Unlock()
		var lines []string
		for _, svc := range d.services {
			state := "stopped"
			if svc.IsRunning() {
				state = fmt.Sprintf("running (pid %d)", svc.Pid)
			}
			lines = append(lines, fmt.Sprintf("%-20s %s", svc.Name, state))
		}
		if len(lines) == 0 {
			return "no services"
		}
		return strings.Join(lines, "\n")
	}
	ctrlServer.StatusFn = func(name string) (string, error) {
		d.mu.Lock()
		svc, ok := d.services[name]
		d.mu.Unlock()
		if !ok {
			return "", fmt.Errorf("unknown service %q", name)
		}
		if svc.IsRunning() {
			return fmt.Sprintf("%s: running (pid %d)", name, svc.Pid), nil
		}
		return fmt.Sprintf("%s: stopped", name), nil
	}
	ctrlServer.StartFn = func(name string) (string, error) {
		d.mu.Lock()
		svc, ok := d.services[name]
		d.mu.Unlock()
		if !ok {
			return "", fmt.Errorf("unknown service %q", name)
		}
		if svc.IsRunning() {
			return fmt.Sprintf("%s: already running (pid %d)", name, svc.Pid), nil
		}
		if err := d.startService(svc); err != nil {
			return "", fmt.Errorf("start %s: %w", name, err)
		}
		return fmt.Sprintf("%s: started (pid %d)", name, svc.Pid), nil
	}
	ctrlServer.StopFn = func(name string) (string, error) {
		d.mu.Lock()
		svc, ok := d.services[name]
		d.mu.Unlock()
		if !ok {
			return "", fmt.Errorf("unknown service %q", name)
		}
		if !svc.IsRunning() {
			return fmt.Sprintf("%s: already stopped", name), nil
		}
		svc.Stop()
		return fmt.Sprintf("%s: stop signal sent", name), nil
	}
	ctrlServer.SignalFn = func(name, sigName string) (string, error) {
		d.mu.Lock()
		svc, ok := d.services[name]
		d.mu.Unlock()
		if !ok {
			return "", fmt.Errorf("unknown service %q", name)
		}
		if !svc.IsRunning() {
			return "", fmt.Errorf("%s is not running", name)
		}
		sig, err := service.ParseSignal(sigName)
		if err != nil {
			return "", err
		}
		svc.Signal(sig)
		return fmt.Sprintf("%s: sent %s", name, sigName), nil
	}
	ctrlServer.RestartFn = func(name string) (string, error) {
		d.mu.Lock()
		svc, ok := d.services[name]
		d.mu.Unlock()
		if !ok {
			return "", fmt.Errorf("unknown service %q", name)
		}
		if svc.IsRunning() {
			svc.Stop()
		}
		d.restartCh <- restartReq{svc: svc, delay: 0}
		return fmt.Sprintf("%s: restart scheduled", name), nil
	}
	ctrlServer.ReloadFn = func() (string, error) {
		return d.reload()
	}
	ctrlServer.LogsFn = func(name string, follow bool) ([][]byte, <-chan []byte, func(), error) {
		d.mu.Lock()
		svc, ok := d.services[name]
		d.mu.Unlock()
		if !ok {
			return nil, nil, nil, fmt.Errorf("unknown service %q", name)
		}
		recent := svc.Stdout.Recent()
		if !follow {
			return recent, nil, nil, nil
		}
		ch, unsub := svc.Stdout.Subscribe()
		return recent, ch, unsub, nil
	}
	if err := ctrlServer.Start(); err != nil {
		log.Printf("warning: control socket: %v", err)
	} else {
		log.Printf("control socket: %s", ctrlServer.SocketPath)
	}

	// Forward signals to all children. SIGHUP triggers reload.
	sigs := make(chan os.Signal, 16)
	goSignal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range sigs {
			d.mu.Lock()
			sysSig := sig.(syscall.Signal)
			switch {
			case sysSig == syscall.SIGTERM || sysSig == syscall.SIGINT:
				if !d.shuttingDown {
					d.initiateShutdown(0)
				}
			case sysSig == syscall.SIGHUP:
				d.mu.Unlock()
				msg, err := d.reload()
				if err != nil {
					log.Printf("reload failed: %v", err)
				} else {
					log.Printf("%s", msg)
				}
				continue
			default:
				for _, svc := range d.services {
					svc.Signal(sig)
				}
			}
			d.mu.Unlock()
		}
	}()

	// Handle restart requests from the reap loop.
	go func() {
		for req := range d.restartCh {
			time.Sleep(req.delay)
			d.mu.Lock()
			if d.shuttingDown {
				d.mu.Unlock()
				continue
			}
			d.mu.Unlock()
			if err := d.startService(req.svc); err != nil {
				log.Printf("restart %s failed: %v", req.svc.Name, err)
				d.mu.Lock()
				if !d.shuttingDown {
					d.initiateShutdown(1)
				}
				d.mu.Unlock()
			}
		}
	}()

	// Single reap loop: handles managed children and orphaned zombies.
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err != nil {
			break
		}
		if pid <= 0 {
			continue
		}

		code := waitStatusCode(ws)

		d.mu.Lock()
		svc, isManaged := d.pidMap[pid]
		if isManaged {
			delete(d.pidMap, pid)
			runDuration := svc.MarkExited()
			log.Printf("%s exited (status %d)", svc.Name, code)
			d.m.ServiceExited(svc.Name, code)

			if d.shuttingDown {
				anyRunning := false
				for _, s := range d.services {
					if s.IsRunning() {
						anyRunning = true
						break
					}
				}
				d.mu.Unlock()
				if !anyRunning {
					break
				}
				continue
			}

			// If we intentionally stopped the service (via Stop()), treat
			// the signal-death exit code as 0 for action evaluation.
			// This prevents stop-signal deaths from looking like crashes.
			effectiveCode := code
			if svc.WasStopped() && code > 128 {
				effectiveCode = 0
			}

			success := effectiveCode == 0
			var action service.ExitAction
			if success {
				action = svc.OnSuccess
			} else {
				action = svc.OnFailure
				for _, other := range d.services {
					if other.Requires[svc.Name] && other.IsRunning() {
						log.Printf("stopping %s: required service %s failed", other.Name, svc.Name)
						other.Stop()
					}
				}
			}

			switch action {
			case service.ActionRestart:
				if runDuration >= svc.Backoff.Limit {
					svc.Backoff.Reset()
				}
				delay := svc.Backoff.Next()
				log.Printf("restarting %s in %s", svc.Name, delay)
				d.m.ServiceRestarted(svc.Name)
				d.mu.Unlock()
				d.restartCh <- restartReq{svc: svc, delay: delay}
				continue

			case service.ActionShutdown:
				d.initiateShutdown(effectiveCode)

			case service.ActionSuccessShutdown:
				d.initiateShutdown(0)

			case service.ActionFailureShutdown:
				d.initiateShutdown(effectiveCode)

			case service.ActionIgnore:
				log.Printf("%s: ignoring exit", svc.Name)
				d.mu.Unlock()
				continue
			}
		}
		d.mu.Unlock()

		if d.shuttingDown {
			d.mu.Lock()
			anyRunning := false
			for _, s := range d.services {
				if s.IsRunning() {
					anyRunning = true
					break
				}
			}
			d.mu.Unlock()
			if !anyRunning {
				break
			}
		}
	}

	ctrlServer.Stop()
	d.stopChecks()
	d.closeLogTargets()

	os.Exit(d.exitCode)
}

func waitStatusCode(ws syscall.WaitStatus) int {
	if ws.Exited() {
		return ws.ExitStatus()
	}
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}
