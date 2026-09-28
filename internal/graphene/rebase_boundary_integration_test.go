package graphene

import (
	"reflect"
	"strings"
	"testing"
)

func TestSyncPreservesLegacyBranchWithUnappliedCommit(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "unique.txt", "unique\n", "One")
	commitFile(t, repo.dir, "applied.txt", "applied\n", "Applied")
	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	state := readState(t, repo.dir)
	state.Boundaries = nil
	if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}
	actor := cloneConfiguredRepo(t, remote, "main")
	commitFile(t, actor, "applied.txt", "applied\n", "Applied upstream")
	runGit(t, actor, "push", "origin", "main")

	code, _, stderr := repo.runGraphene(t, "sync")
	if code == 0 || !strings.Contains(stderr, "one-commit branch") {
		t.Fatalf("sync exited %d: %s", code, stderr)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/one"); got != head {
		t.Fatalf("branch moved from %s to %s", head, got)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, state) {
		t.Fatalf("state changed: %#v", got)
	}
}

func TestRebaseUsesSavedBoundariesAfterHistoryChanges(t *testing.T) {
	for _, command := range []string{"sync", "restack"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			repo, remote := newTestRepoWithOrigin(t)
			initial := runGit(t, repo.dir, "rev-parse", "main")
			commitFile(t, repo.dir, "old-upstream.txt", "old\n", "Old upstream")
			runGit(t, repo.dir, "push", "origin", "main")
			createStackBranch(t, repo, "one.txt", "one\n", "One")
			createStackBranch(t, repo, "two.txt", "two\n", "Two")
			actor := cloneConfiguredRepo(t, remote, "main")
			runGit(t, actor, "reset", "--hard", initial)
			newBase := commitFile(t, actor, "new-upstream.txt", "new\n", "Rewritten upstream")
			runGit(t, actor, "push", "--force", "origin", "main")
			runGit(t, repo.dir, "fetch", "origin")
			runGit(t, repo.dir, "branch", "-f", "main", "origin/main")
			runGit(t, repo.dir, "switch", "stack/one")
			run := func() {
				if command == "restack" {
					expectGrapheneOK(t, repo, "restack", "main")
				} else {
					expectGrapheneOK(t, repo, "sync")
				}
			}
			run()
			assertBranchParent(t, repo.dir, "stack/one", "main")
			assertBranchParent(t, repo.dir, "stack/two", "stack/one")
			if refFileExists(t, repo.dir, "stack/two:old-upstream.txt") {
				t.Fatal("replayed obsolete upstream changes")
			}
			for _, path := range []string{"new-upstream.txt", "one.txt", "two.txt"} {
				if !refFileExists(t, repo.dir, "stack/two:"+path) {
					t.Fatalf("lost %s", path)
				}
			}
			// The parent needs no rebase, but its child still points at the old commit.
			runGit(t, repo.dir, "commit", "--amend", "-m", "One amended outside Graphene")
			one := runGit(t, repo.dir, "rev-parse", "stack/one")
			if command == "sync" {
				runGit(t, repo.dir, "switch", "stack/two")
			}
			run()
			assertBranchParent(t, repo.dir, "stack/two", "stack/one")
			state := readState(t, repo.dir)
			if state.Pending != nil || state.Boundaries["stack/one"] != newBase || state.Boundaries["stack/two"] != one {
				t.Fatalf("rebased state = %#v", state)
			}
		})
	}
}
