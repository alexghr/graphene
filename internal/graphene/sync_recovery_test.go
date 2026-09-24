package graphene

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func assertSyncRestored(t *testing.T, repo testRepo, original State, refs, branch string) {
	t.Helper()
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, original) {
		t.Fatalf("restored state = %#v, want %#v", got, original)
	}
	if got := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refs {
		t.Fatalf("restored refs:\n%s\nwant:\n%s", got, refs)
	}
	if got := currentBranch(t, repo.dir); got != branch {
		t.Fatalf("restored checkout = %s, want %s", got, branch)
	}
	if got := runGit(t, repo.dir, "status", "--porcelain"); got != "" {
		t.Fatalf("restored worktree is dirty: %s", got)
	}
}

func TestSyncSnapshotConflict(t *testing.T) {
	t.Parallel()
	t.Run("abort with base elsewhere", func(t *testing.T) {
		t.Parallel()
		repo, remote := newTestRepoWithOrigin(t)
		createStackBranch(t, repo, "one.txt", "one\n", "One")
		createStackBranch(t, repo, "two.txt", "two\n", "Two")
		runGit(t, repo.dir, "branch", "bookmark")
		createStackBranch(t, repo, "file.txt", "child\n", "Three")
		runGit(t, repo.dir, "config", "rebase.updateRefs", "true")
		runGit(t, repo.dir, "config", "branch.stack/one.remote", "origin")
		runGit(t, repo.dir, "config", "branch.stack/one.merge", "refs/heads/stack/one")
		runGit(t, repo.dir, "switch", "stack/one")
		actor := cloneConfiguredRepo(t, remote, "main")
		commitFile(t, actor, "one.txt", "one\n", "Merged one")
		fetchedBase := commitFile(t, actor, "file.txt", "remote\n", "Conflicting base")
		runGit(t, actor, "push", "origin", "main")
		original := readState(t, repo.dir)
		refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		if code, _, _ := repo.runGraphene(t, "sync"); code == 0 {
			t.Fatal("sync unexpectedly succeeded")
		}
		state := readState(t, repo.dir)
		if state.Pending == nil || state.Pending.Recovery == nil || state.Pending.Recovery.Phase != recoveryConflict {
			t.Fatalf("pending conflict = %#v", state.Pending)
		}
		id := state.Pending.Recovery.Snapshot
		if !reflect.DeepEqual(state.Stacks, original.Stacks) || !refExists(t, repo.dir, "refs/heads/stack/one") {
			t.Fatal("sync published metadata or deleted a branch before completing rebases")
		}
		assertBranchParent(t, repo.dir, "stack/two", "main")
		if runGit(t, repo.dir, "rev-parse", "bookmark") == runGit(t, repo.dir, "rev-parse", "stack/two") {
			t.Fatal("sync moved the unrelated bookmark")
		}
		other := filepath.Join(t.TempDir(), "worktree")
		runGit(t, repo.dir, "worktree", "add", other, "main")
		before := runGit(t, repo.dir, "show-ref")
		if code, _, stderr := repo.runGraphene(t, "abort"); code == 0 || !strings.Contains(stderr, "another worktree") {
			t.Fatalf("abort while base checked out: %d, %s", code, stderr)
		}
		if got := runGit(t, repo.dir, "show-ref"); got != before {
			t.Fatal("refused abort moved refs")
		}
		if active, _ := (Git{Dir: repo.dir}).RebaseInProgress(); !active {
			t.Fatal("refused abort changed the active Git rebase")
		}
		runGit(t, other, "switch", "--detach")
		runGit(t, repo.dir, "reflog", "expire", "--expire=now", "--all")
		runGit(t, repo.dir, "prune", "--expire=now")
		expectGrapheneOK(t, repo, "abort")
		assertSyncRestored(t, repo, original, refs, "stack/one")
		if got := runGit(t, repo.dir, "config", "branch.stack/one.remote"); got != "origin" {
			t.Fatalf("restored branch lost its upstream: %s", got)
		}
		assertRestackSnapshotRemoved(t, repo, id)
		if got := runGit(t, repo.dir, "rev-parse", "origin/main"); got != fetchedBase {
			t.Fatalf("recovery changed fetched upstream: %s, want %s", got, fetchedBase)
		}
	})
}

