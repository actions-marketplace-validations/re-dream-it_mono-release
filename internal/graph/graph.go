// Package graph provides dependency ordering for components.
package graph

import (
	"fmt"
	"sort"
	"strings"
)

// Topo returns node names ordered so that every node comes after its dependencies.
// deps maps a node to the nodes it depends on. Returns an error describing a cycle if one exists.
func Topo(deps map[string][]string) ([]string, error) {
	names := make([]string, 0, len(deps))
	for n := range deps {
		names = append(names, n)
	}
	sort.Strings(names)

	const (
		white = iota
		grey
		black
	)
	state := map[string]int{}
	var order, stack []string

	var visit func(n string) error
	visit = func(n string) error {
		switch state[n] {
		case black:
			return nil
		case grey:
			i := 0
			for j, s := range stack {
				if s == n {
					i = j
				}
			}
			return fmt.Errorf("dependency cycle: %s -> %s", strings.Join(stack[i:], " -> "), n)
		}
		state[n] = grey
		stack = append(stack, n)
		ds := append([]string(nil), deps[n]...)
		sort.Strings(ds)
		for _, d := range ds {
			if err := visit(d); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = black
		order = append(order, n)
		return nil
	}
	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return order, nil
}
