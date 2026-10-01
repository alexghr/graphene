package graphene

import (
	"fmt"
	"strings"
	"testing"
)

func TestUnitOwnPatchDigest(t *testing.T) {
	t.Parallel()
	patch := "diff --git a/file b/file\nindex 1111111..2222222 100644\n--- a/file\n+++ b/file\n@@ -2 +2 @@\n-old\n+new value\n"
	shifted := strings.ReplaceAll(strings.ReplaceAll(patch, "1111111..2222222", "3333333..4444444"), "@@ -2 +2 @@", "@@ -20 +23 @@ section")
	if ownPatchDigest(patch) != ownPatchDigest(shifted) {
		t.Fatal("inherited blobs and shifted lines changed the own patch digest")
	}
	if ownPatchDigest(patch) == ownPatchDigest(strings.ReplaceAll(patch, "+new value", "+new  value")) {
		t.Fatal("whitespace changes were ignored")
	}
	if ownPatchDigest("diff --git a/file b/file\nGIT binary patch\nliteral 3\nabc\n") != "" {
		t.Fatal("binary delta should have unavailable comparison")
	}
	if ownPatchDigest("") == "" {
		t.Fatal("empty own patches must remain comparable")
	}
}

func TestAmendRewriteSummarySeparatesInheritedChanges(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "ancestor.txt", "before\n", "One")
	one := runGit(t, repo.dir, "rev-parse", "HEAD")
	createStackBranch(t, repo, "own.txt", "child\n", "Two")
	two := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "branch", "alias/two")
	runGit(t, repo.dir, "switch", "stack/one")
	writeFile(t, repo.dir, "ancestor.txt", "after\n")
	runGit(t, repo.dir, "add", ".")
	code, stdout, stderr := repo.runGraphene(t, "amend", "--no-edit")
	if code != 0 {
		t.Fatalf("amend exited %d: %s", code, stderr)
	}
	newOne := runGit(t, repo.dir, "rev-parse", "stack/one")
	newTwo := runGit(t, repo.dir, "rev-parse", "stack/two")
	for _, want := range []string{
		fmt.Sprintf("stack/one: %s -> %s (patch changed)", shortSyncRef(one), shortSyncRef(newOne)),
		fmt.Sprintf("stack/two: %s -> %s (patch unchanged)", shortSyncRef(two), shortSyncRef(newTwo)),
		fmt.Sprintf("alias/two: %s -> %s (patch comparison unavailable)", shortSyncRef(two), shortSyncRef(newTwo)),
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("summary missing %q:\n%s", want, stdout)
		}
	}
	if strings.Index(stdout, "alias/two:") > strings.Index(stdout, "stack/one:") || strings.Index(stdout, "stack/one:") > strings.Index(stdout, "stack/two:") {
		t.Fatalf("summary is not ordered by branch name:\n%s", stdout)
	}
	if got := runGit(t, repo.dir, "show", "stack/two:ancestor.txt"); got != "after" {
		t.Fatalf("child did not inherit the amendment: %q", got)
	}
}

func TestRewriteSummaryPersistsThroughContinue(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "file.txt", "parent\n", "One")
	one := runGit(t, repo.dir, "rev-parse", "HEAD")
	createStackBranch(t, repo, "file.txt", "child\n", "Two")
	two := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "switch", "stack/one")
	writeFile(t, repo.dir, "file.txt", "amended\n")
	runGit(t, repo.dir, "add", ".")
	code, stdout, stderr := repo.runGraphene(t, "amend", "--no-edit")
	if code == 0 || !strings.Contains(stdout+stderr, "CONFLICT") {
		t.Fatalf("amend should conflict: %d\n%s\n%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "Rewrite summary:") {
		t.Fatalf("incomplete rewrite was reported as complete:\n%s", stdout)
	}
	before := readState(t, repo.dir).Pending.RewriteBefore
	if before["stack/one"].Head != one || before["stack/two"].Head != two || before["stack/two"].Patch == "" {
		t.Fatalf("missing persisted source evidence: %#v", before)
	}
	writeFile(t, repo.dir, "file.txt", "resolved\n")
	runGit(t, repo.dir, "add", ".")
	code, stdout, stderr = repo.runGraphene(t, "continue")
	if code != 0 {
		t.Fatalf("continue exited %d: %s", code, stderr)
	}
	for branch, old := range map[string]string{"stack/one": one, "stack/two": two} {
		head := runGit(t, repo.dir, "rev-parse", branch)
		want := fmt.Sprintf("%s: %s -> %s (patch changed)", branch, shortSyncRef(old), shortSyncRef(head))
		if !strings.Contains(stdout, want) {
			t.Fatalf("summary missing %q:\n%s", want, stdout)
		}
	}
	if readState(t, repo.dir).Pending != nil {
		t.Fatal("successful continuation retained pending evidence")
	}
}
