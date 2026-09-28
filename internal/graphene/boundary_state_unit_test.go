package graphene

import (
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"
)

func TestUnitBoundaryStateRoundTrip(t *testing.T) {
	t.Parallel()
	state := State{
		Stacks:     []Stack{{Base: "main", Branches: []string{"one"}}},
		Boundaries: map[string]string{"one": strings.Repeat("a", 40)},
		Pending: &Pending{
			Operation:          "squash",
			OriginalStacks:     []Stack{{Base: "main", Branches: []string{"one", "two"}}},
			OriginalBoundaries: map[string]string{"one": strings.Repeat("a", 40), "two": strings.Repeat("b", 40)},
			NextStacks:         []Stack{{Base: "main", Branches: []string{"one"}}},
			NextBoundaries:     map[string]string{"one": strings.Repeat("a", 40)},
		},
	}
	data, err := json.Marshal(newStateFile(state, ""))
	if err != nil {
		t.Fatal(err)
	}
	file, err := decodeStateFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := file.state(); !reflect.DeepEqual(got, state) {
		t.Fatalf("state = %#v, want %#v", got, state)
	}
}

func TestUnitBoundaryStateLegacyFormat(t *testing.T) {
	t.Parallel()
	file, err := decodeStateFile([]byte(`{"version":1,"stacks":[{"base":"main","branches":["one"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	old := State{Stacks: []Stack{{Base: "main", Branches: []string{"one"}}}}
	if got := file.state(); !reflect.DeepEqual(got, old) {
		t.Fatalf("legacy state = %#v, want %#v", got, old)
	}
	data, err := json.Marshal(newStateFile(file.state(), ""))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["boundaries"]; exists {
		t.Fatal("missing boundaries must remain omitted for legacy state")
	}
}

func TestUnitBoundariesFollowTrackedBranches(t *testing.T) {
	original := State{
		Stacks: []Stack{
			{Base: "main", Branches: []string{"one", "two"}},
			{Base: "one", Branches: []string{"fork"}},
		},
		Boundaries: map[string]string{"one": "base-commit", "two": "one-commit", "fork": "one-commit"},
	}
	for _, tc := range []struct {
		name string
		edit func(State) State
		want map[string]string
	}{
		{"delete with dependents", func(s State) State { return RemoveBranches(s, []string{"one"}) },
			map[string]string{"two": "one-commit"}},
		{"delete and reparent", func(s State) State { return RemoveBranchesWithBase(s, []string{"one"}, "main") },
			map[string]string{"two": "one-commit", "fork": "one-commit"}},
		{"forget prefix", func(s State) State { s, _ = RemoveStackThroughBranch(s, "one"); return s },
			map[string]string{"two": "one-commit", "fork": "one-commit"}},
		{"truncate", func(s State) State { s, _ = TruncateStackAfterBranch(s, "one"); return s },
			map[string]string{"one": "base-commit", "fork": "one-commit"}},
		{"reparent", func(s State) State { s, _, _ = ReparentBranch(s, "two", "other"); return s },
			original.Boundaries},
		{"remove all", func(s State) State { return RemoveBranches(s, []string{"one", "two"}) }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Deliberately share the map to check that pruning preserves the original.
			input := State{Stacks: cloneStacks(original.Stacks), Boundaries: original.Boundaries}
			got := tc.edit(input)
			if !maps.Equal(got.Boundaries, tc.want) {
				t.Fatalf("boundaries = %v, want %v", got.Boundaries, tc.want)
			}
			if len(original.Boundaries) != 3 {
				t.Fatal("topology change modified original boundaries")
			}
		})
	}
}

func TestUnitTrackBranchPreservesIndependentBoundaryMaps(t *testing.T) {
	original := State{
		Stacks:     []Stack{{Base: "one", Branches: []string{"two"}}},
		Boundaries: map[string]string{"two": "one-commit"},
	}
	tracked, err := TrackBranch(original, "main", "one")
	if err != nil {
		t.Fatal(err)
	}
	if tracked.Boundaries["two"] != "one-commit" {
		t.Fatal("tracking parent lost child's historical boundary")
	}
	tracked.Boundaries["two"] = "rewritten-one"
	if original.Boundaries["two"] != "one-commit" {
		t.Fatal("changing tracked state modified original boundaries")
	}
}

func TestUnitSplitPlanRestoresSuffixBoundaries(t *testing.T) {
	state := State{
		Stacks:     []Stack{{Base: "main", Branches: []string{"one", "part"}}},
		Boundaries: map[string]string{"one": "base", "part": "new-one"},
		Pending: &Pending{
			Operation: "split", Branch: "one", Branches: []string{"one", "part"}, OriginalHead: "old-one",
			OriginalStacks:     []Stack{{Base: "main", Branches: []string{"one", "two"}}},
			OriginalBoundaries: map[string]string{"one": "base", "two": "old-one"},
		},
	}
	next, _, _, _, err := splitFinalState(state)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"one": "base", "part": "new-one", "two": "old-one"}
	if !maps.Equal(next.Boundaries, want) {
		t.Fatalf("boundaries = %v, want %v", next.Boundaries, want)
	}
	next.Boundaries["part"] = "changed"
	next.Boundaries["two"] = "changed"
	if state.Boundaries["part"] != "new-one" || state.Pending.OriginalBoundaries["two"] != "old-one" {
		t.Fatal("plan modified the current or original boundary map")
	}
}

func TestUnitSquashPlanPrunesRemovedBoundaries(t *testing.T) {
	state := State{
		Stacks:     []Stack{{Base: "main", Branches: []string{"one", "two"}}},
		Boundaries: map[string]string{"one": "base", "two": "one-commit"},
	}
	selection, err := squashRange(state, "two", 2)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := squashFinalState(state, selection, map[string]string{"one": "one-commit", "two": "two-commit"})
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(next.Boundaries, map[string]string{"one": "base"}) {
		t.Fatalf("boundaries = %v", next.Boundaries)
	}
	if state.Boundaries["two"] != "one-commit" {
		t.Fatal("plan pruned the original boundary map")
	}
}
