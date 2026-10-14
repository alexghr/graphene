package graphene

import (
	"reflect"
	"testing"
)

func TestUnitCurrentStackGraphMatchesPushScope(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"one", "two"}},
		{Base: "two", Branches: []string{"three", "four"}},
		{Base: "two", Branches: []string{"fork"}},
		{Base: "main", Branches: []string{"other"}},
	}}
	for _, current := range []string{"main", "one", "two", "three", "four", "fork"} {
		t.Run(current, func(t *testing.T) {
			selected, err := currentStackState(state, current)
			if err != nil {
				t.Fatal(err)
			}
			var tracked []string
			for _, name := range StateRefNames(selected) {
				if selected.ContainsBranch(name) {
					tracked = append(tracked, name)
					if parent, _ := BaseBranch(selected, name); parent != newStackGraph(state).parent[name] {
						t.Fatalf("wrong parent for %s: %s", name, parent)
					}
				}
			}
			if want := BranchesThroughCurrentAndDescendants(state, current); !reflect.DeepEqual(tracked, want) {
				t.Fatalf("graph branches = %v, push branches = %v", tracked, want)
			}
		})
	}
	got, err := RenderCurrentStackGraph(state, "two")
	want := "main\n  `- one\n     `- two *\n        |- three\n        |  `- four\n        `- fork\n"
	if err != nil || got != want {
		t.Fatalf("graph = %q, %v; want %q", got, err, want)
	}
	if _, err := currentStackState(state, "loose"); err == nil {
		t.Fatal("accepted an unrelated branch")
	}
}
