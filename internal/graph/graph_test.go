package graph

import "testing"

func TestTopo(t *testing.T) {
	order, err := Topo(map[string][]string{"a": {"b"}, "b": {"c"}, "c": nil, "d": nil})
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, n := range order {
		pos[n] = i
	}
	if !(pos["c"] < pos["b"] && pos["b"] < pos["a"]) || len(order) != 4 {
		t.Errorf("bad order %v", order)
	}
}

func TestTopoCycle(t *testing.T) {
	if _, err := Topo(map[string][]string{"a": {"b"}, "b": {"a"}}); err == nil {
		t.Error("expected cycle error")
	}
}
