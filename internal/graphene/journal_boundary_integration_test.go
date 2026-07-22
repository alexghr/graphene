package graphene

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitContinueInstallsMissingBackupsFromPreparing(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	writeFile(t, repo.dir, "one.txt", "one\n")
	writeFile(t, repo.dir, "two.txt", "two\n")
	runGit(t, repo.dir, "add", ".")
	expectGrapheneOK(t, repo, "new", "-m", "Combined change")
	expectGrapheneOK(t, repo, "split")

	state := readState(t, repo.dir)
	operation := state.Operation
	if operation == nil || operation.Kind != "split" || operation.Phase != operationInteractive {
		t.Fatalf("operation after split = %#v, want interactive split", operation)
	}
	progress, err := decodeSplitProgress(operation)
	if err != nil {
		t.Fatal(err)
	}
	targetRef := "refs/heads/" + progress.Target
	targetOriginal := operation.Refs[targetRef].Original
	baseOriginal := operation.Refs["refs/heads/"+progress.OriginalBase].Original
	if !targetOriginal.Exists || !baseOriginal.Exists {
		t.Fatal("split fixture is missing target or base ref snapshots")
	}

	runGit(t, repo.dir, "reset", "--hard", targetOriginal.OID)
	for ref, snapshot := range operation.Refs {
		if snapshot.Backup != "" {
			runGit(t, repo.dir, "update-ref", "-d", snapshot.Backup)
		}
		snapshot.Expected = snapshot.Original
		operation.Refs[ref] = snapshot
	}
	operation.Phase = operationPreparing
	operation.Active = nil
	progress.ResetDone = false
	if err := encodeSplitProgress(operation, progress); err != nil {
		t.Fatal(err)
	}
	if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}

	expectGrapheneOK(t, repo, "continue")

	got := readState(t, repo.dir)
	if got.Operation == nil || got.Operation.Phase != operationInteractive {
		t.Fatalf("operation after continue = %#v, want interactive split", got.Operation)
	}
	gotProgress, err := decodeSplitProgress(got.Operation)
	if err != nil {
		t.Fatal(err)
	}
	if !gotProgress.ResetDone {
		t.Fatal("split reset was not completed after resuming from preparing")
	}
	if value := runGit(t, repo.dir, "rev-parse", targetRef); value != baseOriginal.OID {
		t.Fatalf("split target after continue = %s, want base %s", value, baseOriginal.OID)
	}
	for ref, snapshot := range got.Operation.Refs {
		if !snapshot.Original.Exists {
			continue
		}
		if !refExists(t, repo.dir, snapshot.Backup) {
			t.Fatalf("backup for %s was not installed: %s", ref, snapshot.Backup)
		}
		if value := runGit(t, repo.dir, "rev-parse", snapshot.Backup); value != snapshot.Original.OID {
			t.Fatalf("backup for %s = %s, want %s", ref, value, snapshot.Original.OID)
		}
	}
}

func TestJournalContinueRefusesCommittingPhaseRefDriftWithoutMutation(t *testing.T) {
	t.Parallel()
	fixture := newJournalRecoveryFixture(t)
	fixture.state.Operation.Phase = operationCommitting
	if err := fixture.git.WriteState(fixture.state); err != nil {
		t.Fatal(err)
	}
	runGit(t, fixture.repo.dir, "update-ref", fixture.ownedRef, fixture.unexpected, fixture.expected)
	before := readState(t, fixture.repo.dir)

	code, _, stderr := fixture.repo.runGraphene(t, "continue")
	if code == 0 {
		t.Fatal("continue unexpectedly committed after its owned ref moved")
	}
	if !strings.Contains(stderr, "cannot commit delete because operation-owned refs changed") {
		t.Fatalf("stderr = %q, want committing-phase ref drift error", stderr)
	}
	if got := readState(t, fixture.repo.dir); !reflect.DeepEqual(got, before) {
		t.Fatalf("state changed after refused commit:\n before: %#v\n  after: %#v", before, got)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.ownedRef); value != fixture.unexpected {
		t.Fatalf("owned ref after refused commit = %s, want %s", value, fixture.unexpected)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.backupRef); value != fixture.original {
		t.Fatalf("backup after refused commit = %s, want %s", value, fixture.original)
	}
}

