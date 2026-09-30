package graphene

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func captureTestSnapshot(t *testing.T, dir string) string {
	t.Helper()
	var id string
	if err := (Git{Dir: dir}).WithStateLock(func(g Git) (err error) {
		id, err = g.captureSnapshot()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func restoreTestSnapshot(dir, id string, expected map[string]string) (State, error) {
	var state State
	err := (Git{Dir: dir}).WithStateLock(func(g Git) (err error) {
		state, err = g.restoreSnapshot(id, expected)
		return err
	})
	return state, err
}

func TestSnapshotRejectsTrackedChanges(t *testing.T) {
	t.Parallel()
	for _, staged := range []bool{false, true} {
		t.Run(fmt.Sprint(staged), func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			writeFile(t, repo.dir, "file.txt", "local edits\n")
			if staged {
				runGit(t, repo.dir, "add", "file.txt")
			}
			err := (Git{Dir: repo.dir}).WithStateLock(func(g Git) error {
				_, err := g.captureSnapshot()
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "tracked changes") {
				t.Fatalf("capture error = %v", err)
			}
			if got := runGit(t, repo.dir, "for-each-ref", "refs/graphene/snapshots/"); got != "" {
				t.Fatal("rejected capture installed backup refs")
			}
		})
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "switch", "-c", "topic")
	original := commitFile(t, repo.dir, "gone.txt", "original\n", "topic")
	runGit(t, repo.dir, "branch", "archived")
	runGit(t, repo.dir, "branch", "unrelated", "main")
	saved := State{Stacks: []Stack{{Base: "main", Branches: []string{"topic"}}}, Boundaries: map[string]string{"topic": runGit(t, repo.dir, "rev-parse", "main")}}
	if err := (Git{Dir: repo.dir}).WriteState(saved); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo.dir, "sub/untracked\nname", "keep me\n")
	id := captureTestSnapshot(t, filepath.Join(repo.dir, "sub"))
	runGit(t, repo.dir, "rm", "gone.txt")
	writeFile(t, repo.dir, "file.txt", "operation\n")
	runGit(t, repo.dir, "add", "-u")
	runGit(t, repo.dir, "commit", "--amend", "-m", "rewritten")
	changed := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "branch", "-D", "archived")
	runGit(t, repo.dir, "switch", "-c", "created")
	runGit(t, repo.dir, "branch", "-f", "unrelated", changed)
	writeFile(t, repo.dir, "later.txt", "outside the operation\n")
	if err := (Git{Dir: repo.dir}).WriteState(State{}); err != nil {
		t.Fatal(err)
	}
	// Backup refs keep the original commit alive after reflog expiry and pruning.
	runGit(t, repo.dir, "reflog", "expire", "--expire=now", "--all")
	runGit(t, repo.dir, "prune", "--expire=now")
	expected := map[string]string{"topic": changed, "archived": "", "created": changed}
	for range 2 {
		restored, err := restoreTestSnapshot(repo.dir, id, expected)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(restored, saved) {
			t.Fatalf("restored state = %#v, want %#v", restored, saved)
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
		for path, want := range map[string]string{"file.txt": "base\n", "gone.txt": "original\n", "sub/untracked\nname": "keep me\n", "later.txt": "outside the operation\n"} {
			got, err := os.ReadFile(filepath.Join(repo.dir, path))
			if err != nil || string(got) != want {
				t.Fatalf("%q = %q, want %q (error %v)", path, got, want, err)
			}
		}
	}
	if got := runGit(t, repo.dir, "show", ":file.txt"); got != "base" {
		t.Fatalf("restored tracked content = %q", got)
	}
	if got := runGit(t, repo.dir, "ls-files", "sub"); got != "" {
		t.Fatalf("untracked file became tracked: %q", got)
	}
	if err := (Git{Dir: repo.dir}).WithStateLock(func(g Git) error {
		if err := g.WriteState(saved); err != nil {
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
			id := captureTestSnapshot(t, repo.dir)
			snapshot, err := (Git{Dir: repo.dir}).readSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			changed := commitFile(t, repo.dir, "file.txt", "operation\n", "operation")
			// Stop at the boundaries between rollback's ref and checkout writes.
			runGit(t, repo.dir, "update-ref", "refs/heads/main", original, changed)
			if interruptedAfter == "worktree" {
				runGit(t, repo.dir, "read-tree", "--reset", "-u", snapshot.Head)
			}
			if _, err := restoreTestSnapshot(repo.dir, id, map[string]string{"main": changed}); err != nil {
				t.Fatal(err)
			}
			if got := runGit(t, repo.dir, "show", ":file.txt"); got != "base" {
				t.Fatalf("staged content = %q", got)
			}
			content, err := os.ReadFile(filepath.Join(repo.dir, "file.txt"))
			if err != nil || string(content) != "base\n" {
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
			id := captureTestSnapshot(t, repo.dir)
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
	id := captureTestSnapshot(t, linked)
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
