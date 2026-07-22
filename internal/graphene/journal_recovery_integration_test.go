package graphene

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestJournalAbortResumesAfterRefsWereRestored(t *testing.T) {
	t.Parallel()
	fixture := newJournalRecoveryFixture(t)

	runGit(t, fixture.repo.dir, "update-ref", fixture.ownedRef, fixture.original, fixture.expected)
	fixture.state.Operation.Phase = operationRollingBack
	if err := fixture.git.WriteState(fixture.state); err != nil {
		t.Fatal(err)
	}

	expectGrapheneOK(t, fixture.repo, "abort")

	got := readState(t, fixture.repo.dir)
	if got.Operation != nil {
		t.Fatalf("operation after abort = %#v, want nil", got.Operation)
	}
	if !reflect.DeepEqual(got.Stacks, fixture.state.Operation.OriginalStacks) {
		t.Fatalf("stacks after abort = %#v, want %#v", got.Stacks, fixture.state.Operation.OriginalStacks)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.ownedRef); value != fixture.original {
		t.Fatalf("owned ref after abort = %s, want %s", value, fixture.original)
	}
	if refExists(t, fixture.repo.dir, fixture.backupRef) {
		t.Fatalf("backup %s still exists after abort", fixture.backupRef)
	}
}

func TestJournalAbortRefusesMovedOwnedRefWithoutMutation(t *testing.T) {
	t.Parallel()
	fixture := newJournalRecoveryFixture(t)
	runGit(t, fixture.repo.dir, "update-ref", fixture.ownedRef, fixture.unexpected, fixture.expected)

	before := readState(t, fixture.repo.dir)
	code, _, stderr := fixture.repo.runGraphene(t, "abort")
	if code == 0 {
		t.Fatal("abort unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "cannot abort because operation-owned refs changed") {
		t.Fatalf("stderr = %q, want owned-ref drift error", stderr)
	}

	after := readState(t, fixture.repo.dir)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("state changed after refused abort:\n before: %#v\n  after: %#v", before, after)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.ownedRef); value != fixture.unexpected {
		t.Fatalf("owned ref after refused abort = %s, want %s", value, fixture.unexpected)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.backupRef); value != fixture.original {
		t.Fatalf("backup after refused abort = %s, want %s", value, fixture.original)
	}
	if refExists(t, fixture.repo.dir, fixture.recoveryRef()) {
		t.Fatalf("recovery ref %s was created by refused abort", fixture.recoveryRef())
	}
}

func TestJournalForceAbortPreservesMovedOwnedRef(t *testing.T) {
	t.Parallel()
	fixture := newJournalRecoveryFixture(t)
	runGit(t, fixture.repo.dir, "update-ref", fixture.ownedRef, fixture.unexpected, fixture.expected)

	expectGrapheneOK(t, fixture.repo, "abort", "--force")

	got := readState(t, fixture.repo.dir)
	if got.Operation != nil {
		t.Fatalf("operation after forced abort = %#v, want nil", got.Operation)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.ownedRef); value != fixture.original {
		t.Fatalf("owned ref after forced abort = %s, want %s", value, fixture.original)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.recoveryRef()); value != fixture.unexpected {
		t.Fatalf("recovery ref after forced abort = %s, want %s", value, fixture.unexpected)
	}
	if refExists(t, fixture.repo.dir, fixture.backupRef) {
		t.Fatalf("backup %s still exists after forced abort", fixture.backupRef)
	}
}

func TestJournalCleanupRefusesMovedBackup(t *testing.T) {
	t.Parallel()
	fixture := newJournalRecoveryFixture(t)
	fixture.state.Operation.Phase = operationCleanup
	fixture.state.Stacks = cloneStacks(fixture.state.Operation.DesiredStacks)
	if err := fixture.git.WriteState(fixture.state); err != nil {
		t.Fatal(err)
	}
	runGit(t, fixture.repo.dir, "update-ref", fixture.backupRef, fixture.unexpected, fixture.original)

	before := readState(t, fixture.repo.dir)
	code, _, stderr := fixture.repo.runGraphene(t, "continue")
	if code == 0 {
		t.Fatal("continue unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "operation backup "+fixture.backupRef+" moved") {
		t.Fatalf("stderr = %q, want moved-backup error", stderr)
	}

	after := readState(t, fixture.repo.dir)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("state changed after refused cleanup:\n before: %#v\n  after: %#v", before, after)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.backupRef); value != fixture.unexpected {
		t.Fatalf("moved backup after refused cleanup = %s, want %s", value, fixture.unexpected)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.ownedRef); value != fixture.expected {
		t.Fatalf("owned ref after refused cleanup = %s, want %s", value, fixture.expected)
	}
}

