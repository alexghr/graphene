package e2e

import (
	"reflect"
	"strings"
	"testing"
)

var eight = []string{"one", "two", "three", "four", "five", "six", "seven", "eight"}

func TestE2EFullEightCommitLifecycle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, name := range eight {
		f.newDerived(name)
	}
	f.graph("send", "origin")
	f.assertRemoteEquals(eight...)
	for _, name := range eight {
		b := "stack/" + name
		expectSame(t, b+" upstream remote", f.git("config", "--get", "branch."+b+".remote"), "origin")
		expectSame(t, b+" upstream merge", f.git("config", "--get", "branch."+b+".merge"), "refs/heads/"+b)
	}
	f.cleanState(stack{"main", branches(eight...)})
	oldTip := f.oid("stack/eight")
	oldMessage := f.git("log", "-1", "--format=%B", "stack/eight")
	f.write("eight.txt", "eight amended\n")
	f.git("add", "eight.txt")
	f.graph("amend", "--no-edit")
	if f.oid("stack/eight") == oldTip {
		t.Fatal("amend left tip unchanged")
	}
	expectSame(t, "amend --no-edit message", f.git("log", "-1", "--format=%B", "stack/eight"), oldMessage)
	f.reject("non-fast-forward", "send", "origin")
	expectSame(t, "remote tip before force", f.gitAt(f.remote, "rev-parse", "stack/eight"), oldTip)
	f.graph("sendf", "origin")
	f.assertRemoteEquals(eight...)
	f.cleanState(stack{"main", branches(eight...)})

	actor := f.clone()
	f.gitAt(actor, "merge", "--ff-only", "origin/stack/four")
	f.gitAt(actor, "push", "origin", "main")
	f.graph("sync")
	expectSame(t, "current branch after prefix sync", f.branch(), "stack/eight")
	for _, name := range eight[:4] {
		if _, err := f.command(f.dir, "git", "show-ref", "--verify", "--quiet", "refs/heads/stack/"+name); err == nil {
			t.Fatalf("merged branch stack/%s survived", name)
		}
	}
	f.assertParent("stack/five", "main")
	for i := 5; i < 8; i++ {
		f.assertParent("stack/"+eight[i], "stack/"+eight[i-1])
	}
	f.assertFiles("stack/eight", eight[:7]...)
	expectSame(t, "amended content", f.git("show", "stack/eight:eight.txt"), "eight amended")
	f.cleanState(stack{"main", branches(eight[4:]...)})
	f.graph("sendf", "--stack", "origin")
	f.assertRemoteEquals(eight[4:]...)
	f.gitAt(actor, "fetch", "origin")
	f.gitAt(actor, "merge", "--ff-only", "origin/stack/eight")
	f.gitAt(actor, "push", "origin", "main")
	f.graph("sync")
	expectSame(t, "current branch after final sync", f.branch(), "main")
	f.cleanState()
	expectSame(t, "remaining local heads", f.git("for-each-ref", "--format=%(refname)", "refs/heads"), "refs/heads/main")
	expectSame(t, "main after final sync", f.oid("main"), f.gitAt(f.remote, "rev-parse", "main"))
	expectSame(t, "final content", f.git("show", "main:eight.txt"), "eight amended")
}

func TestE2EAmendMiddleForksAndPushScope(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, name := range eight {
		f.new(name)
	}
	f.git("switch", "stack/four")
	f.new("fork")
	f.git("switch", "main")
	f.new("other")
	f.git("switch", "stack/eight")
	f.graph("send", "--stack", "origin")
	f.git("switch", "stack/fork")
	f.graph("send", "origin")
	f.git("switch", "stack/other")
	f.graph("send", "origin")
	unchanged := map[string]string{}
	for _, b := range []string{"main", "stack/one", "stack/two", "stack/three", "stack/other"} {
		unchanged[b] = f.oid(b)
	}
	oldFive, oldFork, oldEight := f.oid("stack/five"), f.oid("stack/fork"), f.oid("stack/eight")
	f.git("switch", "stack/four")
	f.write("four.txt", "four amended\n")
	f.git("add", "four.txt")
	f.graph("amend", "--no-edit")
	for b, oid := range unchanged {
		expectSame(t, b+" OID", f.oid(b), oid)
	}
	if f.oid("stack/five") == oldFive || f.oid("stack/fork") == oldFork || f.oid("stack/eight") == oldEight {
		t.Fatal("amend did not rewrite every dependent path")
	}
	f.assertParent("stack/four", "stack/three")
	f.assertParent("stack/five", "stack/four")
	f.assertParent("stack/fork", "stack/four")
	for i := 5; i < 8; i++ {
		f.assertParent("stack/"+eight[i], "stack/"+eight[i-1])
	}
	f.assertFiles("stack/eight", "one", "two", "three", "five", "six", "seven", "eight")
	expectSame(t, "amended patch on deep tip", f.git("show", "stack/eight:four.txt"), "four amended")
	f.assertFiles("stack/fork", "one", "two", "three", "fork")
	expectSame(t, "amended patch on fork", f.git("show", "stack/fork:four.txt"), "four amended")
	f.graph("sendf", "origin")
	expectSame(t, "current branch pushed", f.gitAt(f.remote, "rev-parse", "stack/four"), f.oid("stack/four"))
	expectSame(t, "descendant not pushed", f.gitAt(f.remote, "rev-parse", "stack/five"), oldFive)
	expectSame(t, "fork not pushed", f.gitAt(f.remote, "rev-parse", "stack/fork"), oldFork)
	f.graph("sendf", "--stack", "origin")
	f.assertRemoteEquals(append(eight, "fork")...)
	expectSame(t, "unrelated remote", f.gitAt(f.remote, "rev-parse", "stack/other"), unchanged["stack/other"])
	f.cleanState(stack{"main", branches(eight...)}, stack{"stack/four", branches("fork")}, stack{"main", branches("other")})
}

