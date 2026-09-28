package graphene

import (
	"path/filepath"
	"reflect"
	"strings"
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
	protectedTip := runGit(t, remote, "rev-parse", "refs/heads/stack/one")

	runGit(t, integrator, "switch", "main")
	writeFile(t, integrator, "base-update.txt", "base update\n")
	runGit(t, integrator, "add", ".")
	runGit(t, integrator, "commit", "-m", "Base update")
	runGit(t, integrator, "push", "origin", "main")

	runGit(t, repo.dir, "switch", "stack/three")
	if code, stdout, stderr := repo.runGraphene(t, "sync"); code != 0 {
		t.Fatalf("sync exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if code, stdout, stderr := repo.runGraphene(t, "sendf", "--stack", "origin"); code == 0 || !strings.Contains(stderr, `refusing to force-push "stack/one" because origin/stack/one already contains the patch from descendant "stack/two"`) {
		t.Fatalf("sendf returned %d, want protective refusal\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := runGit(t, remote, "rev-parse", "refs/heads/stack/one"); got != protectedTip {
		t.Fatalf("protected remote parent moved from %s to %s", protectedTip, got)
	}

	runGit(t, integrator, "fetch", "origin")
	if !refFileExists(t, integrator, "origin/stack/one:two.txt") {
		t.Fatal("sendf removed the squash-merged stack/two patch from remote stack/one")
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

func TestRestackFetchRequiresDestinationUpstream(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "branch", "target", "main")
	before := readState(t, repo.dir)
	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	if code, _, stderr := repo.runGraphene(t, "restack", "--fetch", "target"); code == 0 || !strings.Contains(stderr, `branch "target" has no upstream`) {
		t.Fatalf("restack result: %d, %s", code, stderr)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, before) {
		t.Fatalf("state changed: %#v", got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "HEAD"); got != head {
		t.Fatal("failed restack moved HEAD")
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

func TestSyncUsesRemoteBaseWhenLocalBaseIsAhead(t *testing.T) {
	t.Parallel()
	repo, _ := newTestRepoWithOrigin(t)
	remoteMain := runGit(t, repo.dir, "rev-parse", "origin/main")
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	runGit(t, repo.dir, "switch", "main")
	runGit(t, repo.dir, "merge", "--ff-only", "stack/one")
	localMain := runGit(t, repo.dir, "rev-parse", "main")
	twoBefore := runGit(t, repo.dir, "rev-parse", "stack/two")
	stateBefore := readState(t, repo.dir)

	code, stdout, stderr := repo.runGraphene(t, "sync", "--all", "--dry-run")
	if code != 0 {
		t.Fatalf("graphene sync --all --dry-run exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := runGit(t, repo.dir, "rev-parse", "main"); got != localMain {
		t.Fatalf("main changed from %s to %s during dry run", localMain, got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "origin/main"); got != remoteMain {
		t.Fatalf("origin/main changed from %s to %s during dry run", remoteMain, got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/two"); got != twoBefore {
		t.Fatalf("stack/two changed from %s to %s during dry run", twoBefore, got)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, stateBefore) {
		t.Fatalf("state changed during dry run from %#v to %#v", stateBefore, got)
	}

	expectGrapheneOK(t, repo, "sync", "--all")
	if got := runGit(t, repo.dir, "rev-parse", "main"); got != localMain {
		t.Fatalf("main = %s, want local commit %s", got, localMain)
	}
	if got := runGit(t, repo.dir, "rev-parse", "origin/main"); got != remoteMain {
		t.Fatalf("origin/main = %s, want remote commit %s", got, remoteMain)
	}
	if !refExists(t, repo.dir, "refs/heads/stack/one") {
		t.Fatal("deleted a branch only merged locally")
	}
	assertBranchParent(t, repo.dir, "stack/one", "origin/main")
	assertBranchParent(t, repo.dir, "stack/two", "stack/one")
	if !refFileExists(t, repo.dir, "stack/two:two.txt") {
		t.Fatal("stack/two lost its patch during sync")
	}
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("current branch = %q, want main", got)
	}
	wantState := stateBefore
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, wantState) {
		t.Fatalf("state = %#v, want %#v", got, wantState)
	}
}

func TestSyncUsesRemoteBaseWhenLocalBaseDiverged(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "switch", "main")
	local := commitFile(t, repo.dir, "local.txt", "local\n", "Local base update")
	actor := cloneConfiguredRepo(t, remote, "main")
	upstream := commitFile(t, actor, "remote.txt", "remote\n", "Remote base update")
	runGit(t, actor, "push", "origin", "main")
	expectGrapheneOK(t, repo, "sync", "--all")
	if got := runGit(t, repo.dir, "rev-parse", "main"); got != local {
		t.Fatal("sync changed local-only base history")
	}
	assertBranchParent(t, repo.dir, "stack/one", "origin/main")
	if refFileExists(t, repo.dir, "stack/one:local.txt") {
		t.Fatal("sync included local-only base changes")
	}
	for _, path := range []string{"remote.txt", "one.txt"} {
		if !refFileExists(t, repo.dir, "stack/one:"+path) {
			t.Fatalf("lost %s", path)
		}
	}
	if got := readState(t, repo.dir).Boundaries["stack/one"]; got != upstream {
		t.Fatalf("boundary = %s, want %s", got, upstream)
	}
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("checkout = %s", got)
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
