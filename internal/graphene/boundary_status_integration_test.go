package graphene

import (
	"reflect"
	"strings"
	"testing"
)

func TestGraphBoundaryWarningsUseKnownBaseHistory(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	initial := runGit(t, repo.dir, "rev-parse", "main")
	actor := cloneConfiguredRepo(t, remote, "main")
	commitFile(t, actor, "upstream.txt", "upstream\n", "Upstream")
	runGit(t, actor, "push", "origin", "main")
	runGit(t, repo.dir, "fetch", "origin")
	runGit(t, repo.dir, "switch", "--no-track", "-c", "feature", "origin/main")
	writeFile(t, repo.dir, "feature.txt", "feature\n")
	expectGrapheneOK(t, repo, "new", "-a", "--reuse-current", "--base", "main", "-m", "Feature")
	check := func(want string) {
		t.Helper()
		before := readState(t, repo.dir)
		refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)")
		code, stdout, stderr := repo.runGraphene(t, "graph", "--stack")
		if code != 0 || !strings.Contains(stdout, currentBranch(t, repo.dir)+" *") {
			t.Fatalf("graph result: %d, %s, %s", code, stdout, stderr)
		}
		if (want == "" && stderr != "") || (want != "" && !strings.Contains(stderr, want)) {
			t.Fatalf("warning = %q, want %q", stderr, want)
		}
		if got := readState(t, repo.dir); !reflect.DeepEqual(got, before) {
			t.Fatal("graph changed state")
		}
		if got := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)"); got != refs {
			t.Fatal("graph changed refs")
		}
	}
	check("") // Local main is behind, but the cached upstream contains the boundary.
	runGit(t, actor, "reset", "--hard", initial)
	commitFile(t, actor, "replacement.txt", "replacement\n", "Replace upstream history")
	runGit(t, actor, "push", "--force", "origin", "main")
	check("") // Graph does not fetch to discover the rewritten history.
	runGit(t, repo.dir, "fetch", "origin")
	check(`"feature" needs sync`)
	expectGrapheneOK(t, repo, "sync")
	check("")
	oldFeature := runGit(t, repo.dir, "rev-parse", "feature")
	createStackBranch(t, repo, "child.txt", "child\n", "Child")
	runGit(t, repo.dir, "update-ref", "refs/remotes/origin/feature", oldFeature)
	runGit(t, repo.dir, "branch", "--set-upstream-to=origin/feature", "feature")
	runGit(t, repo.dir, "switch", "feature")
	runGit(t, repo.dir, "commit", "--amend", "-m", "Feature amended")
	runGit(t, repo.dir, "switch", "stack/child")
	check(`"stack/child" needs sync`) // A tracked parent's stale upstream cannot hide an amend.
	runGit(t, repo.dir, "switch", "feature")
	expectGrapheneOK(t, repo, "restack", "main")
	check("")
	state := readState(t, repo.dir)
	state.Boundaries["feature"] = "invalid"
	if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}
	check(`invalid saved historical boundary for "feature"`)
	state.Pending = &Pending{Operation: "split", Branch: "feature"}
	if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}
	check("") // Pending operations already have a recovery indicator in the graph.
}
