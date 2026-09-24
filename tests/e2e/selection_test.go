package e2e

import (
	"reflect"
	"strings"
	"testing"
)

func TestE2ESyncAllFromMain(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, name := range eight {
		f.new(name)
	}
	f.git("switch", "stack/four")
	f.new("nested")
	f.git("switch", "main")
	for _, name := range []string{"alpha", "beta", "gamma"} {
		f.new(name)
	}
	f.git("switch", "main")
	f.new("merged")
	f.graph("send", "origin")
	f.git("switch", "main")
	f.git("branch", "release")
	f.git("switch", "release")
	f.new("releaseonly")
	release, releaseTip := f.oid("release"), f.oid("stack/releaseonly")
	f.git("switch", "main")
	actor := f.clone()
	f.gitAt(actor, "merge", "--ff-only", "origin/stack/merged")
	base := f.actorCommit(actor, "advance.txt", "advance\n", "Advance main")
	f.gitAt(actor, "push", "origin", "main")
	refsBefore := f.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	statusBefore := f.git("status", "--porcelain")
	stateBefore := f.state()
	f.graph("sync", "--all", "--dry-run")
	expectSame(t, "dry-run local refs", f.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"), refsBefore)
	expectSame(t, "dry-run status", f.git("status", "--porcelain"), statusBefore)
	if got := f.state(); !reflect.DeepEqual(got, stateBefore) {
		t.Fatalf("dry-run changed state: %+v", got)
	}
	f.graph("sync", "--all")
	expectSame(t, "current after sync all", f.branch(), "main")
	expectSame(t, "advanced main", f.oid("main"), base)
	expectSame(t, "other base", f.oid("release"), release)
	expectSame(t, "other base stack", f.oid("stack/releaseonly"), releaseTip)
	if _, err := f.command(f.dir, "git", "show-ref", "--verify", "--quiet", "refs/heads/stack/merged"); err == nil {
		t.Fatal("merged stack survived sync all")
	}
	f.assertParent("stack/one", "main")
	for i := 1; i < 8; i++ {
		f.assertParent("stack/"+eight[i], "stack/"+eight[i-1])
	}
	f.assertParent("stack/nested", "stack/four")
	f.assertParent("stack/alpha", "main")
	f.assertParent("stack/beta", "stack/alpha")
	f.assertParent("stack/gamma", "stack/beta")
	for _, name := range eight {
		f.assertPatchFile("stack/"+name, name)
	}
	for _, name := range []string{"nested", "alpha", "beta", "gamma"} {
		f.assertPatchFile("stack/"+name, name)
	}
	f.assertFiles("stack/eight", eight...)
	f.assertFiles("stack/nested", "one", "two", "three", "four", "nested")
	f.assertFiles("stack/gamma", "alpha", "beta", "gamma")
	expectSame(t, "base content on deep stack", f.git("show", "stack/eight:advance.txt"), "advance")
	expectSame(t, "base content on second stack", f.git("show", "stack/gamma:advance.txt"), "advance")
	f.cleanState(stack{"main", branches(eight...)}, stack{"stack/four", branches("nested")}, stack{"main", branches("alpha", "beta", "gamma")}, stack{"release", branches("releaseonly")})
}

func TestE2ERestackFromMiddleAndPublish(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.git("branch", "target")
	for _, name := range eight {
		f.new(name)
	}
	f.git("switch", "stack/two")
	f.new("lowerfork")
	f.git("switch", "stack/six")
	f.new("upperfork")
	f.git("switch", "stack/eight")
	f.graph("send", "--stack", "origin")
	f.git("switch", "stack/upperfork")
	f.graph("send", "origin")
	f.git("switch", "stack/lowerfork")
	f.graph("send", "origin")
	stable := map[string]string{}
	for _, b := range []string{"main", "stack/one", "stack/two", "stack/three", "stack/lowerfork"} {
		stable[b] = f.oid(b)
	}
	oldFour, oldUpper := f.oid("stack/four"), f.oid("stack/upperfork")
	f.git("switch", "target")
	target := f.actorCommit(f.dir, "target.txt", "target\n", "Advance target")
	f.git("switch", "stack/four")
	f.graph("restack", "target")
	expectSame(t, "returned branch", f.branch(), "stack/four")
	for b, oid := range stable {
		expectSame(t, b+" unchanged", f.oid(b), oid)
	}
	if f.oid("stack/four") == oldFour || f.oid("stack/upperfork") == oldUpper {
		t.Fatal("restack left a dependent branch unchanged")
	}
	expectSame(t, "target OID", f.oid("target"), target)
	f.assertParent("stack/four", "target")
	for i := 4; i < 8; i++ {
		f.assertParent("stack/"+eight[i], "stack/"+eight[i-1])
	}
	f.assertParent("stack/upperfork", "stack/six")
	f.assertParent("stack/lowerfork", "stack/two")
	f.assertFiles("stack/eight", "four", "five", "six", "seven", "eight")
	f.assertFiles("stack/upperfork", "four", "five", "six", "upperfork")
	expectSame(t, "target content on deep stack", f.git("show", "stack/eight:target.txt"), "target")
	f.graph("sendf", "--stack", "origin")
	f.assertRemoteEquals("four", "five", "six", "seven", "eight", "upperfork")
	expectSame(t, "lower fork remote scope", f.gitAt(f.remote, "rev-parse", "stack/lowerfork"), stable["stack/lowerfork"])
	f.cleanState(
		stack{"main", branches("one", "two", "three")},
		stack{"stack/two", branches("lowerfork")},
		stack{"stack/six", branches("upperfork")},
		stack{"target", branches("four", "five", "six", "seven", "eight")},
	)
	if got := f.git("status", "--porcelain"); strings.TrimSpace(got) != "" {
		t.Fatalf("dirty after restack: %q", got)
	}
}
