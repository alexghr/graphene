package graphene

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type testRepo struct {
	dir       string
	configDir string
}

func newTestRepo(t *testing.T) testRepo {
	t.Helper()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "Graphene Test")
	runGit(t, dir, "config", "user.email", "graphene@example.test")
	runGit(t, dir, "config", "core.editor", "true")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	writeFile(t, dir, "file.txt", "base\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")

	return testRepo{dir: dir, configDir: filepath.Join(t.TempDir(), "xdg")}
}

func (r testRepo) runGraphene(t *testing.T, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	app := NewApp(r.dir, nil, &stdout, &stderr, func(key string) string {
		switch key {
		case "XDG_CONFIG_HOME":
			return r.configDir
		case "HOME":
			return filepath.Dir(r.configDir)
		default:
			return os.Getenv(key)
		}
	})
	code := app.Run(append([]string{"graphene"}, args...))
	return code, stdout.String(), stderr.String()
}

func TestUnknownCommandFallsThroughToGit(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "file.txt", "changed\n")
	writeFile(t, repo.dir, "untracked.txt", "untracked\n")

	// Regression for https://github.com/alexghr/graphene/issues/1.
	code, stdout, stderr := repo.runGraphene(t, "status", "--porcelain")
	wantCode, wantStdout, wantStderr := runGitResult(t, repo.dir, "status", "--porcelain")
	if code != wantCode || stdout != wantStdout || stderr != wantStderr {
		t.Fatalf("graphene status --porcelain = (%d, %q, %q), want git result (%d, %q, %q)", code, stdout, stderr, wantCode, wantStdout, wantStderr)
	}
}

func TestCommitRecordsExplicitBaseBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "checkout", "-b", "alias/one")

	writeFile(t, repo.dir, "two.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "--base", "stack/one", "-m", "Two")

	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q", got)
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one", "stack/two"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestTrackRejectsMultiCommitBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "checkout", "-b", "z")
	runGit(t, repo.dir, "checkout", "-b", "a")
	writeFile(t, repo.dir, "a.txt", "a\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "A")
	writeFile(t, repo.dir, "aa.txt", "aa\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "AA")

	code, _, stderr := repo.runGraphene(t, "track", "--parent", "z")
	if code == 0 {
		t.Fatal("graphene track unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "contains 2 commits") {
		t.Fatalf("stderr = %q", stderr)
	}
	state := readState(t, repo.dir)
	if len(state.Stacks) != 0 {
		t.Fatalf("stacks = %#v, want none", state.Stacks)
	}
}

func TestTrackDoesNotFetchOrAdvanceParent(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "checkout", "-b", "next")
	runGit(t, repo.dir, "push", "-u", "origin", "next")
	oldParent := runGit(t, repo.dir, "rev-parse", "next")

	runGit(t, repo.dir, "checkout", "-b", "fc/remove-ts-avm-simulator")
	for i := 1; i <= 5; i++ {
		commitFile(t, repo.dir, fmt.Sprintf("change-%d.txt", i), fmt.Sprintf("change %d\n", i), fmt.Sprintf("Change %d", i))
	}

	other := cloneConfiguredRepo(t, remote, "next")
	commitFile(t, other, "base.txt", "base update\n", "Base update")
	runGit(t, other, "push", "origin", "next")

	code, _, stderr := repo.runGraphene(t, "track", "--parent", "next")
	if code == 0 {
		t.Fatal("graphene track unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "contains 5 commits") {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stderr, "not an ancestor") {
		t.Fatalf("track reported ancestry failure after moving parent: %q", stderr)
	}
	if got := runGit(t, repo.dir, "rev-parse", "next"); got != oldParent {
		t.Fatalf("next moved from %s to %s", oldParent, got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "origin/next"); got != oldParent {
		t.Fatalf("origin/next = %s, want %s", got, oldParent)
	}
	state := readState(t, repo.dir)
	if len(state.Stacks) != 0 {
		t.Fatalf("stacks = %#v, want none", state.Stacks)
	}
}

func TestImportRejectsMergeHistoryBeforeCreatingBranches(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "checkout", "-b", "feature/imported")

	commitFile(t, repo.dir, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "checkout", "-b", "side")
	commitFile(t, repo.dir, "side.txt", "side\n", "Side")
	runGit(t, repo.dir, "checkout", "feature/imported")
	commitFile(t, repo.dir, "two.txt", "two\n", "Two")
	runGit(t, repo.dir, "merge", "--no-ff", "side", "-m", "Merge side")

	code, _, stderr := repo.runGraphene(t, "import", "main")
	if code == 0 {
		t.Fatal("graphene import unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "Graphene can only import linear one-commit steps") {
		t.Fatalf("stderr = %q", stderr)
	}
	for _, branch := range []string{"stack/one", "stack/two", "stack/merge-side"} {
		if refExists(t, repo.dir, "refs/heads/"+branch) {
			t.Fatalf("import created branch %s before rejecting merge history", branch)
		}
	}
	state := readState(t, repo.dir)
	if len(state.Stacks) != 0 {
		t.Fatalf("stacks = %#v, want none", state.Stacks)
	}
}

func TestCommitReuseCurrentRequiresBaseWhenBaseAmbiguous(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "branch", "alias/main")
	runGit(t, repo.dir, "checkout", "-b", "foo")
	oldHead := runGit(t, repo.dir, "rev-parse", "HEAD")

	writeFile(t, repo.dir, "foo.txt", "foo\n")
	runGit(t, repo.dir, "add", ".")
	code, _, stderr := repo.runGraphene(t, "new", "--reuse-current", "-m", "Foo")
	if code == 0 {
		t.Fatal("graphene new --reuse-current unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "requires --base") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := currentBranch(t, repo.dir); got != "foo" {
		t.Fatalf("branch = %q, want foo", got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "HEAD"); got != oldHead {
		t.Fatalf("HEAD = %q, want %q", got, oldHead)
	}
	state := readState(t, repo.dir)
	if len(state.Stacks) != 0 {
		t.Fatalf("stacks = %#v, want none", state.Stacks)
	}
}

func TestCommitReuseCurrentRejectsRecordedBranchBeforeCommit(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "branch", "alias/one")
	oldHead := runGit(t, repo.dir, "rev-parse", "HEAD")

	writeFile(t, repo.dir, "two.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	code, _, stderr := repo.runGraphene(t, "new", "--reuse-current", "--base", "alias/one", "-m", "Two")
	if code == 0 {
		t.Fatal("graphene new --reuse-current unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "already recorded") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "HEAD"); got != oldHead {
		t.Fatalf("HEAD = %q, want %q", got, oldHead)
	}

	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestCommitRejectsExplicitBaseAtDifferentCommit(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "checkout", "main")

	writeFile(t, repo.dir, "two.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	code, _, stderr := repo.runGraphene(t, "new", "--base", "stack/one", "-m", "Two")
	if code == 0 {
		t.Fatal("graphene new --base unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "does not point to current HEAD") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
	if refExists(t, repo.dir, "refs/heads/stack/two") {
		t.Fatal("stack/two was created")
	}
}

func TestReuseCurrentBaseUsesConfiguredUpstream(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "remote", "add", "origin", ".")
	runGit(t, repo.dir, "config", "branch.main.remote", "origin")
	runGit(t, repo.dir, "config", "branch.main.merge", "refs/heads/main")
	runGit(t, repo.dir, "switch", "-c", "feature")
	commitFile(t, repo.dir, "upstream.txt", "upstream\n", "Advance upstream")
	runGit(t, repo.dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	app := &App{git: Git{Dir: repo.dir}}
	if got, err := app.inferReuseCurrentBase("feature"); err != nil || got != "main" {
		t.Fatalf("inferred base = %q, %v; want main", got, err)
	}
	if err := app.validateNewBase("main"); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.dir, "branch", "other", "main")
	runGit(t, repo.dir, "branch", "--set-upstream-to=origin/main", "other")
	if _, err := app.inferReuseCurrentBase("feature"); err == nil || !strings.Contains(err.Error(), "requires --base") {
		t.Fatalf("ambiguous base: %v", err)
	}
	runGit(t, repo.dir, "switch", "main")
	commitFile(t, repo.dir, "local.txt", "local\n", "Diverge main")
	runGit(t, repo.dir, "switch", "feature")
	if err := app.validateNewBase("main"); err == nil || !strings.Contains(err.Error(), "does not point to current HEAD") {
		t.Fatalf("divergent base: %v", err)
	}
}

func TestCommitDeletesTemporaryBranchAfterFailedCommit(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	code, _, _ := repo.runGraphene(t, "new", "-m", "No changes")
	if code == 0 {
		t.Fatal("commit unexpectedly succeeded")
	}
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q", got)
	}
	assertNoGrapheneTmpBranches(t, repo.dir)
}

func TestSplitAbortRestoresOriginalBranchAndState(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "one.txt", "one\n")
	writeFile(t, repo.dir, "two.txt", "two\n")
	writeFile(t, repo.dir, "three.txt", "three\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "Combined change")
	originalCombined := runGit(t, repo.dir, "rev-parse", "stack/combined-change")
	createStackBranch(t, repo, "after.txt", "after\n", "After")
	originalState := readState(t, repo.dir)

	runGit(t, repo.dir, "checkout", "stack/combined-change")
	expectGrapheneOK(t, repo, "split")
	runGit(t, repo.dir, "add", "one.txt")
	expectGrapheneOK(t, repo, "new", "--reuse-current", "-m", "Add one")
	runGit(t, repo.dir, "add", "two.txt")
	expectGrapheneOK(t, repo, "new", "-m", "Add two")
	expectGrapheneOK(t, repo, "abort")

	if got := currentBranch(t, repo.dir); got != "stack/combined-change" {
		t.Fatalf("branch = %q, want stack/combined-change", got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/combined-change"); got != originalCombined {
		t.Fatalf("stack/combined-change = %s, want original %s", got, originalCombined)
	}
	if status := runGit(t, repo.dir, "status", "--porcelain"); status != "" {
		t.Fatalf("status = %q, want clean", status)
	}
	if refExists(t, repo.dir, "refs/heads/stack/add-two") {
		t.Fatal("stack/add-two still exists after abort")
	}
	state := readState(t, repo.dir)
	if !reflect.DeepEqual(state, originalState) {
		t.Fatalf("state = %#v, want %#v", state, originalState)
	}
}

func TestAmendRestacksBaseBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")

	runGit(t, repo.dir, "checkout", "main")
	writeFile(t, repo.dir, "file.txt", "base amended\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "amend", "-m", "initial amended")

	parent := runGit(t, repo.dir, "rev-parse", "stack/one^")
	main := runGit(t, repo.dir, "rev-parse", "main")
	if parent != main {
		t.Fatalf("stack/one parent = %s, want main %s", parent, main)
	}
}

func TestRestackOntoBranchAtSameCommitUpdatesStateOnly(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	before := runGit(t, repo.dir, "rev-parse", "stack/two")
	runGit(t, repo.dir, "branch", "alias/one", "stack/one")

	expectGrapheneOK(t, repo, "restack", "alias/one")

	after := runGit(t, repo.dir, "rev-parse", "stack/two")
	if after != before {
		t.Fatalf("stack/two changed from %s to %s", before, after)
	}
	state := readState(t, repo.dir)
	want := []Stack{
		{Base: "main", Branches: []string{"stack/one"}},
		{Base: "alias/one", Branches: []string{"stack/two"}},
	}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestRestackReportsDivergedCurrentUpstream(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	expectGrapheneOK(t, repo, "send", "origin")

	other := cloneConfiguredRepo(t, remote, "main")
	runGit(t, other, "switch", "-c", "stack/one", "--track", "origin/stack/one")
	writeFile(t, other, "remote-one.txt", "remote one\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "--amend", "-m", "One remote")
	runGit(t, other, "push", "--force-with-lease", "origin", "stack/one")

	writeFile(t, repo.dir, "local-one.txt", "local one\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "--amend", "-m", "One local")
	localHead := runGit(t, repo.dir, "rev-parse", "stack/one")
	stateBefore := readState(t, repo.dir)

	runGit(t, repo.dir, "switch", "-c", "target", "main")
	writeFile(t, repo.dir, "target.txt", "target\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Target")
	runGit(t, repo.dir, "switch", "stack/one")

	code, _, stderr := repo.runGraphene(t, "restack", "--fetch", "target")
	if code == 0 {
		t.Fatal("graphene restack unexpectedly succeeded")
	}
	for _, want := range []string{
		`current branch "stack/one" diverged from upstream "stack/one@{upstream}"`,
		"rerun without --fetch",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/one"); got != localHead {
		t.Fatalf("stack/one changed from %s to %s", localHead, got)
	}
	if state := readState(t, repo.dir); !reflect.DeepEqual(state, stateBefore) {
		t.Fatalf("state = %#v, want %#v", state, stateBefore)
	}
}

func TestRestackDefaultsToLocalRefs(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	expectGrapheneOK(t, repo, "send", "origin")
	originOneBefore := runGit(t, repo.dir, "rev-parse", "origin/stack/one")

	other := cloneConfiguredRepo(t, remote, "main")
	runGit(t, other, "switch", "-c", "stack/one", "--track", "origin/stack/one")
	writeFile(t, other, "remote-one.txt", "remote one\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "--amend", "-m", "One remote")
	runGit(t, other, "push", "--force-with-lease", "origin", "stack/one")

	writeFile(t, repo.dir, "local-one.txt", "local one\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "--amend", "-m", "One local")

	runGit(t, repo.dir, "switch", "-c", "target", "main")
	writeFile(t, repo.dir, "target.txt", "target\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Target")
	runGit(t, repo.dir, "switch", "stack/one")

	runGit(t, repo.dir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "offline.git"))
	expectGrapheneOK(t, repo, "restack", "target")

	assertBranchParent(t, repo.dir, "stack/one", "target")
	if got := runGit(t, repo.dir, "rev-parse", "origin/stack/one"); got != originOneBefore {
		t.Fatalf("origin/stack/one changed from %s to %s", originOneBefore, got)
	}
	if refFileExists(t, repo.dir, "stack/one:remote-one.txt") {
		t.Fatal("local restack unexpectedly incorporated remote-only content")
	}
	if !refFileExists(t, repo.dir, "stack/one:local-one.txt") {
		t.Fatal("local restack lost local-only content")
	}
}

func TestRestackRejectsRemoteTrackingBase(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	code, _, stderr := repo.runGraphene(t, "restack", "origin/main")
	if code == 0 {
		t.Fatal("graphene restack origin/main unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "remote-tracking ref") {
		t.Fatalf("stderr = %q", stderr)
	}

	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestSyncDoesNotProbeAppliedBranchUpstream(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	inaccessible := filepath.Join(t.TempDir(), "does-not-exist.git")
	runGit(t, repo.dir, "remote", "add", "inaccessible", inaccessible)
	runGit(t, repo.dir, "config", "branch.stack/one.remote", "inaccessible")
	runGit(t, repo.dir, "config", "branch.stack/one.merge", "refs/heads/stack/one")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	commitFile(t, other, "one.txt", "one\n", "One (#1)")
	runGit(t, other, "push", "origin", "main")

	runGit(t, repo.dir, "checkout", "main")
	code, stdout, stderr := repo.runGraphene(t, "sync")
	if code != 0 {
		t.Fatalf("graphene sync exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if refExists(t, repo.dir, "refs/heads/stack/one") {
		t.Fatal("stack/one still exists")
	}
	if got, want := runGit(t, repo.dir, "rev-parse", "main"), runGit(t, remote, "rev-parse", "main"); got != want {
		t.Fatalf("main = %s, want upstream main %s", got, want)
	}
	if state := readState(t, repo.dir); len(state.Stacks) != 0 {
		t.Fatalf("stacks = %#v, want empty", state.Stacks)
	}
}

func TestSyncAllReportsEveryAmbiguousSiblingWithoutMutation(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "push", "-u", "origin", "stack/one")
	runGit(t, repo.dir, "checkout", "main")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	runGit(t, repo.dir, "push", "-u", "origin", "stack/two")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	runGit(t, other, "push", "origin", "--delete", "stack/one", "stack/two")
	commitFile(t, other, "base.txt", "base update\n", "Base update")
	runGit(t, other, "push", "origin", "main")

	runGit(t, repo.dir, "checkout", "main")
	refsBefore := map[string]string{
		"main":      runGit(t, repo.dir, "rev-parse", "main"),
		"stack/one": runGit(t, repo.dir, "rev-parse", "stack/one"),
		"stack/two": runGit(t, repo.dir, "rev-parse", "stack/two"),
	}
	stateBefore := readState(t, repo.dir)

	code, stdout, stderr := repo.runGraphene(t, "sync", "--all")
	if code == 0 {
		t.Fatalf("graphene sync --all unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	for _, branch := range []string{"stack/one", "stack/two"} {
		if !strings.Contains(stderr, `"`+branch+`"`) {
			t.Fatalf("stderr = %q, want ambiguous branch %q", stderr, branch)
		}
	}
	for branch, want := range refsBefore {
		if got := runGit(t, repo.dir, "rev-parse", branch); got != want {
			t.Fatalf("%s changed from %s to %s after refused sync", branch, want, got)
		}
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, stateBefore) {
		t.Fatalf("state changed after refused sync from %#v to %#v", stateBefore, got)
	}

	code, stdout, stderr = repo.runGraphene(t, "sync", "--all", "--dry-run", "--assume-merged")
	if code != 0 {
		t.Fatalf("graphene sync --all --dry-run --assume-merged exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	wantAssumedMerged := "  delete branches assumed merged:\n    stack/one\n    stack/two\n"
	if !strings.Contains(stdout, wantAssumedMerged) {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, wantAssumedMerged)
	}
	for branch, want := range refsBefore {
		if got := runGit(t, repo.dir, "rev-parse", branch); got != want {
			t.Fatalf("%s changed from %s to %s during dry run", branch, want, got)
		}
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, stateBefore) {
		t.Fatalf("state changed during dry run from %#v to %#v", stateBefore, got)
	}
}

func TestSyncAssumeMergedStopsAtLiveUpstream(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "applied.txt", "applied\n", "Applied")
	createStackBranch(t, repo, "missing.txt", "missing\n", "Missing")
	createStackBranch(t, repo, "live.txt", "live\n", "Live")
	createStackBranch(t, repo, "later-missing.txt", "later missing\n", "Later missing")
	runGit(t, repo.dir, "push", "-u", "origin", "stack/applied", "stack/missing", "stack/live", "stack/later-missing")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	commitFile(t, other, "applied.txt", "applied\n", "Applied (#1)")
	runGit(t, other, "push", "origin", "main")
	runGit(t, other, "push", "origin", "--delete", "stack/missing", "stack/later-missing")

	runGit(t, repo.dir, "checkout", "main")
	code, _, stderr := repo.runGraphene(t, "sync", "--dry-run")
	if code == 0 {
		t.Fatal("graphene sync --dry-run unexpectedly accepted a missing unapplied upstream")
	}
	if !strings.Contains(stderr, `"stack/missing"`) {
		t.Fatalf("stderr = %q, want stack/missing", stderr)
	}
	if strings.Contains(stderr, `"stack/later-missing"`) {
		t.Fatalf("stderr = %q, must not leapfrog the live stack/live upstream", stderr)
	}

	code, stdout, stderr := repo.runGraphene(t, "sync", "--dry-run", "--assume-merged")
	if code != 0 {
		t.Fatalf("graphene sync --dry-run --assume-merged exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	wantApplied := "  delete applied branches:\n    stack/applied\n"
	if !strings.Contains(stdout, wantApplied) {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, wantApplied)
	}
	wantAssumedMerged := "  delete branches assumed merged:\n    stack/missing\n"
	if !strings.Contains(stdout, wantAssumedMerged) {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, wantAssumedMerged)
	}
	if strings.Contains(stdout, "  delete branches assumed merged:\n    stack/missing\n    stack/later-missing\n") {
		t.Fatalf("stdout = %q, must not leapfrog the live stack/live upstream", stdout)
	}
}

func TestSyncRejectsTrackedBranchWithExtraCommit(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	runGit(t, repo.dir, "checkout", "stack/one")
	writeFile(t, repo.dir, "comments.txt", "removed comments\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Comment cleanup")
	beforeTwo := runGit(t, repo.dir, "rev-parse", "stack/two")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	writeFile(t, other, "one.txt", "one\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "One (#1)")
	runGit(t, other, "push", "origin", "main")

	runGit(t, repo.dir, "checkout", "stack/two")
	code, _, stderr := repo.runGraphene(t, "sync")
	if code == 0 {
		t.Fatal("graphene sync unexpectedly succeeded")
	}
	if !strings.Contains(stderr, `branch "stack/one" contains 2 commits on top of "main"`) {
		t.Fatalf("stderr = %q", stderr)
	}
	afterTwo := runGit(t, repo.dir, "rev-parse", "stack/two")
	if afterTwo != beforeTwo {
		t.Fatalf("stack/two changed from %s to %s", beforeTwo, afterTwo)
	}
}

func TestSyncRepairsDependentsAfterDeletingMergedAncestor(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	runGit(t, repo.dir, "checkout", "stack/one")
	createStackBranch(t, repo, "fork.txt", "fork\n", "Fork")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	writeFile(t, other, "one.txt", "one\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "One (#1)")
	runGit(t, other, "push", "origin", "main")

	runGit(t, repo.dir, "checkout", "stack/two")
	expectGrapheneOK(t, repo, "sync")

	main := runGit(t, repo.dir, "rev-parse", "main")
	for _, branch := range []string{"stack/two", "stack/fork"} {
		parent := runGit(t, repo.dir, "rev-parse", branch+"^")
		if parent != main {
			t.Fatalf("%s parent = %s, want main %s", branch, parent, main)
		}
	}
	if refExists(t, repo.dir, "refs/heads/stack/one") {
		t.Fatal("stack/one still exists")
	}
	state := readState(t, repo.dir)
	want := []Stack{
		{Base: "main", Branches: []string{"stack/two"}},
		{Base: "main", Branches: []string{"stack/fork"}},
	}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestSyncRebasesSurvivingStackSuffixAfterDeletingNestedPath(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "child.txt", "child\n", "Child")
	createStackBranch(t, repo, "child-two.txt", "child two\n", "Child two")
	runGit(t, repo.dir, "checkout", "stack/two")
	createStackBranch(t, repo, "leaf.txt", "leaf\n", "Leaf")

	state := readState(t, repo.dir)
	wantSetup := []Stack{
		{Base: "main", Branches: []string{"stack/one", "stack/two", "stack/child", "stack/child-two"}},
		{Base: "stack/two", Branches: []string{"stack/leaf"}},
	}
	if !reflect.DeepEqual(state.Stacks, wantSetup) {
		t.Fatalf("setup stacks = %#v, want %#v", state.Stacks, wantSetup)
	}

	runGit(t, repo.dir, "push", "-u", "origin", "stack/one", "stack/two", "stack/leaf")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	commitFile(t, other, "one.txt", "one\n", "One (#1)")
	commitFile(t, other, "two.txt", "two\n", "Two (#2)")
	commitFile(t, other, "leaf.txt", "leaf\n", "Leaf (#3)")
	runGit(t, other, "push", "origin", "main")
	runGit(t, other, "push", "origin", "--delete", "stack/one", "stack/two", "stack/leaf")

	childBefore := runGit(t, repo.dir, "rev-parse", "stack/child")
	childTwoBefore := runGit(t, repo.dir, "rev-parse", "stack/child-two")
	code, stdout, stderr := repo.runGraphene(t, "sync", "--dry-run")
	if code != 0 {
		t.Fatalf("graphene sync --dry-run exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "git rebase --no-update-refs --onto") || !strings.Contains(stdout, " stack/child-two") {
		t.Fatalf("stdout = %q, want surviving suffix rebase", stdout)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/child"); got != childBefore {
		t.Fatalf("stack/child changed during dry run from %s to %s", childBefore, got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/child-two"); got != childTwoBefore {
		t.Fatalf("stack/child-two changed during dry run from %s to %s", childTwoBefore, got)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, state) {
		t.Fatalf("state changed during dry run from %#v to %#v", state, got)
	}

	expectGrapheneOK(t, repo, "sync")

	main := runGit(t, repo.dir, "rev-parse", "main")
	if got := runGit(t, remote, "rev-parse", "main"); got != main {
		t.Fatalf("upstream main = %s, want main %s", got, main)
	}
	for _, branch := range []string{"stack/one", "stack/two", "stack/leaf"} {
		if refExists(t, repo.dir, "refs/heads/"+branch) {
			t.Fatalf("%s still exists", branch)
		}
	}
	assertBranchParent(t, repo.dir, "stack/child", "main")
	assertBranchParent(t, repo.dir, "stack/child-two", "stack/child")
	for _, edge := range [][2]string{{"main", "stack/child"}, {"stack/child", "stack/child-two"}} {
		if got := runGit(t, repo.dir, "rev-list", "--count", edge[0]+".."+edge[1]); got != "1" {
			t.Fatalf("%s contains %s commits on top of %s, want 1", edge[1], got, edge[0])
		}
	}
	if got := currentBranch(t, repo.dir); got != "stack/child" {
		t.Fatalf("branch = %q, want stack/child", got)
	}
	state = readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/child", "stack/child-two"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
	if state.Pending != nil {
		t.Fatalf("pending state was not cleared: %#v", state.Pending)
	}
}

func TestSyncRetargetPlanFailureDoesNotAdvanceBaseOrCreatePending(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")
	oldMain := runGit(t, repo.dir, "rev-parse", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "survivor.txt", "survivor\n", "Survivor")
	runGit(t, repo.dir, "checkout", "stack/two")
	createStackBranch(t, repo, "child.txt", "child\n", "Child")

	stateBefore := readState(t, repo.dir)
	stateBefore.Stacks = append(stateBefore.Stacks, Stack{
		Base:     "stack/two",
		Branches: []string{"stack/child"},
	})
	if err := (Git{Dir: repo.dir}).WriteState(stateBefore); err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	commitFile(t, other, "one.txt", "one\n", "One (#1)")
	commitFile(t, other, "two.txt", "two\n", "Two (#2)")
	runGit(t, other, "push", "origin", "main")
	newMain := runGit(t, other, "rev-parse", "main")

	runGit(t, repo.dir, "checkout", "main")
	code, _, stderr := repo.runGraphene(t, "sync", "--all")
	if code == 0 {
		t.Fatal("graphene sync --all unexpectedly succeeded")
	}
	if !strings.Contains(stderr, `duplicate branch "stack/child" in stack state`) {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := runGit(t, repo.dir, "rev-parse", "refs/graphene/fetch/main"); got != newMain {
		t.Fatalf("fetched main = %s, want %s", got, newMain)
	}
	if got := runGit(t, repo.dir, "rev-parse", "origin/main"); got != newMain {
		t.Fatalf("origin/main = %s, want fetched commit %s", got, newMain)
	}
	if got := runGit(t, repo.dir, "rev-parse", "main"); got != oldMain {
		t.Fatalf("main changed from %s to %s after planning failed", oldMain, got)
	}
	stateAfter := readState(t, repo.dir)
	if !reflect.DeepEqual(stateAfter, stateBefore) {
		t.Fatalf("state after planning failure = %#v, want %#v", stateAfter, stateBefore)
	}
	if stateAfter.Pending != nil {
		t.Fatalf("pending state was created after planning failure: %#v", stateAfter.Pending)
	}
}

func TestSyncSkipsCheckedOutDescendantBeforeMovingAncestor(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	writeFile(t, other, "base.txt", "base update\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "Base update")
	runGit(t, other, "push", "origin", "main")

	descendantWorktree := filepath.Join(t.TempDir(), "descendant-worktree")
	runGit(t, repo.dir, "worktree", "add", descendantWorktree, "stack/two")
	runGit(t, repo.dir, "switch", "stack/one")

	beforeOne := runGit(t, repo.dir, "rev-parse", "stack/one")
	code, stdout, stderr := repo.runGraphene(t, "sync")
	if code != 0 {
		t.Fatalf("graphene sync exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, "Skipping stacks checked out in another worktree:") ||
		!strings.Contains(stdout, "stack/one: stack/two") {
		t.Fatalf("stdout = %q", stdout)
	}
	afterOne := runGit(t, repo.dir, "rev-parse", "stack/one")
	if afterOne != beforeOne {
		t.Fatalf("stack/one changed from %s to %s", beforeOne, afterOne)
	}
	if got := runGit(t, descendantWorktree, "status", "--porcelain"); got != "" {
		t.Fatalf("descendant worktree status = %q, want clean", got)
	}
}

func TestSyncAllRejectsCheckedOutStackUnlessForced(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")
	oldMain := runGit(t, repo.dir, "rev-parse", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "checkout", "main")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	writeFile(t, other, "base.txt", "base update\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "Base update")
	runGit(t, other, "push", "origin", "main")

	checkedOutWorktree := filepath.Join(t.TempDir(), "checked-out-worktree")
	runGit(t, repo.dir, "worktree", "add", checkedOutWorktree, "stack/one")
	runGit(t, repo.dir, "checkout", "main")

	beforeTwoParent := runGit(t, repo.dir, "rev-parse", "stack/two^")
	stateBefore := readState(t, repo.dir)

	code, stdout, stderr := repo.runGraphene(t, "sync", "--all")
	if code == 0 {
		t.Fatal("graphene sync --all unexpectedly succeeded")
	}
	if !strings.Contains(stdout, "Skipping stacks checked out in another worktree:") ||
		!strings.Contains(stdout, "stack/one: stack/one") {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "sync would leave skipped stacks stale") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := runGit(t, repo.dir, "rev-parse", "main"); got != oldMain {
		t.Fatalf("main changed from %s to %s", oldMain, got)
	}
	if got, want := runGit(t, repo.dir, "rev-parse", "origin/main"), runGit(t, remote, "rev-parse", "main"); got != want {
		t.Fatalf("origin/main = %s, want fetched commit %s", got, want)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/two^"); got != beforeTwoParent {
		t.Fatalf("stack/two parent changed from %s to %s", beforeTwoParent, got)
	}
	if state := readState(t, repo.dir); !reflect.DeepEqual(state, stateBefore) {
		t.Fatalf("state = %#v, want %#v", state, stateBefore)
	}

	code, stdout, stderr = repo.runGraphene(t, "sync", "--all", "--force")
	if code != 0 {
		t.Fatalf("graphene sync --all --force exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Skipping stacks checked out in another worktree:") ||
		!strings.Contains(stdout, "stack/one: stack/one") {
		t.Fatalf("stdout = %q", stdout)
	}

	main := runGit(t, repo.dir, "rev-parse", "main")
	upstreamMain := runGit(t, remote, "rev-parse", "main")
	if main != upstreamMain {
		t.Fatalf("main = %s, want upstream main %s", main, upstreamMain)
	}
	parentOne := runGit(t, repo.dir, "rev-parse", "stack/one^")
	if parentOne != oldMain {
		t.Fatalf("stack/one parent = %s, want old main %s", parentOne, oldMain)
	}
	parentTwo := runGit(t, repo.dir, "rev-parse", "stack/two^")
	if parentTwo != main {
		t.Fatalf("stack/two parent = %s, want main %s", parentTwo, main)
	}
	if status := runGit(t, checkedOutWorktree, "status", "--porcelain"); status != "" {
		t.Fatalf("checked-out worktree status = %q, want clean", status)
	}
	state := readState(t, repo.dir)
	want := []Stack{
		{Base: "main", Branches: []string{"stack/one"}},
		{Base: "main", Branches: []string{"stack/two"}},
	}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestSyncDeletesAppliedStackWhenBaseCheckedOutInAnotherWorktree(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	writeFile(t, other, "one.txt", "one\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "One (#1)")
	runGit(t, other, "push", "origin", "main")

	baseWorktree := filepath.Join(t.TempDir(), "base-worktree")
	runGit(t, repo.dir, "worktree", "add", baseWorktree, "main")
	expectGrapheneOK(t, repo, "sync")

	if got := runGit(t, repo.dir, "branch", "--show-current"); got != "" {
		t.Fatalf("branch = %q, want detached HEAD", got)
	}
	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	upstreamMain := runGit(t, remote, "rev-parse", "main")
	if head != upstreamMain {
		t.Fatalf("HEAD = %s, want upstream main %s", head, upstreamMain)
	}
	if refExists(t, repo.dir, "refs/heads/stack/one") {
		t.Fatal("stack/one still exists")
	}
	if status := runGit(t, baseWorktree, "status", "--porcelain"); status != "" {
		t.Fatalf("base worktree status = %q, want clean", status)
	}
	state := readState(t, repo.dir)
	if len(state.Stacks) != 0 || state.Pending != nil {
		t.Fatalf("state = %#v, want empty", state)
	}
}

func TestSyncAbortRestoresBranchDeletedDuringFinalization(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "push", "-u", "origin", "stack/one")

	original := readState(t, repo.dir)
	one := runGit(t, repo.dir, "rev-parse", "stack/one")
	state := original
	state.Pending = &Pending{
		Operation:      "sync",
		Branch:         "stack/one",
		ReturnBranch:   "main",
		Branches:       []string{"stack/one"},
		OriginalRefs:   map[string]string{"stack/one": one},
		OriginalStacks: cloneStacks(original.Stacks),
	}
	if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.dir, "checkout", "main")
	runGit(t, repo.dir, "update-ref", "-d", "refs/heads/stack/one", one)
	if got := runGit(t, repo.dir, "config", "--get", "branch.stack/one.remote"); got != "origin" {
		t.Fatalf("upstream config was removed during ref-only finalization: %q", got)
	}

	expectGrapheneOK(t, repo, "abort")

	if got := runGit(t, repo.dir, "rev-parse", "stack/one"); got != one {
		t.Fatalf("stack/one = %s after abort, want %s", got, one)
	}
	if got := runGit(t, repo.dir, "config", "--get", "branch.stack/one.remote"); got != "origin" {
		t.Fatalf("upstream remote after abort = %q, want origin", got)
	}
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch after abort = %q, want stack/one", got)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, original) {
		t.Fatalf("state after abort = %#v, want %#v", got, original)
	}
}

func TestContinueResumesJournaledSyncBaseFastForward(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")
	oldMain := runGit(t, repo.dir, "rev-parse", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	oldOne := runGit(t, repo.dir, "rev-parse", "stack/one")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	commitFile(t, other, "base.txt", "base update\n", "Base update")
	runGit(t, other, "push", "origin", "main")
	runGit(t, repo.dir, "fetch", "origin", "main")
	newMain := runGit(t, repo.dir, "rev-parse", "origin/main")

	state := readState(t, repo.dir)
	state.Pending = &Pending{
		Operation:    "sync",
		Branch:       "stack/one",
		ReturnBranch: "stack/one",
		Queue: []RebaseOp{{
			Onto:     newMain,
			Upstream: oldMain,
			Top:      "stack/one",
		}},
		NextStacks:     cloneStacks(state.Stacks),
		OriginalRefs:   map[string]string{"main": oldMain, "stack/one": oldOne},
		OriginalStacks: cloneStacks(state.Stacks),
		SyncBase:       "main",
		SyncBaseOld:    oldMain,
		SyncBaseNew:    newMain,
		SyncBaseUpdate: true,
	}
	if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, repo.dir, "rev-parse", "main"); got != oldMain {
		t.Fatalf("main = %s before continue, want old main %s", got, oldMain)
	}

	expectGrapheneOK(t, repo, "continue")

	if got := runGit(t, repo.dir, "rev-parse", "main"); got != newMain {
		t.Fatalf("main = %s after continue, want fetched main %s", got, newMain)
	}
	assertBranchParent(t, repo.dir, "stack/one", "main")
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q after continue, want stack/one", got)
	}
	state = readState(t, repo.dir)
	if state.Pending != nil {
		t.Fatalf("pending state was not cleared: %#v", state.Pending)
	}
	want := []Stack{{Base: "main", Branches: []string{"stack/one"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestContinueFinishesPendingSyncWithEmptyQueue(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "checkout", "main")
	state := readState(t, repo.dir)
	state.Pending = &Pending{
		Operation:    "sync",
		Branch:       "main",
		ReturnBranch: "main",
		Branches:     []string{"stack/one"},
	}
	if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}

	// A failed finalization may already have completed some cleanup before retry.
	runGit(t, repo.dir, "branch", "-D", "stack/one")
	expectGrapheneOK(t, repo, "continue")

	if refExists(t, repo.dir, "refs/heads/stack/one") {
		t.Fatal("stack/one still exists")
	}
	state = readState(t, repo.dir)
	if len(state.Stacks) != 0 || state.Pending != nil {
		t.Fatalf("state = %#v, want completed empty sync state", state)
	}
}

func TestContinueFinishesLegacyPendingSyncWithoutSnapshots(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "checkout", "main")
	state := readState(t, repo.dir)
	state.Pending = &Pending{
		Operation:    "sync",
		Branch:       "main",
		ReturnBranch: "main",
		Branches:     []string{"stack/one"},
	}
	if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}

	expectGrapheneOK(t, repo, "continue")

	if refExists(t, repo.dir, "refs/heads/stack/one") {
		t.Fatal("stack/one still exists")
	}
	state = readState(t, repo.dir)
	if len(state.Stacks) != 0 || state.Pending != nil {
		t.Fatalf("state = %#v, want completed empty sync state", state)
	}
}

func TestContinueRestoresRebaseStateAfterCommitCreationFailure(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "file.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "One")

	writeFile(t, repo.dir, "file.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "Two")

	runGit(t, repo.dir, "checkout", "stack/one")
	writeFile(t, repo.dir, "file.txt", "amended\n")
	runGit(t, repo.dir, "add", ".")
	code, _, stderr := repo.runGraphene(t, "amend", "-m", "One amended")
	if code == 0 {
		t.Fatal("amend unexpectedly succeeded")
	}
	if stderr == "" {
		t.Fatalf("expected conflict output on stderr")
	}

	hook := filepath.Join(repo.dir, ".git", "hooks", "prepare-commit-msg")
	writeExecutable(t, hook, "#!/bin/sh\necho prepare-commit-msg blocked commit >&2\nexit 1\n")
	writeFile(t, repo.dir, "file.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	code, _, stderr = repo.runGraphene(t, "continue")
	if code == 0 {
		t.Fatal("graphene continue unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "prepare-commit-msg blocked commit") {
		t.Fatalf("stderr = %q", stderr)
	}
	stoppedSHA := filepath.Join(repo.dir, ".git", "rebase-merge", "stopped-sha")
	info, err := os.Stat(stoppedSHA)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("stopped-sha was not restored")
	}

	writeExecutable(t, hook, "#!/bin/sh\nexit 0\n")
	expectGrapheneOK(t, repo, "continue")

	state := readState(t, repo.dir)
	if state.Pending != nil {
		t.Fatalf("pending state was not cleared: %#v", state.Pending)
	}
	parent := runGit(t, repo.dir, "rev-parse", "stack/two^")
	branchOne := runGit(t, repo.dir, "rev-parse", "stack/one")
	if parent != branchOne {
		t.Fatalf("stack/two parent = %s, want stack/one %s", parent, branchOne)
	}
}

func TestCommitPrefixConflictFailsBeforeCommit(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "branch", "stack")

	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	code, _, stderr := repo.runGraphene(t, "new", "-m", "One")
	if code == 0 {
		t.Fatal("graphene new unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "branch prefix") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
	if got := runGit(t, repo.dir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("commit count = %s, want 1", got)
	}
}

func TestCommitSkipsBranchNamesRecordedInStaleState(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")

	runGit(t, repo.dir, "checkout", "main")
	runGit(t, repo.dir, "branch", "-D", "stack/one")

	writeFile(t, repo.dir, "two.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "One")
	if got := currentBranch(t, repo.dir); got != "stack/one-2" {
		t.Fatalf("branch = %q, want stack/one-2", got)
	}
}

func TestForgetForceClearsPendingState(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	state := readState(t, repo.dir)
	state.Pending = &Pending{
		Operation: "sync",
		Branch:    "stack/one",
		Queue: []RebaseOp{
			{Onto: "stack/one", Upstream: "old-one", Top: "stack/two"},
		},
	}
	if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := repo.runGraphene(t, "forget")
	if code == 0 {
		t.Fatal("graphene forget unexpectedly ignored pending state")
	}
	if !strings.Contains(stderr, "pending rebase exists") {
		t.Fatalf("stderr = %q", stderr)
	}

	expectGrapheneOK(t, repo, "forget", "--force")
	state = readState(t, repo.dir)
	if len(state.Stacks) != 0 || state.Pending != nil {
		t.Fatalf("state = %#v, want empty", state)
	}
	for _, branch := range []string{"stack/one", "stack/two"} {
		if !refExists(t, repo.dir, "refs/heads/"+branch) {
			t.Fatalf("%s was deleted", branch)
		}
	}
}

// Feature for https://github.com/alexghr/graphene/issues/3.

func TestDeleteRejectsTrackedDescendants(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	runGit(t, repo.dir, "checkout", "stack/one")
	code, _, stderr := repo.runGraphene(t, "delete")
	if code == 0 {
		t.Fatal("graphene delete unexpectedly deleted a branch with descendants")
	}
	if !strings.Contains(stderr, `branch "stack/one" has tracked descendants`) {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
	}
	for _, branch := range []string{"stack/one", "stack/two"} {
		if !refExists(t, repo.dir, "refs/heads/"+branch) {
			t.Fatalf("%s was deleted", branch)
		}
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one", "stack/two"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestSendAllowsUnrelatedPendingRebaseInAnotherWorktree(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "independent.txt", "independent\n", "Independent")
	runGit(t, repo.dir, "checkout", "main")
	sendWorktree := filepath.Join(t.TempDir(), "send-worktree")
	runGit(t, repo.dir, "worktree", "add", sendWorktree, "stack/independent")

	createStackBranch(t, repo, "file.txt", "one\n", "One")
	createStackBranch(t, repo, "file.txt", "two\n", "Two")
	runGit(t, repo.dir, "checkout", "stack/one")
	writeFile(t, repo.dir, "file.txt", "amended\n")
	runGit(t, repo.dir, "add", ".")
	code, _, stderr := repo.runGraphene(t, "amend", "-m", "One amended")
	if code == 0 {
		t.Fatal("graphene amend unexpectedly succeeded")
	}
	if stderr == "" {
		t.Fatalf("expected conflict output on stderr")
	}

	sendRepo := testRepo{dir: sendWorktree, configDir: repo.configDir}
	expectGrapheneOK(t, sendRepo, "send", "--dry-run", "origin")
	runGit(t, sendWorktree, "switch", "stack/one")
	before := runGit(t, repo.dir, "ls-remote", "origin")
	code, _, stderr = sendRepo.runGraphene(t, "send", "--dry-run", "origin")
	if code == 0 || !strings.Contains(stderr, "pending rebase in another worktree is rewriting branch") {
		t.Fatalf("send for pending branch = (%d, %q), want worktree ownership refusal", code, stderr)
	}
	if after := runGit(t, repo.dir, "ls-remote", "origin"); after != before {
		t.Fatalf("remote refs changed from %q to %q", before, after)
	}
}

func createStackBranch(t *testing.T, repo testRepo, path, content, message string) {
	t.Helper()
	writeFile(t, repo.dir, path, content)
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", message)
}

func commitFile(t *testing.T, dir, path, content, message string) string {
	t.Helper()
	writeFile(t, dir, path, content)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", message)
	return runGit(t, dir, "rev-parse", "HEAD")
}

func expectGrapheneOK(t *testing.T, repo testRepo, args ...string) {
	t.Helper()
	code, stdout, stderr := repo.runGraphene(t, args...)
	if code != 0 {
		t.Fatalf("graphene %v exited %d\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
	}
}

func readState(t *testing.T, dir string) State {
	t.Helper()
	state, err := (Git{Dir: dir}).ReadState()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	branch, err := (Git{Dir: dir}).CurrentBranch()
	if err != nil {
		t.Fatal(err)
	}
	return branch
}

func assertBranchParent(t *testing.T, dir, branch, parent string) {
	t.Helper()
	got := runGit(t, dir, "rev-parse", branch+"^")
	want := runGit(t, dir, "rev-parse", parent)
	if got != want {
		t.Fatalf("%s parent = %s, want %s %s", branch, got, parent, want)
	}
}

func assertNoGrapheneTmpBranches(t *testing.T, dir string) {
	t.Helper()
	branches, err := (Git{Dir: dir}).LocalBranches()
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range branches {
		if strings.HasPrefix(branch, "graphene/tmp-") {
			t.Fatalf("temporary branch was not cleaned up: %s", branch)
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	code, stdout, stderr := runGitResult(t, dir, args...)
	if code != 0 {
		t.Fatalf("git %v exited %d\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
	}
	return strings.TrimRight(stdout, "\n")
}

func runGitResult(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), stdout.String(), stderr.String()
		}
		t.Fatalf("git %v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout.String(), stderr.String())
	}
	return 0, stdout.String(), stderr.String()
}

func refExists(t *testing.T, dir, ref string) bool {
	t.Helper()

	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", ref)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return true
	}
	if _, ok := err.(*exec.ExitError); ok {
		return false
	}
	t.Fatalf("git show-ref failed: %v", err)
	return false
}

func refFileExists(t *testing.T, dir, spec string) bool {
	t.Helper()

	cmd := exec.Command("git", "cat-file", "-e", spec)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return true
	}
	if _, ok := err.(*exec.ExitError); ok {
		return false
	}
	t.Fatalf("git cat-file failed: %v", err)
	return false
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
