package graphene

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRecoveryLeavesNestedCheckoutsAlone(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	module := addTestSubmodule(t, repo)
	originalModule := runGit(t, module, "rev-parse", "HEAD")
	nextModule := commitFile(t, module, "file.txt", "updated module\n", "Update module")
	runGit(t, module, "checkout", "--detach", originalModule)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "file.txt", "two\n", "Two")
	runGit(t, repo.dir, "switch", "-c", "target", "main")
	runGit(t, repo.dir, "update-index", "--cacheinfo", "160000,"+nextModule+",module")
	writeFile(t, repo.dir, "file.txt", "base update\n")
	runGit(t, repo.dir, "add", "file.txt")
	runGit(t, repo.dir, "commit", "-m", "Update base")
	runGit(t, repo.dir, "switch", "stack/one")
	nested := filepath.Join(repo.dir, "scratch", "rpc")
	runGit(t, repo.dir, "worktree", "add", "--detach", nested, "main")
	writeFile(t, nested, "file.txt", "nested staged\n")
	runGit(t, nested, "add", "file.txt")
	writeFile(t, nested, "file.txt", "nested unstaged\n")
	runGit(t, module, "checkout", "--detach", nextModule)
	commitFile(t, module, "local", "local\n", "Local module commit")
	writeFile(t, module, "file.txt", "module staged\n")
	runGit(t, module, "add", "file.txt")
	writeFile(t, module, "file.txt", "module unstaged\n")
	runGit(t, repo.dir, "config", "submodule.recurse", "true")
	beforeState := readState(t, repo.dir)
	beforeRefs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	code, _, stderr := repo.runGraphene(t, "restack", "target")
	pending := readState(t, repo.dir).Pending
	if code == 0 || pending == nil || pending.Recovery.Phase != recoveryConflict {
		t.Fatalf("expected conflict, got %d: %s", code, stderr)
	}
	assertBranchParent(t, repo.dir, "stack/one", "target")
	writeFile(t, module, "during-conflict", "new module work\n")
	writeFile(t, nested, "during-conflict", "new nested work\n")
	beforeModule, beforeNested := nestedState(t, module), nestedState(t, nested)
	expectGrapheneOK(t, repo, "abort")
	if nestedState(t, module) != beforeModule || nestedState(t, nested) != beforeNested {
		t.Fatal("abort changed nested checkout, staging, or edits")
	}
	if !reflect.DeepEqual(readState(t, repo.dir), beforeState) || runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads") != beforeRefs {
		t.Fatal("abort did not restore refs and stack metadata")
	}
	if got := runGit(t, repo.dir, "rev-parse", "HEAD:module"); got != originalModule {
		t.Fatalf("recorded gitlink = %s, want %s", got, originalModule)
	}
	if readState(t, repo.dir).Pending != nil {
		t.Fatal("abort left pending recovery")
	}
}

func TestRestackRejectsNestedRepositoryCollisionBeforeMutation(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"file", "descendant", "gitlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			createStackBranch(t, repo, "one.txt", "one\n", "One")
			runGit(t, repo.dir, "switch", "-c", "target", "main")
			switch kind {
			case "file":
				commitFile(t, repo.dir, "rpc", "target\n", "target")
			case "descendant":
				commitFile(t, repo.dir, "rpc/new", "target\n", "target")
			case "gitlink":
				head := runGit(t, repo.dir, "rev-parse", "HEAD")
				runGit(t, repo.dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",rpc")
				runGit(t, repo.dir, "commit", "-m", "target")
			}
			runGit(t, repo.dir, "switch", "stack/one")
			nested := filepath.Join(repo.dir, "rpc")
			runGit(t, repo.dir, "worktree", "add", "--detach", nested, "main")
			writeFile(t, repo.dir, ".git/info/exclude", "rpc/\n")
			beforeRefs := runGit(t, repo.dir, "show-ref")
			beforeState := readState(t, repo.dir)
			if code, _, stderr := repo.runGraphene(t, "restack", "target"); code == 0 || !strings.Contains(stderr, `nested repository "rpc"`) {
				t.Fatalf("collision returned %d: %s", code, stderr)
			}
			if runGit(t, repo.dir, "show-ref") != beforeRefs || !reflect.DeepEqual(readState(t, repo.dir), beforeState) {
				t.Fatal("collision moved refs or persisted an operation")
			}
		})
	}
}

