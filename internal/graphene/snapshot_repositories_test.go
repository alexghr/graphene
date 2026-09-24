package graphene

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func addTestSubmodule(t *testing.T, repo testRepo) string {
	t.Helper()
	source := newTestRepo(t)
	runGit(t, repo.dir, "-c", "protocol.file.allow=always", "submodule", "add", source.dir, "module")
	runGit(t, repo.dir, "commit", "-m", "Add submodule")
	module := filepath.Join(repo.dir, "module")
	runGit(t, module, "config", "user.name", "Graphene Test")
	runGit(t, module, "config", "user.email", "graphene@example.test")
	runGit(t, module, "config", "commit.gpgsign", "false")
	return module
}

func nestedState(t *testing.T, dir string) string {
	t.Helper()
	return runGit(t, dir, "rev-parse", "HEAD") + "\n" +
		runGit(t, dir, "ls-files", "--stage", "-v") + "\n" +
		runGit(t, dir, "status", "--porcelain", "--untracked-files=all") + "\n" +
		runGit(t, dir, "diff", "--binary")
}

func TestSnapshotLeavesNestedRepositoriesAlone(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"worktree", "clone", "unborn"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			nested := filepath.Join(repo.dir, "scratch", "rpc[1]\nspace")
			switch kind {
			case "worktree":
				runGit(t, repo.dir, "worktree", "add", "--detach", nested)
			case "clone":
				runGit(t, repo.dir, "clone", repo.dir, nested)
			case "unborn":
				runGit(t, repo.dir, "init", nested)
			}
			writeFile(t, nested, "precious", "before\n")
			writeFile(t, repo.dir, "scratch/keep[1]\nspace", "parent untracked\n")
			writeFile(t, repo.dir, ".gitattributes", "scratch/rpc* filter=custom\n")
			id := captureTestSnapshot(t, filepath.Join(repo.dir, "scratch"), true)
			snapshot, err := (Git{Dir: repo.dir}).readSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			if got := runGit(t, repo.dir, "ls-tree", "-r", snapshot.WorktreeTree); strings.Contains(got, "160000") || strings.Contains(got, "rpc") {
				t.Fatalf("snapshot captured nested repo: %s", got)
			}
			writeFile(t, repo.dir, "file.txt", "changed\n")
			runGit(t, repo.dir, "add", "file.txt")
			runGit(t, repo.dir, "commit", "-m", "change parent")
			changed := runGit(t, repo.dir, "rev-parse", "HEAD")
			writeFile(t, nested, "precious", "after capture\n")
			writeFile(t, repo.dir, "scratch/keep[1]\nspace", "changed\n")
			if _, err := restoreTestSnapshot(repo.dir, id, map[string]string{"main": changed}); err != nil {
				t.Fatal(err)
			}
			for path, want := range map[string]string{
				filepath.Join(nested, "precious"):                 "after capture\n",
				filepath.Join(repo.dir, "scratch/keep[1]\nspace"): "parent untracked\n",
			} {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != want {
					t.Fatalf("%s = %q, error %v", path, data, err)
				}
			}
		})
	}
}

func TestSnapshotPreservesSubmoduleIndex(t *testing.T) {
	t.Parallel()
	for _, initialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "uninitialized", true: "dirty checkout"}[initialized], func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			module := addTestSubmodule(t, repo)
			staged := commitFile(t, module, "file.txt", "next\n", "next")
			runGit(t, repo.dir, "add", "module")
			commitFile(t, module, "file.txt", "later\n", "later")
			writeFile(t, module, "file.txt", "staged child edit\n")
			runGit(t, module, "add", "file.txt")
			writeFile(t, module, "file.txt", "unstaged child edit\n")
			if !initialized {
				runGit(t, repo.dir, "submodule", "deinit", "-f", "module")
			}
			beforeIndex := runGit(t, repo.dir, "ls-files", "--stage")
			id := captureTestSnapshot(t, repo.dir, true)
			snapshot, err := (Git{Dir: repo.dir}).readSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			if got := runGit(t, repo.dir, "ls-tree", snapshot.WorktreeTree, "module"); !strings.Contains(got, "160000 commit "+staged) {
				t.Fatalf("snapshot changed gitlink: %s", got)
			}
			runGit(t, repo.dir, "config", "submodule.recurse", "true")
			runGit(t, repo.dir, "commit", "-m", "change gitlink")
			changed := runGit(t, repo.dir, "rev-parse", "HEAD")
			var childState string
			if initialized {
				writeFile(t, module, "after-capture", "preserve\n")
				childState = nestedState(t, module)
			}
			if _, err := restoreTestSnapshot(repo.dir, id, map[string]string{"main": changed}); err != nil {
				t.Fatal(err)
			}
			if got := runGit(t, repo.dir, "ls-files", "--stage"); got != beforeIndex {
				t.Fatalf("restored index = %s, want %s", got, beforeIndex)
			}
			if initialized && nestedState(t, module) != childState {
				t.Fatal("rollback changed submodule checkout or edits")
			}
		})
	}
}

