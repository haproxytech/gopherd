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

package order

import (
	"strings"
	"testing"
)

func TestNoDeps(t *testing.T) {
	t.Parallel()
	order, err := TopoSort([]Service{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("expected 3, got %d", len(order))
	}
}

func TestAfter(t *testing.T) {
	t.Parallel()
	order, err := TopoSort([]Service{
		{Name: "b", After: []string{"a"}},
		{Name: "a"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if order[0] != "a" || order[1] != "b" {
		t.Errorf("expected [a b], got %v", order)
	}
}

func TestBefore(t *testing.T) {
	t.Parallel()
	order, err := TopoSort([]Service{
		{Name: "a", Before: []string{"b"}},
		{Name: "b"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if order[0] != "a" || order[1] != "b" {
		t.Errorf("expected [a b], got %v", order)
	}
}

func TestRequires(t *testing.T) {
	t.Parallel()
	order, err := TopoSort([]Service{
		{Name: "b", Requires: []string{"a"}},
		{Name: "a"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if order[0] != "a" {
		t.Errorf("expected a first, got %v", order)
	}
}

func TestCycleDetected(t *testing.T) {
	t.Parallel()
	_, err := TopoSort([]Service{
		{Name: "a", After: []string{"b"}},
		{Name: "b", After: []string{"a"}},
	})
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Errorf("cycle error should name involved services, got: %v", err)
	}
}

func TestDuplicateName(t *testing.T) {
	t.Parallel()
	_, err := TopoSort([]Service{
		{Name: "a"}, {Name: "a"},
	})
	if err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestUnknownDep(t *testing.T) {
	t.Parallel()
	_, err := TopoSort([]Service{
		{Name: "a", After: []string{"x"}},
	})
	if err == nil {
		t.Fatal("expected unknown dep error")
	}
}

func TestComplex(t *testing.T) {
	t.Parallel()
	order, err := TopoSort([]Service{
		{Name: "d", After: []string{"b", "c"}},
		{Name: "a"},
		{Name: "b", After: []string{"a"}},
		{Name: "c", After: []string{"a"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if order[0] != "a" {
		t.Errorf("expected a first, got %v", order)
	}
	if order[len(order)-1] != "d" {
		t.Errorf("expected d last, got %v", order)
	}
}

// TestAfterRequiresSameDepDeduplication verifies that when a service lists the
// same dependency in both After and Requires, addEdge must deduplicate so that
// inDegree is only incremented once. Without deduplication the node appears to
// have an extra incoming edge and Kahn's algorithm falsely reports a cycle.
func TestAfterRequiresSameDepDeduplication(t *testing.T) {
	t.Parallel()
	order, err := TopoSort([]Service{
		{Name: "a"},
		{Name: "b", After: []string{"a"}, Requires: []string{"a"}},
	})
	if err != nil {
		t.Fatalf("after+requires on same dep reported spurious cycle: %v", err)
	}
	if len(order) != 2 {
		t.Fatalf("expected 2 services, got %d: %v", len(order), order)
	}
	if order[0] != "a" || order[1] != "b" {
		t.Errorf("expected [a b], got %v", order)
	}
}

func TestLayersIndependent(t *testing.T) {
	t.Parallel()
	layers, err := TopoLayers([]Service{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(layers) != 1 {
		t.Fatalf("expected 1 layer for independent services, got %d: %v", len(layers), layers)
	}
	if got := strings.Join(layers[0], ","); got != "a,b,c" {
		t.Errorf("expected layer [a b c] sorted, got %q", got)
	}
}

func TestLayersChain(t *testing.T) {
	t.Parallel()
	layers, err := TopoLayers([]Service{
		{Name: "a"},
		{Name: "b", After: []string{"a"}},
		{Name: "c", After: []string{"b"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(layers) != 3 {
		t.Fatalf("expected 3 layers, got %d: %v", len(layers), layers)
	}
	if layers[0][0] != "a" || layers[1][0] != "b" || layers[2][0] != "c" {
		t.Errorf("expected a->b->c, got %v", layers)
	}
}

func TestLayersDiamond(t *testing.T) {
	t.Parallel()
	// a -> {b, c} -> d. b and c should share the middle layer.
	layers, err := TopoLayers([]Service{
		{Name: "a"},
		{Name: "b", After: []string{"a"}},
		{Name: "c", After: []string{"a"}},
		{Name: "d", After: []string{"b", "c"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(layers) != 3 {
		t.Fatalf("expected 3 layers, got %d: %v", len(layers), layers)
	}
	if len(layers[1]) != 2 || layers[1][0] != "b" || layers[1][1] != "c" {
		t.Errorf("expected middle layer [b c], got %v", layers[1])
	}
}

func TestLayersCycle(t *testing.T) {
	t.Parallel()
	_, err := TopoLayers([]Service{
		{Name: "a", After: []string{"b"}},
		{Name: "b", After: []string{"a"}},
	})
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle message, got %v", err)
	}
}

func TestLayersUnknownDep(t *testing.T) {
	t.Parallel()
	_, err := TopoLayers([]Service{
		{Name: "a", After: []string{"ghost"}},
	})
	if err == nil {
		t.Fatal("expected unknown-dep error")
	}
}

// TestLayersUnknownDepPerConstraint covers each constraint list separately.
// TopoLayers duplicates TopoSort's validation, so a check dropped from one of
// its three loops is invisible to a test that only exercises `after` — and
// invisible at the daemon level too, since run() calls TopoSort first. A case
// per loop is what keeps the two copies from drifting.
func TestLayersUnknownDepPerConstraint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		svc  Service
	}{
		{"after", Service{Name: "a", After: []string{"ghost"}}},
		{"before", Service{Name: "a", Before: []string{"ghost"}}},
		{"requires", Service{Name: "a", Requires: []string{"ghost"}}},
	} {
		// The message matters, not just the error: an edge to a missing name
		// leaves an in-degree nothing can decrement, so a dropped check still
		// errors — as a bogus "dependency cycle" naming neither the service nor
		// the operator's typo.
		for _, fn := range []struct {
			label string
			run   func([]Service) error
		}{
			{"TopoSort", func(s []Service) error { _, err := TopoSort(s); return err }},
			{"TopoLayers", func(s []Service) error { _, err := TopoLayers(s); return err }},
		} {
			err := fn.run([]Service{tc.svc})
			if err == nil {
				t.Errorf("%s with unknown %s dep: expected error, got nil", fn.label, tc.name)
				continue
			}
			if !strings.Contains(err.Error(), "ghost") ||
				!strings.Contains(err.Error(), "unknown") {
				t.Errorf("%s with unknown %s dep: error %q should name the unknown "+
					"process %q, not report a cycle", fn.label, tc.name, err, "ghost")
			}
		}
	}
}

// TestLayersDeterministicOrder pins the per-layer sort. Members come from
// ranging a map, so one call proves nothing: unsorted output lands in sorted
// order often enough by chance. Repeating it, over a wide layer, is what makes
// the assertion decisive.
func TestLayersDeterministicOrder(t *testing.T) {
	t.Parallel()
	// Declared in an order unrelated to the sorted one, so "sorted" cannot be
	// an artifact of insertion order either.
	svcs := []Service{
		{Name: "root"},
		{Name: "gamma", After: []string{"root"}},
		{Name: "alpha", After: []string{"root"}},
		{Name: "epsilon", After: []string{"root"}},
		{Name: "beta", After: []string{"root"}},
		{Name: "delta", After: []string{"root"}},
		{Name: "zeta", After: []string{"root"}},
		{Name: "eta", After: []string{"root"}},
		{Name: "theta", After: []string{"root"}},
	}
	want := "alpha,beta,delta,epsilon,eta,gamma,theta,zeta"
	for i := range 25 {
		layers, err := TopoLayers(svcs)
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if len(layers) != 2 {
			t.Fatalf("call %d: expected 2 layers, got %d: %v", i, len(layers), layers)
		}
		if got := strings.Join(layers[1], ","); got != want {
			t.Fatalf("call %d: layer 1 = %q, want %q (layer contents must be sorted "+
				"for deterministic start order and log output)", i, got, want)
		}
	}
}

// TestNoDuplicatesInSortOrder pins that a service is emitted exactly once even
// when several dependencies converge on it. A duplicate would make the daemon's
// shutdown sequence stop the same service twice.
func TestNoDuplicatesInSortOrder(t *testing.T) {
	t.Parallel()
	order, err := TopoSort([]Service{
		{Name: "a"},
		{Name: "b"},
		{Name: "c"},
		{Name: "sink", After: []string{"a", "b"}, Requires: []string{"b", "c"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 4 {
		t.Fatalf("expected exactly 4 entries, got %d: %v", len(order), order)
	}
	seen := make(map[string]int, len(order))
	for _, n := range order {
		seen[n]++
	}
	for n, c := range seen {
		if c != 1 {
			t.Errorf("%s appears %d times in start order: %v", n, c, order)
		}
	}
	if order[len(order)-1] != "sink" {
		t.Errorf("expected sink last, got %v", order)
	}
}