func TestSyncSnapshotDeletionBoundaries(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"before deletion", "after deletion", "state write fails", "result saved", "rollback restored"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			repo, remote := newTestRepoWithOrigin(t)
			createStackBranch(t, repo, "one.txt", "one\n", "One")
			createStackBranch(t, repo, "two.txt", "two\n", "Two")
			// There is no branch.stack/one section; this section must not match it.
			runGit(t, repo.dir, "config", "branch.stack/one.v2.description", "keep me")
			runGit(t, repo.dir, "config", "branch.stack/two.remote", "origin")
			runGit(t, repo.dir, "config", "branch.stack/two.merge", "refs/heads/stack/two")
			actor := cloneConfiguredRepo(t, remote, "main")
			commitFile(t, actor, "one.txt", "one\n", "Merged one")
			commitFile(t, actor, "two.txt", "two\n", "Merged two")
			runGit(t, actor, "push", "origin", "main")
			original := readState(t, repo.dir)
			refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
			hook := filepath.Join(repo.dir, ".git", "hooks", "reference-transaction")
			phase, failure := "committed", "kill -KILL \"$PPID\""
			if boundary == "before deletion" {
				phase, failure = "prepared", "exit 1"
			}
			if boundary == "state write fails" {
				failure = `mv "$state" "$state.saved"; mkdir "$state"`
			}
			writeExecutable(t, hook, "#!/bin/sh\n[ \"$1\" = "+phase+" ] || exit 0\n"+`grep -q '"phase":"deleting"' "$(git rev-parse --git-common-dir)/graphene/state.json" || exit 0
state="$(git rev-parse --git-common-dir)/graphene/state.json"
while read old new ref; do
  case "$new $ref" in
    000000*' refs/heads/stack/one') `+failure+` ;;
  esac
done
`)
			code, _, stderr := repo.runGraphene(t, "sync")
			if code == 0 {
				t.Fatal("sync ignored the failed Git transaction")
			}
			if err := os.Remove(hook); err != nil {
				t.Fatal(err)
			}
			if boundary == "state write fails" {
				path := filepath.Join(repo.dir, ".git", "graphene", "state.json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".saved", path); err != nil {
					t.Fatal(err)
				}
			}
			state := readState(t, repo.dir)
			if state.Pending == nil || state.Pending.Recovery == nil || state.Pending.Recovery.Phase != recoveryDeleting {
				t.Fatalf("pending deletion = %#v, stderr:\n%s", state.Pending, stderr)
			}
			p, r := state.Pending, state.Pending.Recovery
			for _, branch := range p.Branches {
				if exists := refExists(t, repo.dir, "refs/heads/"+branch); exists != (boundary == "before deletion") {
					t.Fatalf("%s existence = %v at %s", branch, exists, boundary)
				}
			}
			if boundary == "result saved" || boundary == "rollback restored" {
				for _, branch := range p.Branches {
					r.Expected[branch] = ""
				}
				r.Phase = recoveryReady
				if boundary == "rollback restored" {
					r.Phase = recoveryAborting
					if _, err := restoreTestSnapshot(repo.dir, r.Snapshot, r.Expected); err != nil {
						t.Fatal(err)
					}
				}
				if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "result saved" {
				expectGrapheneOK(t, repo, "continue")
				if got := readState(t, repo.dir); len(got.Stacks) != 0 || got.Pending != nil {
					t.Fatalf("final state = %#v", got)
				}
				if got := runGit(t, repo.dir, "config", "branch.stack/one.v2.description"); got != "keep me" {
					t.Fatalf("config cleanup changed similarly named branch: %s", got)
				}
			} else {
				if code, _, _ := repo.runGraphene(t, "continue"); code == 0 {
					t.Fatal("continued an ambiguous deletion or unfinished rollback")
				}
				expectGrapheneOK(t, repo, "abort")
				assertSyncRestored(t, repo, original, refs, "stack/two")
				if got := runGit(t, repo.dir, "config", "branch.stack/two.remote"); got != "origin" {
					t.Fatalf("deleted branch lost upstream on abort: %s", got)
				}
			}
			assertRestackSnapshotRemoved(t, repo, r.Snapshot)
		})
	}
}

func TestSyncInterruptedBaseFastForward(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "switch", "main")
	actor := cloneConfiguredRepo(t, remote, "main")
	commitFile(t, actor, "remote.txt", "remote\n", "Base update")
	runGit(t, actor, "push", "origin", "main")
	original := readState(t, repo.dir)
	refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	writeExecutable(t, filepath.Join(repo.dir, ".git", "hooks", "post-merge"), "#!/bin/sh\nkill -KILL \"$PPID\"\n")
	if code, _, _ := repo.runGraphene(t, "sync", "--all"); code == 0 {
		t.Fatal("sync ignored the interrupted base update")
	}
	state := readState(t, repo.dir)
	if state.Pending == nil || state.Pending.Recovery == nil || state.Pending.Recovery.Phase != recoveryApplying {
		t.Fatalf("pending fast-forward = %#v", state.Pending)
	}
	if runGit(t, repo.dir, "rev-parse", "main") != runGit(t, actor, "rev-parse", "main") {
		t.Fatal("base update did not happen")
	}
	if code, _, stderr := repo.runGraphene(t, "continue"); code == 0 || !strings.Contains(stderr, "abort and rerun") {
		t.Fatalf("continued interrupted fast-forward: %d, %s", code, stderr)
	}
	expectGrapheneOK(t, repo, "abort")
	assertSyncRestored(t, repo, original, refs, "main")
	assertRestackSnapshotRemoved(t, repo, state.Pending.Recovery.Snapshot)
}
