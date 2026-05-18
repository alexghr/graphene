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

func TestCommitRecordsAppendAndFork(t *testing.T) {
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "First change")
	if got := currentBranch(t, repo.dir); got != "stack/first-change" {
		t.Fatalf("branch = %q", got)
	}

	writeFile(t, repo.dir, "two.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "Second change")

	runGit(t, repo.dir, "checkout", "stack/first-change")
	writeFile(t, repo.dir, "fork.txt", "fork\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "Fork change")

	state := readState(t, repo.dir)
	want := []Stack{
		{Base: "main", Branches: []string{"stack/first-change", "stack/second-change"}},
		{Base: "stack/first-change", Branches: []string{"stack/fork-change"}},
	}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestVersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := NewApp("", nil, &stdout, &stderr, os.Getenv)

	gitVersion, err := (Git{}).Version()
	if err != nil {
		t.Fatalf("git version: %v", err)
	}
	want := fmt.Sprintf("graphene dev\ngit %s\n", gitVersion)
	for _, args := range [][]string{
		{"graphene", "version"},
		{"graphene", "--version"},
	} {
		stdout.Reset()
		stderr.Reset()
		code := app.Run(args)
		if code != 0 {
			t.Fatalf("%v exited %d\nstdout:\n%s\nstderr:\n%s", args, code, stdout.String(), stderr.String())
		}
		if stdout.String() != want {
			t.Fatalf("stdout = %q", stdout.String())
		}
		if stderr.String() != "" {
			t.Fatalf("stderr = %q", stderr.String())
		}
	}
}

func TestCommitExactBranch(t *testing.T) {
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "exact.txt", "exact\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "--branch", "feature/exact", "--message", "Exact subject")

	if got := currentBranch(t, repo.dir); got != "feature/exact" {
		t.Fatalf("branch = %q", got)
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"feature/exact"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestCommitNoVerifyBypassesHook(t *testing.T) {
	repo := newTestRepo(t)
	hook := filepath.Join(repo.dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "--no-verify", "-m", "One")

	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q", got)
	}
}

func TestCommitRecordsExplicitBaseBranch(t *testing.T) {
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

func TestCommitRejectsExplicitBaseAtDifferentCommit(t *testing.T) {
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

func TestCommitDoesNotLeaveTemporaryBranch(t *testing.T) {
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "One")

	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q", got)
	}
	assertNoGrapheneTmpBranches(t, repo.dir)
}

func TestCommitDeletesTemporaryBranchAfterFailedCommit(t *testing.T) {
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

func TestAmendRestacksSuffix(t *testing.T) {
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	runGit(t, repo.dir, "checkout", "stack/one")
	writeFile(t, repo.dir, "one.txt", "one amended\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "amend", "-m", "One amended")

	parent := runGit(t, repo.dir, "rev-parse", "stack/two^")
	branchOne := runGit(t, repo.dir, "rev-parse", "stack/one")
	if parent != branchOne {
		t.Fatalf("stack/two parent = %s, want stack/one %s", parent, branchOne)
	}
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
	}
	state := readState(t, repo.dir)
	if state.Pending != nil {
		t.Fatalf("pending state was not cleared: %#v", state.Pending)
	}
}

func TestAmendRestacksForkedDescendants(t *testing.T) {
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	runGit(t, repo.dir, "checkout", "stack/one")
	createStackBranch(t, repo, "fork.txt", "fork\n", "Fork")

	runGit(t, repo.dir, "checkout", "stack/one")
	writeFile(t, repo.dir, "one.txt", "one amended\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "amend", "-m", "One amended")

	branchOne := runGit(t, repo.dir, "rev-parse", "stack/one")
	for _, branch := range []string{"stack/two", "stack/fork"} {
		parent := runGit(t, repo.dir, "rev-parse", branch+"^")
		if parent != branchOne {
			t.Fatalf("%s parent = %s, want stack/one %s", branch, parent, branchOne)
		}
	}
}

func TestAmendRestacksBaseBranch(t *testing.T) {
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

func TestRestackMovesBranchAndDescendants(t *testing.T) {
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")

	runGit(t, repo.dir, "checkout", "-b", "target", "main")
	writeFile(t, repo.dir, "target.txt", "target\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Target")

	runGit(t, repo.dir, "checkout", "stack/two")
	expectGrapheneOK(t, repo, "restack", "target")

	target := runGit(t, repo.dir, "rev-parse", "target")
	parentTwo := runGit(t, repo.dir, "rev-parse", "stack/two^")
	if parentTwo != target {
		t.Fatalf("stack/two parent = %s, want target %s", parentTwo, target)
	}
	two := runGit(t, repo.dir, "rev-parse", "stack/two")
	parentThree := runGit(t, repo.dir, "rev-parse", "stack/three^")
	if parentThree != two {
		t.Fatalf("stack/three parent = %s, want stack/two %s", parentThree, two)
	}
	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q, want stack/two", got)
	}

	state := readState(t, repo.dir)
	want := []Stack{
		{Base: "main", Branches: []string{"stack/one"}},
		{Base: "target", Branches: []string{"stack/two", "stack/three"}},
	}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestRestackOntoBranchAtSameCommitUpdatesStateOnly(t *testing.T) {
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

func TestSyncRebasesCurrentBranchAndDeletesAppliedIntermediateBranches(t *testing.T) {
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
	writeFile(t, other, "one.txt", "one\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "One (#1)")
	runGit(t, other, "push", "origin", "main")

	runGit(t, repo.dir, "checkout", "stack/two")
	expectGrapheneOK(t, repo, "sync")

	main := runGit(t, repo.dir, "rev-parse", "main")
	originMain := runGit(t, repo.dir, "rev-parse", "origin/main")
	if main != originMain {
		t.Fatalf("main = %s, want origin/main %s", main, originMain)
	}
	if refExists(t, repo.dir, "refs/heads/stack/one") {
		t.Fatal("stack/one still exists")
	}
	parentTwo := runGit(t, repo.dir, "rev-parse", "stack/two^")
	if parentTwo != main {
		t.Fatalf("stack/two parent = %s, want main %s", parentTwo, main)
	}
	branchTwo := runGit(t, repo.dir, "rev-parse", "stack/two")
	parentThree := runGit(t, repo.dir, "rev-parse", "stack/three^")
	if parentThree != branchTwo {
		t.Fatalf("stack/three parent = %s, want stack/two %s", parentThree, branchTwo)
	}
	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q, want stack/two", got)
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/two", "stack/three"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
	if state.Pending != nil {
		t.Fatalf("pending state was not cleared: %#v", state.Pending)
	}
}

func TestSyncRepairsDependentsAfterDeletingMergedAncestor(t *testing.T) {
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

func TestSyncKeepsUnappliedBranches(t *testing.T) {
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
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

	runGit(t, repo.dir, "checkout", "stack/two")
	expectGrapheneOK(t, repo, "sync")

	main := runGit(t, repo.dir, "rev-parse", "main")
	parentOne := runGit(t, repo.dir, "rev-parse", "stack/one^")
	if parentOne != main {
		t.Fatalf("stack/one parent = %s, want main %s", parentOne, main)
	}
	branchOne := runGit(t, repo.dir, "rev-parse", "stack/one")
	parentTwo := runGit(t, repo.dir, "rev-parse", "stack/two^")
	if parentTwo != branchOne {
		t.Fatalf("stack/two parent = %s, want stack/one %s", parentTwo, branchOne)
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one", "stack/two"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestSyncUsesFetchedBaseWhenBaseCheckedOutInAnotherWorktree(t *testing.T) {
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
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

	baseWorktree := filepath.Join(t.TempDir(), "base-worktree")
	runGit(t, repo.dir, "worktree", "add", baseWorktree, "main")
	expectGrapheneOK(t, repo, "sync")

	main := runGit(t, repo.dir, "rev-parse", "main")
	originMain := runGit(t, repo.dir, "rev-parse", "origin/main")
	if main == originMain {
		t.Fatalf("main was updated while checked out in another worktree")
	}
	if status := runGit(t, baseWorktree, "status", "--porcelain"); status != "" {
		t.Fatalf("base worktree status = %q, want clean", status)
	}
	parentOne := runGit(t, repo.dir, "rev-parse", "stack/one^")
	if parentOne != originMain {
		t.Fatalf("stack/one parent = %s, want origin/main %s", parentOne, originMain)
	}
	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q, want stack/two", got)
	}
}

func TestSyncDeletesAppliedStackWhenBaseCheckedOutInAnotherWorktree(t *testing.T) {
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
	originMain := runGit(t, repo.dir, "rev-parse", "origin/main")
	if head != originMain {
		t.Fatalf("HEAD = %s, want origin/main %s", head, originMain)
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

func TestSyncLastAppliedBranchDeletesStack(t *testing.T) {
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	writeFile(t, other, "one.txt", "one\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "One (#1)")
	writeFile(t, other, "two.txt", "two\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "Two (#2)")
	runGit(t, other, "push", "origin", "main")

	runGit(t, repo.dir, "checkout", "stack/two")
	expectGrapheneOK(t, repo, "sync")

	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
	main := runGit(t, repo.dir, "rev-parse", "main")
	originMain := runGit(t, repo.dir, "rev-parse", "origin/main")
	if main != originMain {
		t.Fatalf("main = %s, want origin/main %s", main, originMain)
	}
	for _, branch := range []string{"stack/one", "stack/two"} {
		if refExists(t, repo.dir, "refs/heads/"+branch) {
			t.Fatalf("%s still exists", branch)
		}
	}
	state := readState(t, repo.dir)
	if len(state.Stacks) != 0 || state.Pending != nil {
		t.Fatalf("state = %#v, want empty", state)
	}
}

func TestContinueClearsPendingAfterConflict(t *testing.T) {
	repo := createConflictDuringAmend(t)

	writeFile(t, repo.dir, "file.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
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

func TestContinueWithoutRebaseIsFriendly(t *testing.T) {
	repo := newTestRepo(t)

	code, _, stderr := repo.runGraphene(t, "continue")
	if code == 0 {
		t.Fatal("graphene continue unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "no rebase in progress") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestAbortClearsPendingAfterConflict(t *testing.T) {
	repo := createConflictDuringAmend(t)

	expectGrapheneOK(t, repo, "abort")
	state := readState(t, repo.dir)
	if state.Pending != nil {
		t.Fatalf("pending state was not cleared: %#v", state.Pending)
	}
}

func TestAbortWithoutRebaseIsFriendly(t *testing.T) {
	repo := newTestRepo(t)

	code, _, stderr := repo.runGraphene(t, "abort")
	if code == 0 {
		t.Fatal("graphene abort unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "no rebase in progress") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestCommitRejectsAmendMode(t *testing.T) {
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	code, _, stderr := repo.runGraphene(t, "new", "--amend", "-m", "bad")
	if code == 0 {
		t.Fatal("graphene new --amend unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "graphene amend") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
}

func TestCommitRejectsUnsupportedGitFlags(t *testing.T) {
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	code, _, stderr := repo.runGraphene(t, "new", "--signoff", "-m", "bad")
	if code == 0 {
		t.Fatal("graphene new --signoff unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "unsupported argument") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
}

func TestCommitPrefixConflictFailsBeforeCommit(t *testing.T) {
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

func TestForgetRemovesStackThroughCurrentBranchWithoutDeletingBranches(t *testing.T) {
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")

	runGit(t, repo.dir, "checkout", "stack/two")
	expectGrapheneOK(t, repo, "forget")

	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q, want stack/two", got)
	}
	for _, branch := range []string{"stack/one", "stack/two", "stack/three"} {
		if !refExists(t, repo.dir, "refs/heads/"+branch) {
			t.Fatalf("%s was deleted", branch)
		}
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "stack/two", Branches: []string{"stack/three"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestForgetTipRemovesStackWithoutDeletingBranches(t *testing.T) {
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	expectGrapheneOK(t, repo, "forget")

	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q, want stack/two", got)
	}
	for _, branch := range []string{"stack/one", "stack/two"} {
		if !refExists(t, repo.dir, "refs/heads/"+branch) {
			t.Fatalf("%s was deleted", branch)
		}
	}
	state := readState(t, repo.dir)
	if len(state.Stacks) != 0 || state.Pending != nil {
		t.Fatalf("state = %#v, want empty", state)
	}
}

func TestForgetForceClearsPendingState(t *testing.T) {
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

func TestPushPushesStackAndSetsUpstreams(t *testing.T) {
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")
	runGit(t, repo.dir, "checkout", "stack/two")

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)

	expectGrapheneOK(t, repo, "send", "--dry-run")
	if has, err := (Git{Dir: repo.dir}).HasUpstream("stack/one"); err != nil || has {
		t.Fatalf("dry-run upstream = %v, %v", has, err)
	}

	expectGrapheneOK(t, repo, "send", "--remote", "origin")
	runGit(t, remote, "show-ref", "--verify", "refs/heads/stack/one")
	runGit(t, remote, "show-ref", "--verify", "refs/heads/stack/two")
	if refExists(t, remote, "refs/heads/stack/three") {
		t.Fatal("stack/three was pushed from stack/two")
	}

	for _, branch := range []string{"stack/one", "stack/two"} {
		remoteName := runGit(t, repo.dir, "config", "--get", "branch."+branch+".remote")
		if remoteName != "origin" {
			t.Fatalf("%s remote = %q", branch, remoteName)
		}
		merge := runGit(t, repo.dir, "config", "--get", "branch."+branch+".merge")
		if merge != "refs/heads/"+branch {
			t.Fatalf("%s merge = %q", branch, merge)
		}
	}
	if has, err := (Git{Dir: repo.dir}).HasUpstream("stack/three"); err != nil || has {
		t.Fatalf("stack/three upstream = %v, %v", has, err)
	}

	writeFile(t, repo.dir, "two.txt", "two amended\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "--amend", "-m", "Two amended")
	code, _, stderr := repo.runGraphene(t, "send", "origin")
	if code == 0 {
		t.Fatal("plain push unexpectedly rewrote stack/two")
	}
	if !strings.Contains(stderr, "non-fast-forward") && !strings.Contains(stderr, "fetch first") {
		t.Fatalf("plain push stderr = %q", stderr)
	}
	expectGrapheneOK(t, repo, "sendf", "origin")

	localTwo := runGit(t, repo.dir, "rev-parse", "stack/two")
	remoteTwo := runGit(t, remote, "rev-parse", "refs/heads/stack/two")
	if remoteTwo != localTwo {
		t.Fatalf("remote stack/two = %s, want %s", remoteTwo, localTwo)
	}
}

func TestSendRejectsGitPushFlags(t *testing.T) {
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")

	code, _, stderr := repo.runGraphene(t, "send", "--force-with-lease")
	if code == 0 {
		t.Fatal("graphene send --force-with-lease unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "unsupported argument") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestSendfUsesSameBranchSetAsSendAndStackFlagPushesWholeStack(t *testing.T) {
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "checkout", "stack/three")
	expectGrapheneOK(t, repo, "send", "--stack", "origin")

	runGit(t, repo.dir, "checkout", "stack/two")
	writeFile(t, repo.dir, "two.txt", "two amended\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "amend", "-m", "Two amended")
	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q, want stack/two", got)
	}

	localThree := runGit(t, repo.dir, "rev-parse", "stack/three")
	remoteThree := runGit(t, remote, "rev-parse", "refs/heads/stack/three")
	if remoteThree == localThree {
		t.Fatal("remote stack/three was already updated before pushf")
	}

	expectGrapheneOK(t, repo, "sendf", "origin")

	localTwo := runGit(t, repo.dir, "rev-parse", "stack/two")
	remoteTwo := runGit(t, remote, "rev-parse", "refs/heads/stack/two")
	if remoteTwo != localTwo {
		t.Fatalf("remote stack/two = %s, want %s", remoteTwo, localTwo)
	}
	remoteThree = runGit(t, remote, "rev-parse", "refs/heads/stack/three")
	if remoteThree == localThree {
		t.Fatal("sendf without --stack pushed stack/three")
	}

	expectGrapheneOK(t, repo, "sendf", "--stack", "origin")
	remoteThree = runGit(t, remote, "rev-parse", "refs/heads/stack/three")
	if remoteThree != localThree {
		t.Fatalf("remote stack/three = %s, want %s", remoteThree, localThree)
	}
}

func TestSendPrintsCurrentBranchAndDependencyLinks(t *testing.T) {
	repo := newTestRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "config", "--local", "graphene.prUrlTemplate", "https://example.test/pr/${baseBranch}/${targetBranch}")
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")
	runGit(t, repo.dir, "checkout", "stack/two")

	code, stdout, stderr := repo.runGraphene(t, "send", "--remote", "origin")
	if code != 0 {
		t.Fatalf("graphene send exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	want := "" +
		"Pull request URLs:\n" +
		"  stack/one into main: https://example.test/pr/main/stack/one\n" +
		"  stack/two into stack/one: https://example.test/pr/stack/one/stack/two\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestPrintPullRequestURLs(t *testing.T) {
	repo := newTestRepo(t)
	runGit(t, repo.dir, "remote", "add", "origin", "/tmp/fetch.git")
	runGit(t, repo.dir, "remote", "set-url", "--push", "origin", "git@github.com:AztecProtocol/aztec-packages.git")

	var stdout, stderr bytes.Buffer
	app := NewApp(repo.dir, nil, &stdout, &stderr, func(string) string { return "" })
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"ag/base-change", "ag/head-change"}},
	}}

	if err := app.printPullRequestURLs("origin", state, []string{"ag/base-change", "ag/head-change"}); err != nil {
		t.Fatal(err)
	}
	want := "" +
		"Pull request URLs:\n" +
		"  ag/base-change into main: https://github.com/AztecProtocol/aztec-packages/compare/main...ag/base-change?expand=1\n" +
		"  ag/head-change into ag/base-change: https://github.com/AztecProtocol/aztec-packages/compare/ag/base-change...ag/head-change?expand=1\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestPrintPullRequestURLsFromRepoTemplate(t *testing.T) {
	repo := newTestRepo(t)
	runGit(t, repo.dir, "config", "--local", "graphene.prUrlTemplate", "https://example.com/pr/${baseBranch}/${targetBranch}")

	var stdout, stderr bytes.Buffer
	app := NewApp(repo.dir, nil, &stdout, &stderr, func(string) string { return "" })
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"ag/base-change"}},
	}}

	if err := app.printPullRequestURLs("missing", state, []string{"ag/base-change"}); err != nil {
		t.Fatal(err)
	}
	want := "" +
		"Pull request URLs:\n" +
		"  ag/base-change into main: https://example.com/pr/main/ag/base-change\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestGraphDisplaysForkedStack(t *testing.T) {
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	runGit(t, repo.dir, "checkout", "stack/one")
	createStackBranch(t, repo, "fork.txt", "fork\n", "Fork")

	code, stdout, stderr := repo.runGraphene(t, "graph")
	if code != 0 {
		t.Fatalf("graphene graph exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	want := "" +
		"main\n" +
		"  `- stack/one\n" +
		"     |- stack/two\n" +
		"     `- stack/fork *\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func createStackBranch(t *testing.T, repo testRepo, path, content, message string) {
	t.Helper()
	writeFile(t, repo.dir, path, content)
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", message)
}

func createConflictDuringAmend(t *testing.T) testRepo {
	t.Helper()

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
		t.Fatalf("amend unexpectedly succeeded")
	}
	if stderr == "" {
		t.Fatalf("expected conflict output on stderr")
	}

	state := readState(t, repo.dir)
	if state.Pending == nil {
		t.Fatal("pending state was not recorded")
	}
	return repo
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

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout.String(), stderr.String())
	}
	return strings.TrimRight(stdout.String(), "\n")
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
