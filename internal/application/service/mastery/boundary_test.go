package mastery

import "testing"

func TestComputeBoundaryColdStartReturnsNil(t *testing.T) {
	levels := map[string]int{"a": 10, "b": 0} // nothing reaches familiar
	adj := map[string][]string{"a": {"b"}, "b": {"a"}}
	if got := ComputeBoundary(levels, adj, FamiliarLevel, 10); got != nil {
		t.Fatalf("cold start should return nil, got %v", got)
	}
}

func TestComputeBoundaryHighlightsUnfamiliarNeighbors(t *testing.T) {
	// a is familiar; b and c are its neighbors; d is two hops away.
	levels := map[string]int{"a": 60, "b": 0, "c": 10, "d": 0}
	adj := map[string][]string{
		"a": {"b", "c"},
		"b": {"a", "d"},
		"c": {"a"},
		"d": {"b"},
	}
	got := ComputeBoundary(levels, adj, FamiliarLevel, 10)
	if len(got) == 0 {
		t.Fatalf("expected boundary candidates, got none")
	}
	set := make(map[string]bool, len(got))
	for _, s := range got {
		set[s.Slug] = true
	}
	if set["a"] {
		t.Fatalf("familiar node must not be a boundary candidate: %v", got)
	}
	// The one-hop unfamiliar neighbor b must rank ahead of the two-hop d.
	if !set["b"] {
		t.Fatalf("one-hop unfamiliar neighbor b should be a boundary candidate: %v", got)
	}
}

func TestComputeBoundaryExcludesAlreadyFamiliar(t *testing.T) {
	levels := map[string]int{"a": 80, "b": 50, "c": 20}
	adj := map[string][]string{
		"a": {"b"},
		"b": {"a", "c"},
		"c": {"b"},
	}
	got := ComputeBoundary(levels, adj, FamiliarLevel, 10)
	for _, s := range got {
		if levels[s.Slug] >= FamiliarLevel {
			t.Fatalf("already-familiar node %q (%d) leaked into boundary: %v", s.Slug, levels[s.Slug], got)
		}
	}
	if len(got) != 1 || got[0].Slug != "c" {
		t.Fatalf("expected only c as boundary candidate, got %v", got)
	}
}
