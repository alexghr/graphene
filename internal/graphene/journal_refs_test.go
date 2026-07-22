package graphene

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateRefsIsAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git := Git{Dir: dir, Stdin: os.Stdin, Stdout: io.Discard, Stderr: io.Discard}
	if err := git.Run("init", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := git.Run("config", "user.name", "Graphene Test"); err != nil {
		t.Fatal(err)
	}
	if err := git.Run("config", "user.email", "graphene@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := git.Run("config", "commit.gpgsign", "false"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git.Run("add", "file"); err != nil {
		t.Fatal(err)
	}
	if err := git.Run("commit", "-qm", "one"); err != nil {
		t.Fatal(err)
	}
	head, err := git.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := git.UpdateRefs([]refEdit{
		{Ref: "refs/heads/created", New: RefValue{Exists: true, OID: head}},
		{Ref: "refs/heads/missing", Old: RefValue{Exists: true, OID: head}, New: RefValue{}},
	}); err == nil {
		t.Fatal("UpdateRefs unexpectedly succeeded")
	}
	if value, err := git.RefValue("refs/heads/created"); err != nil {
		t.Fatal(err)
	} else if value.Exists {
		t.Fatal("transaction partially created refs/heads/created")
	}
}

func TestUpdateRefsSuppressesTransactionProtocol(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	var stdout bytes.Buffer
	git := Git{Dir: repo.dir, Stdout: &stdout, Stderr: io.Discard}
	head, err := git.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := git.UpdateRefs([]refEdit{{
		Ref: "refs/heads/created",
		New: RefValue{Exists: true, OID: head},
	}}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("update-ref protocol leaked to stdout: %q", stdout.String())
	}

	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("delete", worktree, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	value := RefValue{Exists: true, OID: head}
	operation.Refs["refs/heads/main"] = JournalRef{Original: value, Expected: value}
	operation.ValidationRefsComplete = true
	stdout.Reset()
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("backup transaction protocol leaked to stdout: %q", stdout.String())
	}
}
