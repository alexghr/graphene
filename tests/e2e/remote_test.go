package e2e

import (
	"reflect"
	"strings"
	"testing"
)

func TestE2ESquashMergedBottomAndRemoteRewrite(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, name := range []string{"one", "two", "three"} {
		f.new(name)
	}
	f.graph("send", "--stack", "origin")
	actor := f.clone()
	oldOne := f.gitAt(actor, "rev-parse", "origin/stack/one")
	f.gitAt(actor, "cherry-pick", "--no-commit", "origin/stack/one")
	f.gitAt(actor, "commit", "-m", "Squash bottom patch into main")
	f.gitAt(actor, "push", "origin", "main")
	f.gitAt(actor, "push", "origin", "--delete", "stack/one")
	f.gitAt(actor, "switch", "-c", "stack/two", "--track", "origin/stack/two")
	f.gitAt(actor, "rebase", "--onto", "main", oldOne)
	f.gitAt(actor, "switch", "-c", "stack/three", "--track", "origin/stack/three")
	f.gitAt(actor, "rebase", "--onto", "stack/two", "origin/stack/two")
	f.gitAt(actor, "push", "--force-with-lease", "origin", "stack/two", "stack/three")
	remoteTwo, remoteThree := f.gitAt(f.remote, "rev-parse", "stack/two"), f.gitAt(f.remote, "rev-parse", "stack/three")
	f.graph("sync")
	if _, err := f.command(f.dir, "git", "show-ref", "--verify", "--quiet", "refs/heads/stack/one"); err == nil {
		t.Fatal("merged bottom branch survived")
	}
	expectSame(t, "updated two upstream", f.oid("stack/two@{upstream}"), remoteTwo)
	expectSame(t, "updated three upstream", f.oid("stack/three@{upstream}"), remoteThree)
	f.assertParent("stack/two", "main")
	f.assertParent("stack/three", "stack/two")
	f.assertFiles("stack/three", "one", "two", "three")
	expectSame(t, "surviving patch count", f.git("rev-list", "--count", "main..stack/three"), "2")
	f.cleanState(stack{"main", branches("two", "three")})
	f.write("three.txt", "three amended\n")
	f.git("add", "three.txt")
	f.graph("amend", "--no-edit")
	before := f.remoteRefs()
	f.graph("sendf", "--dry-run", "origin")
	expectSame(t, "remote refs after dry run", f.remoteRefs(), before)
	f.graph("sendf", "origin")
	f.assertRemoteEquals("two", "three")
	expectSame(t, "remote patch count", f.gitAt(f.remote, "rev-list", "--count", "main..stack/three"), "2")
	for _, n := range []string{"one", "two"} {
		expectSame(t, "remote "+n, f.gitAt(f.remote, "show", "stack/three:"+n+".txt"), n)
	}
	expectSame(t, "remote amended three", f.gitAt(f.remote, "show", "stack/three:three.txt"), "three amended")
	f.cleanState(stack{"main", branches("two", "three")})
}

func TestE2EStaleLeaseProtectsCollaborator(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.new("one")
	f.new("two")
	f.graph("send", "--stack", "origin")
	actor := f.clone()
	f.actorCommit(actor, "advance.txt", "advance\n", "Advance main")
	f.gitAt(actor, "push", "origin", "main")
	f.graph("sync")
	f.write("two.txt", "two local amended\n")
	f.git("add", "two.txt")
	f.graph("amend", "--no-edit")
	f.gitAt(actor, "switch", "-c", "stack/two", "--track", "origin/stack/two")
	collaborator := f.actorCommit(actor, "collaborator.txt", "collaborator\n", "Collaborator change")
	f.gitAt(actor, "push", "origin", "stack/two")
	before := f.remoteRefs()
	f.reject("stale info", "sendf", "--dry-run", "origin")
	expectSame(t, "dry run remote refs", f.remoteRefs(), before)
	f.reject("stale info", "sendf", "origin")
	expectSame(t, "collaborator remote tip", f.gitAt(f.remote, "rev-parse", "stack/two"), collaborator)
	expectSame(t, "collaborator remote content", f.gitAt(f.remote, "show", "stack/two:collaborator.txt"), "collaborator")
	if f.oid("stack/two") == collaborator {
		t.Fatal("local branch unexpectedly adopted collaborator commit")
	}
	if f.state().Pending != nil {
		t.Fatal("rejected push left a pending operation")
	}
}

func TestE2EDeletedUnmergedBranchRequiresAssumption(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, n := range []string{"one", "two", "three"} {
		f.new(n)
	}
	f.graph("send", "--stack", "origin")
	f.git("switch", "main")
	f.new("other")
	f.graph("send", "origin")
	other := f.oid("stack/other")
	f.git("switch", "stack/three")
	actor := f.clone()
	f.gitAt(actor, "switch", "-c", "stack/one", "--track", "origin/stack/one")
	remoteChanged := f.actorCommit(actor, "one.txt", "one\nremote edit before deletion\n", "Remote branch changed before deletion")
	f.gitAt(actor, "push", "origin", "stack/one")
	f.git("fetch", "origin")
	expectSame(t, "fetched collaborator branch", f.oid("origin/stack/one"), remoteChanged)
	f.gitAt(actor, "push", "origin", "--delete", "stack/one")
	f.gitAt(actor, "switch", "main")
	f.actorCommit(actor, "advance.txt", "advance\n", "Advance main")
	f.gitAt(actor, "push", "origin", "main")
	refsBefore := f.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	stateBefore := f.state()
	statusBefore := f.git("status", "--porcelain")
	f.reject("--assume-merged", "sync")
	expectSame(t, "refused sync refs", f.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"), refsBefore)
	expectSame(t, "refused sync status", f.git("status", "--porcelain"), statusBefore)
	if got := f.state(); !reflect.DeepEqual(got, stateBefore) {
		t.Fatalf("refused sync state = %+v", got)
	}
	preview := f.graph("sync", "--dry-run", "--assume-merged")
	if !strings.Contains(preview, "stack/one") || !strings.Contains(preview, "delete branches assumed merged") {
		t.Fatalf("assume-merged preview = %q", preview)
	}
	expectSame(t, "preview refs", f.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"), refsBefore)
	if got := f.state(); !reflect.DeepEqual(got, stateBefore) {
		t.Fatalf("preview changed state = %+v", got)
	}
	f.graph("sync", "--assume-merged")
	if _, err := f.command(f.dir, "git", "show-ref", "--verify", "--quiet", "refs/heads/stack/one"); err == nil {
		t.Fatal("approved deleted prefix survived")
	}
	f.assertParent("stack/two", "main")
	f.assertParent("stack/three", "stack/two")
	f.assertFiles("stack/three", "two", "three")
	expectSame(t, "unrelated stack OID", f.oid("stack/other"), other)
	f.cleanState(stack{"main", branches("two", "three")}, stack{"main", branches("other")})
}
