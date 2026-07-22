package graphene

import (
	"reflect"
	"testing"
)

func TestEqualConfigEntriesPreservesOrderAndDuplicates(t *testing.T) {
	t.Parallel()
	left := []ConfigEntry{{Key: "branch.a.merge", Value: "one"}, {Key: "branch.a.merge", Value: "two"}}
	right := []ConfigEntry{{Key: "branch.a.merge", Value: "two"}, {Key: "branch.a.merge", Value: "one"}}
	if equalConfigEntries(left, right) {
		t.Fatal("equalConfigEntries accepted reordered multivalue config")
	}
	right = append([]ConfigEntry(nil), left...)
	if !equalConfigEntries(left, right) {
		t.Fatal("equalConfigEntries rejected an identical ordered config")
	}
	right = append(right, left[0])
	if equalConfigEntries(left, right) {
		t.Fatal("equalConfigEntries accepted different duplicate counts")
	}
}

func TestRestoreBranchConfigPreservesOrderedDuplicateKeys(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	runGit(t, repo.dir, "config", "--local", "branch.stack/config.remote", "origin-one")
	runGit(t, repo.dir, "config", "--local", "branch.stack/config.merge", "refs/heads/stack/config")
	runGit(t, repo.dir, "config", "--local", "--add", "branch.stack/config.remote", "origin-two")

	original, err := git.BranchConfig("stack/config")
	if err != nil {
		t.Fatal(err)
	}
	if err := git.RestoreBranchConfig("branch.stack/config", original, original); err != nil {
		t.Fatal(err)
	}
	actual, err := git.BranchConfig("stack/config")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, original) {
		t.Fatalf("restored config = %#v, want %#v", actual, original)
	}
	if got := runGit(t, repo.dir, "config", "--local", "--get-all", "branch.stack/config.remote"); got != "origin-one\norigin-two" {
		t.Fatalf("restored duplicate values = %q, want %q", got, "origin-one\\norigin-two")
	}
}

func TestRestoreBranchConfigOwnsExactlyOneSection(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	runGit(t, repo.dir, "config", "--local", "branch.stack/config.remote", "unexpected")
	runGit(t, repo.dir, "config", "--local", "--add", "branch.stack/config.extra", "remove-me")
	original := []ConfigEntry{{Key: "branch.stack/config.remote", Value: "origin"}}
	actual := []ConfigEntry{{Key: "branch.stack/config.remote", Value: "unexpected"}}
	if err := git.RestoreBranchConfig("branch.stack/config", original, actual); err != nil {
		t.Fatal(err)
	}
	got, err := git.BranchConfig("stack/config")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("restored section = %#v, want %#v", got, original)
	}

	outside := []ConfigEntry{{Key: "branch.other.remote", Value: "origin"}}
	if err := git.RestoreBranchConfig("branch.stack/config", original, outside); err == nil {
		t.Fatal("RestoreBranchConfig accepted an entry outside its owned section")
	}
	got, err = git.BranchConfig("stack/config")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("validation failure changed section to %#v", got)
	}
}

func TestConfigEntriesPrefix(t *testing.T) {
	t.Parallel()
	original := []ConfigEntry{
		{Key: "branch.a.remote", Value: "one"},
		{Key: "branch.a.merge", Value: "refs/heads/a"},
		{Key: "branch.a.remote", Value: "two"},
	}
	if !configEntriesPrefix(original[:2], original) {
		t.Fatal("ordered partial restore was not accepted as a prefix")
	}
	reordered := []ConfigEntry{original[2], original[1]}
	if configEntriesPrefix(reordered, original) {
		t.Fatal("reordered entries were accepted as a partial restore")
	}
}
