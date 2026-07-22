package graphene

import (
	"reflect"
	"testing"
)

func TestContinueNewAfterDerivedRenameCheckpointBeforeCallerWrite(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	app := &App{git: git}
	originalState := readState(t, repo.dir)
	originalHead := runGit(t, repo.dir, "rev-parse", "main")

	writeFile(t, repo.dir, "new.txt", "new\n")
	runGit(t, repo.dir, "add", "new.txt")

	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("new", worktree, "main", originalHead, originalState.Stacks)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	if err := app.prepareOperationWorktree(operation); err != nil {
		t.Fatal(err)
	}

	const temporaryBranch = "graphene-tmp-new-rename-crash"
	const finalBranch = "stack/derived-new"
	mainValue := RefValue{Exists: true, OID: originalHead}
	operation.Refs["refs/heads/main"] = JournalRef{Original: mainValue, Expected: mainValue}
	operation.Refs["refs/heads/"+temporaryBranch] = JournalRef{}
	operation.Refs["refs/heads/"+finalBranch] = JournalRef{}
	operation.DesiredStacks = []Stack{{Base: "main", Branches: []string{finalBranch}}}
	if err := app.snapshotOperationValidationRefs(operation, operation.OriginalStacks, operation.DesiredStacks); err != nil {
		t.Fatal(err)
	}
	if err := app.verifyUnpublishedOperationWorktree(operation); err != nil {
		t.Fatal(err)
	}
	if err := prepareOperationBackups(operation); err != nil {
		t.Fatal(err)
	}
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}

	runGit(t, repo.dir, "switch", "-c", temporaryBranch)
	runGit(t, repo.dir, "commit", "-m", "Derived new")
	commit := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "branch", "-m", finalBranch)

	// Reconstruct the crash window after finishJournalNewBranch checkpointed
	// the completed rename, but before its caller could commit the operation.
	temporaryRef := "refs/heads/" + temporaryBranch
	finalRef := "refs/heads/" + finalBranch
	temporarySnapshot := operation.Refs[temporaryRef]
	temporarySnapshot.Expected = RefValue{}
	operation.Refs[temporaryRef] = temporarySnapshot
	finalSnapshot := operation.Refs[finalRef]
	finalSnapshot.Expected = RefValue{Exists: true, OID: commit}
	operation.Refs[finalRef] = finalSnapshot
	operation.Active = nil
	operation.Phase = operationApplying
	progress := rebaseJournalProgress{
		Commit: &journalCommit{
			Branch:       temporaryBranch,
			Mode:         "new",
			CreateBranch: true,
			Args:         []string{"commit", "-m", "Derived new"},
		},
		CommitPrepared:    true,
		CommitPrepareStep: 1,
		CommitDone:        true,
		New: &journalNewBranch{
			RecordBase:   "main",
			Derive:       true,
			BranchPrefix: "stack/",
			FinalBranch:  finalBranch,
		},
	}
	if err := encodeRebaseProgress(operation, progress); err != nil {
		t.Fatal(err)
	}
	if err := git.WriteState(State{Stacks: cloneStacks(originalState.Stacks), Operation: operation}); err != nil {
		t.Fatal(err)
	}

	durable := readState(t, repo.dir)
	if durable.Operation == nil || durable.Operation.Active != nil {
		t.Fatalf("durable rename checkpoint = %#v, want inactive operation", durable.Operation)
	}
	if durable.Operation.Refs[temporaryRef].Expected.Exists {
		t.Fatal("temporary branch is still expected after the rename checkpoint")
	}
	if got := durable.Operation.Refs[finalRef].Expected; got != (RefValue{Exists: true, OID: commit}) {
		t.Fatalf("final ref checkpoint = %s, want %s", formatRefValue(got), commit)
	}
	durableProgress, err := decodeRebaseProgress(durable.Operation)
	if err != nil {
		t.Fatal(err)
	}
	if durableProgress.Commit == nil || durableProgress.Commit.Branch != temporaryBranch {
		t.Fatalf("durable caller progress = %#v, want temporary branch %q", durableProgress.Commit, temporaryBranch)
	}

	expectGrapheneOK(t, repo, "continue")

	if got := currentBranch(t, repo.dir); got != finalBranch {
		t.Fatalf("branch after continue = %q, want %q", got, finalBranch)
	}
	if refExists(t, repo.dir, temporaryRef) {
		t.Fatalf("temporary ref %q exists after continue", temporaryRef)
	}
	if got := runGit(t, repo.dir, "rev-parse", finalRef); got != commit {
		t.Fatalf("final ref after continue = %s, want %s", got, commit)
	}
	wantStacks := []Stack{{Base: "main", Branches: []string{finalBranch}}}
	if got := readState(t, repo.dir); got.Operation != nil || !reflect.DeepEqual(got.Stacks, wantStacks) {
		t.Fatalf("state after continue = %#v, want stacks %#v and no operation", got, wantStacks)
	}
	if refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname)", "refs/graphene/journal"); refs != "" {
		t.Fatalf("journal backup refs remain after continue: %q", refs)
	}
}

