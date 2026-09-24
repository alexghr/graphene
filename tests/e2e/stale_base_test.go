package e2e

import (
	"path/filepath"
	"testing"
)

func TestE2EReuseCurrentWithStaleBase(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mainDir := f.dir
	oldMain := f.oid("main")
	f.new("older")
	older := f.oid("stack/older")
	f.git("switch", "main")
	f.git("branch", "target")
	actor := f.clone()
	newMain := f.actorCommit(actor, "advance.txt", "advance\n", "Advance main")
	f.gitAt(actor, "push", "origin", "main")
	f.git("fetch", "origin")
	linked := filepath.Join(t.TempDir(), "linked")
	f.git("worktree", "add", "--no-track", "-b", "feature", linked, "origin/main")
	f.dir = linked
	f.write("feature.txt", "feature\n")
	f.git("add", "feature.txt")
	f.graph("new", "--reuse-current", "--base", "main", "-m", "Feature")
	expectSame(t, "new commit parent", f.parent("feature"), newMain)
	f.assertPatchFile("feature", "feature")
	f.assertFiles("feature", "feature", "advance")
	f.new("child")
	f.graph("send", "origin")
	newMain = f.actorCommit(actor, "later.txt", "later\n", "Advance main again")
	f.gitAt(actor, "push", "origin", "main")
	f.graph("sync")
	expectSame(t, "synced root parent", f.parent("feature"), newMain)
	f.assertParent("stack/child", "feature")
	f.assertFiles("stack/child", "feature", "child", "advance", "later")
	f.graph("sendf", "origin")
	f.cleanState(stack{"main", branches("older")}, stack{"main", []string{"feature", "stack/child"}})
	f.git("switch", "feature")
	f.graph("restack", "--fetch", "target")
	f.assertParent("feature", "target")
	f.assertParent("stack/child", "feature")
	f.assertPatchFile("feature", "feature")
	f.assertPatchFile("stack/child", "child")
	f.assertFiles("stack/child", "feature", "child")
	expectSame(t, "upstream changes not replayed", f.git("ls-tree", "--name-only", "feature", "advance.txt", "later.txt"), "")
	f.graph("sendf", "--stack", "origin")
	for _, branch := range []string{"feature", "stack/child"} {
		expectSame(t, "remote "+branch, f.gitAt(f.remote, "rev-parse", "refs/heads/"+branch), f.oid(branch))
	}
	f.cleanState(stack{"main", branches("older")}, stack{"target", []string{"feature", "stack/child"}})
	expectSame(t, "older stack", f.oid("stack/older"), older)
	expectSame(t, "older stack parent", f.parent("stack/older"), oldMain)
	expectSame(t, "main worktree HEAD", f.gitAt(mainDir, "rev-parse", "HEAD"), oldMain)
	expectSame(t, "main worktree branch", f.gitAt(mainDir, "symbolic-ref", "--short", "HEAD"), "main")
	expectSame(t, "main worktree status", f.gitAt(mainDir, "status", "--porcelain"), "")
	expectSame(t, "current branch", f.branch(), "feature")
}
