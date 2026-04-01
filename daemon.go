package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/haproxytech/gopherd/check"
	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/logger"
	"github.com/haproxytech/gopherd/metrics"
	"github.com/haproxytech/gopherd/order"
	"github.com/haproxytech/gopherd/service"
	"github.com/haproxytech/gopherd/yml"
)

// daemon holds all mutable daemon state so reload can update it.
type daemon struct {
	cfg       *yml.Config
	services  map[string]*service.Service
	m         *metrics.Metrics
	pidMap    map[int]*service.Service
	restartCh chan restartReq

	configPath     string
	checkers       []*check.Checker
	logTargets     []*logger.Target
	entrypointArgs []string
	exitCode       int

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
		// Inject entrypoint args into the designated service.
		if p.UseEntrypointArgs && len(d.entrypointArgs) > 0 {
			p.Args = append(p.Args, d.entrypointArgs...)
		}
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

	if d.shuttingDown {
		d.mu.Unlock()
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

	// Compute start order while still holding the lock.
	startOrd, err := d.startOrder()
	if err != nil {
		d.mu.Unlock()
		return "", fmt.Errorf("reload dependencies: %w", err)
	}

	// Collect services that need starting.
	var toStart []*service.Service
	for _, name := range startOrd {
		svc := d.services[name]
		if !svc.Enabled || svc.Oneshot {
			continue
		}
		if svc.IsRunning() {
			continue // already running (preserved from old config)
		}
		toStart = append(toStart, svc)
	}

	// Release the lock before starting services, since startService
	// acquires d.mu internally to update pidMap.
	d.mu.Unlock()

	for _, svc := range toStart {
		if err := d.startService(svc); err != nil {
			log.Printf("reload: start %s failed: %v", svc.Name, err)
		}
	}

	// Restart checks with new config.
	d.startChecks()

	return "reload: ok", nil
}

// setupControl wires all control-socket handler callbacks and starts the server.
func (d *daemon) setupControl() *control.Server {
	ctrlServer := control.NewServer(d.cfg.Control)
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
	return ctrlServer
}
