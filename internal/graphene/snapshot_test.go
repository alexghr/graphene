package graphene

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func captureTestSnapshot(t *testing.T, dir string, worktree bool) string {
	t.Helper()
	var id string
	if err := (Git{Dir: dir}).WithStateLock(func(g Git) (err error) {
		id, err = g.captureSnapshot(worktree)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func restoreTestSnapshot(dir, id string, expected map[string]string) ([]Stack, error) {
	var stacks []Stack
	err := (Git{Dir: dir}).WithStateLock(func(g Git) (err error) {
		stacks, err = g.restoreSnapshot(id, expected)
		return err
	})
	return stacks, err
}

func TestSnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "switch", "-c", "topic")
	original := commitFile(t, repo.dir, "gone.txt", "delete me\n", "topic")
	runGit(t, repo.dir, "branch", "archived")
	runGit(t, repo.dir, "branch", "unrelated", "main")
	stacks := []Stack{{Base: "main", Branches: []string{"topic"}}}
	if err := (Git{Dir: repo.dir}).WriteState(State{Stacks: stacks}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo.dir, "file.txt", "staged\n")
	runGit(t, repo.dir, "add", "file.txt")
	writeFile(t, repo.dir, "file.txt", "unstaged\n")
	runGit(t, repo.dir, "rm", "gone.txt")
	writeFile(t, repo.dir, "binary", "\x00staged\xff")
	runGit(t, repo.dir, "add", "binary")
	writeFile(t, repo.dir, "binary", "\x00unstaged\xfe")
	writeFile(t, repo.dir, "sub/untracked\nname", "keep me\n")
	indexPath, err := (Git{Dir: repo.dir}).GitPath("index")
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	// Capturing from a subdirectory still backs up the entire worktree.
	id := captureTestSnapshot(t, filepath.Join(repo.dir, "sub"), true)
	after, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(index, after) {
		t.Fatalf("capture changed the live index: %v", err)
	}
	writeFile(t, repo.dir, "file.txt", "operation\n")
	runGit(t, repo.dir, "add", "-A")
	runGit(t, repo.dir, "commit", "--amend", "-m", "rewritten")
	changed := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "branch", "-D", "archived")
	runGit(t, repo.dir, "switch", "-c", "created")
	runGit(t, repo.dir, "branch", "-f", "unrelated", changed)
	writeFile(t, repo.dir, "later.txt", "outside the operation\n")
	if err := (Git{Dir: repo.dir}).WriteState(State{}); err != nil {
		t.Fatal(err)
	}
	// Only the backup refs now keep the old commit and staged-only blobs alive.
	runGit(t, repo.dir, "reflog", "expire", "--expire=now", "--all")
	runGit(t, repo.dir, "prune", "--expire=now")
	expected := map[string]string{"topic": changed, "archived": "", "created": changed}
	for range 2 {
		// Reopen the snapshot and lock on each attempt, including after rollback.
		restored, err := restoreTestSnapshot(repo.dir, id, expected)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(restored, stacks) {
			t.Fatalf("restored stacks = %#v, want %#v", restored, stacks)
		}
		after, err = os.ReadFile(indexPath)
		if err != nil || !bytes.Equal(index, after) {
			t.Fatalf("rollback did not restore the index: %v", err)
		}
		if got := currentBranch(t, repo.dir); got != "topic" {
			t.Fatalf("branch = %q", got)
		}
		for _, branch := range []string{"topic", "archived"} {
			if got := runGit(t, repo.dir, "rev-parse", branch); got != original {
				t.Fatalf("%s = %s, want %s", branch, got, original)
			}
		}
		if refExists(t, repo.dir, "refs/heads/created") {
			t.Fatal("created branch survived rollback")
		}
		if got := runGit(t, repo.dir, "rev-parse", "unrelated"); got != changed {
			t.Fatal("rollback changed an unrelated branch")
		}
		for path, want := range map[string]string{
			"file.txt": "unstaged\n", "binary": "\x00unstaged\xfe",
			"sub/untracked\nname": "keep me\n", "later.txt": "outside the operation\n",
		} {
			got, err := os.ReadFile(filepath.Join(repo.dir, path))
			if err != nil || string(got) != want {
				t.Fatalf("%q = %q, want %q (error %v)", path, got, want, err)
			}
		}
		if _, err := os.Stat(filepath.Join(repo.dir, "gone.txt")); !os.IsNotExist(err) {
			t.Fatalf("staged deletion not preserved: %v", err)
		}
	}
	if got := runGit(t, repo.dir, "show", ":file.txt"); got != "staged" {
		t.Fatalf("staged file content = %q", got)
	}
	if got := runGit(t, repo.dir, "ls-files", "sub"); got != "" {
		t.Fatalf("previously untracked file became tracked: %q", got)
	}
	if err := (Git{Dir: repo.dir}).WithStateLock(func(g Git) error {
		if err := g.WriteState(State{Stacks: stacks}); err != nil {
			return err
		}
		if err := g.removeSnapshot(id); err != nil {
			return err
		}
		return g.removeSnapshot(id)
	}); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, repo.dir, "for-each-ref", "--format=%(refname)", snapshotRefPrefix(id)); got != "" {
		t.Fatalf("backup refs survived cleanup: %s", got)
	}
}

