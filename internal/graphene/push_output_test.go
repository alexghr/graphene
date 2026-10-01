package graphene

import (
	"bytes"
	"strings"
	"testing"
)

func TestUnitPushScope(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{{Base: "main", Branches: []string{"one", "two"}}}}
	for _, tc := range []struct {
		current string
		stack   bool
		want    string
	}{
		{"one", false, "current branch and tracked ancestors"},
		{"one", true, "current branch, tracked ancestors, and descendants of the current branch (--stack)"},
		{"main", false, "current base branch only"},
		{"main", true, "tracked descendants of the current base branch (--stack); base excluded"},
		{"loose", false, "current branch only (untracked)"},
		{"loose", true, "current branch only (untracked)"},
	} {
		if got := pushScope(state, tc.current, tc.stack); got != tc.want {
			t.Errorf("pushScope(%q, %v) = %q, want %q", tc.current, tc.stack, got, tc.want)
		}
	}
}

func TestUnitPrintPushPlan(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mode string
		force, dry bool
	}{
		{"send", "atomic", false, false},
		{"send dry run", "atomic (dry run)", false, true},
		{"sendf", "atomic, force-with-lease", true, false},
		{"sendf dry run", "atomic, force-with-lease (dry run)", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			app := &App{stdout: &out}
			if err := app.printPushPlan(pushPlan{
				Remote: "review", Branches: []string{"stack/one", "stack/two"},
				Scope: "current branch and tracked ancestors", ForceWithLease: tc.force, DryRun: tc.dry,
			}); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"  Remote: review\n", "  Mode: " + tc.mode + "\n",
				"  Scope: current branch and tracked ancestors\n",
				"  Branches:\n    stack/one\n    stack/two\n\n",
			} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("push plan missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}
