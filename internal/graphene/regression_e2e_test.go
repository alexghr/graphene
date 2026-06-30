package graphene

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Regression for https://github.com/alexghr/graphene/issues/12.
func TestRegressionSyncSendfPreservesSquashMergedMiddleBranchPatch(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")
	expectGrapheneOK(t, repo, "send", "--stack", "origin")

	integrator := cloneConfiguredRepo(t, remote, "main")
	runGit(t, integrator, "switch", "-c", "stack/one", "--track", "origin/stack/one")
	runGit(t, integrator, "cherry-pick", "--no-commit", "origin/stack/two")
	runGit(t, integrator, "commit", "-m", "Squash stack/two into stack/one")
	runGit(t, integrator, "push", "origin", "stack/one")
	if !refFileExists(t, integrator, "stack/one:two.txt") {
		t.Fatal("test setup failed: squash-merged parent does not contain two.txt")
	}

	runGit(t, integrator, "switch", "main")
	writeFile(t, integrator, "base-update.txt", "base update\n")
	runGit(t, integrator, "add", ".")
	runGit(t, integrator, "commit", "-m", "Base update")
	runGit(t, integrator, "push", "origin", "main")

	runGit(t, repo.dir, "switch", "stack/three")
	if code, stdout, stderr := repo.runGraphene(t, "sync"); code != 0 {
		t.Logf("graphene sync rejected unsafe state, which is acceptable for this safety regression\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if code, stdout, stderr := repo.runGraphene(t, "sendf", "--stack", "origin"); code != 0 {
		t.Logf("graphene sendf rejected unsafe state, which is acceptable for this safety regression\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	runGit(t, integrator, "fetch", "origin")
	if !refFileExists(t, integrator, "origin/stack/one:two.txt") {
		t.Fatal("sendf removed the squash-merged stack/two patch from remote stack/one")
	}
}

func TestRegressionSendfDryRunAfterSyncWithNewDescendant(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	expectGrapheneOK(t, repo, "send", "origin")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	integrator := cloneConfiguredRepo(t, remote, "main")
	writeFile(t, integrator, "base-update.txt", "base update\n")
	runGit(t, integrator, "add", ".")
	runGit(t, integrator, "commit", "-m", "Base update")
	runGit(t, integrator, "push", "origin", "main")

	expectGrapheneOK(t, repo, "sync")
	if code, stdout, stderr := repo.runGraphene(t, "sendf", "--stack", "--dry-run", "origin"); code != 0 {
		t.Fatalf("graphene sendf --stack --dry-run exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// Regression for https://github.com/alexghr/graphene/issues/8.
func TestRegressionSyncUsesVisibleBaseForNestedStackWithoutIntermediateUpstream(t *testing.T) {
	t.Parallel()
	repo, _ := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")

	runGit(t, repo.dir, "switch", "stack/three")
	expectGrapheneOK(t, repo, "restack", "stack/two")
	state := readState(t, repo.dir)
	wantNested := []Stack{
		{Base: "main", Branches: []string{"stack/one", "stack/two"}},
		{Base: "stack/two", Branches: []string{"stack/three"}},
	}
	if !reflect.DeepEqual(state.Stacks, wantNested) {
		t.Fatalf("setup stacks = %#v, want %#v", state.Stacks, wantNested)
	}

	code, stdout, stderr := repo.runGraphene(t, "sync")
	if code != 0 {
		t.Fatalf("graphene sync exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// Regression for https://github.com/alexghr/graphene/issues/10.
func TestRegressionSquashUsesRenderedParentAcrossNestedStacks(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")

	runGit(t, repo.dir, "switch", "stack/three")
	expectGrapheneOK(t, repo, "restack", "stack/two")
	expectGrapheneOK(t, repo, "squash", "--no-edit")

	if refExists(t, repo.dir, "refs/heads/stack/three") {
		t.Fatal("stack/three still exists after squash")
	}
	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q, want stack/two", got)
	}
	if !refFileExists(t, repo.dir, "stack/two:three.txt") {
		t.Fatal("stack/two does not contain squashed three.txt")
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one", "stack/two"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

// Regression for https://github.com/alexghr/graphene/issues/13.
func TestRegressionRestackUpdatesCurrentBranchFromUpstream(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	expectGrapheneOK(t, repo, "send", "--stack", "origin")

	other := cloneConfiguredRepo(t, remote, "main")
	runGit(t, other, "switch", "-c", "stack/one", "--track", "origin/stack/one")
	writeFile(t, other, "remote-one.txt", "remote one\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "Remote one update")
	runGit(t, other, "push", "origin", "stack/one")

	runGit(t, repo.dir, "switch", "-c", "target", "main")
	writeFile(t, repo.dir, "target.txt", "target\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Target")

	runGit(t, repo.dir, "switch", "stack/one")
	expectGrapheneOK(t, repo, "restack", "target")

	if !refFileExists(t, repo.dir, "stack/one:remote-one.txt") {
		t.Fatal("restack did not incorporate the upstream stack/one update")
	}
}

// Regression for https://github.com/alexghr/graphene/issues/7.
func TestRegressionAmendFailsWithOnlyUnstagedChanges(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	oldHead := runGit(t, repo.dir, "rev-parse", "HEAD")

	writeFile(t, repo.dir, "one.txt", "one amended but unstaged\n")
	code, _, stderr := repo.runGraphene(t, "amend", "-m", "One amended")
	if code == 0 {
		t.Fatal("graphene amend unexpectedly succeeded with only unstaged changes")
	}
	if stderr == "" {
		t.Fatal("graphene amend failed without a diagnostic")
	}
	if got := runGit(t, repo.dir, "rev-parse", "HEAD"); got != oldHead {
		t.Fatalf("HEAD changed from %s to %s", oldHead, got)
	}
	if got := runGit(t, repo.dir, "status", "--short"); got != " M one.txt" {
		t.Fatalf("status = %q, want unstaged one.txt", got)
	}
}

// Regression for https://github.com/alexghr/graphene/issues/2.
func TestRegressionSyncDeletesBranchWhoseUpstreamWasDeleted(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	writeFile(t, repo.dir, "fast.txt", "fast path v1\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "--branch", "fc/fast-field-path", "-m", "Fast field path")
	expectGrapheneOK(t, repo, "send", "origin")

	actor := cloneConfiguredRepo(t, remote, "main")
	runGit(t, actor, "switch", "-c", "fc/fast-field-path", "--track", "origin/fc/fast-field-path")
	writeFile(t, actor, "fast.txt", "fast path v1\nremote edit before delete\n")
	runGit(t, actor, "add", ".")
	runGit(t, actor, "commit", "-m", "Remote branch changed before delete")
	runGit(t, actor, "push", "origin", "fc/fast-field-path")

	runGit(t, repo.dir, "fetch", "origin")
	runGit(t, actor, "push", "origin", "--delete", "fc/fast-field-path")
	runGit(t, actor, "switch", "main")
	writeFile(t, actor, "base-update.txt", "base update\n")
	runGit(t, actor, "add", ".")
	runGit(t, actor, "commit", "-m", "Base update")
	runGit(t, actor, "push", "origin", "main")

	code, stdout, stderr := repo.runGraphene(t, "sync")
	if code != 0 {
		t.Fatalf("graphene sync exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if refExists(t, repo.dir, "refs/heads/fc/fast-field-path") {
		t.Fatal("local branch still exists after its upstream was deleted")
	}
	state := readState(t, repo.dir)
	if len(state.Stacks) != 0 {
		t.Fatalf("stacks = %#v, want empty after deleting remote-removed branch", state.Stacks)
	}
}

func TestRegressionSyncAllowsStackAlreadyBasedOnFetchedBase(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	oldMain := runGit(t, repo.dir, "rev-parse", "main")

	actor := cloneConfiguredRepo(t, remote, "main")
	writeFile(t, actor, "base-update.txt", "base update\n")
	runGit(t, actor, "add", ".")
	runGit(t, actor, "commit", "-m", "Base update")
	runGit(t, actor, "push", "origin", "main")

	runGit(t, repo.dir, "fetch", "origin")
	runGit(t, repo.dir, "merge", "--ff-only", "origin/main")
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	oneBefore := runGit(t, repo.dir, "rev-parse", "stack/one")
	twoBefore := runGit(t, repo.dir, "rev-parse", "stack/two")

	runGit(t, repo.dir, "update-ref", "refs/heads/main", oldMain)
	if got := runGit(t, repo.dir, "rev-list", "--count", "main..stack/one"); got != "2" {
		t.Fatalf("test setup commit count = %q, want 2", got)
	}

	code, stdout, stderr := repo.runGraphene(t, "sync")
	if code != 0 {
		t.Fatalf("graphene sync exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := runGit(t, repo.dir, "rev-parse", "main"); got != runGit(t, repo.dir, "rev-parse", "origin/main") {
		t.Fatalf("main = %s, want origin/main", got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/one"); got != oneBefore {
		t.Fatalf("stack/one changed from %s to %s", oneBefore, got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/two"); got != twoBefore {
		t.Fatalf("stack/two changed from %s to %s", twoBefore, got)
	}
}

func newTestRepoWithOrigin(t *testing.T) (testRepo, string) {
	t.Helper()

	repo := newTestRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")
	return repo, remote
}

func cloneConfiguredRepo(t *testing.T, remote, branch string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "clone")
	args := []string{"clone"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, remote, dir)
	runGit(t, "", args...)
	runGit(t, dir, "config", "user.name", "Graphene Test")
	runGit(t, dir, "config", "user.email", "graphene@example.test")
	runGit(t, dir, "config", "core.editor", "true")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}