func TestSnapshotTrackedFileInIgnoredDirectory(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	tracked := "spartan/scripts/logs/.gitignore"
	writeFile(t, repo.dir, "spartan/.gitignore", "scripts/logs\n")
	writeFile(t, repo.dir, tracked, "*\n!.gitignore\n")
	runGit(t, repo.dir, "add", "-f", "spartan/.gitignore", tracked)
	runGit(t, repo.dir, "commit", "-m", "Track placeholder in ignored directory")
	writeFile(t, repo.dir, tracked, "*\n!.gitignore\n# staged\n")
	runGit(t, repo.dir, "add", "-u")
	writeFile(t, repo.dir, tracked, "*\n!.gitignore\n# unstaged\n")
	writeFile(t, repo.dir, "spartan/scripts/logs/output.log", "ignored\n")
	writeFile(t, repo.dir, "untracked.txt", "untracked\n")
	before := runGit(t, repo.dir, "status", "--porcelain")
	id := captureTestSnapshot(t, repo.dir, true)
	snapshot, err := (Git{Dir: repo.dir}).readSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, repo.dir, "show", snapshot.WorktreeTree+":"+tracked); got != "*\n!.gitignore\n# unstaged" {
		t.Fatalf("snapshot content = %q", got)
	}
	if got := runGit(t, repo.dir, "show", snapshot.IndexTree+":"+tracked); got != "*\n!.gitignore\n# staged" {
		t.Fatalf("snapshot staged content = %q", got)
	}
	if got := runGit(t, repo.dir, "ls-tree", "-r", "--name-only", snapshot.WorktreeTree); strings.Contains(got, "output.log") || !strings.Contains(got, "untracked.txt") {
		t.Fatalf("incorrect snapshot files: %s", got)
	}
	if got := runGit(t, repo.dir, "status", "--porcelain"); got != before {
		t.Fatalf("snapshot changed staging: %s", got)
	}
}

func TestSnapshotRefTransactionIsAtomic(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	old := runGit(t, repo.dir, "rev-parse", "HEAD")
	newOID := commitFile(t, repo.dir, "file.txt", "next\n", "next")
	runGit(t, repo.dir, "branch", "a", old)
	runGit(t, repo.dir, "branch", "b", newOID)
	g := Git{Dir: repo.dir}
	err := g.updateSnapshotRefs([]snapshotRefEdit{
		{Ref: "refs/heads/a", Old: old, New: newOID},
		{Ref: "refs/heads/b", Old: old, New: newOID},
	})
	if err == nil {
		t.Fatal("transaction accepted a stale branch tip")
	}
	if got := runGit(t, repo.dir, "rev-parse", "a"); got != old {
		t.Fatal("failed transaction partially updated refs")
	}
}

