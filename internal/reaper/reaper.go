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

// Package reaper routes exit statuses of unmanaged children from the daemon's
// Wait4(-1) reap loop to the goroutine that forked them. A targeted cmd.Wait()
// would race the reap loop for the status and lose (ECHILD); registering here
// makes the reap loop the single waiter.
package reaper

import (
	"sync"
	"sync/atomic"
)

// Registry maps forked pids to their status channels.
type Registry struct {
	wake    func() // wakes the reap loop from its no-children idle wait
	waiters map[int]chan int
	mu      sync.Mutex
	active  atomic.Bool
}

// New creates a Registry. wake (optional) is called after each successful
// Start so an idling reap loop resumes Wait4.
func New(wake func()) *Registry {
	return &Registry{
		waiters: make(map[int]chan int),
		wake:    wake,
	}
}

// Activate marks the reap loop as the sole Wait4(-1) owner. Before this,
// callers must wait on their children directly.
func (r *Registry) Activate() {
	r.active.Store(true)
}

// Active reports whether the reap loop delivers statuses.
func (r *Registry) Active() bool {
	return r.active.Load()
}

// Start forks via fork() and registers the returned pid under the registry
// lock, so a concurrent Deliver cannot observe the pid before registration.
// The returned channel receives the child's exit code exactly once.
func (r *Registry) Start(fork func() (int, error)) (int, <-chan int, error) {
	r.mu.Lock()
	pid, err := fork()
	if err != nil {
		r.mu.Unlock()
		return 0, nil, err
	}
	status := make(chan int, 1)
	r.waiters[pid] = status
	r.mu.Unlock()
	if r.wake != nil {
		r.wake()
	}
	return pid, status, nil
}

// Deliver hands a reaped pid's exit code to its waiter and reports whether
// one claimed it. Never blocks: status channels are buffered and receive a
// single send.
func (r *Registry) Deliver(pid, code int) bool {
	r.mu.Lock()
	status, ok := r.waiters[pid]
	if ok {
		delete(r.waiters, pid)
		status <- code
	}
	r.mu.Unlock()
	return ok
}

// Forget drops the waiter for pid. No-op if already delivered.
func (r *Registry) Forget(pid int) {
	r.mu.Lock()
	delete(r.waiters, pid)
	r.mu.Unlock()
}
