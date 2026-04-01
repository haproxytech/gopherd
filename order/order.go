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

import "fmt"

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

	// Build adjacency list: edge from A -> B means A must start before B.
	edges := make(map[string][]string)
	inDegree := make(map[string]int)

	for _, s := range services {
		if _, ok := inDegree[s.Name]; !ok {
			inDegree[s.Name] = 0
		}

		for _, dep := range s.After {
			if !names[dep] {
				return nil, fmt.Errorf("process %s: after references unknown process %q", s.Name, dep)
			}
			edges[dep] = append(edges[dep], s.Name)
			inDegree[s.Name]++
		}

		for _, dep := range s.Before {
			if !names[dep] {
				return nil, fmt.Errorf("process %s: before references unknown process %q", s.Name, dep)
			}
			edges[s.Name] = append(edges[s.Name], dep)
			inDegree[dep]++
		}

		for _, dep := range s.Requires {
			if !names[dep] {
				return nil, fmt.Errorf("process %s: requires references unknown process %q", s.Name, dep)
			}
			edges[dep] = append(edges[dep], s.Name)
			inDegree[s.Name]++
		}
	}

	// Kahn's algorithm
	var queue []string
	for _, s := range services {
		if inDegree[s.Name] == 0 {
			queue = append(queue, s.Name)
		}
	}

	var result []string
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		result = append(result, node)

		for _, neighbor := range edges[node] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if len(result) != len(names) {
		return nil, fmt.Errorf("dependency cycle detected")
	}

	return result, nil
}
