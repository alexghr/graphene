package graphene

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestImportUsesFeatureRangeWithBaseInAnotherWorktree(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	oldMain := runGit(t, repo.dir, "rev-parse", "main")
	actor := cloneConfiguredRepo(t, remote, "main")
	upstream := commitFile(t, actor, "upstream.txt", "upstream\n", "Upstream update")
	runGit(t, actor, "push", "origin", "main")
	runGit(t, repo.dir, "fetch", "origin")
	worktree := testRepo{dir: filepath.Join(t.TempDir(), "feature"), configDir: repo.configDir}
	runGit(t, repo.dir, "worktree", "add", "--no-track", "-b", "feature", worktree.dir, "origin/main")
	one := commitFile(t, worktree.dir, "one.txt", "one\n", "One")
	top := commitFile(t, worktree.dir, "two.txt", "two\n", "Two")

	expectGrapheneOK(t, worktree, "import", "main")
	want := State{
		Stacks:     []Stack{{Base: "main", Branches: []string{"stack/one", "feature"}}},
		Boundaries: map[string]string{"stack/one": upstream, "feature": one},
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("imported state = %#v, want %#v", got, want)
	}
	if refExists(t, repo.dir, "refs/heads/stack/upstream-update") {
		t.Fatal("import created a stack branch for an upstream commit")
	}
	for branch, expected := range map[string]string{"main": oldMain, "origin/main": upstream, "stack/one": one, "feature": top} {
		if got := runGit(t, repo.dir, "rev-parse", branch); got != expected {
			t.Fatalf("%s = %s, want %s", branch, got, expected)
		}
	}

	expectGrapheneOK(t, worktree, "forget")
	runGit(t, repo.dir, "merge", "--ff-only", "stack/one")
	runGit(t, repo.dir, "push", "origin", "main")
	// A range captured before the other worktree moved main still has both commits.
	app := &App{git: Git{Dir: worktree.dir}}
	commits, err := app.importCommits(upstream, top)
	if err != nil || !reflect.DeepEqual(commits, []string{one, top}) {
		t.Fatalf("frozen range = %v, error = %v; want [%s %s]", commits, err, one, top)
	}
	if err := app.validateImportHistory(upstream, commits); err != nil {
		t.Fatal(err)
	}
	if err := app.validateImportedBranches([]importBranch{{Branch: "stack/one", Commit: one}, {Branch: "feature", Commit: top}}); err != nil {
		t.Fatal(err)
	}

	expectGrapheneOK(t, worktree, "import", "main")
	want.Stacks[0].Branches = []string{"feature"}
	delete(want.Boundaries, "stack/one")
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("reimported state = %#v, want %#v", got, want)
	}
	for branch, expected := range map[string]string{"main": one, "origin/main": one, "stack/one": one, "feature": top} {
		if got := runGit(t, repo.dir, "rev-parse", branch); got != expected {
			t.Fatalf("%s = %s, want %s after reimport", branch, got, expected)
		}
	}
}
