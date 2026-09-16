package graphene

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func restackConflict(t *testing.T) (testRepo, State, string) {
	t.Helper()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "sub/one.txt", "one\n", "One")
	runGit(t, repo.dir, "branch", "bookmark")
	createStackBranch(t, repo, "file.txt", "child\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")
	runGit(t, repo.dir, "switch", "-c", "target", "main")
	commitFile(t, repo.dir, "file.txt", "target\n", "Target")
	runGit(t, repo.dir, "switch", "stack/one")
	runGit(t, repo.dir, "config", "rebase.updateRefs", "true")
	writeFile(t, repo.dir, "notes", "untracked notes\n")
	state := readState(t, repo.dir)
	refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	fromSubdir := repo
	fromSubdir.dir = filepath.Join(repo.dir, "sub")
	code, _, stderr := fromSubdir.runGraphene(t, "restack", "target")
	if code == 0 {
		t.Fatal("restack unexpectedly succeeded")
	}
	pending := readState(t, repo.dir)
	if pending.Pending == nil || pending.Pending.Recovery == nil || pending.Pending.Recovery.Phase != recoveryConflict {
		t.Fatalf("pending = %#v, stderr %q", pending.Pending, stderr)
	}
	if !reflect.DeepEqual(pending.Stacks, state.Stacks) {
		t.Fatal("restack published metadata before completing the rebases")
	}
	assertBranchParent(t, repo.dir, "stack/one", "target")
	if got := runGit(t, repo.dir, "rev-parse", "bookmark"); got == runGit(t, repo.dir, "rev-parse", "stack/one") {
		t.Fatal("rebase moved an unrelated bookmark")
	}
	return repo, state, refs
}

func assertRestackRestored(t *testing.T, repo testRepo, state State, refs string) {
	t.Helper()
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, state) {
		t.Fatalf("restored state = %#v, want %#v", got, state)
	}
	if got := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refs {
		t.Fatalf("restored refs:\n%s\nwant:\n%s", got, refs)
	}
	if got := currentBranch(t, repo.dir); got != "stack/one" {
		t.Fatalf("restored checkout = %s", got)
	}
	if got := runGit(t, repo.dir, "status", "--porcelain"); got != "?? notes" {
		t.Fatalf("restored worktree status = %q", got)
	}
	data, err := os.ReadFile(filepath.Join(repo.dir, "notes"))
	if err != nil || string(data) != "untracked notes\n" {
		t.Fatalf("untracked notes = %q, error %v", data, err)
	}
}

func assertRestackSnapshotRemoved(t *testing.T, repo testRepo, id string) {
	t.Helper()
	if got := runGit(t, repo.dir, "for-each-ref", snapshotRefPrefix(id)); got != "" {
		t.Fatal("completed restack retained backup refs")
	}
	path, err := (Git{Dir: repo.dir}).snapshotPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("snapshot manifest survived completion: %v", err)
	}
}

func TestRestackConflictRecovery(t *testing.T) {
	for _, action := range []string{"continue", "abort"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			repo, original, refs := restackConflict(t)
			id := readState(t, repo.dir).Pending.Recovery.Snapshot
			if code, _, _ := repo.runGraphene(t, "forget", "--force", "stack/one"); code == 0 {
				t.Fatal("forget discarded an operation that still needs its snapshot")
			}
			if action == "continue" {
				writeFile(t, repo.dir, "file.txt", "resolved\n")
				runGit(t, repo.dir, "add", "file.txt")
			}
			expectGrapheneOK(t, repo, action)
			if action == "abort" {
				assertRestackRestored(t, repo, original, refs)
			} else {
				assertBranchParent(t, repo.dir, "stack/one", "target")
				assertBranchParent(t, repo.dir, "stack/two", "stack/one")
				assertBranchParent(t, repo.dir, "stack/three", "stack/two")
				state := readState(t, repo.dir)
				want := []Stack{{Base: "target", Branches: []string{"stack/one", "stack/two", "stack/three"}}}
				if state.Pending != nil || !reflect.DeepEqual(state.Stacks, want) {
					t.Fatalf("completed state = %#v", state)
				}
				if got := currentBranch(t, repo.dir); got != "stack/one" {
					t.Fatalf("checkout after continue = %s", got)
				}
			}
			assertRestackSnapshotRemoved(t, repo, id)
		})
	}
}

func TestRestackAbortRefusesBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"other worktree", "branch drift", "unrelated rebase"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			repo, _, _ := restackConflict(t)
			want := ""
			switch scenario {
			case "other worktree":
				linked := t.TempDir()
				runGit(t, repo.dir, "worktree", "add", linked, "stack/one")
				foreign := testRepo{dir: linked, configDir: repo.configDir}
				if code, _, stderr := foreign.runGraphene(t, "abort"); code == 0 || !strings.Contains(stderr, "original worktree") {
					t.Fatalf("abort from another worktree: %d, %s", code, stderr)
				}
				want = "checked out in another worktree"
			case "branch drift":
				runGit(t, repo.dir, "update-ref", "refs/heads/stack/one", "main")
				want = "changed outside the operation"
			case "unrelated rebase":
				runGit(t, repo.dir, "rebase", "--abort")
				code, _, stderr := runGitResult(t, repo.dir, "rebase", "--no-update-refs", "--onto", "target", "main", "stack/two")
				if code == 0 {
					t.Fatalf("expected another conflicting rebase: %s", stderr)
				}
				want = "does not match this operation"
			}
			beforeRefs := runGit(t, repo.dir, "show-ref")
			beforeStatus := runGit(t, repo.dir, "status", "--porcelain")
			beforeState := readState(t, repo.dir)
			if code, _, stderr := repo.runGraphene(t, "abort"); code == 0 || !strings.Contains(stderr, want) {
				t.Fatalf("abort: %d, %s; want %q", code, stderr, want)
			}
			if got := runGit(t, repo.dir, "show-ref"); got != beforeRefs {
				t.Fatal("refused abort moved refs")
			}
			if got := runGit(t, repo.dir, "status", "--porcelain"); got != beforeStatus {
				t.Fatal("refused abort changed the worktree")
			}
			if got := readState(t, repo.dir); !reflect.DeepEqual(got, beforeState) {
				t.Fatal("refused abort changed pending state")
			}
		})
	}
}

func TestRestackResumesOnlyRecordedResults(t *testing.T) {
	for _, boundary := range []string{"before Git", "after Git", "after result saved"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			repo, original, refs := restackConflict(t)
			state := readState(t, repo.dir)
			// Persist the same intent as continue, then stop before acknowledging Git.
			state.Pending.Recovery.Phase = recoveryApplying
			if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
				t.Fatal(err)
			}
			if boundary != "before Git" {
				writeFile(t, repo.dir, "file.txt", "resolved\n")
				runGit(t, repo.dir, "add", "file.txt")
				runGit(t, repo.dir, "rebase", "--continue")
			}
			if boundary == "after result saved" {
				state.Pending.Recovery.Expected["stack/two"] = runGit(t, repo.dir, "rev-parse", "stack/two")
				state.Pending.Queue = state.Pending.Queue[1:]
				state.Pending.Recovery.Phase = recoveryReady
				state.Pending.Recovery.Onto = ""
				if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
					t.Fatal(err)
				}
				expectGrapheneOK(t, repo, "continue")
				assertBranchParent(t, repo.dir, "stack/three", "stack/two")
				assertRestackSnapshotRemoved(t, repo, state.Pending.Recovery.Snapshot)
				return
			}
			before := runGit(t, repo.dir, "show-ref")
			if code, _, stderr := repo.runGraphene(t, "continue"); code == 0 || !strings.Contains(stderr, "interrupted during a Git step") {
				t.Fatalf("continue: %d, %s", code, stderr)
			}
			if got := runGit(t, repo.dir, "show-ref"); got != before {
				t.Fatal("ambiguous continue moved refs")
			}
			runGit(t, repo.dir, "reflog", "expire", "--expire=now", "--all")
			runGit(t, repo.dir, "prune", "--expire=now")
			expectGrapheneOK(t, repo, "abort")
			assertRestackRestored(t, repo, original, refs)
		})
	}
}

func TestRestackRetriesInterruptedAbort(t *testing.T) {
	for _, boundary := range []string{"refs restored", "worktree restored"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			repo, original, refs := restackConflict(t)
			state := readState(t, repo.dir)
			state.Pending.Recovery.Phase = recoveryAborting
			if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo.dir, "rebase", "--abort")
			if err := (Git{Dir: repo.dir}).WithStateLock(func(g Git) error {
				r := state.Pending.Recovery
				if boundary == "worktree restored" {
					_, err := g.restoreSnapshot(r.Snapshot, r.Expected)
					return err
				}
				snapshot, err := g.readSnapshot(r.Snapshot)
				if err != nil {
					return err
				}
				var edits []snapshotRefEdit
				for branch, oid := range r.Expected {
					edits = append(edits, snapshotRefEdit{Ref: "refs/heads/" + branch, Old: oid, New: snapshot.Refs[branch]})
				}
				return g.updateSnapshotRefs(edits)
			}); err != nil {
				t.Fatal(err)
			}
			if code, _, stderr := repo.runGraphene(t, "continue"); code == 0 || !strings.Contains(stderr, "rollback is in progress") {
				t.Fatalf("continue during rollback: %d, %s", code, stderr)
			}
			expectGrapheneOK(t, repo, "abort")
			assertRestackRestored(t, repo, original, refs)
			assertRestackSnapshotRemoved(t, repo, state.Pending.Recovery.Snapshot)
		})
	}
}

