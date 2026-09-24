package graphene

import (
	"reflect"
	"strings"
	"testing"
)

func TestUnitSyncSelectionForCurrent(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b", "c"}},
		{Base: "b", Branches: []string{"fork", "fork-tip"}},
		{Base: "main", Branches: []string{"other", "other-tip"}},
		{Base: "second-base", Branches: []string{"isolated"}},
	}}
	tests := []struct {
		name, current, base string
		branches            []string
		index               int
	}{
		{"middle", "b", "main", []string{"a", "b"}, 1},
		{"tip", "c", "main", []string{"a", "b", "c"}, 2},
		{"nested tip", "fork-tip", "main", []string{"a", "b", "fork", "fork-tip"}, 3},
		{"sibling", "other", "main", []string{"other"}, 0},
		{"other base", "isolated", "second-base", []string{"isolated"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := syncSelectionForCurrent(state, tt.current)
			if !ok || got.Base != tt.base || got.Current != tt.current || got.BaseCurrent || len(got.Paths) != 1 {
				t.Fatalf("selection = %#v, ok = %v", got, ok)
			}
			path := got.Paths[0]
			if !reflect.DeepEqual(path.Stack, Stack{Base: tt.base, Branches: tt.branches}) || path.BranchLimit != len(tt.branches) || path.CurrentBranchIndex != tt.index {
				t.Fatalf("path = %#v, want branches %#v and index %d", path, tt.branches, tt.index)
			}
		})
	}
	if got, ok := syncSelectionForCurrent(state, "untracked"); ok {
		t.Fatalf("untracked branch selected: %#v", got)
	}
}

func TestUnitSyncSelectionForBase(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b"}},
		{Base: "b", Branches: []string{"nested"}},
		{Base: "main", Branches: []string{"sibling"}},
		{Base: "main"},
		{Base: "other", Branches: []string{"isolated"}},
	}}
	got, ok := syncSelectionForBase(state, "main")
	if !ok || !got.BaseCurrent || got.Base != "main" || got.Current != "main" || len(got.Paths) != 2 {
		t.Fatalf("base selection = %#v, ok = %v", got, ok)
	}
	if got.Paths[0].StackIndex != 0 || got.Paths[1].StackIndex != 2 || got.Paths[0].BranchLimit != 2 || got.Paths[1].BranchLimit != 1 {
		t.Fatalf("base paths = %#v", got.Paths)
	}
	for _, path := range got.Paths {
		if path.CurrentBranchIndex != -1 {
			t.Fatalf("base path has current branch: %#v", path)
		}
	}
	if selected, ok := syncSelectionForBase(state, "absent"); ok {
		t.Fatalf("absent base selected: %#v", selected)
	}
}

func TestUnitPendingAffectedBranches(t *testing.T) {
	t.Parallel()
	stacks := []Stack{
		{Base: "main", Branches: []string{"a", "b", "c"}},
		{Base: "b", Branches: []string{"fork", "fork-tip"}},
		{Base: "main", Branches: []string{"other"}},
	}
	tests := []struct {
		name    string
		pending *Pending
		want    map[string]bool
	}{
		{"none", nil, map[string]bool{}},
		{"branch affects its stack", &Pending{Branch: "b"}, map[string]bool{"a": true, "b": true, "c": true}},
		{"nested queue affects nested stack", &Pending{Queue: []RebaseOp{{Top: "fork-tip"}}}, map[string]bool{"fork": true, "fork-tip": true}},
		{"recovery includes only recorded branch", &Pending{Recovery: &recoveryState{Expected: map[string]string{"fork": "old", "missing": "old"}}}, map[string]bool{"fork": true}},
		{"untracked names ignored", &Pending{Branch: "missing", Branches: []string{"other", "missing"}}, map[string]bool{"other": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := State{Stacks: stacks, Pending: tt.pending}
			if got := pendingAffectedBranches(state); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("affected = %#v, want %#v", got, tt.want)
			}
		})
	}
	state := State{Stacks: stacks, Pending: &Pending{Branch: "b"}}
	if got := pendingBranchBeingRewritten(state, []string{"other", "c", "a"}); got != "c" {
		t.Fatalf("first rewritten branch = %q, want c", got)
	}
	if got := pendingBranchBeingRewritten(state, []string{"other", "fork"}); got != "" {
		t.Fatalf("unaffected branch = %q", got)
	}
}

func TestUnitCommitAndSendOptionRejections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		parse func([]string) error
		args  []string
		want  string
	}{
		{"new amend", func(args []string) error { _, err := parseNewArgs(args); return err }, []string{"--amend", "-m", "bad"}, "cannot use --amend"},
		{"new Git flag", func(args []string) error { _, err := parseNewArgs(args); return err }, []string{"--signoff", "-m", "bad"}, "unsupported argument"},
		{"amend branch", func(args []string) error { _, err := parseAmendArgs(args); return err }, []string{"--branch", "other"}, "does not support --branch"},
		{"send Git flag", func(args []string) error { _, err := parseSendArgs(args); return err }, []string{"--force-with-lease"}, "unsupported argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.parse(tt.args); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parse(%#v) error = %v, want %q", tt.args, err, tt.want)
			}
		})
	}
}

func TestUnitSquashCountBoundary(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "one", "0", "1", "-2"} {
		if _, err := parseSquashCount(raw); err == nil {
			t.Fatalf("parseSquashCount(%q) unexpectedly succeeded", raw)
		}
	}
	if got, err := parseSquashCount("2"); err != nil || got != 2 {
		t.Fatalf("parseSquashCount(2) = (%d, %v)", got, err)
	}
}