func TestJournalAbortRejectsCorruptArtifactBeforeMutation(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	app := &App{git: git}
	original := runGit(t, repo.dir, "rev-parse", "main")
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("amend", worktree, "main", original, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	if err := app.prepareOperationWorktree(operation); err != nil {
		t.Fatal(err)
	}
	ref := "refs/heads/main"
	originalValue := RefValue{Exists: true, OID: original}
	operation.Refs[ref] = JournalRef{Original: originalValue, Expected: originalValue}
	if err := prepareOperationBackups(operation); err != nil {
		t.Fatal(err)
	}
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}

	changed := commitFile(t, repo.dir, "committed.txt", "committed\n", "operation result")
	snapshot := operation.Refs[ref]
	snapshot.Expected = RefValue{Exists: true, OID: changed}
	operation.Refs[ref] = snapshot
	operation.Phase = operationApplying
	state := State{Operation: operation}
	if err := git.WriteState(state); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo.dir, "file.txt", "dirty after operation\n")
	statusBefore := runGit(t, repo.dir, "status", "--short")
	stateBefore := readState(t, repo.dir)

	artifactDir, err := app.operationArtifactDir(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, operation.IndexArtifact), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := repo.runGraphene(t, "abort")
	if code == 0 {
		t.Fatal("abort unexpectedly accepted a corrupt recovery artifact")
	}
	if !strings.Contains(stderr, "validate operation recovery artifacts before abort") {
		t.Fatalf("stderr = %q, want artifact validation error", stderr)
	}
	if value := runGit(t, repo.dir, "rev-parse", ref); value != changed {
		t.Fatalf("main after refused abort = %s, want %s", value, changed)
	}
	if got := runGit(t, repo.dir, "status", "--short"); got != statusBefore {
		t.Fatalf("status changed after refused abort: got %q, want %q", got, statusBefore)
	}
	data, err := os.ReadFile(filepath.Join(repo.dir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "dirty after operation\n" {
		t.Fatalf("file.txt after refused abort = %q", got)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, stateBefore) {
		t.Fatalf("state changed after refused abort:\n before: %#v\n  after: %#v", stateBefore, got)
	}
	if !refExists(t, repo.dir, snapshot.Backup) {
		t.Fatalf("backup %s was removed by refused abort", snapshot.Backup)
	}
}

func TestFailedSquashCommitLeavesJournalAndHookEdits(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	commitFile(t, repo.dir, ".gitignore", "hook-ignored.txt\n", "Ignore hook output")
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")

	hook := "#!/bin/sh\nprintf 'tracked hook edit\\n' > one.txt\nprintf 'ignored hook edit\\n' > hook-ignored.txt\nexit 1\n"
	writeExecutable(t, filepath.Join(repo.dir, ".git", "hooks", "pre-commit"), hook)

	code, _, stderr := repo.runGraphene(t, "squash", "--no-edit")
	if code == 0 {
		t.Fatal("squash unexpectedly succeeded despite the failing commit hook")
	}
	if !strings.Contains(stderr, "left the squash operation pending to preserve any commit-hook side effects") || !strings.Contains(stderr, "graphene continue") || !strings.Contains(stderr, "graphene abort") {
		t.Fatalf("stderr = %q, want retained-journal recovery guidance", stderr)
	}
	state := readState(t, repo.dir)
	if state.Operation == nil || state.Operation.Kind != "squash" || state.Operation.Active == nil || state.Operation.Active.Kind != "commit" {
		t.Fatalf("state after failed squash = %#v, want pending squash commit", state)
	}
	data, err := os.ReadFile(filepath.Join(repo.dir, "one.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "tracked hook edit\n" {
		t.Fatalf("one.txt after failed hook = %q, want tracked hook edit", got)
	}
	data, err = os.ReadFile(filepath.Join(repo.dir, "hook-ignored.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "ignored hook edit\n" {
		t.Fatalf("hook-ignored.txt after failed hook = %q, want ignored hook edit", got)
	}
	if refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname)", "refs/graphene/journal"); refs == "" {
		t.Fatal("journal backup refs were removed after the failed squash")
	}
}

func TestDuplicateNewBranchRemovesUnpublishedArtifacts(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	code, _, stderr := repo.runGraphene(t, "new", "--branch", "main", "-m", "Duplicate main")
	if code == 0 {
		t.Fatal("graphene new unexpectedly replaced main")
	}
	if !strings.Contains(stderr, "new branch \"main\" already exists") {
		t.Fatalf("stderr = %q, want existing-branch error", stderr)
	}
	if state := readState(t, repo.dir); state.Operation != nil {
		t.Fatalf("operation after rejected new = %#v, want nil", state.Operation)
	}
	grapheneDir, err := (Git{Dir: repo.dir}).GrapheneDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(grapheneDir, "artifacts"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unpublished operation left artifact directories: %v", entries)
	}
}

func TestJournalFinalStackValidationRejectsDroppedRebaseResult(t *testing.T) {
	t.Parallel()
	fixture := newJournalRebaseRecoveryFixture(t)
	state := readState(t, fixture.repo.dir)
	operation := state.Operation
	progress, err := decodeRebaseProgress(operation)
	if err != nil {
		t.Fatal(err)
	}
	progress.Next = len(progress.Steps)
	if err := encodeRebaseProgress(operation, progress); err != nil {
		t.Fatal(err)
	}
	operation.Active = nil
	operation.Phase = operationCommitting
	dropped := runGit(t, fixture.repo.dir, "rev-parse", fixture.onto)
	runGit(t, fixture.repo.dir, "reset", "--hard", dropped)
	snapshot := operation.Refs[fixture.ownedRef]
	snapshot.Expected = RefValue{Exists: true, OID: dropped}
	operation.Refs[fixture.ownedRef] = snapshot
	if err := (Git{Dir: fixture.repo.dir}).WriteState(state); err != nil {
		t.Fatal(err)
	}
	before := readState(t, fixture.repo.dir)

	code, _, stderr := fixture.repo.runGraphene(t, "continue")
	if code == 0 {
		t.Fatal("continue unexpectedly accepted a dropped rebase result")
	}
	if !strings.Contains(stderr, "is not exactly one commit on top of its planned base") {
		t.Fatalf("stderr = %q, want desired-stack invariant error", stderr)
	}
	if got := readState(t, fixture.repo.dir); !reflect.DeepEqual(got, before) {
		t.Fatalf("state changed after invalid final result:\n before: %#v\n  after: %#v", before, got)
	}
	if value := runGit(t, fixture.repo.dir, "rev-parse", fixture.ownedRef); value != dropped {
		t.Fatalf("dropped result changed after refused commit: got %s, want %s", value, dropped)
	}
}

func TestJournalPlanningRejectsMovedRefsBeforeWritingState(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"split", "sync"} {
		t.Run(kind, func(t *testing.T) {
			repo := newTestRepo(t)
			createStackBranch(t, repo, "part.txt", "part\n", "Part")
			stateBefore := readState(t, repo.dir)
			branch := "stack/part"
			plannedBranch := runGit(t, repo.dir, "rev-parse", branch)
			base := runGit(t, repo.dir, "rev-parse", "main")
			commitFile(t, repo.dir, "outside.txt", "outside\n", "outside movement")
			movedBranch := runGit(t, repo.dir, "rev-parse", branch)
			app := &App{git: Git{Dir: repo.dir}}

			var err error
			switch kind {
			case "split":
				err = app.startSplitOperation(stateBefore, branch, branch, "main", base, plannedBranch, stateBefore)
			case "sync":
				err = app.startSyncOperation(
					stateBefore,
					branch,
					branch,
					plannedBranch,
					"main",
					base,
					base,
					false,
					nil,
					map[string]string{branch: plannedBranch},
					map[string]string{branch: plannedBranch},
					nil,
				)
			}
			if err == nil {
				t.Fatalf("%s planning unexpectedly accepted branch movement", kind)
			}
			if !strings.Contains(err.Error(), "planned branch \"stack/part\" moved") {
				t.Fatalf("%s planning error = %q, want stale-plan error", kind, err)
			}
			if value := runGit(t, repo.dir, "rev-parse", branch); value != movedBranch {
				t.Fatalf("%s planning changed moved branch: got %s, want %s", kind, value, movedBranch)
			}
			if got := readState(t, repo.dir); !reflect.DeepEqual(got, stateBefore) {
				t.Fatalf("state changed after refused %s planning:\n before: %#v\n  after: %#v", kind, stateBefore, got)
			}
			if refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname)", "refs/graphene/journal"); refs != "" {
				t.Fatalf("%s planning installed journal refs: %q", kind, refs)
			}
		})
	}
}