func TestContinueInteractiveSplitAfterDerivedRenameCheckpointBeforeCallerWrite(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "one.txt", "one\n")
	writeFile(t, repo.dir, "two.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "Combined change")
	expectGrapheneOK(t, repo, "split")

	runGit(t, repo.dir, "add", "one.txt")
	expectGrapheneOK(t, repo, "new", "--reuse-current", "-m", "Add one")

	state := readState(t, repo.dir)
	operation := state.Operation
	if operation == nil || operation.Kind != "split" || operation.Phase != operationInteractive {
		t.Fatalf("split operation before crash fixture = %#v, want interactive split", operation)
	}
	progress, err := decodeSplitProgress(operation)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Commit != nil || len(progress.Branches) != 1 {
		t.Fatalf("split progress before second part = %#v, want one completed part", progress)
	}

	const temporaryBranch = "graphene-tmp-split-rename-crash"
	const finalBranch = "stack/add-two"
	temporaryRef := "refs/heads/" + temporaryBranch
	finalRef := "refs/heads/" + finalBranch
	operation.Refs[temporaryRef] = JournalRef{}
	operation.Refs[finalRef] = JournalRef{}

	runGit(t, repo.dir, "add", "two.txt")
	runGit(t, repo.dir, "switch", "-c", temporaryBranch)
	runGit(t, repo.dir, "commit", "-m", "Add two")
	commit := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "branch", "-m", finalBranch)

	// Reconstruct the crash window after finishSplitCommitBranchName wrote the
	// ref checkpoint, but before continueSplitCommit recorded the final branch
	// in DraftStacks and Branches and cleared Commit.
	temporarySnapshot := operation.Refs[temporaryRef]
	temporarySnapshot.Expected = RefValue{}
	operation.Refs[temporaryRef] = temporarySnapshot
	finalSnapshot := operation.Refs[finalRef]
	finalSnapshot.Expected = RefValue{Exists: true, OID: commit}
	operation.Refs[finalRef] = finalSnapshot
	operation.Active = nil
	progress.Commit = &splitCommitJournal{
		Branch:         temporaryBranch,
		PreviousBranch: progress.Target,
		Temporary:      true,
		BranchPrefix:   "stack/",
		FinalBranch:    finalBranch,
		Args:           []string{"commit", "-m", "Add two"},
		Created:        true,
		Prepared:       true,
		Committed:      true,
	}
	if err := encodeSplitProgress(operation, progress); err != nil {
		t.Fatal(err)
	}
	if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}

	durable := readState(t, repo.dir)
	if durable.Operation == nil || durable.Operation.Active != nil {
		t.Fatalf("durable split rename checkpoint = %#v, want inactive operation", durable.Operation)
	}
	if durable.Operation.Refs[temporaryRef].Expected.Exists {
		t.Fatal("temporary split branch is still expected after the rename checkpoint")
	}
	if got := durable.Operation.Refs[finalRef].Expected; got != (RefValue{Exists: true, OID: commit}) {
		t.Fatalf("final split ref checkpoint = %s, want %s", formatRefValue(got), commit)
	}
	durableProgress, err := decodeSplitProgress(durable.Operation)
	if err != nil {
		t.Fatal(err)
	}
	if durableProgress.Commit == nil || durableProgress.Commit.Branch != temporaryBranch {
		t.Fatalf("durable split caller progress = %#v, want temporary branch %q", durableProgress.Commit, temporaryBranch)
	}
	if len(durableProgress.Branches) != 1 || StateContainsName(State{Stacks: durableProgress.DraftStacks}, finalBranch) {
		t.Fatalf("split caller progress advanced past crash window: branches=%#v stacks=%#v", durableProgress.Branches, durableProgress.DraftStacks)
	}

	expectGrapheneOK(t, repo, "continue")

	if got := currentBranch(t, repo.dir); got != finalBranch {
		t.Fatalf("branch after continue = %q, want %q", got, finalBranch)
	}
	if refExists(t, repo.dir, temporaryRef) {
		t.Fatalf("temporary split ref %q exists after continue", temporaryRef)
	}
	if got := runGit(t, repo.dir, "rev-parse", finalRef); got != commit {
		t.Fatalf("final split ref after continue = %s, want %s", got, commit)
	}
	wantStacks := []Stack{{Base: "main", Branches: []string{progress.Target, finalBranch}}}
	if got := readState(t, repo.dir); got.Operation != nil || !reflect.DeepEqual(got.Stacks, wantStacks) {
		t.Fatalf("state after continue = %#v, want stacks %#v and no operation", got, wantStacks)
	}
	if refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname)", "refs/graphene/journal"); refs != "" {
		t.Fatalf("journal backup refs remain after continue: %q", refs)
	}
}
