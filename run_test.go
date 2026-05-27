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

package main

import (
	"fmt"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/order"
	"github.com/haproxytech/gopherd/service"
)

// TestStartupTiming reports wall-clock time to bring up N independent
// long-running services in a single layer. Run with -v to see the number:
//
//	go test -run TestStartupTiming -v ./
//
// Useful as a coarse before/after gauge when changing the startup hot path.
// Not a regression gate — there's no assertion on duration.
func TestStartupTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("startup timing spawns real processes")
	}
	cases := []struct {
		n int
	}{
		{1}, {5}, {10}, {20},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("n=%d", tc.n), func(t *testing.T) {
			procs := make([]service.Process, tc.n)
			for i := range procs {
				procs[i] = service.Process{
					Name:    fmt.Sprintf("svc-%d", i),
					Command: "/bin/sleep",
					Args:    []string{"300"},
				}
			}
			d := newTestDaemon(procs)
			defer killAllChildren(d)

			for _, svc := range d.services {
				d.m.RegisterService(svc.Name, svc.Enabled)
			}
			layers, err := order.TopoLayers(buildOrderServices(d.cfg))
			if err != nil {
				t.Fatalf("topo: %v", err)
			}

			start := time.Now()
			d.startServiceLayers(d.cfg, layers)
			parElapsed := time.Since(start)
			killAllChildren(d)
			waitChildrenGone(d)

			// Sequential baseline for comparison. Rebuild the daemon — the same
			// services can't be started twice.
			d2 := newTestDaemon(procs)
			defer killAllChildren(d2)
			for _, svc := range d2.services {
				d2.m.RegisterService(svc.Name, svc.Enabled)
			}
			start = time.Now()
			startLayerSequential(d2, layers)
			seqElapsed := time.Since(start)

			t.Logf("n=%2d  parallel=%v  sequential=%v  speedup=%.2fx",
				tc.n,
				parElapsed.Round(time.Microsecond),
				seqElapsed.Round(time.Microsecond),
				float64(seqElapsed)/float64(parElapsed))
		})
	}
}

// TestStartupTimingGated demonstrates the parallel speedup that actually
// matters: when services have any gating delay (ready-check, sd_notify), the
// gates run in goroutines and overlap. Simulated via a sleep inside the
// startup helper rather than a real readiness probe.
func TestStartupTimingGated(t *testing.T) {
	if testing.Short() {
		t.Skip("startup timing spawns real processes")
	}
	const (
		n        = 8
		gateWait = 100 * time.Millisecond
	)
	procs := make([]service.Process, n)
	for i := range procs {
		procs[i] = service.Process{
			Name:    fmt.Sprintf("svc-%d", i),
			Command: "/bin/sleep",
			Args:    []string{"300"},
		}
	}

	d := newTestDaemon(procs)
	defer killAllChildren(d)
	for _, svc := range d.services {
		d.m.RegisterService(svc.Name, svc.Enabled)
	}
	layers, _ := order.TopoLayers(buildOrderServices(d.cfg))

	// Parallel: each goroutine sleeps then forks.
	start := time.Now()
	var wg sync.WaitGroup
	for _, name := range layers[0] {
		svc := d.services[name]
		wg.Go(func() {
			time.Sleep(gateWait)
			d.startService(svc)
		})
	}
	wg.Wait()
	parElapsed := time.Since(start)
	killAllChildren(d)
	waitChildrenGone(d)

	// Sequential.
	d2 := newTestDaemon(procs)
	defer killAllChildren(d2)
	start = time.Now()
	for _, name := range layers[0] {
		svc := d2.services[name]
		time.Sleep(gateWait)
		d2.startService(svc)
	}
	seqElapsed := time.Since(start)

	t.Logf("gated n=%d gate=%v  parallel=%v  sequential=%v  speedup=%.2fx",
		n, gateWait,
		parElapsed.Round(time.Millisecond),
		seqElapsed.Round(time.Millisecond),
		float64(seqElapsed)/float64(parElapsed))
}

// killAllChildren SIGKILLs every running service so the test does not leave
// 5-minute sleep processes around. Zombies are reaped by the test runner's
// reparent-to-init when the test binary exits.
func killAllChildren(d *daemon) {
	for _, svc := range d.services {
		if !svc.IsRunning() {
			continue
		}
		pid := int(svc.Pid.Load())
		if pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}
}

// waitChildrenGone gives killed children a moment to be reparented; without
// this, the back-to-back sequential run can race with the first batch's
// teardown and skew the timing.
func waitChildrenGone(_ *daemon) {
	time.Sleep(50 * time.Millisecond)
}

// startLayerSequential is the pre-parallelism start path, kept as a baseline
// for TestStartupTiming so the speedup of startServiceLayers is visible. It
// only handles non-gated services (no ready-check, no sd_notify) — sufficient
// for the timing test, not a substitute for the production code path.
func startLayerSequential(d *daemon, startLayers [][]string) {
	for _, layer := range startLayers {
		for _, name := range layer {
			svc, ok := d.services[name]
			if !ok || !svc.Enabled || svc.Oneshot || svc.IsRunning() {
				continue
			}
			if _, err := d.startService(svc); err != nil {
				return
			}
		}
	}
}