func TestJournalAbortResumesAfterRestoredWorktreeArtifactsWereRemoved(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	head := runGit(t, repo.dir, "rev-parse", "main")
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("amend", worktree, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.Phase = operationRollingBack
	operation.WorktreePolicy = worktreeRestoreIndex
	operation.WorktreeRestored = true
	value := RefValue{Exists: true, OID: head}
	operation.Refs["refs/heads/main"] = JournalRef{Original: value, Expected: value}
	if err := (&App{git: git}).snapshotOperationIndex(operation); err != nil {
		t.Fatal(err)
	}
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}
	state := State{Operation: operation}
	if err := git.WriteState(state); err != nil {
		t.Fatal(err)
	}
	artifactDir, err := (&App{git: git}).operationArtifactDir(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(artifactDir); err != nil {
		t.Fatal(err)
	}

	expectGrapheneOK(t, repo, "abort")

	if got := readState(t, repo.dir); got.Operation != nil {
		t.Fatalf("operation after resumed abort = %#v, want nil", got.Operation)
	}
}

func TestJournalAbortRestoresUntrackedFileStagedByOperation(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	original := runGit(t, repo.dir, "rev-parse", "main")
	writeFile(t, repo.dir, "draft.txt", "untracked before operation\n")

	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("amend", worktree, "main", original, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	app := &App{git: git}
	if err := app.snapshotOperationIndex(operation); err != nil {
		t.Fatal(err)
	}
	if err := app.snapshotOperationUntracked(operation); err != nil {
		t.Fatal(err)
	}
	originalValue := RefValue{Exists: true, OID: original}
	operation.Refs["refs/heads/main"] = JournalRef{Original: originalValue, Expected: originalValue}
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.dir, "add", "-A")
	runGit(t, repo.dir, "commit", "-m", "temporarily track draft")
	committed := runGit(t, repo.dir, "rev-parse", "main")
	snapshot := operation.Refs["refs/heads/main"]
	snapshot.Expected = RefValue{Exists: true, OID: committed}
	operation.Refs["refs/heads/main"] = snapshot
	operation.Phase = operationApplying
	if err := git.WriteState(State{Operation: operation}); err != nil {
		t.Fatal(err)
	}

	expectGrapheneOK(t, repo, "abort")

	if got := runGit(t, repo.dir, "rev-parse", "main"); got != original {
		t.Fatalf("main after abort = %s, want %s", got, original)
	}
	data, err := os.ReadFile(filepath.Join(repo.dir, "draft.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "untracked before operation\n" {
		t.Fatalf("draft.txt after abort = %q", got)
	}
	if got := runGit(t, repo.dir, "status", "--short"); got != "?? draft.txt" {
		t.Fatalf("status after abort = %q, want untracked draft", got)
	}
}

type journalRecoveryFixture struct {
	repo       testRepo
	git        Git
	state      State
	ownedRef   string
	backupRef  string
	original   string
	expected   string
	unexpected string
}

func newJournalRecoveryFixture(t *testing.T) journalRecoveryFixture {
	t.Helper()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	original := runGit(t, repo.dir, "rev-parse", "main")

	runGit(t, repo.dir, "switch", "-c", "stack/owned")
	expected := commitFile(t, repo.dir, "owned.txt", "owned\n", "owned change")
	runGit(t, repo.dir, "switch", "main")
	runGit(t, repo.dir, "switch", "-c", "manual-tip")
	unexpected := commitFile(t, repo.dir, "manual.txt", "manual\n", "manual tip")
	runGit(t, repo.dir, "switch", "main")

	ownedRef := "refs/heads/stack/owned"
	runGit(t, repo.dir, "update-ref", ownedRef, original, expected)
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("delete", worktree, "main", original, []Stack{{Base: "main", Branches: []string{"stack/owned"}}})
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreNone
	operation.DesiredStacks = []Stack{}
	operation.Refs[ownedRef] = JournalRef{
		Original: RefValue{Exists: true, OID: original},
		Expected: RefValue{Exists: true, OID: expected},
	}
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}
	backupRef := operation.Refs[ownedRef].Backup
	if backupRef == "" {
		t.Fatal("operation backup was not recorded")
	}
	runGit(t, repo.dir, "update-ref", ownedRef, expected, original)
	operation.Phase = operationApplying
	state := State{Stacks: cloneStacks(operation.OriginalStacks), Operation: operation}
	if err := git.WriteState(state); err != nil {
		t.Fatal(err)
	}

	return journalRecoveryFixture{
		repo:       repo,
		git:        git,
		state:      state,
		ownedRef:   ownedRef,
		backupRef:  backupRef,
		original:   original,
		expected:   expected,
		unexpected: unexpected,
	}
}

func (f journalRecoveryFixture) recoveryRef() string {
	return "refs/graphene/recovery/" + f.state.Operation.ID + "/0000"
}
