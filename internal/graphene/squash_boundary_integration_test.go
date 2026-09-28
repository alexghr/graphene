package graphene

import (
	"reflect"
	"testing"
)

func TestSquashUsesHistoricalBaseAndRestacksDescendant(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "saved"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			runGit(t, repo.dir, "switch", "-c", "upstream")
			boundary := commitFile(t, repo.dir, "upstream.txt", "upstream\n", "Upstream")
			runGit(t, repo.dir, "remote", "add", "origin", ".")
			runGit(t, repo.dir, "update-ref", "refs/remotes/origin/main", boundary)
			runGit(t, repo.dir, "branch", "--set-upstream-to=origin/main", "main")
			writeFile(t, repo.dir, "one.txt", "one\n")
			expectGrapheneOK(t, repo, "new", "-a", "--base", "main", "-m", "One")
			createStackBranch(t, repo, "two.txt", "two\n", "Two")
			topTree := runGit(t, repo.dir, "rev-parse", "HEAD^{tree}")
			createStackBranch(t, repo, "child.txt", "child\n", "Child")
			childTree := runGit(t, repo.dir, "rev-parse", "HEAD^{tree}")
			if legacy {
				state := readState(t, repo.dir)
				state.Boundaries = nil
				if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
					t.Fatal(err)
				}
			}
			// The saved case also covers a base advanced past the stack's fork.
			if !legacy {
				runGit(t, repo.dir, "switch", "main")
				runGit(t, repo.dir, "merge", "--ff-only", "upstream")
				commitFile(t, repo.dir, "later.txt", "later\n", "Later upstream")
			}
			main := runGit(t, repo.dir, "rev-parse", "main")
			runGit(t, repo.dir, "switch", "stack/two")
			expectGrapheneOK(t, repo, "squash", "-c", "2", "--no-edit")
			for ref, want := range map[string]string{
				"main": main, "stack/one^": boundary, "stack/one^{tree}": topTree, "stack/child^{tree}": childTree,
			} {
				if got := runGit(t, repo.dir, "rev-parse", ref); got != want {
					t.Fatalf("%s = %s, want %s", ref, got, want)
				}
			}
			assertBranchParent(t, repo.dir, "stack/child", "stack/one")
			if refExists(t, repo.dir, "refs/heads/stack/two") {
				t.Fatal("squashed branch survived")
			}
			want := State{
				Stacks:     []Stack{{Base: "main", Branches: []string{"stack/one", "stack/child"}}},
				Boundaries: map[string]string{"stack/one": boundary, "stack/child": runGit(t, repo.dir, "rev-parse", "stack/one")},
			}
			if got := readState(t, repo.dir); !reflect.DeepEqual(got, want) {
				t.Fatalf("state = %#v, want %#v", got, want)
			}
		})
	}
}
