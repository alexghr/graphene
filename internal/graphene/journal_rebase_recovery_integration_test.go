package graphene

import (
	"reflect"
	"strings"
	"testing"
)

func TestJournalRebaseRetriesUnchangedActiveAction(t *testing.T) {
	t.Parallel()
	fixture := newJournalRebaseRecoveryFixture(t)

	expectGrapheneOK(t, fixture.repo, "continue")

	got := readState(t, fixture.repo.dir)
	if got.Operation != nil {
		t.Fatalf("operation after retry = %#v, want nil", got.Operation)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.ownedRef); value == fixture.before {
		t.Fatalf("owned ref was not rewritten: still %s", value)
	}
	assertBranchParent(t, fixture.repo.dir, fixture.top, fixture.onto)
	if refExists(t, fixture.repo.dir, fixture.backupRef) {
		t.Fatalf("backup %s still exists after retry", fixture.backupRef)
	}
}

func TestJournalRebaseExternalCompletionRequiresAcceptance(t *testing.T) {
	t.Parallel()
	fixture := newJournalRebaseRecoveryFixture(t)
	runGit(t, fixture.repo.dir, "rebase", "--onto", fixture.onto, fixture.upstream, fixture.top)
	rewritten := runGit(t, fixture.repo.dir, "rev-parse", fixture.ownedRef)
	if rewritten == fixture.before {
		t.Fatal("external rebase did not rewrite the owned ref")
	}

	beforeState := readState(t, fixture.repo.dir)
	code, _, stderr := fixture.repo.runGraphene(t, "continue")
	if code == 0 {
		t.Fatal("continue unexpectedly accepted an externally completed rebase")
	}
	if !strings.Contains(stderr, "run graphene continue --accept-current") {
		t.Fatalf("stderr = %q, want --accept-current guidance", stderr)
	}
	if got := readState(t, fixture.repo.dir); !reflect.DeepEqual(got, beforeState) {
		t.Fatalf("state changed before external result was accepted:\n before: %#v\n  after: %#v", beforeState, got)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.ownedRef); value != rewritten {
		t.Fatalf("owned ref after refused continue = %s, want %s", value, rewritten)
	}

	expectGrapheneOK(t, fixture.repo, "continue", "--accept-current")
	got := readState(t, fixture.repo.dir)
	if got.Operation != nil {
		t.Fatalf("operation after accepting external rebase = %#v, want nil", got.Operation)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.ownedRef); value != rewritten {
		t.Fatalf("accepted owned ref = %s, want %s", value, rewritten)
	}
	assertBranchParent(t, fixture.repo.dir, fixture.top, fixture.onto)
}

func TestJournalRebaseRejectsInvalidExternalResult(t *testing.T) {
	t.Parallel()
	fixture := newJournalRebaseRecoveryFixture(t)
	runGit(t, fixture.repo.dir, "update-ref", fixture.ownedRef, fixture.upstream, fixture.before)

	beforeState := readState(t, fixture.repo.dir)
	code, _, stderr := fixture.repo.runGraphene(t, "continue", "--accept-current")
	if code == 0 {
		t.Fatal("continue --accept-current unexpectedly accepted an invalid rebase result")
	}
	if !strings.Contains(stderr, "cannot accept current refs") || !strings.Contains(stderr, "is not descended from") {
		t.Fatalf("stderr = %q, want invalid-descendant error", stderr)
	}
	if got := readState(t, fixture.repo.dir); !reflect.DeepEqual(got, beforeState) {
		t.Fatalf("state changed after invalid result was refused:\n before: %#v\n  after: %#v", beforeState, got)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.ownedRef); value != fixture.upstream {
		t.Fatalf("invalid owned ref after refused continue = %s, want %s", value, fixture.upstream)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.backupRef); value != fixture.before {
		t.Fatalf("backup after refused continue = %s, want %s", value, fixture.before)
	}
}

func TestGraphDisplaysActiveJournalOperation(t *testing.T) {
	t.Parallel()
	fixture := newJournalRebaseRecoveryFixture(t)

	code, stdout, stderr := fixture.repo.runGraphene(t, "graph")
	if code != 0 {
		t.Fatalf("graphene graph exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	want := "" +
		"main\n" +
		"  `- stack/one *\n" +
		"pending restack: stack/one\n" +
		"  phase: applying\n" +
		"  active: rebase-0\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

type journalRebaseRecoveryFixture struct {
	repo      testRepo
	ownedRef  string
	backupRef string
	top       string
	onto      string
	upstream  string
	before    string
}

func newJournalRebaseRecoveryFixture(t *testing.T) journalRebaseRecoveryFixture {
	t.Helper()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	upstream := runGit(t, repo.dir, "rev-parse", "main")
	top := "stack/one"
	runGit(t, repo.dir, "switch", "-c", top)
	before := commitFile(t, repo.dir, "stack.txt", "stack\n", "stack change")
	runGit(t, repo.dir, "switch", "main")
	commitFile(t, repo.dir, "main.txt", "main\n", "main change")
	runGit(t, repo.dir, "switch", top)

	stacks := []Stack{{Base: "main", Branches: []string{top}}}
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("restack", worktree, top, before, stacks)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreHard
	operation.DesiredStacks = cloneStacks(stacks)
	ownedRef := "refs/heads/" + top
	beforeValue := RefValue{Exists: true, OID: before}
	operation.Refs[ownedRef] = JournalRef{Original: beforeValue, Expected: beforeValue}
	progress := rebaseJournalProgress{
		Steps: []journalRebaseStep{{
			Op: RebaseOp{
				Onto:     "main",
				Upstream: upstream,
				Top:      top,
			},
			RefNames: []string{ownedRef},
		}},
		ReturnBranch: top,
	}
	if err := encodeRebaseProgress(operation, progress); err != nil {
		t.Fatal(err)
	}
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}
	backupRef := operation.Refs[ownedRef].Backup
	if backupRef == "" {
		t.Fatal("operation backup was not recorded")
	}
	inventory, err := git.LocalHeadRefValues()
	if err != nil {
		t.Fatal(err)
	}
	operation.Phase = operationApplying
	operation.Active = &JournalAction{
		ID:           "rebase-0",
		Kind:         "rebase",
		RefsBefore:   map[string]RefValue{ownedRef: beforeValue},
		RefInventory: inventory,
	}
	if err := git.WriteState(State{Stacks: cloneStacks(stacks), Operation: operation}); err != nil {
		t.Fatal(err)
	}

	return journalRebaseRecoveryFixture{
		repo:      repo,
		ownedRef:  ownedRef,
		backupRef: backupRef,
		top:       top,
		onto:      "main",
		upstream:  upstream,
		before:    before,
	}
}
