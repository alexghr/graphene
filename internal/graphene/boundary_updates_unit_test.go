package graphene

import (
	"maps"
	"testing"
)

func TestUnitPlanBoundaryUpdates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		boundaries map[string]string
		want       map[string]string
		wantError  bool
	}{
		{"root", map[string]string{"one": "old-base"}, map[string]string{"one": "new-base"}, false},
		{"chain", map[string]string{"one": "old-base", "two": "old-one"}, map[string]string{"one": "new-base", "two": "refs/heads/one"}, false},
		{"fork", map[string]string{"two": "old-one", "fork": "old-one"}, map[string]string{"two": "refs/heads/one", "fork": "refs/heads/one"}, false},
		{"retained history", map[string]string{"one": "older"}, map[string]string{"one": "older"}, false},
		{"untracked rewritten boundary", map[string]string{"two": "intermediate"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := maps.Clone(tc.boundaries)
			got, err := planBoundaryUpdates(tc.boundaries, map[string]string{"one": "old-one", "two": "old-two"}, map[string]bool{"old-one": true, "old-two": true, "intermediate": true}, "old-base", "new-base")
			if (err != nil) != tc.wantError || !maps.Equal(got, tc.want) {
				t.Fatalf("updates = %v, error = %v; want %v, error %v", got, err, tc.want, tc.wantError)
			}
			if !maps.Equal(tc.boundaries, before) {
				t.Fatal("planning changed original boundaries")
			}
		})
	}
}

func TestUnitSetBoundaryKeepsOriginalState(t *testing.T) {
	original := State{Boundaries: map[string]string{"one": "base"}}
	next := original
	next.setBoundary("two", "one-commit")
	next.setBoundary("one", "new-base")
	if !maps.Equal(original.Boundaries, map[string]string{"one": "base"}) {
		t.Fatal("recording a boundary changed original state")
	}
}
