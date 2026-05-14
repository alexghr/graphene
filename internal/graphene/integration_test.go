package graphene

import (
	"bytes"
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
	expectGrapheneOK(t, repo, "commit", "-m", "First change")
	if got := currentBranch(t, repo.dir); got != "stack/first-change" {
		t.Fatalf("branch = %q", got)
	}

	writeFile(t, repo.dir, "two.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "commit", "-m", "Second change")

	runGit(t, repo.dir, "checkout", "stack/first-change")
	writeFile(t, repo.dir, "fork.txt", "fork\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "commit", "-m", "Fork change")

	state := readState(t, repo.dir)
	want := []Stack{
		{Base: "main", Branches: []string{"stack/first-change", "stack/second-change"}},
		{Base: "stack/first-change", Branches: []string{"stack/fork-change"}},
	}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestCommitExactBranch(t *testing.T) {
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "exact.txt", "exact\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "commit", "-b", "feature/exact", "-m", "Exact subject")

	if got := currentBranch(t, repo.dir); got != "feature/exact" {
		t.Fatalf("branch = %q", got)
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"feature/exact"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
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

func TestRebasePullsBaseAndRestacksWholeStack(t *testing.T) {
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

	runGit(t, repo.dir, "checkout", "stack/one")
	expectGrapheneOK(t, repo, "rebase")

	main := runGit(t, repo.dir, "rev-parse", "main")
	originMain := runGit(t, repo.dir, "rev-parse", "origin/main")
	if main != originMain {
		t.Fatalf("main = %s, want origin/main %s", main, originMain)
	}
	parentOne := runGit(t, repo.dir, "rev-parse", "stack/one^")
	if parentOne != main {
		t.Fatalf("stack/one parent = %s, want main %s", parentOne, main)
	}
	branchOne := runGit(t, repo.dir, "rev-parse", "stack/one")
	parentTwo := runGit(t, repo.dir, "rev-parse", "stack/two^")
	if parentTwo != branchOne {
		t.Fatalf("stack/two parent = %s, want stack/one %s", parentTwo, branchOne)
	}
	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q, want stack/two", got)
	}
	state := readState(t, repo.dir)
	if state.Pending != nil {
		t.Fatalf("pending state was not cleared: %#v", state.Pending)
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

func TestAbortClearsPendingAfterConflict(t *testing.T) {
	repo := createConflictDuringAmend(t)

	expectGrapheneOK(t, repo, "abort")
	state := readState(t, repo.dir)
	if state.Pending != nil {
		t.Fatalf("pending state was not cleared: %#v", state.Pending)
	}
}

func TestCommitRejectsAmendMode(t *testing.T) {
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	code, _, stderr := repo.runGraphene(t, "commit", "--amend", "-m", "bad")
	if code == 0 {
		t.Fatal("graphene commit --amend unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "graphene amend") {
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
	code, _, stderr := repo.runGraphene(t, "commit", "-m", "One")
	if code == 0 {
		t.Fatal("graphene commit unexpectedly succeeded")
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
	expectGrapheneOK(t, repo, "commit", "-m", "One")
	if got := currentBranch(t, repo.dir); got != "stack/one-2" {
		t.Fatalf("branch = %q, want stack/one-2", got)
	}
}

func TestPushPushesStackAndSetsUpstreams(t *testing.T) {
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)

	expectGrapheneOK(t, repo, "push", "--dry-run")
	if has, err := (Git{Dir: repo.dir}).HasUpstream("stack/one"); err != nil || has {
		t.Fatalf("dry-run upstream = %v, %v", has, err)
	}

	expectGrapheneOK(t, repo, "push", "origin")
	runGit(t, remote, "show-ref", "--verify", "refs/heads/stack/one")
	runGit(t, remote, "show-ref", "--verify", "refs/heads/stack/two")

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
}

func createStackBranch(t *testing.T, repo testRepo, path, content, message string) {
	t.Helper()
	writeFile(t, repo.dir, path, content)
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "commit", "-m", message)
}

func createConflictDuringAmend(t *testing.T) testRepo {
	t.Helper()

	repo := newTestRepo(t)
	writeFile(t, repo.dir, "file.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "commit", "-m", "One")

	writeFile(t, repo.dir, "file.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "commit", "-m", "Two")

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
