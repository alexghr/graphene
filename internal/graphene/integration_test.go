package graphene

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	graphenestackedprs "github.com/alexghr/graphene/skills/graphene-stacked-prs"
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
	t.Parallel()
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
	t.Parallel()
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

func TestUnknownCommandReturnsGitFailure(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	code, stdout, stderr := repo.runGraphene(t, "graphene-not-a-git-command")
	wantCode, wantStdout, wantStderr := runGitResult(t, repo.dir, "graphene-not-a-git-command")
	if code != wantCode || stdout != wantStdout || stderr != wantStderr {
		t.Fatalf("unknown git command result = (%d, %q, %q), want git result (%d, %q, %q)", code, stdout, stderr, wantCode, wantStdout, wantStderr)
	}
}

func TestAliasTakesPrecedenceOverGitFallback(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "config", "graphene.alias.status", "graph")

	code, stdout, stderr := repo.runGraphene(t, "status")
	if code != 0 {
		t.Fatalf("graphene status alias exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stdout != "no graphene stacks\n" || stderr != "" {
		t.Fatalf("unexpected alias output\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

func TestKnownCommandParseErrorDoesNotFallThroughToGit(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	code, stdout, stderr := repo.runGraphene(t, "new", "--amend")
	if code == 0 {
		t.Fatal("graphene new --amend unexpectedly succeeded")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "graphene new cannot use --amend; use graphene amend") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestSkillWritesStdoutAndOutFile(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	code, stdout, stderr := repo.runGraphene(t, "skill")
	if code != 0 {
		t.Fatalf("graphene skill exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stdout != graphenestackedprs.Content {
		t.Fatalf("stdout = %q, want bundled skill", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}

	code, stdout, stderr = repo.runGraphene(t, "skill", "--out", "-")
	if code != 0 {
		t.Fatalf("graphene skill --out - exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stdout != graphenestackedprs.Content {
		t.Fatalf("--out - stdout = %q, want bundled skill", stdout)
	}
	if stderr != "" {
		t.Fatalf("--out - stderr = %q", stderr)
	}

	out := filepath.Join(repo.dir, "nested", "SKILL.md")
	code, stdout, stderr = repo.runGraphene(t, "skill", "--out", out)
	if code != 0 {
		t.Fatalf("graphene skill --out exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stdout != "Wrote Graphene skill to "+out+"\n" || stderr != "" {
		t.Fatalf("unexpected --out output\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != graphenestackedprs.Content {
		t.Fatalf("out file = %q, want bundled skill", string(data))
	}

	aliasOut := filepath.Join(repo.dir, "alias", "SKILL.md")
	expectGrapheneOK(t, repo, "agent-skill", "--out", aliasOut)
	data, err = os.ReadFile(aliasOut)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != graphenestackedprs.Content {
		t.Fatalf("alias out file = %q, want bundled skill", string(data))
	}
}

func TestSkillShortcutsWriteCommonAgentPaths(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	home := filepath.Dir(repo.configDir)

	code, stdout, stderr := repo.runGraphene(t, "skill", "--codex")
	if code != 0 {
		t.Fatalf("graphene skill --codex exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	codexSkill := filepath.Join(home, ".codex", "skills", "graphene-stacked-prs", "SKILL.md")
	if stdout != "Wrote Graphene skill to "+codexSkill+"\n" || stderr != "" {
		t.Fatalf("unexpected output\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	data, err := os.ReadFile(codexSkill)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != graphenestackedprs.Content {
		t.Fatalf("codex skill = %q, want bundled skill", string(data))
	}

	code, stdout, stderr = repo.runGraphene(t, "skill", "--claude")
	if code != 0 {
		t.Fatalf("graphene skill --claude exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	claudeSkill := filepath.Join(home, ".claude", "skills", "graphene-stacked-prs", "SKILL.md")
	if stdout != "Wrote Graphene skill to "+claudeSkill+"\n" || stderr != "" {
		t.Fatalf("unexpected output\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	data, err = os.ReadFile(claudeSkill)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != graphenestackedprs.Content {
		t.Fatalf("claude skill = %q, want bundled skill", string(data))
	}
}

func TestCommitExactBranch(t *testing.T) {
	t.Parallel()
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

func TestCommitPositionalBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "positional.txt", "positional\n")
	runGit(t, repo.dir, "add", ".")
	// Regression for https://github.com/alexghr/graphene/issues/4.
	expectGrapheneOK(t, repo, "new", "feature/positional", "--message", "Positional subject")

	if got := currentBranch(t, repo.dir); got != "feature/positional" {
		t.Fatalf("branch = %q", got)
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"feature/positional"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestCommitNoVerifyBypassesHook(t *testing.T) {
	t.Parallel()
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

func TestCommitNoEditIsPassedToGitCommit(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "--no-edit", "-m", "One")

	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q", got)
	}
	if got := runGit(t, repo.dir, "log", "-1", "--format=%s"); got != "One" {
		t.Fatalf("subject = %q, want One", got)
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

func TestCommitRecordsExplicitParentAlias(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "checkout", "-b", "alias/one")

	writeFile(t, repo.dir, "two.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	// Regression for https://github.com/alexghr/graphene/issues/11.
	expectGrapheneOK(t, repo, "new", "--parent", "stack/one", "-m", "Two")

	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q", got)
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one", "stack/two"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestCommitReusesCurrentBranchWithExplicitBase(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	baseHead := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "checkout", "-b", "merge-train/spartan")
	runGit(t, repo.dir, "checkout", "-b", "foo")

	writeFile(t, repo.dir, "foo.txt", "foo\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "--reuse-current", "--base", "merge-train/spartan", "-m", "Foo")

	if got := currentBranch(t, repo.dir); got != "foo" {
		t.Fatalf("branch = %q", got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "merge-train/spartan"); got != baseHead {
		t.Fatalf("base ref = %q, want %q", got, baseHead)
	}
	if got := runGit(t, repo.dir, "rev-list", "--count", "merge-train/spartan..foo"); got != "1" {
		t.Fatalf("commit count = %q, want 1", got)
	}

	state := readState(t, repo.dir)
	want := []Stack{{Base: "merge-train/spartan", Branches: []string{"foo"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestCommitReuseCurrentInfersUniqueBaseAtHead(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "checkout", "-b", "viem-err")
	writeFile(t, repo.dir, "shared.txt", "shared\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Shared")
	baseHead := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "checkout", "-b", "ag/more-packing")

	writeFile(t, repo.dir, "pods.txt", "pods\n")
	runGit(t, repo.dir, "add", ".")
	// Regression for https://github.com/alexghr/graphene/issues/5.
	expectGrapheneOK(t, repo, "new", "--reuse-current", "-m", "chore: improve pod scheduling")

	if got := currentBranch(t, repo.dir); got != "ag/more-packing" {
		t.Fatalf("branch = %q", got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "viem-err"); got != baseHead {
		t.Fatalf("base ref = %q, want %q", got, baseHead)
	}
	if got := runGit(t, repo.dir, "rev-list", "--count", "viem-err..ag/more-packing"); got != "1" {
		t.Fatalf("commit count = %q, want 1", got)
	}

	state := readState(t, repo.dir)
	want := []Stack{{Base: "viem-err", Branches: []string{"ag/more-packing"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestTrackCurrentBranchExtendsExistingStack(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "checkout", "-b", "z")
	runGit(t, repo.dir, "checkout", "-b", "a")
	writeFile(t, repo.dir, "a.txt", "a\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "A")

	writeFile(t, repo.dir, "b.txt", "b\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "--branch", "b", "-m", "B")

	runGit(t, repo.dir, "checkout", "a")
	expectGrapheneOK(t, repo, "track", "--parent", "z")

	state := readState(t, repo.dir)
	want := []Stack{{Base: "z", Branches: []string{"a", "b"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}

	runGit(t, repo.dir, "checkout", "b")
	code, stdout, stderr := repo.runGraphene(t, "graph")
	if code != 0 {
		t.Fatalf("graphene graph exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	graph := "" +
		"z\n" +
		"  `- a\n" +
		"     `- b *\n"
	if stdout != graph {
		t.Fatalf("stdout = %q, want %q", stdout, graph)
	}
}

func TestTrackAcceptsBaseAlias(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "checkout", "-b", "z")
	runGit(t, repo.dir, "checkout", "-b", "a")
	writeFile(t, repo.dir, "a.txt", "a\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "A")

	// Regression for https://github.com/alexghr/graphene/issues/11.
	expectGrapheneOK(t, repo, "track", "--base", "z")

	state := readState(t, repo.dir)
	want := []Stack{{Base: "z", Branches: []string{"a"}}}
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

func TestTrackFastForwardsParentFromUpstream(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "checkout", "-b", "merge-train/spartan")
	runGit(t, repo.dir, "push", "-u", "origin", "merge-train/spartan")
	oldParent := runGit(t, repo.dir, "rev-parse", "merge-train/spartan")

	other := cloneConfiguredRepo(t, remote, "merge-train/spartan")
	writeFile(t, other, "base.txt", "base update\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "Base update")
	runGit(t, other, "push", "origin", "merge-train/spartan")

	runGit(t, repo.dir, "fetch", "origin")
	originParent := runGit(t, repo.dir, "rev-parse", "origin/merge-train/spartan")
	if originParent == oldParent {
		t.Fatal("remote parent did not advance")
	}
	runGit(t, repo.dir, "checkout", "-b", "ag/fix-partial-epoch-job", "origin/merge-train/spartan")
	writeFile(t, repo.dir, "feature.txt", "feature\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Feature")
	featureParent := runGit(t, repo.dir, "rev-parse", "ag/fix-partial-epoch-job^")
	if featureParent != originParent {
		t.Fatalf("feature parent = %s, want %s", featureParent, originParent)
	}

	// Regression for https://github.com/alexghr/graphene/issues/14.
	expectGrapheneOK(t, repo, "track", "--parent", "merge-train/spartan", "ag/fix-partial-epoch-job")

	localParent := runGit(t, repo.dir, "rev-parse", "merge-train/spartan")
	if localParent != originParent {
		t.Fatalf("local parent = %s, want %s", localParent, originParent)
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "merge-train/spartan", Branches: []string{"ag/fix-partial-epoch-job"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestImportCreatesBranchesToHead(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "checkout", "-b", "feature/imported")

	one := commitFile(t, repo.dir, "one.txt", "one\n", "Change")
	two := commitFile(t, repo.dir, "two.txt", "two\n", "Change")
	three := commitFile(t, repo.dir, "three.txt", "three\n", "Change")

	expectGrapheneOK(t, repo, "import", "main")

	if got := currentBranch(t, repo.dir); got != "feature/imported" {
		t.Fatalf("branch = %q, want feature/imported", got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/change"); got != one {
		t.Fatalf("stack/change = %s, want %s", got, one)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/change-2"); got != two {
		t.Fatalf("stack/change-2 = %s, want %s", got, two)
	}
	if got := runGit(t, repo.dir, "rev-parse", "feature/imported"); got != three {
		t.Fatalf("feature/imported = %s, want %s", got, three)
	}

	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/change", "stack/change-2", "feature/imported"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestImportReusesExistingBranches(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "checkout", "-b", "feature/imported")

	one := commitFile(t, repo.dir, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "branch", "existing/one")
	two := commitFile(t, repo.dir, "two.txt", "two\n", "Two")

	expectGrapheneOK(t, repo, "import", "main")

	if got := runGit(t, repo.dir, "rev-parse", "existing/one"); got != one {
		t.Fatalf("existing/one = %s, want %s", got, one)
	}
	if got := runGit(t, repo.dir, "rev-parse", "feature/imported"); got != two {
		t.Fatalf("feature/imported = %s, want %s", got, two)
	}
	if refExists(t, repo.dir, "refs/heads/stack/one") {
		t.Fatal("import created stack/one instead of reusing existing/one")
	}

	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"existing/one", "feature/imported"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestImportRejectsDirtyWorktree(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "checkout", "-b", "feature/imported")
	commitFile(t, repo.dir, "one.txt", "one\n", "One")
	writeFile(t, repo.dir, "file.txt", "dirty\n")

	code, _, stderr := repo.runGraphene(t, "import", "main")
	if code == 0 {
		t.Fatal("graphene import unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "tracked changes would prevent import") {
		t.Fatalf("stderr = %q", stderr)
	}
	if refExists(t, repo.dir, "refs/heads/stack/one") {
		t.Fatal("import created a branch despite dirty worktree")
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

func TestCreateAliasStagesAll(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "config", "graphene.alias.create", "new")

	writeFile(t, repo.dir, "one.txt", "one\n")
	expectGrapheneOK(t, repo, "create", "-am", "One")

	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
	}
	if got := runGit(t, repo.dir, "show", "HEAD:one.txt"); got != "one" {
		t.Fatalf("one.txt = %q, want one", got)
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestModifyAliasStagesAllAndRestacksDescendants(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "config", "graphene.alias.modify", "amend")
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	runGit(t, repo.dir, "checkout", "stack/one")
	writeFile(t, repo.dir, "one.txt", "one amended\n")
	expectGrapheneOK(t, repo, "modify", "-am", "One amended")

	if got := runGit(t, repo.dir, "show", "stack/one:one.txt"); got != "one amended" {
		t.Fatalf("one.txt = %q, want one amended", got)
	}
	parentTwo := runGit(t, repo.dir, "rev-parse", "stack/two^")
	branchOne := runGit(t, repo.dir, "rev-parse", "stack/one")
	if parentTwo != branchOne {
		t.Fatalf("stack/two parent = %s, want stack/one %s", parentTwo, branchOne)
	}
}

func TestNavigationAndLogAliases(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "config", "graphene.alias.down", "go down")
	runGit(t, repo.dir, "config", "graphene.alias.up", "go up")
	runGit(t, repo.dir, "config", "graphene.alias.bottom", "go bottom")
	runGit(t, repo.dir, "config", "graphene.alias.top", "go top")
	runGit(t, repo.dir, "config", "graphene.alias.log", "graph")
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	expectGrapheneOK(t, repo, "down")
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch after down = %q, want stack/one", got)
	}
	expectGrapheneOK(t, repo, "up")
	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch after up = %q, want stack/two", got)
	}
	expectGrapheneOK(t, repo, "bottom")
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch after bottom = %q, want stack/one", got)
	}
	expectGrapheneOK(t, repo, "top")
	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch after top = %q, want stack/two", got)
	}

	code, stdout, stderr := repo.runGraphene(t, "log")
	if code != 0 {
		t.Fatalf("graphene log exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	want := "" +
		"main\n" +
		"  `- stack/one\n" +
		"     `- stack/two *\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestTrackShortAlias(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "config", "graphene.alias.tr", "track")
	runGit(t, repo.dir, "checkout", "-b", "z")
	runGit(t, repo.dir, "checkout", "-b", "a")
	writeFile(t, repo.dir, "a.txt", "a\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "A")

	expectGrapheneOK(t, repo, "tr", "--parent", "z")

	state := readState(t, repo.dir)
	want := []Stack{{Base: "z", Branches: []string{"a"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestConfigAliasExpandsBeforeDispatch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "config", "graphene.alias.makeone", `new -m "Alias One"`)

	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "makeone")

	if got := currentBranch(t, repo.dir); got != "stack/alias-one" {
		t.Fatalf("branch = %q, want stack/alias-one", got)
	}
}

func TestGraphiteAliasFileExpandsBeforeDispatch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	writeFile(t, repo.dir, "aliases/graphite.gitconfig", `[graphene "alias"]
	create = new
	down = go down
`)
	runGit(t, repo.dir, "config", "graphene.aliasFile", "aliases/graphite.gitconfig")

	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "create", "-m", "Alias File One")
	if got := currentBranch(t, repo.dir); got != "stack/alias-file-one" {
		t.Fatalf("branch = %q, want stack/alias-file-one", got)
	}

	expectGrapheneOK(t, repo, "down")
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch after down = %q, want main", got)
	}
}

func TestRemoteAliasFileExpandsBeforeDispatch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[graphene "alias"]
	create = new
`)
	}))
	t.Cleanup(server.Close)
	runGit(t, repo.dir, "config", "graphene.aliasFile", server.URL)

	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "create", "-m", "Remote Alias")
	if got := currentBranch(t, repo.dir); got != "stack/remote-alias" {
		t.Fatalf("branch = %q, want stack/remote-alias", got)
	}
}

func TestAliasesImportStoresRemoteAliases(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[graphene "alias"]
	create = new
`)
	}))

	code, stdout, stderr := repo.runGraphene(t, "aliases", "import", "--local", server.URL)
	if code != 0 {
		t.Fatalf("graphene aliases import exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stdout != "imported 1 aliases\n" || stderr != "" {
		t.Fatalf("unexpected aliases import output\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	server.Close()

	if got := runGit(t, repo.dir, "config", "--get", "graphene.alias.create"); got != "new" {
		t.Fatalf("imported alias = %q, want new", got)
	}
	writeFile(t, repo.dir, "one.txt", "one\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "create", "-m", "Imported Alias")
	if got := currentBranch(t, repo.dir); got != "stack/imported-alias" {
		t.Fatalf("branch = %q, want stack/imported-alias", got)
	}
}

func TestAliasesImportRefusesOverwriteWithoutForce(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	writeFile(t, repo.dir, "aliases/graphite.gitconfig", `[graphene "alias"]
	create = new
	up = go up
`)
	runGit(t, repo.dir, "config", "graphene.alias.create", "graph")

	code, _, stderr := repo.runGraphene(t, "aliases", "import", "--local", "aliases/graphite.gitconfig")
	if code == 0 {
		t.Fatal("aliases import unexpectedly overwrote existing alias")
	}
	if !strings.Contains(stderr, "refusing to overwrite existing aliases: create") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := runGit(t, repo.dir, "config", "--get", "graphene.alias.create"); got != "graph" {
		t.Fatalf("alias after failed import = %q, want graph", got)
	}
	if code, _, _ := runGitResult(t, repo.dir, "config", "--get", "graphene.alias.up"); code == 0 {
		t.Fatal("non-conflicting alias was imported despite conflict")
	}

	expectGrapheneOK(t, repo, "aliases", "import", "--local", "--force", "aliases/graphite.gitconfig")
	if got := runGit(t, repo.dir, "config", "--get", "graphene.alias.create"); got != "new" {
		t.Fatalf("forced alias import = %q, want new", got)
	}
	if got := runGit(t, repo.dir, "config", "--get", "graphene.alias.up"); got != "go up" {
		t.Fatalf("forced alias import up = %q, want go up", got)
	}
}

func TestShellAliasRunsWithArguments(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "config", "graphene.alias.echo-one", `!printf '%s\n'`)

	code, stdout, stderr := repo.runGraphene(t, "echo-one", "a'b")
	if code != 0 {
		t.Fatalf("shell alias exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stdout != "a'b\n" || stderr != "" {
		t.Fatalf("unexpected shell alias output\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

func TestAliasLoopFails(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "config", "graphene.alias.one", "two")
	runGit(t, repo.dir, "config", "graphene.alias.two", "one")

	code, _, stderr := repo.runGraphene(t, "one")
	if code == 0 {
		t.Fatal("alias loop unexpectedly succeeded")
	}
	if !strings.Contains(stderr, `alias loop detected at "one"`) {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestConfigSetGetUnsetAndBranchPrefix(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	expectGrapheneOK(t, repo, "config", "set", "alias.up", "go", "up")
	code, stdout, stderr := repo.runGraphene(t, "config", "get", "alias.up")
	if code != 0 {
		t.Fatalf("graphene config get exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stdout != "go up\n" || stderr != "" {
		t.Fatalf("unexpected config get output\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	expectGrapheneOK(t, repo, "config", "set", "branchPrefix", "feature")
	writeFile(t, repo.dir, "one.txt", "one\n")
	expectGrapheneOK(t, repo, "new", "-am", "One")
	if got := currentBranch(t, repo.dir); got != "feature/one" {
		t.Fatalf("branch = %q, want feature/one", got)
	}

	expectGrapheneOK(t, repo, "config", "unset", "alias.up")
	code, _, stderr = repo.runGraphene(t, "config", "get", "alias.up")
	if code == 0 {
		t.Fatal("graphene config get unexpectedly found alias.up after unset")
	}
	if !strings.Contains(stderr, `config key "alias.up" is not set`) {
		t.Fatalf("stderr = %q", stderr)
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

func TestCommitDoesNotLeaveTemporaryBranch(t *testing.T) {
	t.Parallel()
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

func TestSplitCurrentBranchWithNewRestacksDescendants(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "one.txt", "one\n")
	writeFile(t, repo.dir, "two.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "Combined change")
	originalCombined := runGit(t, repo.dir, "rev-parse", "stack/combined-change")
	createStackBranch(t, repo, "after.txt", "after\n", "After")
	runGit(t, repo.dir, "checkout", "stack/combined-change")
	createStackBranch(t, repo, "fork.txt", "fork\n", "Fork")

	runGit(t, repo.dir, "checkout", "stack/combined-change")
	expectGrapheneOK(t, repo, "split")
	if got := runGit(t, repo.dir, "rev-parse", "stack/combined-change"); got == originalCombined {
		t.Fatalf("stack/combined-change was not reset during split")
	}

	runGit(t, repo.dir, "add", "one.txt")
	expectGrapheneOK(t, repo, "new", "--reuse-current", "-m", "Add one")
	state := readState(t, repo.dir)
	if state.Pending == nil || state.Pending.Operation != "split" {
		t.Fatalf("pending split = %#v, want active split", state.Pending)
	}

	runGit(t, repo.dir, "add", "two.txt")
	expectGrapheneOK(t, repo, "new", "-m", "Add two")

	if got := currentBranch(t, repo.dir); got != "stack/add-two" {
		t.Fatalf("branch = %q, want stack/add-two", got)
	}
	main := runGit(t, repo.dir, "rev-parse", "main")
	parentCombined := runGit(t, repo.dir, "rev-parse", "stack/combined-change^")
	if parentCombined != main {
		t.Fatalf("stack/combined-change parent = %s, want main %s", parentCombined, main)
	}
	combined := runGit(t, repo.dir, "rev-parse", "stack/combined-change")
	parentAddTwo := runGit(t, repo.dir, "rev-parse", "stack/add-two^")
	if parentAddTwo != combined {
		t.Fatalf("stack/add-two parent = %s, want stack/combined-change %s", parentAddTwo, combined)
	}
	addTwo := runGit(t, repo.dir, "rev-parse", "stack/add-two")
	parentAfter := runGit(t, repo.dir, "rev-parse", "stack/after^")
	if parentAfter != addTwo {
		t.Fatalf("stack/after parent = %s, want stack/add-two %s", parentAfter, addTwo)
	}
	parentFork := runGit(t, repo.dir, "rev-parse", "stack/fork^")
	if parentFork != addTwo {
		t.Fatalf("stack/fork parent = %s, want stack/add-two %s", parentFork, addTwo)
	}
	if got := runGit(t, repo.dir, "show", "stack/combined-change:one.txt"); got != "one" {
		t.Fatalf("stack/combined-change:one.txt = %q, want one", got)
	}
	if refFileExists(t, repo.dir, "stack/combined-change:two.txt") {
		t.Fatal("stack/combined-change unexpectedly contains two.txt")
	}

	state = readState(t, repo.dir)
	want := []Stack{
		{Base: "main", Branches: []string{"stack/combined-change", "stack/add-two", "stack/after"}},
		{Base: "stack/add-two", Branches: []string{"stack/fork"}},
	}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
	if state.Pending != nil {
		t.Fatalf("pending state was not cleared: %#v", state.Pending)
	}
}

func TestSplitFirstCommitRequiresReuseCurrent(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "one.txt", "one\n")
	writeFile(t, repo.dir, "two.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "Combined change")

	expectGrapheneOK(t, repo, "split")
	runGit(t, repo.dir, "add", "one.txt")
	code, _, stderr := repo.runGraphene(t, "new", "-m", "Add one")
	if code == 0 {
		t.Fatal("graphene new unexpectedly created first split commit without --reuse-current")
	}
	if !strings.Contains(stderr, "first split commit must use graphene new --reuse-current") {
		t.Fatalf("stderr = %q", stderr)
	}
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

func TestSquashDefaultIntoParentRestacksDescendants(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")

	runGit(t, repo.dir, "checkout", "stack/two")
	createStackBranch(t, repo, "fork.txt", "fork\n", "Fork")
	runGit(t, repo.dir, "checkout", "stack/two")

	expectGrapheneOK(t, repo, "squash", "--no-edit")

	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
	}
	if refExists(t, repo.dir, "refs/heads/stack/two") {
		t.Fatal("stack/two still exists after squash")
	}
	branchOne := runGit(t, repo.dir, "rev-parse", "stack/one")
	for _, branch := range []string{"stack/three", "stack/fork"} {
		parent := runGit(t, repo.dir, "rev-parse", branch+"^")
		if parent != branchOne {
			t.Fatalf("%s parent = %s, want stack/one %s", branch, parent, branchOne)
		}
	}
	if got := runGit(t, repo.dir, "show", "stack/one:one.txt"); got != "one" {
		t.Fatalf("stack/one:one.txt = %q, want one", got)
	}
	if got := runGit(t, repo.dir, "show", "stack/one:two.txt"); got != "two" {
		t.Fatalf("stack/one:two.txt = %q, want two", got)
	}
	if got := runGit(t, repo.dir, "log", "-1", "--format=%B", "stack/one"); !strings.Contains(got, "Squashed commits:\n\n- Two") {
		t.Fatalf("stack/one message = %q, want generated squash message", got)
	}
	if refFileExists(t, repo.dir, "stack/one:three.txt") {
		t.Fatal("stack/one unexpectedly contains three.txt")
	}

	state := readState(t, repo.dir)
	want := []Stack{
		{Base: "main", Branches: []string{"stack/one", "stack/three"}},
		{Base: "stack/one", Branches: []string{"stack/fork"}},
	}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
	if state.Pending != nil {
		t.Fatalf("pending state was not cleared: %#v", state.Pending)
	}
}

func TestSquashCountThree(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")
	createStackBranch(t, repo, "four.txt", "four\n", "Four")

	runGit(t, repo.dir, "checkout", "stack/three")
	expectGrapheneOK(t, repo, "squash", "-c", "3", "-m", "Combined first three")

	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
	}
	for _, branch := range []string{"stack/two", "stack/three"} {
		if refExists(t, repo.dir, "refs/heads/"+branch) {
			t.Fatalf("%s still exists after squash", branch)
		}
	}
	branchOne := runGit(t, repo.dir, "rev-parse", "stack/one")
	parentFour := runGit(t, repo.dir, "rev-parse", "stack/four^")
	if parentFour != branchOne {
		t.Fatalf("stack/four parent = %s, want stack/one %s", parentFour, branchOne)
	}
	if got := runGit(t, repo.dir, "show", "stack/one:three.txt"); got != "three" {
		t.Fatalf("stack/one:three.txt = %q, want three", got)
	}
	if refFileExists(t, repo.dir, "stack/one:four.txt") {
		t.Fatal("stack/one unexpectedly contains four.txt")
	}
	if got := runGit(t, repo.dir, "log", "-1", "--format=%s", "stack/one"); got != "Combined first three" {
		t.Fatalf("stack/one subject = %q, want Combined first three", got)
	}

	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one", "stack/four"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestSquashRejectsCountPastBottom(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")

	code, _, stderr := repo.runGraphene(t, "squash", "--count", "2")
	if code == 0 {
		t.Fatal("graphene squash unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "only 1 tracked branches are available") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
	}
}

func TestAmendRestacksSuffix(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestAmendNoEditPreservesCommitMessage(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")

	writeFile(t, repo.dir, "one.txt", "one amended\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "amend", "--no-edit")

	if got := runGit(t, repo.dir, "log", "-1", "--format=%s"); got != "One" {
		t.Fatalf("subject = %q, want One", got)
	}
	if got := runGit(t, repo.dir, "show", "HEAD:one.txt"); got != "one amended" {
		t.Fatalf("one.txt = %q, want one amended", got)
	}
}

func TestAmendMessageOnlyWithCleanWorktree(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")

	expectGrapheneOK(t, repo, "amend", "-m", "One renamed")

	if got := runGit(t, repo.dir, "log", "-1", "--format=%s"); got != "One renamed" {
		t.Fatalf("subject = %q, want One renamed", got)
	}
	if got := runGit(t, repo.dir, "show", "HEAD:one.txt"); got != "one" {
		t.Fatalf("one.txt = %q, want one", got)
	}
}

func TestRestackMovesWholeStackFromAnyBranch(t *testing.T) {
	t.Parallel()

	for _, current := range []string{"stack/one", "stack/two", "stack/three"} {
		current := current
		t.Run(current, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			createStackBranch(t, repo, "one.txt", "one\n", "One")
			createStackBranch(t, repo, "two.txt", "two\n", "Two")
			createStackBranch(t, repo, "three.txt", "three\n", "Three")

			runGit(t, repo.dir, "checkout", "-b", "target", "main")
			writeFile(t, repo.dir, "target.txt", "target\n")
			runGit(t, repo.dir, "add", ".")
			runGit(t, repo.dir, "commit", "-m", "Target")

			runGit(t, repo.dir, "checkout", current)
			// Regression for https://github.com/alexghr/graphene/issues/9.
			expectGrapheneOK(t, repo, "restack", "target")

			assertBranchParent(t, repo.dir, "stack/one", "target")
			assertBranchParent(t, repo.dir, "stack/two", "stack/one")
			assertBranchParent(t, repo.dir, "stack/three", "stack/two")
			if got := currentBranch(t, repo.dir); got != current {
				t.Fatalf("branch = %q, want %s", got, current)
			}

			state := readState(t, repo.dir)
			want := []Stack{{Base: "target", Branches: []string{"stack/one", "stack/two", "stack/three"}}}
			if !reflect.DeepEqual(state.Stacks, want) {
				t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
			}
		})
	}
}

func TestRestackWholeStackRestacksForkedDescendants(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")

	runGit(t, repo.dir, "checkout", "stack/one")
	createStackBranch(t, repo, "fork.txt", "fork\n", "Fork")

	runGit(t, repo.dir, "checkout", "-b", "target", "main")
	writeFile(t, repo.dir, "target.txt", "target\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Target")

	runGit(t, repo.dir, "checkout", "stack/two")
	expectGrapheneOK(t, repo, "restack", "target")

	assertBranchParent(t, repo.dir, "stack/one", "target")
	assertBranchParent(t, repo.dir, "stack/two", "stack/one")
	assertBranchParent(t, repo.dir, "stack/three", "stack/two")
	assertBranchParent(t, repo.dir, "stack/fork", "stack/one")
	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q, want stack/two", got)
	}

	state := readState(t, repo.dir)
	want := []Stack{
		{Base: "stack/one", Branches: []string{"stack/fork"}},
		{Base: "target", Branches: []string{"stack/one", "stack/two", "stack/three"}},
	}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
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

	code, _, stderr := repo.runGraphene(t, "restack", "target")
	if code == 0 {
		t.Fatal("graphene restack unexpectedly succeeded")
	}
	for _, want := range []string{
		`current branch "stack/one" diverged from upstream "stack/one@{upstream}"`,
		"rerun with --force",
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

func TestRestackLocalSkipsCurrentUpstreamFetch(t *testing.T) {
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

	expectGrapheneOK(t, repo, "restack", "--force", "target")

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

func TestSyncRebasesCurrentBranchAndDeletesAppliedIntermediateBranches(t *testing.T) {
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
	writeFile(t, other, "one.txt", "one\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "One (#1)")
	runGit(t, other, "push", "origin", "main")

	runGit(t, repo.dir, "checkout", "stack/two")
	code, stdout, stderr := repo.runGraphene(t, "sync")
	if code != 0 {
		t.Fatalf("graphene sync exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	wantStdout := "Retarget existing PRs after sync:\n  stack/two: stack/one -> main\n"
	if !strings.Contains(stdout, wantStdout) {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, wantStdout)
	}

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

func TestSyncFromBaseRebasesDescendantStack(t *testing.T) {
	t.Parallel()
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

	runGit(t, repo.dir, "checkout", "main")
	expectGrapheneOK(t, repo, "sync")

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
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one", "stack/two"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestSyncAllFromBaseRebasesSiblingAndNestedStacks(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "one-top.txt", "one top\n", "One top")
	runGit(t, repo.dir, "checkout", "main")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	runGit(t, repo.dir, "checkout", "stack/one")
	createStackBranch(t, repo, "fork.txt", "fork\n", "Fork")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	writeFile(t, other, "base.txt", "base update\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "Base update")
	runGit(t, other, "push", "origin", "main")

	runGit(t, repo.dir, "checkout", "main")
	expectGrapheneOK(t, repo, "sync", "--all")

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
	for _, branch := range []string{"stack/one-top", "stack/fork"} {
		parent := runGit(t, repo.dir, "rev-parse", branch+"^")
		if parent != branchOne {
			t.Fatalf("%s parent = %s, want stack/one %s", branch, parent, branchOne)
		}
	}
	parentTwo := runGit(t, repo.dir, "rev-parse", "stack/two^")
	if parentTwo != main {
		t.Fatalf("stack/two parent = %s, want main %s", parentTwo, main)
	}
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}

	state := readState(t, repo.dir)
	want := []Stack{
		{Base: "main", Branches: []string{"stack/one", "stack/one-top"}},
		{Base: "main", Branches: []string{"stack/two"}},
		{Base: "stack/one", Branches: []string{"stack/fork"}},
	}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestSyncAllFromUntrackedNonBaseNamesValidBases(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "checkout", "main")
	runGit(t, repo.dir, "checkout", "-b", "unrelated")

	code, _, stderr := repo.runGraphene(t, "sync", "--all")
	if code == 0 {
		t.Fatal("graphene sync --all unexpectedly succeeded")
	}
	want := `graphene sync --all must be run from a stack base; "unrelated" is not a stack base (available bases: main)`
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want message containing %q", stderr, want)
	}
}

func TestSyncDryRunPrintsPlanWithoutChangingRefsOrState(t *testing.T) {
	t.Parallel()
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
	runGit(t, other, "push", "origin", "main")

	runGit(t, repo.dir, "checkout", "main")
	mainBefore := runGit(t, repo.dir, "rev-parse", "main")
	originMainBefore := runGit(t, repo.dir, "rev-parse", "origin/main")
	oneBefore := runGit(t, repo.dir, "rev-parse", "stack/one")
	twoBefore := runGit(t, repo.dir, "rev-parse", "stack/two")
	stateBefore := readState(t, repo.dir)

	code, stdout, stderr := repo.runGraphene(t, "sync", "-a", "-n")
	if code != 0 {
		t.Fatalf("graphene sync -a -n exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	for _, want := range []string{
		"Dry run: sync main\n",
		"  fetch: origin/main\n",
		"  delete applied branches:\n    stack/one\n",
		"  retarget existing PRs:\n    stack/two: stack/one -> main\n",
		"  rebase:\n    git rebase --update-refs --onto ",
		" stack/two\n",
		"  return: main\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want it to contain %q", stdout, want)
		}
	}

	if got := runGit(t, repo.dir, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("main changed from %s to %s", mainBefore, got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "origin/main"); got != originMainBefore {
		t.Fatalf("origin/main changed from %s to %s", originMainBefore, got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/one"); got != oneBefore {
		t.Fatalf("stack/one changed from %s to %s", oneBefore, got)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/two"); got != twoBefore {
		t.Fatalf("stack/two changed from %s to %s", twoBefore, got)
	}
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
	state := readState(t, repo.dir)
	if !reflect.DeepEqual(state, stateBefore) {
		t.Fatalf("state = %#v, want %#v", state, stateBefore)
	}
}

func TestSyncFromBaseDeletesAppliedIntermediateBranches(t *testing.T) {
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
	writeFile(t, other, "one.txt", "one\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "One (#1)")
	runGit(t, other, "push", "origin", "main")

	runGit(t, repo.dir, "checkout", "main")
	code, stdout, stderr := repo.runGraphene(t, "sync")
	if code != 0 {
		t.Fatalf("graphene sync exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	wantStdout := "Retarget existing PRs after sync:\n  stack/two: stack/one -> main\n"
	if !strings.Contains(stdout, wantStdout) {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, wantStdout)
	}

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
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/two", "stack/three"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
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

func TestSyncKeepsUnappliedBranches(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	if got := runGit(t, repo.dir, "rev-parse", "origin/main"); got != oldMain {
		t.Fatalf("origin/main changed from %s to %s", oldMain, got)
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
	originMain := runGit(t, repo.dir, "rev-parse", "origin/main")
	if main != originMain {
		t.Fatalf("main = %s, want origin/main %s", main, originMain)
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
	t.Parallel()
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
	t.Parallel()
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

func TestContinueWithoutRebaseIsFriendly(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	repo := createConflictDuringAmend(t)

	expectGrapheneOK(t, repo, "abort")
	state := readState(t, repo.dir)
	if state.Pending != nil {
		t.Fatalf("pending state was not cleared: %#v", state.Pending)
	}
}

func TestAbortWithoutRebaseIsFriendly(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestForgetRemovesStackThroughCurrentBranchWithoutDeletingBranches(t *testing.T) {
	t.Parallel()
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

func TestForgetNamedBranchWithoutCheckoutDoesNotDeleteBranches(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")

	runGit(t, repo.dir, "checkout", "main")
	// Regression for https://github.com/alexghr/graphene/issues/6.
	expectGrapheneOK(t, repo, "forget", "stack/two")

	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
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
	t.Parallel()
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
func TestDeleteCurrentTrackedTipSwitchesToParentAndDeletesBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	expectGrapheneOK(t, repo, "delete")

	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
	}
	if refExists(t, repo.dir, "refs/heads/stack/two") {
		t.Fatal("stack/two still exists")
	}
	if !refExists(t, repo.dir, "refs/heads/stack/one") {
		t.Fatal("stack/one was deleted")
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestDeleteNamedTrackedTipDeletesBranchWithoutCheckout(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	runGit(t, repo.dir, "checkout", "main")
	expectGrapheneOK(t, repo, "delete", "stack/two")

	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
	if refExists(t, repo.dir, "refs/heads/stack/two") {
		t.Fatal("stack/two still exists")
	}
	state := readState(t, repo.dir)
	want := []Stack{{Base: "main", Branches: []string{"stack/one"}}}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

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

func TestDeleteStackDeletesBranchAndTrackedDescendants(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	runGit(t, repo.dir, "checkout", "stack/one")
	createStackBranch(t, repo, "fork.txt", "fork\n", "Fork")

	expectGrapheneOK(t, repo, "delete", "--stack", "stack/one")

	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
	for _, branch := range []string{"stack/one", "stack/two", "stack/fork"} {
		if refExists(t, repo.dir, "refs/heads/"+branch) {
			t.Fatalf("%s still exists", branch)
		}
	}
	state := readState(t, repo.dir)
	if len(state.Stacks) != 0 {
		t.Fatalf("stacks = %#v, want empty", state.Stacks)
	}
}

func TestPushPushesStackAndSetsUpstreams(t *testing.T) {
	t.Parallel()
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
}

func TestSendRejectsBranchPendingInAnotherWorktree(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	state := readState(t, repo.dir)
	state.Pending = &Pending{
		Operation: "amend",
		Worktree:  filepath.Join(t.TempDir(), "other-worktree-git-dir"),
		Branch:    "stack/one",
		Queue: []RebaseOp{
			{Onto: "stack/one", Upstream: "old-one", Top: "stack/two"},
		},
	}
	if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := repo.runGraphene(t, "send", "--dry-run", "origin")
	if code == 0 {
		t.Fatal("graphene send unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "pending rebase in another worktree is rewriting branch") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestSendRejectsGitPushFlags(t *testing.T) {
	t.Parallel()
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

func TestSendfUsesSameBranchSetAsSendAndStackFlagPushesDescendants(t *testing.T) {
	t.Parallel()
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

func TestSendStackDoesNotPushSiblingBranches(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	runGit(t, repo.dir, "checkout", "stack/one")
	createStackBranch(t, repo, "fork.txt", "fork\n", "Fork")

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)

	runGit(t, repo.dir, "checkout", "stack/two")
	expectGrapheneOK(t, repo, "send", "--stack", "origin")
	for _, branch := range []string{"stack/one", "stack/two"} {
		runGit(t, remote, "show-ref", "--verify", "refs/heads/"+branch)
	}
	if refExists(t, remote, "refs/heads/stack/fork") {
		t.Fatal("stack/fork was pushed from sibling stack/two")
	}

	runGit(t, repo.dir, "checkout", "stack/one")
	expectGrapheneOK(t, repo, "send", "--stack", "origin")
	runGit(t, remote, "show-ref", "--verify", "refs/heads/stack/fork")
}

func TestSendPrintsCurrentBranchAndDependencyLinks(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestGraphStackDisplaysCurrentStackOnly(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	runGit(t, repo.dir, "checkout", "stack/one")
	createStackBranch(t, repo, "fork.txt", "fork\n", "Fork")

	code, stdout, stderr := repo.runGraphene(t, "graph", "--stack")
	if code != 0 {
		t.Fatalf("graphene graph --stack exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	want := "" +
		"main\n" +
		"  `- stack/one\n" +
		"     `- stack/fork *\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}

	runGit(t, repo.dir, "checkout", "stack/two")
	code, stdout, stderr = repo.runGraphene(t, "graph", "--stack")
	if code != 0 {
		t.Fatalf("graphene graph --stack exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	want = "" +
		"main\n" +
		"  `- stack/one\n" +
		"     `- stack/two *\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestGoWalksForkedStack(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	runGit(t, repo.dir, "checkout", "stack/one")
	createStackBranch(t, repo, "fork.txt", "fork\n", "Fork")

	expectGrapheneOK(t, repo, "go", "down")
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
	}

	code, _, stderr := repo.runGraphene(t, "go", "up")
	if code == 0 {
		t.Fatal("graphene go up unexpectedly succeeded")
	}
	wantNext := "" +
		"multiple branches match --up; rerun with --up <number>:\n" +
		"possible branches:\n" +
		"  1. stack/two\n" +
		"  2. stack/fork\n"
	if stderr != wantNext {
		t.Fatalf("stderr = %q, want %q", stderr, wantNext)
	}
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
	}

	code, _, stderr = repo.runGraphene(t, "go", "top")
	if code == 0 {
		t.Fatal("graphene go top unexpectedly succeeded")
	}
	wantTop := "" +
		"multiple branches match --top; rerun with --top <number>:\n" +
		"possible branches:\n" +
		"  1. stack/two\n" +
		"  2. stack/fork\n"
	if stderr != wantTop {
		t.Fatalf("stderr = %q, want %q", stderr, wantTop)
	}

	expectGrapheneOK(t, repo, "go", "up", "2")
	if got := currentBranch(t, repo.dir); got != "stack/fork" {
		t.Fatalf("branch = %q, want stack/fork", got)
	}

	expectGrapheneOK(t, repo, "go", "bottom")
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
	}

	expectGrapheneOK(t, repo, "go", "top", "1")
	if got := currentBranch(t, repo.dir); got != "stack/two" {
		t.Fatalf("branch = %q, want stack/two", got)
	}

	expectGrapheneOK(t, repo, "go", "-b")
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("branch = %q, want stack/one", got)
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
