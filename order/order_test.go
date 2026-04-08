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

// TestAfterRequiresSameDepDeduplication covers M-02: when a service lists the
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