func TestSnapshotRetriesInterruptedRollback(t *testing.T) {
	t.Parallel()
	for _, interruptedAfter := range []string{"refs", "worktree"} {
		t.Run(interruptedAfter, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			original := runGit(t, repo.dir, "rev-parse", "HEAD")
			writeFile(t, repo.dir, "file.txt", "staged\n")
			runGit(t, repo.dir, "add", "file.txt")
			writeFile(t, repo.dir, "file.txt", "unstaged\n")
			id := captureTestSnapshot(t, repo.dir, true)
			snapshot, err := (Git{Dir: repo.dir}).readSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			changed := commitFile(t, repo.dir, "file.txt", "operation\n", "operation")
			// Stop at the boundaries between rollback's ref, worktree and index writes.
			runGit(t, repo.dir, "update-ref", "refs/heads/main", original, changed)
			if interruptedAfter == "worktree" {
				runGit(t, repo.dir, "read-tree", "--reset", "-u", snapshot.WorktreeTree)
			}
			if _, err := restoreTestSnapshot(repo.dir, id, map[string]string{"main": changed}); err != nil {
				t.Fatal(err)
			}
			if got := runGit(t, repo.dir, "show", ":file.txt"); got != "staged" {
				t.Fatalf("staged content = %q", got)
			}
			content, err := os.ReadFile(filepath.Join(repo.dir, "file.txt"))
			if err != nil || string(content) != "unstaged\n" {
				t.Fatalf("worktree content = %q, error %v", content, err)
			}
		})
	}
}

func TestSnapshotRefusalDoesNotMutate(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"branch drift", "other worktree", "different checkout", "missing backup", "untracked collision", "ignored collision"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			runGit(t, repo.dir, "switch", "-c", "topic")
			old := runGit(t, repo.dir, "rev-parse", "HEAD")
			runGit(t, repo.dir, "branch", "parent")
			id := captureTestSnapshot(t, repo.dir, true)
			newOID := commitFile(t, repo.dir, "other.txt", "next\n", "next")
			runGit(t, repo.dir, "branch", "-f", "parent", newOID)
			expected := map[string]string{"topic": newOID, "parent": newOID}
			wantError := ""
			switch scenario {
			case "branch drift":
				commitFile(t, repo.dir, "other.txt", "external\n", "external")
				wantError = "changed outside the operation"
			case "other worktree":
				runGit(t, repo.dir, "worktree", "add", t.TempDir(), "parent")
				wantError = "checked out in another worktree"
			case "different checkout":
				runGit(t, repo.dir, "switch", "main")
				wantError = "switch back"
			case "missing backup":
				runGit(t, repo.dir, "update-ref", "-d", snapshotRefPrefix(id)+"heads/topic", old)
				wantError = "read snapshot backup"
			case "untracked collision", "ignored collision":
				runGit(t, repo.dir, "rm", "file.txt")
				writeFile(t, repo.dir, "file.txt/precious", "do not delete\n")
				if scenario == "ignored collision" {
					writeFile(t, repo.dir, ".git/info/exclude", "file.txt/\n")
				}
				wantError = "would be overwritten"
			}
			beforeRefs := runGit(t, repo.dir, "show-ref")
			beforeStatus := runGit(t, repo.dir, "status", "--porcelain=v1", "--untracked-files=all")
			if _, err := restoreTestSnapshot(repo.dir, id, expected); err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("restore error = %v, want %q", err, wantError)
			}
			if got := runGit(t, repo.dir, "show-ref"); got != beforeRefs {
				t.Fatal("failed preflight changed refs")
			}
			if got := runGit(t, repo.dir, "status", "--porcelain=v1", "--untracked-files=all"); got != beforeStatus {
				t.Fatal("failed preflight changed the worktree or index")
			}
			if strings.Contains(scenario, "collision") {
				data, err := os.ReadFile(filepath.Join(repo.dir, "file.txt/precious"))
				if err != nil || string(data) != "do not delete\n" {
					t.Fatalf("untracked content was lost: %v", err)
				}
			}
		})
	}
}