func TestAbortProtectsRepositoryCreatedDuringConflict(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	commitFile(t, repo.dir, "rpc/original", "original\n", "original path")
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "file.txt", "child\n", "Two")
	runGit(t, repo.dir, "switch", "-c", "target", "main")
	runGit(t, repo.dir, "rm", "rpc/original")
	commitFile(t, repo.dir, "file.txt", "target\n", "target")
	runGit(t, repo.dir, "switch", "stack/one")
	if code, _, stderr := repo.runGraphene(t, "restack", "target"); code == 0 || readState(t, repo.dir).Pending.Recovery.Phase != recoveryConflict {
		t.Fatalf("expected conflict, got %d: %s", code, stderr)
	}
	nested := filepath.Join(repo.dir, "rpc")
	runGit(t, repo.dir, "worktree", "add", "--detach", nested, "target")
	writeFile(t, nested, "precious", "keep\n")
	before := nestedState(t, nested)
	beforeRefs := runGit(t, repo.dir, "show-ref")
	beforeState := readState(t, repo.dir)
	if code, _, stderr := repo.runGraphene(t, "abort"); code == 0 || !strings.Contains(stderr, `nested repository "rpc"`) {
		t.Fatalf("abort returned %d: %s", code, stderr)
	}
	if active, err := (Git{Dir: repo.dir}).RebaseInProgress(); err != nil || !active {
		t.Fatal("refused abort terminated the rebase")
	}
	if runGit(t, repo.dir, "show-ref") != beforeRefs || !reflect.DeepEqual(readState(t, repo.dir), beforeState) || nestedState(t, nested) != before {
		t.Fatal("refused abort changed refs, state, or nested files")
	}
	runGit(t, repo.dir, "worktree", "move", nested, filepath.Join(t.TempDir(), "moved"))
	expectGrapheneOK(t, repo, "abort")
	if _, err := os.Stat(filepath.Join(repo.dir, "rpc/original")); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverySubmoduleAdditionAndRemoval(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"add", "remove"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			var repo testRepo
			var remote string
			if change == "remove" {
				repo, remote = newTestRepoWithOrigin(t)
			} else {
				repo = newTestRepo(t)
			}
			var module, before string
			if change == "remove" {
				module = addTestSubmodule(t, repo)
				before = nestedState(t, module)
				runGit(t, repo.dir, "push", "origin", "main")
			}
			createStackBranch(t, repo, "one.txt", "one\n", "One")
			baseDir := repo.dir
			args := []string{"restack", "target"}
			if change == "remove" {
				baseDir = cloneConfiguredRepo(t, remote, "main")
				args = []string{"sync"}
			} else {
				runGit(t, repo.dir, "switch", "-c", "target", "main")
			}
			var moduleHead string
			if change == "add" {
				source := newTestRepo(t)
				moduleHead = runGit(t, source.dir, "rev-parse", "HEAD")
				writeFile(t, baseDir, ".gitmodules", "[submodule \"module\"]\n\tpath = module\n\turl = "+source.dir+"\n")
				runGit(t, baseDir, "add", ".gitmodules")
				runGit(t, baseDir, "update-index", "--add", "--cacheinfo", "160000,"+moduleHead+",module")
			} else {
				runGit(t, baseDir, "rm", "--cached", "module")
			}
			runGit(t, baseDir, "commit", "-m", change+" submodule")
			if change == "remove" {
				runGit(t, baseDir, "push", "origin", "main")
			}
			runGit(t, repo.dir, "switch", "stack/one")
			runGit(t, repo.dir, "config", "submodule.recurse", "true")
			expectGrapheneOK(t, repo, args...)
			if change == "remove" {
				if nestedState(t, module) != before || runGit(t, repo.dir, "ls-files", "module") != "" {
					t.Fatal("submodule removal changed checkout or retained gitlink")
				}
			} else {
				if got := runGit(t, repo.dir, "rev-parse", "HEAD:module"); got != moduleHead {
					t.Fatalf("new gitlink = %s, want %s", got, moduleHead)
				}
				if _, err := os.Stat(filepath.Join(repo.dir, "module/.git")); !os.IsNotExist(err) {
					t.Fatalf("new submodule was initialized: %v", err)
				}
			}
		})
	}
}

func TestSyncFastForwardLeavesSubmoduleCheckoutAlone(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	module := addTestSubmodule(t, repo)
	runGit(t, repo.dir, "push", "origin", "main")
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "switch", "main")
	next := commitFile(t, module, "file.txt", "next\n", "next")
	actor := cloneConfiguredRepo(t, remote, "main")
	runGit(t, actor, "update-index", "--cacheinfo", "160000,"+next+",module")
	runGit(t, actor, "commit", "-m", "update submodule")
	runGit(t, actor, "push", "origin", "main")
	writeFile(t, module, "file.txt", "local edits\n")
	before := nestedState(t, module)
	runGit(t, repo.dir, "config", "submodule.recurse", "true")
	expectGrapheneOK(t, repo, "sync")
	if nestedState(t, module) != before {
		t.Fatal("fast-forward changed submodule checkout")
	}
	if got := runGit(t, repo.dir, "rev-parse", "main:module"); got != next {
		t.Fatalf("base gitlink = %s, want %s", got, next)
	}
}
