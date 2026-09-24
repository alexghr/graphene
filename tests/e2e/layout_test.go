package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2ELinkedWorktreeSubmoduleAndIgnoredTrackedPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mainWorktree := f.dir
	moduleRoot := filepath.Join(t.TempDir(), "module")
	f.gitAt(filepath.Dir(moduleRoot), "init", "-b", "main", moduleRoot)
	f.configure(moduleRoot)
	moduleFile := filepath.Join(moduleRoot, "module.txt")
	if err := os.WriteFile(moduleFile, []byte("module base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.gitAt(moduleRoot, "add", ".")
	f.gitAt(moduleRoot, "commit", "-m", "Module initial")
	f.git("-c", "protocol.file.allow=always", "submodule", "add", moduleRoot, "module")
	f.write(".gitignore", "logs/\n")
	f.write("logs/placeholder", "tracked placeholder\n")
	f.git("add", ".gitignore", "module")
	f.git("add", "-f", "logs/placeholder")
	f.git("commit", "-m", "Repository layout")
	f.git("push", "origin", "main")
	oldMain := f.oid("main")
	f.new("one")
	f.graph("send", "origin")
	f.git("switch", "main")
	linked := filepath.Join(t.TempDir(), "linked")
	f.git("worktree", "add", linked, "stack/one")
	f.dir = linked
	f.new("two")
	f.graph("send", "--stack", "origin")
	f.git("-c", "protocol.file.allow=always", "submodule", "update", "--init", "module")
	moduleDir := filepath.Join(linked, "module")
	moduleHead := f.gitAt(moduleDir, "rev-parse", "HEAD")
	f.gitAt(moduleDir, "config", "user.name", "Graphene E2E")
	f.gitAt(moduleDir, "config", "user.email", "graphene-e2e@example.test")
	if err := os.WriteFile(filepath.Join(moduleDir, "module.txt"), []byte("module dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.gitAt(moduleDir, "add", "module.txt")
	moduleIndex := f.gitAt(moduleDir, "ls-files", "--stage")
	f.write("logs/ignored.txt", "ignored logs stay\n")
	beforeMainStatus := f.gitAt(mainWorktree, "status", "--porcelain")
	actor := f.clone()
	newMain := f.actorCommit(actor, "advance.txt", "advance\n", "Advance main")
	f.gitAt(actor, "push", "origin", "main")
	f.graph("sync")
	expectSame(t, "linked current branch", f.branch(), "stack/two")
	expectSame(t, "main other checkout branch", f.gitAt(mainWorktree, "symbolic-ref", "--short", "HEAD"), "main")
	expectSame(t, "main other checkout ref", f.gitAt(mainWorktree, "rev-parse", "HEAD"), oldMain)
	expectSame(t, "main other checkout status", f.gitAt(mainWorktree, "status", "--porcelain"), beforeMainStatus)
	expectSame(t, "fetched base", f.oid("origin/main"), newMain)
	expectSame(t, "stack root parent", f.parent("stack/one"), newMain)
	f.assertParent("stack/two", "stack/one")
	f.assertFiles("stack/two", "one", "two")
	expectSame(t, "submodule HEAD", f.gitAt(moduleDir, "rev-parse", "HEAD"), moduleHead)
	expectSame(t, "submodule index", f.gitAt(moduleDir, "ls-files", "--stage"), moduleIndex)
	expectSame(t, "submodule staged content", f.gitAt(moduleDir, "show", ":module.txt"), "module dirty")
	expectSame(t, "submodule worktree content", f.read("module/module.txt"), "module dirty\n")
	expectSame(t, "tracked ignored placeholder", f.read("logs/placeholder"), "tracked placeholder\n")
	expectSame(t, "ignored log", f.read("logs/ignored.txt"), "ignored logs stay\n")
	if got := f.git("ls-files", "logs/placeholder"); got != "logs/placeholder" {
		t.Fatalf("tracked placeholder missing: %q", got)
	}
	if got := f.git("status", "--porcelain"); strings.Contains(got, "logs/placeholder") {
		t.Fatalf("clean tracked placeholder changed: %q", got)
	}
	f.cleanState(stack{"main", branches("one", "two")})
}