func TestSnapshotInLinkedWorktree(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	linked := t.TempDir()
	runGit(t, repo.dir, "worktree", "add", "-b", "topic", linked)
	original := runGit(t, linked, "rev-parse", "HEAD")
	id := captureTestSnapshot(t, linked, true)
	changed := commitFile(t, linked, "file.txt", "rewritten\n", "rewrite")
	expected := map[string]string{"topic": changed}
	if _, err := restoreTestSnapshot(repo.dir, id, expected); err == nil || !strings.Contains(err.Error(), "original worktree") {
		t.Fatalf("restore from another worktree = %v", err)
	}
	if got := runGit(t, linked, "rev-parse", "HEAD"); got != changed {
		t.Fatal("refusal changed the linked worktree's branch")
	}
	if _, err := restoreTestSnapshot(linked, id, expected); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, linked, "rev-parse", "HEAD"); got != original {
		t.Fatalf("restored HEAD = %s, want %s", got, original)
	}
	if got := runGit(t, linked, "status", "--porcelain"); got != "" {
		t.Fatalf("restored worktree is dirty: %s", got)
	}
}

func TestSnapshotRefsWithoutWorktree(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	original := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "branch", "topic")
	changed := commitFile(t, repo.dir, "file.txt", "next\n", "next")
	writeFile(t, repo.dir, "file.txt", "local edits\n")
	id := captureTestSnapshot(t, repo.dir, false)
	runGit(t, repo.dir, "branch", "-f", "topic", changed)
	if _, err := restoreTestSnapshot(repo.dir, id, map[string]string{"topic": changed}); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, repo.dir, "rev-parse", "topic"); got != original {
		t.Fatalf("restored branch = %s, want %s", got, original)
	}
	if got := runGit(t, repo.dir, "rev-parse", "HEAD"); got != changed {
		t.Fatal("refs-only rollback changed HEAD")
	}
	content, err := os.ReadFile(filepath.Join(repo.dir, "file.txt"))
	if err != nil || string(content) != "local edits\n" {
		t.Fatalf("refs-only rollback changed local edits: %v", err)
	}
}

func TestSnapshotRejectsUnsupportedIndex(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"split index", "skip worktree", "assume unchanged", "unmerged", "filter"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			wantError := ""
			switch scenario {
			case "split index":
				runGit(t, repo.dir, "update-index", "--split-index")
				wantError = "split indexes"
			case "skip worktree":
				runGit(t, repo.dir, "update-index", "--skip-worktree", "file.txt")
				wantError = `"file.txt": skip-worktree`
			case "assume unchanged":
				runGit(t, repo.dir, "update-index", "--assume-unchanged", "file.txt")
				wantError = `"file.txt": assume-unchanged`
			case "unmerged":
				blob := runGit(t, repo.dir, "rev-parse", "HEAD:file.txt")
				g := Git{Dir: repo.dir}
				if _, err := g.outputWithInput(strings.NewReader("100644 "+blob+" 1\tfile.txt\n"), "update-index", "--index-info"); err != nil {
					t.Fatal(err)
				}
				wantError = `"file.txt": unmerged`
			case "filter":
				writeFile(t, repo.dir, ".gitattributes", "untracked filter=custom\n")
				writeFile(t, repo.dir, "untracked", "content\n")
				wantError = "do not support filter"
			}
			writeFile(t, repo.dir, "sub/file", "subdirectory\n")
			before := runGit(t, repo.dir, "ls-files", "--stage", "-v")
			err := (Git{Dir: filepath.Join(repo.dir, "sub")}).WithStateLock(func(g Git) error {
				_, err := g.captureSnapshot(true)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("capture error = %v, want %q", err, wantError)
			}
			if got := runGit(t, repo.dir, "ls-files", "--stage", "-v"); got != before {
				t.Fatal("rejected capture changed the live index")
			}
			if got := runGit(t, repo.dir, "for-each-ref", "refs/graphene/snapshots/"); got != "" {
				t.Fatal("rejected capture installed backup refs")
			}
		})
	}
}
