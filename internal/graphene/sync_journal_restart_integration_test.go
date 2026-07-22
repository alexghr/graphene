package graphene

import (
	"reflect"
	"strings"
	"testing"
)

func TestSyncContinueRecoversFinalBackgroundComponentRollback(t *testing.T) {
	t.Parallel()
	fixture := newSyncRollbackRestartFixture(t, false)

	code, stdout, stderr := fixture.repo.runGraphene(t, "continue")
	if code == 0 {
		t.Fatal("graphene continue unexpectedly succeeded after an isolated background conflict")
	}
	if !strings.Contains(stderr, "sync completed with 1 stack component(s) restored unchanged") {
		t.Fatalf("stderr = %q, want partial sync result", stderr)
	}
	if !strings.Contains(stdout, "Could not sync; restored unchanged:\n  stack/failed") {
		t.Fatalf("stdout = %q, want restored component summary", stdout)
	}
	assertSyncRollbackRestartCompleted(t, fixture, false)
}

func TestSyncContinueRecoversBackgroundRollbackBeforeSuccessfulComponent(t *testing.T) {
	t.Parallel()
	fixture := newSyncRollbackRestartFixture(t, true)

	code, stdout, stderr := fixture.repo.runGraphene(t, "continue")
	if code == 0 {
		t.Fatal("graphene continue unexpectedly succeeded after an isolated background conflict")
	}
	if !strings.Contains(stderr, "sync completed with 1 stack component(s) restored unchanged") {
		t.Fatalf("stderr = %q, want partial sync result", stderr)
	}
	for _, want := range []string{
		"Synced:\n  stack/success\n",
		"Could not sync; restored unchanged:\n  stack/failed",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
	assertSyncRollbackRestartCompleted(t, fixture, true)
	assertBranchParent(t, fixture.repo.dir, "stack/success", "main")
	if got := runGit(t, fixture.repo.dir, "rev-parse", "stack/success"); got == fixture.originalRefs["stack/success"] {
		t.Fatal("stack/success was not rebased after recovering the preceding component")
	}
}

func TestSyncContinueRetriesPartiallyCheckpointedConfigCleanup(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	main := runGit(t, repo.dir, "rev-parse", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "switch", "main")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	runGit(t, repo.dir, "switch", "main")
	original := readState(t, repo.dir)

	for _, branch := range []string{"stack/one", "stack/two"} {
		runGit(t, repo.dir, "config", "--local", "branch."+branch+".remote", "origin")
		runGit(t, repo.dir, "config", "--local", "branch."+branch+".merge", "refs/heads/"+branch)
	}
	oneConfig, err := git.BranchConfig("stack/one")
	if err != nil {
		t.Fatal(err)
	}
	twoConfig, err := git.BranchConfig("stack/two")
	if err != nil {
		t.Fatal(err)
	}

	operation := newSyncRestartOperation(t, git, original.Stacks, main)
	addSyncRestartRef(t, git, operation, "main", RefValue{Exists: true, OID: main})
	for _, branch := range []string{"stack/one", "stack/two"} {
		addSyncRestartRef(t, git, operation, branch, mustSyncRestartRef(t, git, branch))
	}
	operation.Configs = []JournalConfig{
		{Section: "branch.stack/one", Original: oneConfig},
		{Section: "branch.stack/two", Original: twoConfig, Expected: append([]ConfigEntry(nil), twoConfig...)},
	}
	installSyncRestartBackups(t, git, operation)
	if err := git.RemoveBranchConfig("stack/one"); err != nil {
		t.Fatal(err)
	}
	if err := git.RemoveBranchConfig("stack/two"); err != nil {
		t.Fatal(err)
	}
	operation.Active = &JournalAction{ID: "delete-config:branch.stack/two", Kind: "delete-config"}
	progress := completedDeletedSyncProgress(main, []string{"stack/one", "stack/two"})
	writeSyncRestartState(t, git, original.Stacks, operation, progress)

	expectGrapheneOK(t, repo, "continue")

	for _, branch := range []string{"stack/one", "stack/two"} {
		if refExists(t, repo.dir, "refs/heads/"+branch) {
			t.Fatalf("%s still exists after resumed finalization", branch)
		}
		entries, err := git.BranchConfig(branch)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("branch config for %s = %#v, want none", branch, entries)
		}
	}
	assertCompletedSyncRestartState(t, repo, nil)
}

