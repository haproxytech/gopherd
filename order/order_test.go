package order

import (
	"testing"
)

func TestNoDeps(t *testing.T) {
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
	_, err := TopoSort([]Service{
		{Name: "a", After: []string{"b"}},
		{Name: "b", After: []string{"a"}},
	})
	if err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestDuplicateName(t *testing.T) {
	_, err := TopoSort([]Service{
		{Name: "a"}, {Name: "a"},
	})
	if err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestUnknownDep(t *testing.T) {
	_, err := TopoSort([]Service{
		{Name: "a", After: []string{"x"}},
	})
	if err == nil {
		t.Fatal("expected unknown dep error")
	}
}

func TestComplex(t *testing.T) {
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
