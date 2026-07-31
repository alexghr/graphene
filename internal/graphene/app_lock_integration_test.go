package graphene

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const nestedGrapheneAliasHelperEnv = "GRAPHENE_NESTED_ALIAS_HELPER"

func TestShellAliasCanInvokeGraphene(t *testing.T) {
	repo := newTestRepo(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	helperArgs := []string{
		shellQuote(executable),
		"-test.run=" + shellQuote("^TestNestedGrapheneAliasHelper$"),
		"--",
		"new",
		"-m",
		shellQuote("Nested alias"),
	}
	script := "!git add -A && " + nestedGrapheneAliasHelperEnv + "=1 " + strings.Join(helperArgs, " ")
	runGit(t, repo.dir, "config", "graphene.alias.commit-all", script)
	writeFile(t, repo.dir, "nested.txt", "nested\n")

	code, stdout, stderr := repo.runGraphene(t, "commit-all")
	if code != 0 {
		t.Fatalf("shell alias exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := currentBranch(t, repo.dir); got != "stack/nested-alias" {
		t.Fatalf("branch = %q, want stack/nested-alias", got)
	}
	if got := runGit(t, repo.dir, "status", "--porcelain"); got != "" {
		t.Fatalf("status after nested Graphene command = %q, want clean", got)
	}
}

func TestStatefulCommandRejectsHeldStateLock(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo.dir, "blocked.txt", "blocked\n")
	lock, err := (Git{Dir: repo.dir}).AcquireStateLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	code, _, stderr := repo.runGraphene(t, "new", "-m", "Blocked")
	if code == 0 || !strings.Contains(stderr, ErrStateLocked.Error()) {
		t.Fatalf("graphene new with held lock = (%d, %q), want state-lock error", code, stderr)
	}
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch after rejected command = %q, want main", got)
	}
}

func TestNestedGrapheneAliasHelper(t *testing.T) {
	if os.Getenv(nestedGrapheneAliasHelperEnv) != "1" {
		return
	}

	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		fmt.Fprintln(os.Stderr, "nested Graphene test helper is missing --")
		os.Exit(2)
	}

	app := NewApp(".", os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	args := append([]string{"graphene"}, os.Args[separator+1:]...)
	os.Exit(app.Run(args))
}

func TestGitPassthroughWorksOutsideRepository(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(parent, "missing-global-config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	code, stdout, stderr := runAppInDir(parent, "init", "-q", "initialized")
	if code != 0 {
		t.Fatalf("graphene init exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(parent, "initialized", ".git")); err != nil {
		t.Fatalf("initialized repository: %v", err)
	}

	source := newTestRepo(t)
	code, stdout, stderr = runAppInDir(parent, "clone", "-q", source.dir, "cloned")
	if code != 0 {
		t.Fatalf("graphene clone exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := currentBranch(t, filepath.Join(parent, "cloned")); got != "main" {
		t.Fatalf("cloned branch = %q, want main", got)
	}
}

func TestGitPassthroughDoesNotRequireGrapheneStateDirectory(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo.dir, "untracked.txt", "untracked\n")

	graphenePath := filepath.Join(repo.dir, ".git", grapheneStateDirName)
	if err := os.WriteFile(graphenePath, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := repo.runGraphene(t, "status", "--porcelain")
	wantCode, wantStdout, wantStderr := runGitResult(t, repo.dir, "status", "--porcelain")
	if code != wantCode || stdout != wantStdout || stderr != wantStderr {
		t.Fatalf("graphene status --porcelain = (%d, %q, %q), want git result (%d, %q, %q)", code, stdout, stderr, wantCode, wantStdout, wantStderr)
	}
}

func runAppInDir(dir string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	app := NewApp(dir, nil, &stdout, &stderr, os.Getenv)
	code := app.Run(append([]string{"graphene"}, args...))
	return code, stdout.String(), stderr.String()
}
