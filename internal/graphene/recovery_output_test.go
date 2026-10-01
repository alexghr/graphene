package graphene

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAmendAbortReportsRetainedChanges(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{false, true} {
		name := "recorded original tips"
		if legacy {
			name = "legacy pending state"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			createStackBranch(t, repo, "one.txt", "one\n", "One")
			createStackBranch(t, repo, "two.txt", "two\n", "Two")
			oldTwo := runGit(t, repo.dir, "rev-parse", "HEAD")
			runGit(t, repo.dir, "switch", "-c", "stack/sibling", "stack/one")
			commitFile(t, repo.dir, "one.txt", "sibling\n", "Sibling")
			expectGrapheneOK(t, repo, "track", "--parent", "stack/one")
			sibling := runGit(t, repo.dir, "rev-parse", "HEAD")
			runGit(t, repo.dir, "switch", "stack/one")
			writeFile(t, repo.dir, "one.txt", "amended\n")
			runGit(t, repo.dir, "add", "one.txt")
			if code, _, stderr := repo.runGraphene(t, "amend", "--no-edit"); code == 0 || !strings.Contains(stderr, "could not apply") {
				t.Fatalf("expected sibling conflict: %d, %s", code, stderr)
			}
			amendedOne := runGit(t, repo.dir, "rev-parse", "stack/one")
			rewrittenTwo := runGit(t, repo.dir, "rev-parse", "stack/two")
			if rewrittenTwo == oldTwo {
				t.Fatal("amend did not finish rewriting the first descendant")
			}
			if legacy {
				state := readState(t, repo.dir)
				state.Pending.RewriteBefore = nil
				if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
					t.Fatal(err)
				}
			}
			code, stdout, stderr := repo.runGraphene(t, "abort")
			if code != 0 || !strings.Contains(stdout, "Retained amended commit on stack/one.") || !strings.Contains(stdout, "Checkout: stack/sibling.") {
				t.Fatalf("abort = %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			if legacy {
				if !strings.Contains(stdout, "legacy pending state does not record which branches") || strings.Contains(stdout, "Retained descendant branch changes:") {
					t.Fatalf("legacy abort claimed branch evidence: %s", stdout)
				}
			} else if !strings.Contains(stdout, "Retained descendant branch changes: stack/two.") {
				t.Fatalf("abort omitted completed rewrite: %s", stdout)
			}
			for branch, want := range map[string]string{"stack/one": amendedOne, "stack/two": rewrittenTwo, "stack/sibling": sibling} {
				if got := runGit(t, repo.dir, "rev-parse", branch); got != want {
					t.Fatalf("%s after abort = %s, want %s", branch, got, want)
				}
			}
			for branch, want := range map[string]string{"stack/one": "amended", "stack/two": "amended", "stack/sibling": "sibling"} {
				if got := runGit(t, repo.dir, "show", branch+":one.txt"); got != want {
					t.Fatalf("%s content after abort = %q, want %q", branch, got, want)
				}
			}
			if state := readState(t, repo.dir); state.Pending != nil {
				t.Fatalf("abort left pending state = %#v", state.Pending)
			}
		})
	}
}

func TestAbortReportsDetachedCheckout(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "switch", "-c", "target")
	commitFile(t, repo.dir, "file.txt", "target\n", "Target")
	runGit(t, repo.dir, "switch", "-c", "topic", "main")
	original := commitFile(t, repo.dir, "file.txt", "topic\n", "Topic")
	runGit(t, repo.dir, "switch", "--detach")
	if code, _, stderr := runGitResult(t, repo.dir, "rebase", "target"); code == 0 || !strings.Contains(stderr, "could not apply") {
		t.Fatalf("expected conflicting rebase: %d, %s", code, stderr)
	}
	code, stdout, stderr := repo.runGraphene(t, "abort")
	if code != 0 || !strings.Contains(stdout, "Undone: in-progress Git rebase.") || !strings.Contains(stdout, "Checkout: detached at "+original+".") {
		t.Fatalf("abort = %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if branch := runGit(t, repo.dir, "branch", "--show-current"); branch != "" {
		t.Fatalf("checkout attached to %s", branch)
	}
	if got := runGit(t, repo.dir, "rev-parse", "HEAD"); got != original {
		t.Fatalf("abort left HEAD at %s, want %s", got, original)
	}
	if got := runGit(t, repo.dir, "show", "HEAD:file.txt"); got != "topic" {
		t.Fatalf("restored content = %q", got)
	}
}

func TestSquashAbortReportsRestoration(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	createStackBranch(t, repo, "three.txt", "three\n", "Three")
	state := readState(t, repo.dir)
	refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	runGit(t, repo.dir, "switch", "stack/two")
	writeExecutable(t, filepath.Join(repo.dir, ".git", "hooks", "pre-rebase"), "#!/bin/sh\necho 'stop descendant rebase' >&2\nexit 1\n")
	if code, _, stderr := repo.runGraphene(t, "squash", "--no-edit"); code == 0 || !strings.Contains(stderr, "stop descendant rebase") {
		t.Fatalf("expected rebase hook rejection: %d, %s", code, stderr)
	}
	if pending := readState(t, repo.dir).Pending; pending == nil || pending.Operation != "squash" {
		t.Fatalf("pending squash = %#v", pending)
	}
	code, stdout, stderr := repo.runGraphene(t, "abort")
	if code != 0 || !strings.Contains(stdout, "Aborted squash.") || !strings.Contains(stdout, "Restored original branch tips: stack/one, stack/three, stack/two.") || !strings.Contains(stdout, "Checkout: stack/two.") {
		t.Fatalf("abort = %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if got := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refs {
		t.Fatalf("restored refs:\n%s\nwant:\n%s", got, refs)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, state) {
		t.Fatalf("restored metadata = %#v, want %#v", got, state)
	}
	if got := runGit(t, repo.dir, "show", "HEAD:two.txt"); got != "two" {
		t.Fatalf("restored squash content = %q", got)
	}
}