func TestSyncContinueRetriesCompletedFinalRefDeletion(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	main := runGit(t, repo.dir, "rev-parse", "main")

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "switch", "main")
	original := readState(t, repo.dir)
	one := mustSyncRestartRef(t, git, "stack/one")

	operation := newSyncRestartOperation(t, git, original.Stacks, main)
	addSyncRestartRef(t, git, operation, "main", RefValue{Exists: true, OID: main})
	addSyncRestartRef(t, git, operation, "stack/one", one)
	installSyncRestartBackups(t, git, operation)
	ref := "refs/heads/stack/one"
	operation.Active = &JournalAction{
		ID:         "delete-merged-refs",
		Kind:       "delete-refs",
		RefsBefore: map[string]RefValue{ref: one},
		RefsAfter:  map[string]RefValue{ref: {}},
	}
	runGit(t, repo.dir, "update-ref", "-d", ref, one.OID)
	progress := completedDeletedSyncProgress(main, []string{"stack/one"})
	writeSyncRestartState(t, git, original.Stacks, operation, progress)

	expectGrapheneOK(t, repo, "continue")

	if refExists(t, repo.dir, ref) {
		t.Fatal("stack/one was recreated while resuming completed ref deletion")
	}
	assertCompletedSyncRestartState(t, repo, nil)
}

type syncRollbackRestartFixture struct {
	repo         testRepo
	newMain      string
	originalRefs map[string]string
	original     State
}

func newSyncRollbackRestartFixture(t *testing.T, includeSuccess bool) syncRollbackRestartFixture {
	t.Helper()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	oldMain := runGit(t, repo.dir, "rev-parse", "main")

	writeFile(t, repo.dir, "file.txt", "local\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "Failed")
	runGit(t, repo.dir, "switch", "main")
	if includeSuccess {
		createStackBranch(t, repo, "success.txt", "success\n", "Success")
		runGit(t, repo.dir, "switch", "main")
	}
	original := readState(t, repo.dir)
	originalRefs := map[string]string{
		"stack/failed": runGit(t, repo.dir, "rev-parse", "stack/failed"),
	}
	if includeSuccess {
		originalRefs["stack/success"] = runGit(t, repo.dir, "rev-parse", "stack/success")
	}
	runGit(t, repo.dir, "switch", "--detach", oldMain)
	newMain := commitFile(t, repo.dir, "file.txt", "remote\n", "Conflicting base update")
	runGit(t, repo.dir, "switch", "main")

	operation := newSyncRestartOperation(t, git, original.Stacks, oldMain)
	addSyncRestartRef(t, git, operation, "main", RefValue{Exists: true, OID: oldMain})
	mainSnapshot := operation.Refs["refs/heads/main"]
	mainSnapshot.Expected = RefValue{Exists: true, OID: newMain}
	operation.Refs["refs/heads/main"] = mainSnapshot
	addSyncRestartRef(t, git, operation, "stack/failed", RefValue{Exists: true, OID: originalRefs["stack/failed"]})
	if includeSuccess {
		addSyncRestartRef(t, git, operation, "stack/success", RefValue{Exists: true, OID: originalRefs["stack/success"]})
	}
	installSyncRestartBackups(t, git, operation)
	runGit(t, repo.dir, "merge", "--ff-only", newMain)
	inventory, err := git.LocalHeadRefValues()
	if err != nil {
		t.Fatal(err)
	}
	failedRef := "refs/heads/stack/failed"
	operation.Active = &JournalAction{
		ID:           "component-0-rebase-0",
		Kind:         "rebase",
		RefsBefore:   map[string]RefValue{failedRef: {Exists: true, OID: originalRefs["stack/failed"]}},
		RefInventory: inventory,
	}
	code, _, _ := runGitResult(t, repo.dir, "rebase", "--update-refs", "--onto", newMain, oldMain, "stack/failed")
	if code == 0 {
		t.Fatal("fixture rebase unexpectedly succeeded")
	}
	inProgress, err := git.RebaseInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if !inProgress {
		t.Fatal("fixture did not leave a conflicted rebase in progress")
	}

	components := []syncJournalComponent{{
		Names:      []string{"stack/failed"},
		Branches:   []string{"stack/failed"},
		Ops:        []RebaseOp{{Onto: newMain, Upstream: oldMain, Top: "stack/failed"}},
		RefNames:   []string{failedRef},
		Status:     syncComponentRollingBack,
		FailureTop: "stack/failed",
	}}
	if includeSuccess {
		components = append(components, syncJournalComponent{
			Names:    []string{"stack/success"},
			Branches: []string{"stack/success"},
			Ops:      []RebaseOp{{Onto: newMain, Upstream: oldMain, Top: "stack/success"}},
			RefNames: []string{"refs/heads/stack/success"},
			Status:   syncComponentPending,
		})
	}
	progress := syncJournalProgress{
		Base:           "main",
		OriginalBranch: "main",
		ReturnBranch:   "main",
		BaseOld:        oldMain,
		BaseNew:        newMain,
		BaseUpdate:     true,
		BaseDone:       true,
		Components:     components,
	}
	writeSyncRestartState(t, git, original.Stacks, operation, progress)

	return syncRollbackRestartFixture{
		repo:         repo,
		newMain:      newMain,
		originalRefs: originalRefs,
		original:     original,
	}
}

func assertSyncRollbackRestartCompleted(t *testing.T, fixture syncRollbackRestartFixture, includedSuccess bool) {
	t.Helper()
	git := Git{Dir: fixture.repo.dir}
	if got := runGit(t, fixture.repo.dir, "rev-parse", "main"); got != fixture.newMain {
		t.Fatalf("main = %s after recovery, want %s", got, fixture.newMain)
	}
	if got := runGit(t, fixture.repo.dir, "rev-parse", "stack/failed"); got != fixture.originalRefs["stack/failed"] {
		t.Fatalf("stack/failed = %s after recovery, want original %s", got, fixture.originalRefs["stack/failed"])
	}
	if got := currentBranch(t, fixture.repo.dir); got != "main" {
		t.Fatalf("branch after recovery = %q, want main", got)
	}
	if got := runGit(t, fixture.repo.dir, "status", "--porcelain"); got != "" {
		t.Fatalf("worktree after recovery = %q, want clean", got)
	}
	inProgress, err := git.RebaseInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if inProgress {
		t.Fatal("rebase still in progress after recovery")
	}
	if includedSuccess && len(fixture.original.Stacks) != 2 {
		t.Fatalf("fixture has %d stacks, want failed and successful siblings", len(fixture.original.Stacks))
	}
	assertCompletedSyncRestartState(t, fixture.repo, fixture.original.Stacks)
}

func newSyncRestartOperation(t *testing.T, git Git, stacks []Stack, originalHead string) *OperationJournal {
	t.Helper()
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("sync", worktree, "main", originalHead, stacks)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreHard
	operation.Phase = operationApplying
	return operation
}

func addSyncRestartRef(t *testing.T, git Git, operation *OperationJournal, branch string, original RefValue) {
	t.Helper()
	actual, err := git.RefValue("refs/heads/" + branch)
	if err != nil {
		t.Fatal(err)
	}
	if !actual.Exists {
		t.Fatalf("fixture branch %q does not exist", branch)
	}
	operation.Refs["refs/heads/"+branch] = JournalRef{Original: original, Expected: actual}
}

func mustSyncRestartRef(t *testing.T, git Git, branch string) RefValue {
	t.Helper()
	value, err := git.RefValue("refs/heads/" + branch)
	if err != nil {
		t.Fatal(err)
	}
	if !value.Exists {
		t.Fatalf("fixture branch %q does not exist", branch)
	}
	return value
}

func installSyncRestartBackups(t *testing.T, git Git, operation *OperationJournal) {
	t.Helper()
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}
}

