package graphene

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeRestoreIndexPreservesRawFilteredBytesAndModes(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	app := &App{git: git}

	runGit(t, repo.dir, "config", "filter.redact.clean", "sed s/SECRET/REDACTED/g")
	runGit(t, repo.dir, "config", "filter.redact.smudge", "cat")
	writeFile(t, repo.dir, ".gitattributes", "*.filtered filter=redact\n")
	writeFile(t, repo.dir, "tracked.filtered", "base\n")
	runGit(t, repo.dir, "add", ".gitattributes", "tracked.filtered")
	runGit(t, repo.dir, "commit", "--no-gpg-sign", "-m", "Add filtered file")

	trackedPath := filepath.Join(repo.dir, "tracked.filtered")
	untrackedPath := filepath.Join(repo.dir, "untracked.filtered")
	writeFile(t, repo.dir, "tracked.filtered", "SECRET staged bytes\n")
	runGit(t, repo.dir, "add", "tracked.filtered")
	writeFile(t, repo.dir, "tracked.filtered", "SECRET tracked bytes\n")
	writeFile(t, repo.dir, "untracked.filtered", "SECRET untracked bytes\n")
	if err := os.Chmod(trackedPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(untrackedPath, 0o600); err != nil {
		t.Fatal(err)
	}

	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("amend", worktree, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	if err := app.prepareOperationWorktree(operation); err != nil {
		t.Fatal(err)
	}
	artifacts, err := app.loadOperationArtifacts(operation)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(trackedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(untrackedPath); err != nil {
		t.Fatal(err)
	}
	if err := app.restoreOperationWorktree(operation, artifacts); err != nil {
		t.Fatal(err)
	}

	assertRawWorktreeFile(t, trackedPath, "SECRET tracked bytes\n", 0o600)
	assertRawWorktreeFile(t, untrackedPath, "SECRET untracked bytes\n", 0o600)
	if got := runGit(t, repo.dir, "show", ":tracked.filtered"); got != "REDACTED staged bytes" {
		t.Fatalf("staged filtered contents = %q, want canonical staged bytes", got)
	}
}

func TestRawWorktreeRestorePreservesDirectoryModeAndMissingTrackedPath(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	app := &App{git: git}

	writeFile(t, repo.dir, "private/present.txt", "present\n")
	writeFile(t, repo.dir, "private/missing.txt", "missing\n")
	writeFile(t, repo.dir, "gone/missing.txt", "missing parent\n")
	runGit(t, repo.dir, "add", "private", "gone")
	runGit(t, repo.dir, "commit", "--no-gpg-sign", "-m", "Add private files")
	privateDir := filepath.Join(repo.dir, "private")
	missingPath := filepath.Join(privateDir, "missing.txt")
	if err := os.Chmod(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(missingPath); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(repo.dir, "gone")); err != nil {
		t.Fatal(err)
	}

	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("amend", worktree, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	if err := app.prepareOperationWorktree(operation); err != nil {
		t.Fatal(err)
	}
	artifacts, err := app.loadOperationArtifacts(operation)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(privateDir); err != nil {
		t.Fatal(err)
	}
	if err := app.restoreOperationWorktree(operation, artifacts); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(privateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("private directory mode = %#o, want 0700", got)
	}
	if _, err := os.Lstat(missingPath); !os.IsNotExist(err) {
		t.Fatalf("missing tracked path after restore: err = %v, want not exist", err)
	}
	if _, err := os.Lstat(filepath.Join(repo.dir, "gone")); !os.IsNotExist(err) {
		t.Fatalf("missing tracked parent after restore: err = %v, want not exist", err)
	}
}

func TestRawWorktreeSnapshotRejectsIntermediateSymlink(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	app := &App{git: git}

	writeFile(t, repo.dir, "nested/file.txt", "inside\n")
	runGit(t, repo.dir, "add", "nested/file.txt")
	runGit(t, repo.dir, "commit", "--no-gpg-sign", "-m", "Add nested file")
	if err := os.RemoveAll(filepath.Join(repo.dir, "nested")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeFile(t, outside, "file.txt", "outside secret\n")
	if err := os.Symlink(outside, filepath.Join(repo.dir, "nested")); err != nil {
		t.Fatal(err)
	}

	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("amend", worktree, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	err = app.prepareOperationWorktree(operation)
	if err == nil || !strings.Contains(err.Error(), "non-directory parent \"nested\"") {
		t.Fatalf("prepareOperationWorktree error = %v, want intermediate-symlink refusal", err)
	}
	data, err := os.ReadFile(filepath.Join(outside, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "outside secret\n" {
		t.Fatalf("outside file changed to %q", data)
	}
}

func TestRawWorktreeIncrementKeepsAuthoritativeNonDirectoryAncestor(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	app := &App{git: git}
	writeFile(t, repo.dir, "a", "pre-operation untracked file\n")

	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("split", worktree, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	if err := app.prepareOperationWorktree(operation); err != nil {
		t.Fatal(err)
	}
	originalArtifact := operation.RawWorktreeArtifact

	if err := os.Remove(filepath.Join(repo.dir, "a")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo.dir, "a/b", "operation-added descendant\n")
	runGit(t, repo.dir, "add", "a/b")
	changed, err := app.snapshotOperationAddedPaths(operation)
	if err != nil {
		t.Fatalf("snapshotOperationAddedPaths: %v", err)
	}
	if changed {
		t.Fatal("incremental snapshot replaced an authoritative pre-operation ancestor")
	}
	if operation.RawWorktreeArtifact != originalArtifact {
		t.Fatalf("raw artifact changed from %q to %q", originalArtifact, operation.RawWorktreeArtifact)
	}
	artifacts, err := app.loadOperationArtifacts(operation)
	if err != nil {
		t.Fatalf("load snapshot after blocked descendant: %v", err)
	}
	if err := app.restoreOperationWorktree(operation, artifacts); err != nil {
		t.Fatal(err)
	}
	assertRawWorktreeFile(t, filepath.Join(repo.dir, "a"), "pre-operation untracked file\n", 0o644)
}

func assertRawWorktreeFile(t *testing.T, path, want string, wantMode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Error(err)
		return
	}
	if got := string(data); got != want {
		t.Errorf("contents of %s = %q, want exact pre-operation bytes %q", filepath.Base(path), got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Error(err)
		return
	}
	if got := info.Mode().Perm(); got != wantMode {
		t.Errorf("mode of %s = %#o, want pre-operation mode %#o", filepath.Base(path), got, wantMode)
	}
}
