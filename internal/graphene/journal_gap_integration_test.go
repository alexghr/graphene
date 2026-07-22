package graphene

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestJournalAbortReconcilesUncheckpointedConfigDeletion(t *testing.T) {
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
	if len(original) == 0 {
		t.Fatal("fixture branch config is empty")
	}

	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("delete", worktree, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreNone
	operation.Phase = operationApplying
	operation.Configs = []JournalConfig{{
		Section:  "branch.stack/config",
		Original: append([]ConfigEntry(nil), original...),
		Expected: append([]ConfigEntry(nil), original...),
	}}
	operation.Active = &JournalAction{
		ID:   "delete-config:branch.stack/config",
		Kind: "delete-config",
	}

	if err := git.RemoveBranchConfig("stack/config"); err != nil {
		t.Fatal(err)
	}
	if err := git.WriteState(State{Operation: operation}); err != nil {
		t.Fatal(err)
	}

	expectGrapheneOK(t, repo, "abort")

	got, err := git.BranchConfig("stack/config")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("branch config after abort = %#v, want %#v", got, original)
	}
	if state := readState(t, repo.dir); state.Operation != nil {
		t.Fatalf("operation after abort = %#v, want nil", state.Operation)
	}
}

func TestJournalResumedAbortDistinguishesPartialAndReorderedConfig(t *testing.T) {
	t.Parallel()

	t.Run("ordered prefix resumes", func(t *testing.T) {
		repo := newTestRepo(t)
		git := Git{Dir: repo.dir}
		original := writeOrderedBranchConfig(t, repo, git)
		operation := rollingBackConfigOperation(t, git, original)
		if err := git.RestoreBranchConfig("branch.stack/config", original[:2], original); err != nil {
			t.Fatal(err)
		}
		if err := git.WriteState(State{Operation: operation}); err != nil {
			t.Fatal(err)
		}

		expectGrapheneOK(t, repo, "abort")
		actual, err := git.BranchConfig("stack/config")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, original) {
			t.Fatalf("config after resumed abort = %#v, want %#v", actual, original)
		}
	})

	t.Run("reorder requires force and is preserved", func(t *testing.T) {
		repo := newTestRepo(t)
		git := Git{Dir: repo.dir}
		original := writeOrderedBranchConfig(t, repo, git)
		operation := rollingBackConfigOperation(t, git, original)
		reordered := []ConfigEntry{original[2], original[1], original[0]}
		if err := git.RestoreBranchConfig("branch.stack/config", reordered, original); err != nil {
			t.Fatal(err)
		}
		if err := git.WriteState(State{Operation: operation}); err != nil {
			t.Fatal(err)
		}

		code, stdout, stderr := repo.runGraphene(t, "abort")
		if code == 0 || stdout != "" || !strings.Contains(stderr, "operation-owned config changed") || !strings.Contains(stderr, "abort --force") {
			t.Fatalf("plain abort = (%d, %q, %q), want config drift refusal", code, stdout, stderr)
		}
		code, stdout, stderr = repo.runGraphene(t, "abort", "--force")
		if code != 0 || stderr != "" || !strings.Contains(stdout, "Preserved displaced config:") {
			t.Fatalf("forced abort = (%d, %q, %q), want preserved config", code, stdout, stderr)
		}

		grapheneDir, err := git.GrapheneDir()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(grapheneDir, "recovery", operation.ID, "config.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		recovery, err := decodeConfigRecoveryFile(data, operation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(recovery.Configs) != 1 || !reflect.DeepEqual(recovery.Configs[0].Entries, reordered) {
			t.Fatalf("preserved config = %#v, want reordered entries %#v", recovery.Configs, reordered)
		}
	})

	t.Run("preserved partial removal resumes", func(t *testing.T) {
		repo := newTestRepo(t)
		git := Git{Dir: repo.dir}
		original := writeOrderedBranchConfig(t, repo, git)
		operation := rollingBackConfigOperation(t, git, original)
		displaced := []ConfigEntry{original[2], original[1], original[0]}
		if err := git.RestoreBranchConfig("branch.stack/config", displaced, original); err != nil {
			t.Fatal(err)
		}
		app := &App{git: git}
		if err := app.preserveUnexpectedConfigs(operation, []ConfigDrift{{
			Section: "branch.stack/config",
			Actual:  displaced,
		}}); err != nil {
			t.Fatal(err)
		}
		if operation.RecoveryArtifact == "" {
			t.Fatal("preserved config did not record its recovery artifact")
		}
		if err := git.WriteState(State{Operation: operation}); err != nil {
			t.Fatal(err)
		}

		// Older recovery removed values through separate config rewrites. Model a
		// crash after the first removal, leaving a non-prefix subsequence behind.
		runGit(t, repo.dir, "config", "--local", "--fixed-value", "--unset-all", displaced[0].Key, displaced[0].Value)
		partial, err := git.BranchConfig("stack/config")
		if err != nil {
			t.Fatal(err)
		}
		if configEntriesPrefix(partial, original) || !configEntriesSubsequence(partial, displaced) {
			t.Fatalf("partial removal fixture = %#v, want a non-prefix subsequence", partial)
		}

		code, stdout, stderr := repo.runGraphene(t, "abort", "--force")
		if code != 0 || stderr != "" || !strings.Contains(stdout, "Preserved displaced config:") {
			t.Fatalf("resumed forced abort = (%d, %q, %q)", code, stdout, stderr)
		}
		actual, err := git.BranchConfig("stack/config")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, original) {
			t.Fatalf("config after resumed forced abort = %#v, want %#v", actual, original)
		}

		grapheneDir, err := git.GrapheneDir()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(grapheneDir, "recovery", operation.ID, "config.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		recovery, err := decodeConfigRecoveryFile(data, operation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(recovery.Configs) != 1 || !reflect.DeepEqual(recovery.Configs[0].Entries, displaced) {
			t.Fatalf("preserved config changed to %#v, want original displacement %#v", recovery.Configs, displaced)
		}
	})
}

