package e2e

import (
	"reflect"
	"strings"
	"testing"
)

func TestE2EConflictRecovery(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"amend", "sync", "restack"} {
		for _, action := range []string{"continue", "abort"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				t.Parallel()
				f := newFixture(t)
				f.new("one")
				f.new("two")
				f.write("file.txt", "child\n")
				f.git("add", "file.txt")
				f.graph("new", "--branch", "stack/three", "-m", "three")
				f.graph("send", "--stack", "origin")
				f.git("branch", "bookmark", "main")
				bookmark := f.oid("bookmark")
				f.write("notes.txt", "unrelated notes\n")
				var conflictFile, resolved string
				switch kind {
				case "amend":
					f.git("switch", "stack/one")
					f.write("file.txt", "amended\n")
					f.git("add", "file.txt")
					conflictFile, resolved = "file.txt", "amended\nchild\n"
				case "sync":
					actor := f.clone()
					f.gitAt(actor, "merge", "--ff-only", "origin/stack/one")
					f.actorCommit(actor, "file.txt", "remote\n", "Conflicting base")
					f.gitAt(actor, "push", "origin", "main")
					conflictFile, resolved = "file.txt", "remote\nchild\n"
				case "restack":
					f.git("switch", "-c", "target", "main")
					f.actorCommit(f.dir, "file.txt", "target\n", "Conflicting target")
					f.git("switch", "stack/one")
					conflictFile, resolved = "file.txt", "target\nchild\n"
				}
				beforeRefs := f.git("for-each-ref", "--sort=refname", "--format=%(refname) %(objectname)", "refs/heads")
				beforeState := f.state()
				beforeBranch := f.branch()
				beforeStatus := f.git("status", "--porcelain")
				beforeStaged := f.git("diff", "--cached", "--binary")
				oldOne := f.oid("stack/one")
				oldTwo := f.oid("stack/two")
				oldThree := f.oid("stack/three")
				conflictOutput := f.reject("CONFLICT", kindArgs(kind)...)
				amendedOne := f.oid("stack/one")
				if f.state().Pending == nil {
					t.Fatalf("%s conflict has no persisted pending operation", kind)
				}
				if kind == "amend" {
					// Amend rebases a whole suffix before updating its intermediate refs.
					if f.oid("HEAD") == oldTwo || f.git("show", "HEAD:two.txt") != "two" {
						t.Fatalf("amend did not complete the second commit before conflict; output:\n%s", conflictOutput)
					}
				} else if f.oid("stack/two") == oldTwo {
					t.Fatalf("%s did not complete a descendant rewrite before conflict; output:\n%s\nstate: %+v\nrefs:\n%s", kind, conflictOutput, f.state(), f.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"))
				}
				expectSame(t, "unrelated bookmark during conflict", f.oid("bookmark"), bookmark)
				if action == "continue" {
					f.write(conflictFile, resolved)
					f.git("add", conflictFile)
					f.graph("continue")
					expectSame(t, "checkout after continue", f.branch(), beforeBranch)
					switch kind {
					case "amend":
						f.cleanState(stack{"main", branches("one", "two", "three")})
					case "sync":
						f.cleanState(stack{"main", branches("two", "three")})
					case "restack":
						f.cleanState(stack{"target", branches("one", "two", "three")})
					}
					f.assertParent("stack/three", "stack/two")
					f.assertFiles("stack/three", "two")
					expectSame(t, "resolved content", f.git("show", "stack/three:file.txt"), strings.TrimSpace(resolved))
					if kind == "sync" {
						f.assertParent("stack/two", "main")
						if _, err := f.command(f.dir, "git", "show-ref", "--verify", "--quiet", "refs/heads/stack/one"); err == nil {
							t.Fatal("sync continue retained merged branch")
						}
					} else if kind == "restack" {
						f.assertParent("stack/one", "target")
						f.assertParent("stack/two", "stack/one")
					} else {
						f.assertParent("stack/two", "stack/one")
					}
					f.git("switch", "stack/three")
					f.graph("sendf", "--stack", "origin")
					f.assertRemoteEquals("two", "three")
				} else {
					f.graph("abort")
					if kind == "amend" {
						// Amend commits the staged edit before its legacy suffix rebase.
						// Abort cancels that rebase but retains the amended current commit.
						if amendedOne == oldOne {
							t.Fatal("amend did not write the current commit")
						}
						expectSame(t, "amended commit after abort", f.oid("stack/one"), amendedOne)
						expectSame(t, "original second branch after abort", f.oid("stack/two"), oldTwo)
						expectSame(t, "original third branch after abort", f.oid("stack/three"), oldThree)
						expectSame(t, "amended file after abort", f.git("show", "stack/one:file.txt"), "amended")
						expectSame(t, "staged edit consumed by amend", f.git("diff", "--cached", "--binary"), "")
						expectSame(t, "untracked notes after amend abort", f.git("status", "--porcelain"), "?? notes.txt")
					} else {
						expectSame(t, "abort refs", f.git("for-each-ref", "--sort=refname", "--format=%(refname) %(objectname)", "refs/heads"), beforeRefs)
						expectSame(t, "abort status", f.git("status", "--porcelain"), beforeStatus)
						expectSame(t, "abort staged patch", f.git("diff", "--cached", "--binary"), beforeStaged)
					}
					if kind == "amend" {
						expectSame(t, "amend abort checkout", f.branch(), "stack/three")
					} else {
						expectSame(t, "abort checkout", f.branch(), beforeBranch)
					}
					if got := f.state(); !reflect.DeepEqual(got, beforeState) {
						t.Fatalf("abort state = %+v, want %+v", got, beforeState)
					}
				}
				expectSame(t, "unrelated notes", f.read("notes.txt"), "unrelated notes\n")
				expectSame(t, "unrelated bookmark", f.oid("bookmark"), bookmark)
			})
		}
	}
}

func kindArgs(kind string) []string {
	switch kind {
	case "amend":
		return []string{"amend", "--no-edit"}
	case "sync":
		return []string{"sync"}
	default:
		return []string{"restack", "target"}
	}
}
