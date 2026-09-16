package graphene

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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
