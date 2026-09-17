package graphene

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSyncFetchesOncePerRemote(t *testing.T) {
	for _, separateRemote := range []bool{false, true} {
		for _, dryRun := range []bool{false, true} {
			name := fmt.Sprintf("separate-remote=%t/dry-run=%t", separateRemote, dryRun)
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				repo, _ := newTestRepoWithOrigin(t)
				remote := "origin"
				if separateRemote {
					fork := filepath.Join(t.TempDir(), "fork.git")
					runGit(t, "", "init", "--bare", fork)
					runGit(t, repo.dir, "remote", "add", "fork", fork)
					remote = "fork"
				}
				for _, name := range []string{"one", "two", "three"} {
					createStackBranch(t, repo, name+".txt", name+"\n", name)
					runGit(t, repo.dir, "push", "-u", remote, "HEAD")
				}
				runGit(t, repo.dir, "checkout", "main")
				createStackBranch(t, repo, "sibling.txt", "sibling\n", "sibling")
				runGit(t, repo.dir, "push", "-u", remote, "HEAD")
				runGit(t, repo.dir, "checkout", "main")

				// Count real transport connections, including any ls-remote probes.
				log := filepath.Join(t.TempDir(), "connections")
				uploadPack := filepath.Join(t.TempDir(), "upload-pack")
				if err := os.WriteFile(uploadPack, []byte("#!/bin/sh\necho connection >> '"+log+"'\nexec git-upload-pack \"$@\"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				runGit(t, repo.dir, "config", "remote.origin.uploadpack", "'"+uploadPack+"'")
				if separateRemote {
					runGit(t, repo.dir, "config", "remote.fork.uploadpack", "'"+uploadPack+"'")
				}
				expectGrapheneOK(t, repo, "sync", "--all", "--dry-run")
				runGit(t, repo.dir, "push", remote, "--delete", "stack/one", "stack/two")
				args := []string{"sync", "--all", "--assume-merged"}
				if dryRun {
					args = append(args, "--dry-run")
				}
				code, stdout, stderr := repo.runGraphene(t, args...)
				if code != 0 {
					t.Fatalf("sync exited %d\n%s\n%s", code, stdout, stderr)
				}
				if dryRun {
					if !strings.Contains(stdout, "  delete branches assumed merged:\n    stack/one\n    stack/two\n") {
						t.Fatalf("missing deleted upstreams in plan: %s", stdout)
					}
				} else if refExists(t, repo.dir, "refs/heads/stack/one") || refExists(t, repo.dir, "refs/heads/stack/two") {
					t.Fatal("stale cached upstreams prevented branch deletion")
				}
				connections, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				want := 2
				if separateRemote {
					want = 4
				}
				if got := strings.Count(string(connections), "connection\n"); got != want {
					t.Fatalf("transport connections across two syncs = %d, want %d", got, want)
				}
			})
		}
	}
}

func TestSyncFetchIgnoresConfiguredRefspecsAndTags(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	runGit(t, repo.dir, "branch", "victim")
	runGit(t, repo.dir, "tag", "keep-local-tag")
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "fetch", "origin")
	fetchHeadPath := filepath.Join(repo.dir, ".git", "FETCH_HEAD")
	fetchHead, err := os.ReadFile(fetchHeadPath)
	if err != nil {
		t.Fatal(err)
	}
	refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/remotes", "refs/tags")
	state := readState(t, repo.dir)

	other := cloneConfiguredRepo(t, remote, "main")
	newMain := commitFile(t, other, "base.txt", "remote update\n", "Remote update")
	runGit(t, other, "tag", "remote-tag")
	runGit(t, other, "push", "origin", "main", "refs/tags/remote-tag")

	// A plain fetch would move a local branch, update origin/main and alter tags.
	runGit(t, repo.dir, "config", "--add", "remote.origin.fetch", "+refs/heads/main:refs/heads/victim")
	runGit(t, repo.dir, "config", "remote.origin.prune", "true")
	runGit(t, repo.dir, "config", "remote.origin.pruneTags", "true")
	runGit(t, repo.dir, "config", "remote.origin.tagOpt", "--tags")
	expectGrapheneOK(t, repo, "sync", "--dry-run")

	if got := runGit(t, repo.dir, "rev-parse", "refs/graphene/fetch/main"); got != newMain {
		t.Fatalf("fetched commit = %s, want %s", got, newMain)
	}
	if got := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/remotes", "refs/tags"); got != refs {
		t.Fatalf("fetch moved refs outside its private cache:\nbefore:\n%s\nafter:\n%s", refs, got)
	}
	after, err := os.ReadFile(fetchHeadPath)
	if err != nil || !bytes.Equal(after, fetchHead) {
		t.Fatalf("fetch changed FETCH_HEAD: %v", err)
	}
	if got := runGit(t, repo.dir, "status", "--porcelain"); got != "" {
		t.Fatalf("dry run changed the worktree: %s", got)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, state) {
		t.Fatalf("dry run changed stack state: %#v", got)
	}
}