func TestRestackContinueCommitFailureRequiresAbort(t *testing.T) {
	t.Parallel()
	repo, original, refs := restackConflict(t)
	writeExecutable(t, filepath.Join(repo.dir, ".git", "hooks", "prepare-commit-msg"), "#!/bin/sh\nexit 1\n")
	writeFile(t, repo.dir, "file.txt", "resolved\n")
	runGit(t, repo.dir, "add", "file.txt")
	if code, _, _ := repo.runGraphene(t, "continue"); code == 0 {
		t.Fatal("continue ignored the failed commit hook")
	}
	if code, _, stderr := repo.runGraphene(t, "continue"); code == 0 || !strings.Contains(stderr, "interrupted during a Git step") {
		t.Fatalf("continue after failed commit: %d, %s", code, stderr)
	}
	expectGrapheneOK(t, repo, "abort")
	assertRestackRestored(t, repo, original, refs)
}

func TestRestackKilledAfterRewriteCanAbort(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	original := readState(t, repo.dir)
	oldOne := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "switch", "-c", "target", "main")
	commitFile(t, repo.dir, "target.txt", "target\n", "Target")
	runGit(t, repo.dir, "switch", "stack/one")
	// Terminate the real Git process after its rewrite, before it reports success.
	writeExecutable(t, filepath.Join(repo.dir, ".git", "hooks", "post-rewrite"), "#!/bin/sh\nkill -KILL \"$PPID\"\n")
	if code, _, _ := repo.runGraphene(t, "restack", "target"); code == 0 {
		t.Fatal("restack ignored the terminated Git process")
	}
	state := readState(t, repo.dir)
	if state.Pending == nil || state.Pending.Recovery.Phase != recoveryApplying {
		t.Fatalf("pending after interrupted Git = %#v", state.Pending)
	}
	expectGrapheneOK(t, repo, "abort")
	if got := runGit(t, repo.dir, "rev-parse", "stack/one"); got != oldOne {
		t.Fatalf("abort restored %s, want %s", got, oldOne)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, original) {
		t.Fatalf("abort restored metadata = %#v, want %#v", got, original)
	}
}

func TestRestackSnapshotsBeforeFastForward(t *testing.T) {
	for _, action := range []string{"abort", "preflight"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			repo, remote := newTestRepoWithOrigin(t)
			createStackBranch(t, repo, "one.txt", "one\n", "One")
			createStackBranch(t, repo, "two.txt", "two\n", "Two")
			expectGrapheneOK(t, repo, "send", "origin")
			oldOne := runGit(t, repo.dir, "rev-parse", "stack/one")
			other := cloneConfiguredRepo(t, remote, "stack/one")
			remoteOne := commitFile(t, other, "file.txt", "upstream\n", "Upstream")
			runGit(t, other, "push", "origin", "stack/one")
			runGit(t, repo.dir, "switch", "-c", "target", "main")
			commitFile(t, repo.dir, "file.txt", "target\n", "Target")
			runGit(t, repo.dir, "switch", "stack/one")
			if action == "preflight" {
				runGit(t, repo.dir, "worktree", "add", t.TempDir(), "stack/two")
			}
			original := readState(t, repo.dir)
			code, _, stderr := repo.runGraphene(t, "restack", "--fetch", "target")
			if code == 0 {
				t.Fatal("restack unexpectedly succeeded")
			}
			state := readState(t, repo.dir)
			if action == "preflight" {
				if !strings.Contains(stderr, "checked out in another worktree") || state.Pending != nil {
					t.Fatalf("preflight result: %#v, stderr %s", state.Pending, stderr)
				}
			} else {
				if state.Pending == nil || state.Pending.Recovery == nil || state.Pending.Recovery.Expected["stack/one"] != remoteOne {
					t.Fatalf("fast-forward was not recorded: %#v, stderr %s", state.Pending, stderr)
				}
				expectGrapheneOK(t, repo, "abort")
			}
			if got := runGit(t, repo.dir, "rev-parse", "stack/one"); got != oldOne {
				t.Fatalf("original branch tip %s was not preserved: %s", oldOne, got)
			}
			if got := readState(t, repo.dir); !reflect.DeepEqual(got, original) {
				t.Fatalf("original metadata was not preserved: %#v", got)
			}
		})
	}
}
