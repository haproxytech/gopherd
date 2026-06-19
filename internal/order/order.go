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

// Package order provides topological sorting for service dependencies.
package order

import (
	"fmt"
	"slices"
	"strings"
)

// Service represents the dependency information needed for topological sorting.
type Service struct {
	Name     string
	After    []string
	Before   []string
	Requires []string
}

// TopoSort returns service names in start order based on after/before/requires dependencies.
// It returns an error if there is a cycle.
func TopoSort(services []Service) ([]string, error) {
	names := make(map[string]bool)
	for _, s := range services {
		if names[s.Name] {
			return nil, fmt.Errorf("duplicate process name: %s", s.Name)
		}
		names[s.Name] = true
	}

	// Adjacency list: edge A -> B means A must start before B. seenEdge
	// deduplicates edges appearing in multiple constraint lists (e.g. after
	// and requires); duplicates would double-count inDegree and report false cycles.
	edges := make(map[string][]string)
	inDegree := make(map[string]int)
	seenEdge := make(map[[2]string]bool)

	addEdge := func(from, to string) {
		key := [2]string{from, to}
		if seenEdge[key] {
			return
		}
		seenEdge[key] = true
		edges[from] = append(edges[from], to)
		inDegree[to]++
	}

	for _, s := range services {
		if _, ok := inDegree[s.Name]; !ok {
			inDegree[s.Name] = 0
		}

		for _, dep := range s.After {
			if !names[dep] {
				return nil, fmt.Errorf("process %s: after references unknown process %q", s.Name, dep)
			}
			addEdge(dep, s.Name)
		}

		for _, dep := range s.Before {
			if !names[dep] {
				return nil, fmt.Errorf("process %s: before references unknown process %q", s.Name, dep)
			}
			addEdge(s.Name, dep)
		}

		for _, dep := range s.Requires {
			if !names[dep] {
				return nil, fmt.Errorf("process %s: requires references unknown process %q", s.Name, dep)
			}
			addEdge(dep, s.Name)
		}
	}

	// Kahn's algorithm. Head index instead of queue[1:] avoids the O(n) shift
	// per dequeue; pre-allocation avoids reallocations as nodes enqueue (P2).
	queue := make([]string, 0, len(services))
	for _, s := range services {
		if inDegree[s.Name] == 0 {
			queue = append(queue, s.Name)
		}
	}

	var result []string
	for head := 0; head < len(queue); head++ {
		node := queue[head]
		result = append(result, node)

		for _, neighbor := range edges[node] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if len(result) != len(names) {
		// Nodes with non-zero in-degree after Kahn's algorithm are part of the cycle.
		var cycled []string
		for name := range names {
			if inDegree[name] > 0 {
				cycled = append(cycled, name)
			}
		}
		slices.Sort(cycled)
		return nil, fmt.Errorf("dependency cycle detected among: %s", strings.Join(cycled, ", "))
	}

	return result, nil
}

// TopoLayers returns service names grouped into topologically-ordered layers:
// layer N's dependencies are all satisfied by layers 0..N-1, so its members
// have no edges between each other and can start in parallel; layer N+1 waits
// for layer N. Each layer is sorted alphabetically for determinism. Returns an
// error on cycles or unknown dependency references, like TopoSort.
func TopoLayers(services []Service) ([][]string, error) {
	names := make(map[string]bool)
	for _, s := range services {
		if names[s.Name] {
			return nil, fmt.Errorf("duplicate process name: %s", s.Name)
		}
		names[s.Name] = true
	}

	edges := make(map[string][]string)
	inDegree := make(map[string]int)
	seenEdge := make(map[[2]string]bool)

	addEdge := func(from, to string) {
		key := [2]string{from, to}
		if seenEdge[key] {
			return
		}
		seenEdge[key] = true
		edges[from] = append(edges[from], to)
		inDegree[to]++
	}

	for _, s := range services {
		if _, ok := inDegree[s.Name]; !ok {
			inDegree[s.Name] = 0
		}
		for _, dep := range s.After {
			if !names[dep] {
				return nil, fmt.Errorf("process %s: after references unknown process %q", s.Name, dep)
			}
			addEdge(dep, s.Name)
		}
		for _, dep := range s.Before {
			if !names[dep] {
				return nil, fmt.Errorf("process %s: before references unknown process %q", s.Name, dep)
			}
			addEdge(s.Name, dep)
		}
		for _, dep := range s.Requires {
			if !names[dep] {
				return nil, fmt.Errorf("process %s: requires references unknown process %q", s.Name, dep)
			}
			addEdge(dep, s.Name)
		}
	}

	var layers [][]string
	emitted := 0
	for emitted < len(names) {
		var layer []string
		for name := range names {
			if inDegree[name] == 0 {
				layer = append(layer, name)
			}
		}
		if len(layer) == 0 {
			// Remaining nodes are in a cycle.
			var cycled []string
			for name := range names {
				if inDegree[name] > 0 {
					cycled = append(cycled, name)
				}
			}
			slices.Sort(cycled)
			return nil, fmt.Errorf("dependency cycle detected among: %s", strings.Join(cycled, ", "))
		}
		slices.Sort(layer)
		for _, n := range layer {
			// -1 marks drained so the inDegree==0 check skips it next loop.
			inDegree[n] = -1
			for _, neighbor := range edges[n] {
				inDegree[neighbor]--
			}
		}
		layers = append(layers, layer)
		emitted += len(layer)
	}
	return layers, nil
}