func writeOrderedBranchConfig(t *testing.T, repo testRepo, git Git) []ConfigEntry {
	t.Helper()
	runGit(t, repo.dir, "config", "--local", "branch.stack/config.remote", "origin-one")
	runGit(t, repo.dir, "config", "--local", "branch.stack/config.merge", "refs/heads/stack/config")
	runGit(t, repo.dir, "config", "--local", "--add", "branch.stack/config.remote", "origin-two")
	entries, err := git.BranchConfig("stack/config")
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func rollingBackConfigOperation(t *testing.T, git Git, original []ConfigEntry) *OperationJournal {
	t.Helper()
	head, err := git.Head()
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("delete", worktree, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreNone
	operation.Phase = operationRollingBack
	operation.Configs = []JournalConfig{{
		Section:  "branch.stack/config",
		Original: append([]ConfigEntry(nil), original...),
	}}
	return operation
}

func TestJournalFinalValidationRejectsUnchangedChildAfterSuccessfulParentRewrite(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	main := runGit(t, repo.dir, "rev-parse", "main")

	runGit(t, repo.dir, "switch", "-c", "stack/parent")
	originalParent := commitFile(t, repo.dir, "parent.txt", "parent v1\n", "Parent v1")
	runGit(t, repo.dir, "switch", "-c", "stack/child")
	originalChild := commitFile(t, repo.dir, "child.txt", "child\n", "Child")

	runGit(t, repo.dir, "switch", "main")
	runGit(t, repo.dir, "switch", "-c", "rewritten-parent")
	rewrittenParent := commitFile(t, repo.dir, "parent.txt", "parent v2\n", "Parent v2")
	runGit(t, repo.dir, "update-ref", "refs/heads/stack/parent", rewrittenParent, originalParent)

	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	stacks := []Stack{{Base: "main", Branches: []string{"stack/parent", "stack/child"}}}
	operation, err := newOperationJournal("sync", worktree, "rewritten-parent", rewrittenParent, stacks)
	if err != nil {
		t.Fatal(err)
	}
	operation.DesiredStacks = cloneStacks(stacks)
	operation.Refs["refs/heads/stack/parent"] = JournalRef{
		Original: RefValue{Exists: true, OID: originalParent},
		Expected: RefValue{Exists: true, OID: rewrittenParent},
	}
	operation.Refs["refs/heads/stack/child"] = JournalRef{
		Original: RefValue{Exists: true, OID: originalChild},
		Expected: RefValue{Exists: true, OID: originalChild},
	}
	operation.ValidationRefs = map[string]RefValue{
		"refs/heads/main": {Exists: true, OID: main},
	}
	operation.ValidationRefsComplete = true
	progress := syncJournalProgress{
		Base:           "main",
		OriginalBranch: "stack/child",
		BaseOld:        main,
		BaseNew:        main,
		BaseDone:       true,
		Components: []syncJournalComponent{
			{
				Names:    []string{"stack/parent"},
				Branches: []string{"stack/parent"},
				Ops: []RebaseOp{{
					Onto: main, Upstream: main, Top: "stack/parent",
				}},
				RefNames: []string{"refs/heads/stack/parent"},
				Status:   syncComponentSucceeded,
				NextOp:   1,
			},
			{
				Names:    []string{"stack/child"},
				Branches: []string{"stack/child"},
				Ops: []RebaseOp{{
					Onto: rewrittenParent, Upstream: originalParent, Top: "stack/child",
				}},
				RefNames: []string{"refs/heads/stack/child"},
				Status:   syncComponentSucceeded,
				NextOp:   1,
			},
		},
		NextComponent: 2,
	}
	if err := encodeSyncProgress(operation, progress); err != nil {
		t.Fatal(err)
	}

	err = (&App{git: git}).validateOperationDesiredStacks(operation)
	if err == nil {
		t.Fatal("final validation unexpectedly accepted an unchanged child after its parent was rewritten")
	}
	if !strings.Contains(err.Error(), "branch \"stack/child\" is not exactly one commit on top of its planned base \"stack/parent\"") {
		t.Fatalf("validation error = %q, want stale-child topology error", err)
	}
}

func TestJournalValidationRefDriftSurvivesAbort(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	originalMain := runGit(t, repo.dir, "rev-parse", "main")

	runGit(t, repo.dir, "switch", "-c", "stack/owned")
	owned := commitFile(t, repo.dir, "owned.txt", "owned\n", "Owned")
	runGit(t, repo.dir, "switch", "--detach", originalMain)
	movedMain := commitFile(t, repo.dir, "main-next.txt", "next\n", "Move main")
	runGit(t, repo.dir, "switch", "stack/owned")

	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	stacks := []Stack{{Base: "main", Branches: []string{"stack/owned"}}}
	operation, err := newOperationJournal("delete", worktree, "stack/owned", owned, stacks)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreNone
	operation.Phase = operationCommitting
	operation.DesiredStacks = cloneStacks(stacks)
	ownedValue := RefValue{Exists: true, OID: owned}
	operation.Refs["refs/heads/stack/owned"] = JournalRef{Original: ownedValue, Expected: ownedValue}
	operation.ValidationRefs = map[string]RefValue{
		"refs/heads/main": {Exists: true, OID: originalMain},
	}
	operation.ValidationRefsComplete = true
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}
	if err := git.WriteState(State{Stacks: cloneStacks(stacks), Operation: operation}); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.dir, "update-ref", "refs/heads/main", movedMain, originalMain)

	before := readState(t, repo.dir)
	code, _, stderr := repo.runGraphene(t, "continue")
	if code == 0 {
		t.Fatal("continue unexpectedly committed after a validation ref moved")
	}
	if !strings.Contains(stderr, "stack dependency refs changed") || !strings.Contains(stderr, "refs/heads/main") {
		t.Fatalf("stderr = %q, want validation-ref drift error", stderr)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, before) {
		t.Fatalf("state changed after refused commit:\n before: %#v\n  after: %#v", before, got)
	}

	expectGrapheneOK(t, repo, "abort")

	if got := runGit(t, repo.dir, "rev-parse", "main"); got != movedMain {
		t.Fatalf("validation ref main after abort = %s, want independently moved value %s", got, movedMain)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/owned"); got != owned {
		t.Fatalf("owned ref after abort = %s, want %s", got, owned)
	}
	if state := readState(t, repo.dir); state.Operation != nil {
		t.Fatalf("operation after abort = %#v, want nil", state.Operation)
	}
}