func TestSyncFetchPrefixLeavesOtherStackUntouched(t *testing.T) {
	for _, prefix := range []string{"stack", "/work/team/", ""} {
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()
			repo, remote := newTestRepoWithOrigin(t)
			runGit(t, repo.dir, "config", "graphene.branchPrefix", prefix)
			createStackBranch(t, repo, "one.txt", "one\n", "One")
			one := currentBranch(t, repo.dir)
			runGit(t, repo.dir, "push", "-u", "origin", one)
			runGit(t, repo.dir, "checkout", "main")
			createStackBranch(t, repo, "two.txt", "two\n", "Two")
			two := currentBranch(t, repo.dir)
			runGit(t, repo.dir, "push", "-u", "origin", two)
			twoHead := runGit(t, repo.dir, "rev-parse", two)
			before := readState(t, repo.dir)
			tracking := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes")

			other := cloneConfiguredRepo(t, remote, "main")
			runGit(t, other, "checkout", "-b", "unrelated")
			unrelated := commitFile(t, other, "unrelated.txt", "unrelated\n", "Unrelated")
			runGit(t, other, "push", "origin", "unrelated")
			runGit(t, other, "checkout", two)
			commitFile(t, other, "two.txt", "changed remotely\n", "Update other stack")
			runGit(t, other, "push", "origin", two)
			runGit(t, other, "checkout", "main")
			base := commitFile(t, other, "base.txt", "base update\n", "Base update")
			runGit(t, other, "push", "origin", "main")

			runGit(t, repo.dir, "checkout", one)
			expectGrapheneOK(t, repo, "sync")
			if got := runGit(t, repo.dir, "rev-parse", one+"^"); got != base {
				t.Fatalf("synced branch parent = %s, want %s", got, base)
			}
			if got := runGit(t, repo.dir, "rev-parse", two); got != twoHead {
				t.Fatalf("other stack moved from %s to %s", twoHead, got)
			}
			if after := readState(t, repo.dir); !reflect.DeepEqual(after.Stacks, before.Stacks) {
				t.Fatalf("sync changed stack metadata: before %#v, after %#v", before.Stacks, after.Stacks)
			}
			if got := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes"); got != tracking {
				t.Fatalf("sync changed remote-tracking refs: %s", got)
			}
			git := Git{Dir: repo.dir}
			err := git.OutputErr("cat-file", "-e", unrelated)
			if (err == nil) != (prefix == "") {
				t.Fatalf("unrelated object present = %t, want %t", err == nil, prefix == "")
			}
		})
	}
}

func TestSyncChecksUpstreamsOutsidePrefix(t *testing.T) {
	for _, namespace := range []string{"refs/heads/old/", "refs/changes/"} {
		t.Run(namespace, func(t *testing.T) {
			t.Parallel()
			repo, remote := newTestRepoWithOrigin(t)
			for _, name := range []string{"one", "two", "three"} {
				createStackBranch(t, repo, name+".txt", name+"\n", name)
				runGit(t, repo.dir, "push", "origin", "HEAD:"+namespace+name)
				runGit(t, repo.dir, "config", "branch.stack/"+name+".remote", "origin")
				runGit(t, repo.dir, "config", "branch.stack/"+name+".merge", namespace+name)
			}
			expectGrapheneOK(t, repo, "sync", "--dry-run")
			runGit(t, remote, "update-ref", "-d", namespace+"one")
			runGit(t, remote, "update-ref", "-d", namespace+"two")
			if code, _, stderr := repo.runGraphene(t, "sync"); code == 0 || !strings.Contains(stderr, "configured upstreams no longer exist") {
				t.Fatalf("sync with missing upstreams exited %d: %s", code, stderr)
			}
			expectGrapheneOK(t, repo, "sync", "--assume-merged")
			if refExists(t, repo.dir, "refs/heads/stack/one") || refExists(t, repo.dir, "refs/heads/stack/two") || !refExists(t, repo.dir, "refs/heads/stack/three") {
				t.Fatal("sync did not preserve the live upstream boundary")
			}
		})
	}
}