func writeSyncRestartState(t *testing.T, git Git, stacks []Stack, operation *OperationJournal, progress syncJournalProgress) {
	t.Helper()
	if err := encodeSyncProgress(operation, progress); err != nil {
		t.Fatal(err)
	}
	state := State{Stacks: cloneStacks(stacks), Operation: operation}
	if err := git.WriteState(state); err != nil {
		t.Fatal(err)
	}
}

func completedDeletedSyncProgress(main string, branches []string) syncJournalProgress {
	components := make([]syncJournalComponent, 0, len(branches))
	for _, branch := range branches {
		components = append(components, syncJournalComponent{
			Names:    []string{branch},
			Branches: []string{branch},
			Deleted:  []string{branch},
			RefNames: []string{"refs/heads/" + branch},
			Status:   syncComponentSucceeded,
		})
	}
	return syncJournalProgress{
		Base:           "main",
		OriginalBranch: "main",
		ReturnBranch:   "main",
		BaseOld:        main,
		BaseNew:        main,
		BaseDone:       true,
		Components:     components,
		NextComponent:  len(components),
	}
}

func assertCompletedSyncRestartState(t *testing.T, repo testRepo, wantStacks []Stack) {
	t.Helper()
	state := readState(t, repo.dir)
	if state.Operation != nil || state.Pending != nil {
		t.Fatalf("operation state was not cleared: operation=%#v pending=%#v", state.Operation, state.Pending)
	}
	if len(wantStacks) == 0 && len(state.Stacks) == 0 {
		return
	}
	if !reflect.DeepEqual(state.Stacks, wantStacks) {
		t.Fatalf("stacks after resumed sync = %#v, want %#v", state.Stacks, wantStacks)
	}
}
