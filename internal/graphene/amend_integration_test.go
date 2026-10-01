package graphene

import (
	"reflect"
	"testing"
)

func TestAmendRestacksSiblingBranches(t *testing.T) {
	for _, tc := range []struct {
		name     string
		empty    bool
		legacy   bool
		conflict bool
	}{
		{name: "empty sibling", empty: true},
		{name: "legacy empty sibling", empty: true, legacy: true},
		{name: "sibling with own commit"},
		{name: "empty sibling after continue", empty: true, conflict: true},
		{name: "sibling with own commit after continue", conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			createStackBranch(t, repo, "one.txt", "one\n", "One")
			if tc.conflict {
				createStackBranch(t, repo, "one.txt", "descendant\n", "Two")
			} else {
				createStackBranch(t, repo, "two.txt", "two\n", "Two")
			}
			parent := runGit(t, repo.dir, "rev-parse", "stack/two")
			createStackBranch(t, repo, "three.txt", "three\n", "Three")
			runGit(t, repo.dir, "switch", "-c", "stack/sibling", "stack/two")
			if !tc.empty {
				commitFile(t, repo.dir, "sibling.txt", "sibling\n", "Sibling")
			}
			state := readState(t, repo.dir)
			state.Stacks = append(state.Stacks, Stack{Base: "stack/two", Branches: []string{"stack/sibling"}})
			state.setBoundary("stack/sibling", parent)
			if tc.legacy {
				state.Boundaries = nil
			}
			if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
				t.Fatal(err)
			}

			runGit(t, repo.dir, "switch", "stack/one")
			writeFile(t, repo.dir, "one.txt", "amended\n")
			runGit(t, repo.dir, "add", "one.txt")
			code, stdout, stderr := repo.runGraphene(t, "amend", "--no-edit")
			if tc.conflict {
				if code == 0 {
					t.Fatal("amend unexpectedly succeeded without a conflict")
				}
				pending := readState(t, repo.dir).Pending
				if pending == nil || len(pending.Queue) != 2 || pending.Queue[0].Top != "stack/three" {
					t.Fatalf("pending = %#v; stderr: %s", pending, stderr)
				}
				writeFile(t, repo.dir, "one.txt", "amended descendant\n")
				runGit(t, repo.dir, "add", "one.txt")
				expectGrapheneOK(t, repo, "continue")
			} else if code != 0 {
				t.Fatalf("amend exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}

			assertBranchParent(t, repo.dir, "stack/two", "stack/one")
			assertBranchParent(t, repo.dir, "stack/three", "stack/two")
			parent = runGit(t, repo.dir, "rev-parse", "stack/two")
			if tc.empty {
				if got := runGit(t, repo.dir, "rev-parse", "stack/sibling"); got != parent {
					t.Fatalf("empty sibling = %s, want parent %s", got, parent)
				}
			} else {
				assertBranchParent(t, repo.dir, "stack/sibling", "stack/two")
				if got := runGit(t, repo.dir, "show", "stack/sibling:sibling.txt"); got != "sibling" {
					t.Fatalf("sibling content = %q", got)
				}
			}
			if got := runGit(t, repo.dir, "show", "stack/one:one.txt"); got != "amended" {
				t.Fatalf("amended content = %q", got)
			}
			want := "amended"
			if tc.conflict {
				want = "amended descendant"
			}
			if got := runGit(t, repo.dir, "show", "stack/sibling:one.txt"); got != want {
				t.Fatalf("descendant content = %q, want %q", got, want)
			}
			final := readState(t, repo.dir)
			if final.Pending != nil || !reflect.DeepEqual(final.Stacks, state.Stacks) || final.Boundaries["stack/sibling"] != parent {
				t.Fatalf("completed state = %#v", final)
			}
			if got := currentBranch(t, repo.dir); got != "stack/one" {
				t.Fatalf("current branch = %q", got)
			}
			if got := runGit(t, repo.dir, "status", "--porcelain"); got != "" {
				t.Fatalf("dirty worktree: %s", got)
			}
		})
	}
}