func TestWorktreeRestoreIndexPreservesSplitIndexModes(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	runGit(t, repo.dir, "config", "core.sharedRepository", "group")
	runGit(t, repo.dir, "update-index", "--split-index")

	indexPath, err := git.GitPath("index")
	if err != nil {
		t.Fatal(err)
	}
	sharedPath := runGit(t, repo.dir, "rev-parse", "--shared-index-path")
	if sharedPath == "" {
		t.Skip("Git did not create a split-index shared file")
	}
	if !filepath.IsAbs(sharedPath) {
		sharedPath = filepath.Join(repo.dir, sharedPath)
	}
	sharedPath = filepath.Clean(sharedPath)

	const indexMode = os.FileMode(0o640)
	const sharedMode = os.FileMode(0o440)
	if err := os.Chmod(indexPath, indexMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sharedPath, sharedMode); err != nil {
		t.Fatal(err)
	}

	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("amend", worktree, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	app := &App{git: git}
	if err := app.snapshotOperationIndex(operation); err != nil {
		t.Fatal(err)
	}
	artifacts, err := app.loadOperationArtifacts(operation)
	if err != nil {
		t.Fatal(err)
	}
	if operation.SharedIndexPath == "" || len(artifacts.SharedIndex) == 0 {
		t.Fatal("snapshot did not retain the split-index shared file")
	}

	if err := os.Chmod(indexPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sharedPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.restoreOperationIndex(operation, artifacts.Index, artifacts.SharedIndex); err != nil {
		t.Fatal(err)
	}

	assertJournalFileMode(t, indexPath, indexMode)
	assertJournalFileMode(t, sharedPath, sharedMode)
}

func TestJournalPreBoundaryFingerprintDriftRequiresForcedAbort(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	app := &App{git: git}
	head := runGit(t, repo.dir, "rev-parse", "main")
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("delete", worktree, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	if err := app.prepareOperationWorktree(operation); err != nil {
		t.Fatal(err)
	}
	value := RefValue{Exists: true, OID: head}
	operation.Refs["refs/heads/main"] = JournalRef{Original: value, Expected: value}
	progress := refMutationProgress{
		RefNames:  []string{"refs/heads/main"},
		RefsAfter: map[string]RefValue{"refs/heads/main": value},
	}
	if err := encodeRefMutationProgress(operation, progress); err != nil {
		t.Fatal(err)
	}
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}
	operation.Phase = operationApplying
	if err := git.WriteState(State{Operation: operation}); err != nil {
		t.Fatal(err)
	}
	if operation.WorktreeBoundaryCrossed {
		t.Fatal("fixture unexpectedly crossed its worktree boundary")
	}

	writeFile(t, repo.dir, "file.txt", "independent change after journal snapshot\n")
	stateBefore := readState(t, repo.dir)
	code, _, stderr := repo.runGraphene(t, "continue")
	if code == 0 {
		t.Fatal("continue unexpectedly accepted pre-boundary worktree drift")
	}
	if !strings.Contains(stderr, "worktree or index changed after Graphene took its recovery snapshot") {
		t.Fatalf("continue stderr = %q, want fingerprint drift error", stderr)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, stateBefore) {
		t.Fatalf("state changed after refused continue:\n before: %#v\n  after: %#v", stateBefore, got)
	}

	code, _, stderr = repo.runGraphene(t, "abort")
	if code == 0 {
		t.Fatal("abort unexpectedly discarded pre-boundary worktree drift without --force")
	}
	if !strings.Contains(stderr, "use graphene abort --force") {
		t.Fatalf("abort stderr = %q, want forced-abort guidance", stderr)
	}
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, stateBefore) {
		t.Fatalf("state changed after refused abort:\n before: %#v\n  after: %#v", stateBefore, got)
	}
	data, err := os.ReadFile(filepath.Join(repo.dir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "independent change after journal snapshot\n" {
		t.Fatalf("drifted worktree file = %q, want independent change", data)
	}

	expectGrapheneOK(t, repo, "abort", "--force")
	data, err = os.ReadFile(filepath.Join(repo.dir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "base\n" {
		t.Fatalf("file after forced abort = %q, want pre-operation contents", data)
	}
	if state := readState(t, repo.dir); state.Operation != nil {
		t.Fatalf("operation after forced abort = %#v, want nil", state.Operation)
	}
}

func assertJournalFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %#o, want %#o", path, got, want)
	}
}