func TestNestedRepositoryRollbackCollision(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"rpc", "rpc/file", "scratch"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			commitFile(t, repo.dir, path, "parent\n", "parent file")
			id := captureTestSnapshot(t, repo.dir, true)
			runGit(t, repo.dir, "rm", path)
			runGit(t, repo.dir, "commit", "-m", "remove parent path")
			changed := runGit(t, repo.dir, "rev-parse", "HEAD")
			nestedPath := "rpc"
			if path == "scratch" {
				nestedPath = "scratch/rpc"
			}
			nested := filepath.Join(repo.dir, nestedPath)
			runGit(t, repo.dir, "worktree", "add", "--detach", nested)
			writeFile(t, nested, "precious", "keep\n")
			// Ignored repositories need the same protection as visible ones.
			writeFile(t, repo.dir, ".git/info/exclude", nestedPath+"/\n")
			before := nestedState(t, nested)
			if _, err := restoreTestSnapshot(repo.dir, id, map[string]string{"main": changed}); err == nil || !strings.Contains(err.Error(), "nested repository") {
				t.Fatalf("rollback error = %v", err)
			}
			if runGit(t, repo.dir, "rev-parse", "HEAD") != changed || nestedState(t, nested) != before {
				t.Fatal("refused rollback changed parent or nested repository")
			}
			runGit(t, repo.dir, "worktree", "move", nested, filepath.Join(t.TempDir(), "moved"))
			if _, err := restoreTestSnapshot(repo.dir, id, map[string]string{"main": changed}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParentTrackedChanges(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	module := addTestSubmodule(t, repo)
	commitFile(t, module, "file.txt", "local commit\n", "local commit")
	writeFile(t, module, "file.txt", "dirty\n")
	g := Git{Dir: repo.dir}
	if dirty, err := g.hasParentTrackedChanges(); err != nil || dirty {
		t.Fatalf("submodule checkout blocks parent: %v, %v", dirty, err)
	}
	runGit(t, repo.dir, "add", "module")
	runGit(t, repo.dir, "config", "diff.ignoreSubmodules", "all")
	if dirty, err := g.hasParentTrackedChanges(); err != nil || !dirty {
		t.Fatalf("staged gitlink was ignored: %v, %v", dirty, err)
	}
}

func TestSnapshotOnlyGitlinks(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "rm", "file.txt")
	runGit(t, repo.dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
	runGit(t, repo.dir, "commit", "-m", "only gitlink")
	id := captureTestSnapshot(t, repo.dir, true)
	snapshot, err := (Git{Dir: repo.dir}).readSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.IndexTree != snapshot.WorktreeTree {
		t.Fatal("snapshot with no eligible files changed the tree")
	}
}

func TestRollbackProtectsCurrentIndexPaths(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	id := captureTestSnapshot(t, repo.dir, true)
	changed := commitFile(t, repo.dir, "rpc/tracked", "keep\n", "add path")
	// The restore target lacks rpc, but reset would delete its tracked files.
	runGit(t, repo.dir, "init", filepath.Join(repo.dir, "rpc"))
	writeFile(t, repo.dir, "other/notes", "notes\n")
	g := Git{Dir: filepath.Join(repo.dir, "other")}
	if err := g.checkNestedRepositories("", "main^"); err == nil || !strings.Contains(err.Error(), `nested repository "rpc"`) {
		t.Fatalf("subdirectory preflight error = %v", err)
	}
	if _, err := restoreTestSnapshot(repo.dir, id, map[string]string{"main": changed}); err == nil || !strings.Contains(err.Error(), `nested repository "rpc"`) {
		t.Fatalf("rollback error = %v", err)
	}
	if got := runGit(t, repo.dir, "rev-parse", "HEAD"); got != changed {
		t.Fatal("refused rollback moved branch")
	}
	if data, err := os.ReadFile(filepath.Join(repo.dir, "rpc/tracked")); err != nil || string(data) != "keep\n" {
		t.Fatalf("nested file = %q, error %v", data, err)
	}
}