func TestE2ETipSyncAfterMainAdvances(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, name := range eight[:7] {
		f.new(name)
	}
	f.graph("send", "--stack", "origin")
	f.new("eight")
	f.git("switch", "main")
	f.new("other")
	f.new("otherchild")
	f.graph("send", "--stack", "origin")
	other, otherChild := f.oid("stack/other"), f.oid("stack/otherchild")
	f.git("switch", "stack/eight")
	actor := f.clone()
	base := f.actorCommit(actor, "advance.txt", "advance\n", "Advance main")
	f.gitAt(actor, "push", "origin", "main")
	f.graph("sync")
	expectSame(t, "main after sync", f.oid("main"), base)
	expectSame(t, "unrelated stack", f.oid("stack/other"), other)
	expectSame(t, "unrelated child", f.oid("stack/otherchild"), otherChild)
	f.assertParent("stack/one", "main")
	for i := 1; i < 8; i++ {
		f.assertParent("stack/"+eight[i], "stack/"+eight[i-1])
	}
	for _, name := range eight {
		f.assertPatchFile("stack/"+name, name)
	}
	f.assertFiles("stack/eight", eight...)
	expectSame(t, "base content", f.git("show", "stack/eight:advance.txt"), "advance")
	remoteBeforeDryRun := f.remoteRefs()
	f.graph("sendf", "--stack", "--dry-run", "origin")
	expectSame(t, "remote refs after dry run", f.remoteRefs(), remoteBeforeDryRun)
	f.graph("sendf", "--stack", "origin")
	f.assertRemoteEquals(eight...)
	expectSame(t, "unrelated remote", f.gitAt(f.remote, "rev-parse", "stack/other"), other)
	expectSame(t, "unrelated child remote", f.gitAt(f.remote, "rev-parse", "stack/otherchild"), otherChild)
	refs, before := f.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"), f.state()
	f.graph("sync")
	expectSame(t, "repeat sync refs", f.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"), refs)
	if got := f.state(); !reflect.DeepEqual(got, before) {
		t.Fatalf("repeat sync state = %+v, before %+v", got, before)
	}
	if got := f.git("status", "--porcelain"); got != "" {
		t.Fatalf("worktree after sync = %q", got)
	}
	f.cleanState(stack{"main", branches(eight...)}, stack{"main", branches("other", "otherchild")})
	if got := f.git("rev-list", "--count", "main..stack/eight"); strings.TrimSpace(got) != "8" {
		t.Fatalf("surviving patch count = %q", got)
	}
	firstPath := map[string]string{}
	for _, name := range eight {
		firstPath[name] = f.oid("stack/" + name)
	}
	f.git("switch", "stack/otherchild")
	f.graph("sync")
	f.assertParent("stack/other", "main")
	f.assertParent("stack/otherchild", "stack/other")
	f.assertPatchFile("stack/other", "other")
	f.assertPatchFile("stack/otherchild", "otherchild")
	f.assertFiles("stack/otherchild", "other", "otherchild")
	expectSame(t, "other stack base content", f.git("show", "stack/otherchild:advance.txt"), "advance")
	for _, name := range eight {
		expectSame(t, "first stack unchanged after second sync", f.oid("stack/"+name), firstPath[name])
	}
	f.cleanState(stack{"main", branches(eight...)}, stack{"main", branches("other", "otherchild")})
}
