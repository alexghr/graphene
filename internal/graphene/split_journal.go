package graphene

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

type splitJournalProgress struct {
	Target       string              `json:"target"`
	OriginalBase string              `json:"originalBase"`
	OriginalHead string              `json:"originalHead"`
	ReturnBranch string              `json:"returnBranch"`
	ResetDone    bool                `json:"resetDone,omitempty"`
	Branches     []string            `json:"branches"`
	DraftStacks  []Stack             `json:"draftStacks"`
	Commit       *splitCommitJournal `json:"commit,omitempty"`
}

type splitCommitJournal struct {
	Branch         string   `json:"branch"`
	PreviousBranch string   `json:"previousBranch"`
	ReuseCurrent   bool     `json:"reuseCurrent,omitempty"`
	Temporary      bool     `json:"temporary,omitempty"`
	BranchPrefix   string   `json:"branchPrefix,omitempty"`
	FinalBranch    string   `json:"finalBranch,omitempty"`
	StageAll       bool     `json:"stageAll,omitempty"`
	StageUpdate    bool     `json:"stageUpdate,omitempty"`
	Args           []string `json:"args"`
	Created        bool     `json:"created,omitempty"`
	Prepared       bool     `json:"prepared,omitempty"`
	Committed      bool     `json:"committed,omitempty"`
}

func encodeSplitProgress(operation *OperationJournal, progress splitJournalProgress) error {
	data, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	operation.Progress = data
	return nil
}

func decodeSplitProgress(operation *OperationJournal) (splitJournalProgress, error) {
	var progress splitJournalProgress
	decoder := json.NewDecoder(bytes.NewReader(operation.Progress))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&progress); err != nil {
		return progress, fmt.Errorf("parse pending split: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return progress, fmt.Errorf("parse pending split: %w", err)
	}
	if progress.Target == "" || progress.OriginalBase == "" || progress.OriginalHead == "" || progress.ReturnBranch == "" {
		return progress, fmt.Errorf("pending split is missing its original branch data")
	}
	for name, branch := range map[string]string{"target": progress.Target, "base": progress.OriginalBase, "return branch": progress.ReturnBranch} {
		if !validBranchArgument(branch) {
			return progress, fmt.Errorf("pending split has invalid %s %q", name, branch)
		}
	}
	if !validObjectID(progress.OriginalHead) {
		return progress, fmt.Errorf("pending split has invalid original HEAD %q", progress.OriginalHead)
	}
	if len(progress.Branches) == 0 || progress.Branches[0] != progress.Target {
		return progress, fmt.Errorf("pending split has an invalid branch list")
	}
	for _, branch := range progress.Branches {
		if !validBranchArgument(branch) {
			return progress, fmt.Errorf("pending split has invalid branch %q", branch)
		}
		if _, ok := operation.Refs["refs/heads/"+branch]; !ok {
			return progress, fmt.Errorf("pending split does not own branch %q", branch)
		}
	}
	for _, branch := range []string{progress.Target, progress.OriginalBase} {
		if _, ok := operation.Refs["refs/heads/"+branch]; !ok {
			return progress, fmt.Errorf("pending split does not own branch %q", branch)
		}
	}
	if progress.DraftStacks == nil {
		return progress, fmt.Errorf("pending split is missing draft stacks")
	}
	if progress.Commit != nil {
		commit := progress.Commit
		if !validBranchArgument(commit.Branch) || !validBranchArgument(commit.PreviousBranch) || len(commit.Args) == 0 {
			return progress, fmt.Errorf("pending split has an invalid commit")
		}
		if err := validateJournalCommitArgs("new", commit.Args); err != nil {
			return progress, err
		}
		if _, ok := operation.Refs["refs/heads/"+commit.Branch]; !ok {
			return progress, fmt.Errorf("pending split does not own commit branch %q", commit.Branch)
		}
		if _, ok := operation.Refs["refs/heads/"+commit.PreviousBranch]; !ok {
			return progress, fmt.Errorf("pending split does not own previous branch %q", commit.PreviousBranch)
		}
		if commit.FinalBranch != "" && !validBranchArgument(commit.FinalBranch) {
			return progress, fmt.Errorf("pending split has invalid final branch %q", commit.FinalBranch)
		}
	}
	return progress, nil
}

func (a *App) startSplitOperation(state State, current, target, base, baseHead, originalHead string, draft State) (resultErr error) {
	if err := a.requirePlannedBranchRefs(map[string]string{target: originalHead, base: baseHead}); err != nil {
		return fmt.Errorf("cannot start split: %w", err)
	}
	worktree, err := a.git.WorktreeID()
	if err != nil {
		return err
	}
	head, err := a.git.Head()
	if err != nil {
		return err
	}
	operation, err := newOperationJournal("split", worktree, current, head, state.Stacks)
	if err != nil {
		return err
	}
	operation.WorktreePolicy = worktreeRestoreHard
	operation.DesiredStacks = cloneStacks(state.Stacks)
	artifactDir, err := a.operationArtifactDir(operation)
	if err != nil {
		return err
	}
	publicationAttempted := false
	defer func() {
		cleanupUnpublishedOperationArtifacts(artifactDir, publicationAttempted, &resultErr)
	}()
	if err := a.prepareOperationWorktree(operation); err != nil {
		return err
	}
	localRefs, err := a.git.LocalHeadRefValues()
	if err != nil {
		return err
	}
	for ref, value := range localRefs {
		operation.Refs[ref] = JournalRef{Original: value, Expected: value}
	}
	progress := splitJournalProgress{
		Target:       target,
		OriginalBase: base,
		OriginalHead: originalHead,
		ReturnBranch: current,
		Branches:     []string{target},
		DraftStacks:  cloneStacks(draft.Stacks),
	}
	if err := a.snapshotOperationValidationRefs(operation, operation.OriginalStacks, operation.DesiredStacks); err != nil {
		return err
	}
	if err := a.requirePlannedBranchRefs(map[string]string{target: originalHead, base: baseHead}); err != nil {
		return fmt.Errorf("cannot start split: %w", err)
	}
	if err := a.verifyUnpublishedOperationWorktree(operation); err != nil {
		return fmt.Errorf("cannot start split: %w", err)
	}
	if err := encodeSplitProgress(operation, progress); err != nil {
		return err
	}
	if err := prepareOperationBackups(operation); err != nil {
		return err
	}
	state.Pending = nil
	state.Operation = operation
	publicationAttempted = true
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	if err := a.git.InstallOperationBackups(operation); err != nil {
		return err
	}
	operation.Phase = operationInteractive
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.continueSplitOperation(state, continueOptions{})
}

func (a *App) continueSplitReset(state State, progress *splitJournalProgress) error {
	operation := state.Operation
	if progress.ResetDone {
		return nil
	}
	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	if current != progress.Target {
		if operation.Active != nil {
			return fmt.Errorf("cannot switch to split target while journal action %q is active", operation.Active.ID)
		}
		if err := a.git.RunOperationSwitch(progress.Target); err != nil {
			return err
		}
	}
	ref := "refs/heads/" + progress.Target
	snapshot := operation.Refs[ref]
	before := snapshot.Expected
	baseSnapshot, ok := operation.Refs["refs/heads/"+progress.OriginalBase]
	if !ok {
		return fmt.Errorf("split journal does not own base %q", progress.OriginalBase)
	}
	baseValue := baseSnapshot.Expected
	if !baseValue.Exists {
		return fmt.Errorf("split base %q no longer exists", progress.OriginalBase)
	}
	const actionID = "split-reset"
	if operation.Active == nil {
		drift, _, err := a.git.OperationRefDrift(operation)
		if err != nil {
			return err
		}
		if len(drift) > 0 {
			return fmt.Errorf("cannot reset split target because operation-owned refs changed:\n%s", formatRefDrift(drift))
		}
		actual, err := a.git.RefValue(ref)
		if err != nil {
			return err
		}
		if actual != before {
			return fmt.Errorf("cannot reset split target because it moved from %s to %s", formatRefValue(before), formatRefValue(actual))
		}
		operation.Active = &JournalAction{
			ID:         actionID,
			Kind:       "reset",
			RefsBefore: map[string]RefValue{ref: before},
			RefsAfter:  map[string]RefValue{ref: baseValue},
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	} else if operation.Active.ID != actionID || operation.Active.Kind != "reset" {
		return fmt.Errorf("cannot reset split target while journal action %q is active", operation.Active.ID)
	}
	actual, err := a.git.RefValue(ref)
	if err != nil {
		return err
	}
	if actual == baseValue {
		snapshot.Expected = baseValue
		operation.Refs[ref] = snapshot
		operation.Active = nil
		progress.ResetDone = true
		if err := encodeSplitProgress(operation, *progress); err != nil {
			return err
		}
		return a.git.WriteState(state)
	}
	if actual != before {
		return fmt.Errorf("cannot recover split reset because %q is at %s", progress.Target, formatRefValue(actual))
	}
	if err := a.git.RunOperationReset("-N", baseValue.OID); err != nil {
		return err
	}
	return a.continueSplitReset(state, progress)
}

func (a *App) newDuringSplitOperation(opts commitOptions, current string, state State) error {
	operation := state.Operation
	progress, err := decodeSplitProgress(operation)
	if err != nil {
		return err
	}
	if operation.Phase != operationInteractive || !progress.ResetDone {
		return fmt.Errorf("split is not ready for a new commit")
	}
	if progress.Commit != nil {
		return fmt.Errorf("a split commit is already pending; use graphene continue or graphene abort")
	}
	if opts.base != "" {
		return fmt.Errorf("graphene new --base cannot be used during graphene split")
	}
	top := progress.Branches[len(progress.Branches)-1]
	if current != top {
		return fmt.Errorf("split in progress for %q; switch to %q before graphene new", progress.Target, top)
	}
	targetCount, err := a.commitCount(progress.OriginalBase, progress.Target)
	if err != nil {
		return err
	}
	if opts.reuseCurrent {
		if opts.branch != "" {
			return fmt.Errorf("graphene new --reuse-current cannot use --branch")
		}
		if current != progress.Target {
			return fmt.Errorf("only the first split commit can use --reuse-current")
		}
		if targetCount != 0 {
			return fmt.Errorf("the first split commit already exists; use graphene new without --reuse-current for the next split part")
		}
	} else if targetCount == 0 {
		return fmt.Errorf("the first split commit must use graphene new --reuse-current")
	}

	branch := current
	temporary := false
	var cfg Config
	if !opts.reuseCurrent {
		branch = opts.branch
		if branch == "" {
			cfg, err = a.loadConfig()
			if err != nil {
				return err
			}
			if err := a.ensureBranchPrefixAvailable(cfg.BranchPrefix); err != nil {
				return err
			}
			branch = tempBranchName(os.Getpid(), time.Now().UnixNano())
			temporary = true
		}
		ref := "refs/heads/" + branch
		actual, err := a.git.RefValue(ref)
		if err != nil {
			return err
		}
		if actual.Exists {
			return fmt.Errorf("branch %q already exists", branch)
		}
		operation.Refs[ref] = JournalRef{Original: RefValue{}, Expected: RefValue{}}
	}
	progress.Commit = &splitCommitJournal{
		Branch:         branch,
		PreviousBranch: current,
		ReuseCurrent:   opts.reuseCurrent,
		Temporary:      temporary,
		BranchPrefix:   cfg.BranchPrefix,
		FinalBranch:    branch,
		StageAll:       opts.stageAll,
		StageUpdate:    opts.stageUpdate,
		Args:           append([]string{"commit"}, opts.commitArgs...),
	}
	if temporary {
		progress.Commit.FinalBranch = ""
	}
	if err := encodeSplitProgress(operation, progress); err != nil {
		return err
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.continueSplitCommit(state, &progress, continueOptions{})
}

func (a *App) continueSplitOperation(state State, opts continueOptions) error {
	if state.Operation.Phase == operationPreparing {
		if err := a.git.InstallOperationBackups(state.Operation); err != nil {
			return err
		}
		state.Operation.Phase = operationInteractive
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	if err := a.crossOperationWorktreeBoundary(state); err != nil {
		return err
	}
	progress, err := decodeSplitProgress(state.Operation)
	if err != nil {
		return err
	}
	if !progress.ResetDone {
		return a.continueSplitReset(state, &progress)
	}
	if progress.Commit != nil {
		return a.continueSplitCommit(state, &progress, opts)
	}
	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("split in progress; use graphene new to commit split parts or graphene abort")
	}
	return a.finishSplitOperation(state, progress)
}

func (a *App) continueSplitCommit(state State, progress *splitJournalProgress, opts continueOptions) error {
	operation := state.Operation
	commit := progress.Commit
	ref := "refs/heads/" + commit.Branch
	if operation.Active != nil && operation.Active.Kind == "cleanup-commit" {
		if err := a.finishFailedSplitCommit(state, progress); err != nil {
			return err
		}
		return fmt.Errorf("the previous split commit failed and was rolled back; rerun graphene new")
	}
	if !commit.ReuseCurrent && !commit.Created {
		before := RefValue{}
		parent := operation.Refs["refs/heads/"+commit.PreviousBranch].Expected
		actual, err := a.git.RefValue(ref)
		if err != nil {
			return err
		}
		actionID := "split-create:" + commit.Branch
		if operation.Active == nil {
			if actual != before {
				return fmt.Errorf("cannot create split branch %q at %s", commit.Branch, formatRefValue(actual))
			}
			operation.Active = &JournalAction{ID: actionID, Kind: "create-branch", RefsBefore: map[string]RefValue{ref: before}, RefsAfter: map[string]RefValue{ref: parent}}
			if err := a.git.WriteState(state); err != nil {
				return err
			}
		} else if operation.Active.ID != actionID || operation.Active.Kind != "create-branch" {
			return fmt.Errorf("cannot create split branch while journal action %q is active", operation.Active.ID)
		}
		switch actual {
		case parent:
			current, err := a.git.CurrentBranch()
			if err != nil {
				return err
			}
			if current != commit.Branch {
				if err := a.git.RunOperationSwitch(commit.Branch); err != nil {
					return err
				}
			}
			snapshot := operation.Refs[ref]
			snapshot.Expected = parent
			operation.Refs[ref] = snapshot
			operation.Active = nil
			commit.Created = true
			if err := encodeSplitProgress(operation, *progress); err != nil {
				return err
			}
			if err := a.git.WriteState(state); err != nil {
				return err
			}
		case before:
			if err := a.git.RunOperationSwitch("-c", commit.Branch, parent.OID); err != nil {
				return err
			}
			return a.continueSplitCommit(state, progress, opts)
		default:
			return fmt.Errorf("cannot recover split branch creation at %s", formatRefValue(actual))
		}
	}
	if !commit.Prepared {
		current, err := a.git.CurrentBranch()
		if err != nil {
			return err
		}
		if current != commit.Branch {
			return fmt.Errorf("pending split commit belongs to branch %q, currently on %q", commit.Branch, current)
		}
		stage := commitOptions{stageAll: commit.StageAll, stageUpdate: commit.StageUpdate}
		if err := a.stageRequestedChanges(stage); err != nil {
			return err
		}
		commit.Prepared = true
		if err := encodeSplitProgress(operation, *progress); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	if !commit.Committed {
		before := operation.Refs[ref].Expected
		actual, err := a.git.RefValue(ref)
		if err != nil {
			return err
		}
		actionID := "split-commit:" + commit.Branch
		if operation.Active == nil {
			if actual != before {
				return fmt.Errorf("split commit branch %q moved to %s", commit.Branch, formatRefValue(actual))
			}
			operation.Active = &JournalAction{ID: actionID, Kind: "commit", RefsBefore: map[string]RefValue{ref: before}}
			if err := a.git.WriteState(state); err != nil {
				return err
			}
		} else if operation.Active.ID != actionID || operation.Active.Kind != "commit" {
			return fmt.Errorf("cannot commit split part while journal action %q is active", operation.Active.ID)
		}
		actual, err = a.git.RefValue(ref)
		if err != nil {
			return err
		}
		if actual != before {
			if !opts.acceptCurrent {
				return fmt.Errorf("git may have completed %s; inspect %q, then run graphene continue --accept-current or graphene abort --force", actionID, commit.Branch)
			}
			if err := a.validateJournalCommitResult(journalCommit{Branch: commit.Branch, Mode: "new"}, before, actual); err != nil {
				return err
			}
		} else {
			current, err := a.git.CurrentBranch()
			if err != nil {
				return err
			}
			if current != commit.Branch {
				return fmt.Errorf("pending split commit belongs to branch %q, currently on %q", commit.Branch, current)
			}
			marker := fmt.Sprintf("graphene:%s:%s", operation.ID, actionID)
			if err := a.git.RunWithEnv(map[string]string{"GIT_REFLOG_ACTION": marker}, commit.Args...); err != nil {
				currentValue, refErr := a.git.RefValue(ref)
				if refErr != nil {
					return fmt.Errorf("%w; additionally failed to inspect split commit ref: %v", err, refErr)
				}
				if currentValue != before {
					return err
				}
				after := before
				if !commit.ReuseCurrent && commit.Created {
					after = RefValue{}
				}
				operation.Active = &JournalAction{
					ID:         "split-cleanup:" + commit.Branch,
					Kind:       "cleanup-commit",
					RefsBefore: map[string]RefValue{ref: before},
					RefsAfter:  map[string]RefValue{ref: after},
				}
				if writeErr := a.git.WriteState(state); writeErr != nil {
					return fmt.Errorf("%w; additionally failed to checkpoint split recovery: %v", err, writeErr)
				}
				if cleanupErr := a.finishFailedSplitCommit(state, progress); cleanupErr != nil {
					return fmt.Errorf("%w; additionally failed to roll back the split commit: %v", err, cleanupErr)
				}
				return err
			}
			actual, err = a.git.RefValue(ref)
			if err != nil {
				return err
			}
			if err := a.validateJournalCommitResult(journalCommit{Branch: commit.Branch, Mode: "new"}, before, actual); err != nil {
				return err
			}
		}
		snapshot := operation.Refs[ref]
		snapshot.Expected = actual
		operation.Refs[ref] = snapshot
		operation.Active = nil
		commit.Committed = true
		if err := encodeSplitProgress(operation, *progress); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	finalBranch, err := a.finishSplitCommitBranchName(state, progress)
	if err != nil {
		return err
	}
	if !commit.ReuseCurrent {
		draft := State{Stacks: cloneStacks(progress.DraftStacks)}
		if err := draft.AddCommit(commit.PreviousBranch, finalBranch); err != nil {
			return err
		}
		progress.DraftStacks = draft.Stacks
		progress.Branches = append(progress.Branches, finalBranch)
	}
	progress.Commit = nil
	if err := encodeSplitProgress(operation, *progress); err != nil {
		return err
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return nil
	}
	return a.finishSplitOperation(state, *progress)
}

func (a *App) finishFailedSplitCommit(state State, progress *splitJournalProgress) error {
	operation := state.Operation
	commit := progress.Commit
	if commit == nil || operation.Active == nil || operation.Active.Kind != "cleanup-commit" || operation.Active.ID != "split-cleanup:"+commit.Branch {
		return fmt.Errorf("pending split has invalid failed-commit cleanup")
	}
	ref := "refs/heads/" + commit.Branch
	before := operation.Active.RefsBefore[ref]
	after := operation.Active.RefsAfter[ref]
	actual, err := a.git.RefValue(ref)
	if err != nil {
		return err
	}
	if actual != before && actual != after {
		return fmt.Errorf("cannot recover failed split commit because %s changed from %s to %s", ref, formatRefValue(before), formatRefValue(actual))
	}
	if !commit.ReuseCurrent && commit.Created {
		current, err := a.git.Output("branch", "--show-current")
		if err != nil {
			return err
		}
		if current == commit.Branch {
			if err := a.git.RunOperationSwitch(commit.PreviousBranch); err != nil {
				return err
			}
		} else if current != commit.PreviousBranch {
			return fmt.Errorf("cannot recover failed split commit from branch %q", current)
		}
		if actual == before {
			if err := a.git.UpdateRefs([]refEdit{{Ref: ref, Old: before}}); err != nil {
				return err
			}
		}
		snapshot := operation.Refs[ref]
		snapshot.Expected = RefValue{}
		operation.Refs[ref] = snapshot
	}
	operation.Active = nil
	progress.Commit = nil
	if err := encodeSplitProgress(operation, *progress); err != nil {
		return err
	}
	return a.git.WriteState(state)
}

func (a *App) finishSplitCommitBranchName(state State, progress *splitJournalProgress) (string, error) {
	operation := state.Operation
	commit := progress.Commit
	if !commit.Temporary {
		return commit.Branch, nil
	}
	if commit.FinalBranch == "" {
		subject, err := a.git.Output("log", "-1", "--format=%s", commit.Branch)
		if err != nil {
			return "", err
		}
		finalBranch, err := a.derivedBranchNameWithState(Config{BranchPrefix: commit.BranchPrefix}, State{Stacks: progress.DraftStacks}, subject, nil)
		if err != nil {
			return "", err
		}
		finalRef := "refs/heads/" + finalBranch
		actual, err := a.git.RefValue(finalRef)
		if err != nil {
			return "", err
		}
		if actual.Exists {
			return "", fmt.Errorf("derived split branch %q already exists", finalBranch)
		}
		operation.Refs[finalRef] = JournalRef{Original: RefValue{}, Expected: RefValue{}}
		commit.FinalBranch = finalBranch
		if err := encodeSplitProgress(operation, *progress); err != nil {
			return "", err
		}
		if err := a.git.WriteState(state); err != nil {
			return "", err
		}
	}
	tempRef := "refs/heads/" + commit.Branch
	finalRef := "refs/heads/" + commit.FinalBranch
	tempSnapshot := operation.Refs[tempRef]
	finalSnapshot := operation.Refs[finalRef]
	if operation.Active == nil && !tempSnapshot.Expected.Exists && finalSnapshot.Expected.Exists {
		actual, err := a.git.RefValues([]string{tempRef, finalRef})
		if err != nil {
			return "", err
		}
		expected := map[string]RefValue{tempRef: {}, finalRef: finalSnapshot.Expected}
		if drift := refDrift(expected, actual); len(drift) > 0 {
			return "", fmt.Errorf("cannot finish checkpointed split branch rename:\n%s", formatRefDrift(drift))
		}
		current, err := a.git.CurrentBranch()
		if err != nil {
			return "", err
		}
		if current != commit.FinalBranch {
			return "", fmt.Errorf("renamed split branch %q is not checked out", commit.FinalBranch)
		}
		return commit.FinalBranch, nil
	}
	tempValue := operation.Refs[tempRef].Expected
	before := map[string]RefValue{tempRef: tempValue, finalRef: {}}
	after := map[string]RefValue{tempRef: {}, finalRef: tempValue}
	actionID := "split-rename:" + commit.Branch + ":" + commit.FinalBranch
	actual, err := a.git.RefValues([]string{tempRef, finalRef})
	if err != nil {
		return "", err
	}
	if operation.Active == nil {
		if drift := refDrift(before, actual); len(drift) > 0 {
			return "", fmt.Errorf("cannot rename split branch:\n%s", formatRefDrift(drift))
		}
		operation.Active = &JournalAction{ID: actionID, Kind: "rename-branch", RefsBefore: before, RefsAfter: after}
		if err := a.git.WriteState(state); err != nil {
			return "", err
		}
	} else if operation.Active.ID != actionID || operation.Active.Kind != "rename-branch" {
		return "", fmt.Errorf("cannot rename split branch while journal action %q is active", operation.Active.ID)
	}
	if len(refDrift(after, actual)) == 0 {
		tempSnapshot := operation.Refs[tempRef]
		tempSnapshot.Expected = RefValue{}
		operation.Refs[tempRef] = tempSnapshot
		finalSnapshot := operation.Refs[finalRef]
		finalSnapshot.Expected = tempValue
		operation.Refs[finalRef] = finalSnapshot
		operation.Active = nil
		if err := a.git.WriteState(state); err != nil {
			return "", err
		}
		return commit.FinalBranch, nil
	}
	if drift := refDrift(before, actual); len(drift) > 0 {
		return "", fmt.Errorf("cannot recover split branch rename:\n%s", formatRefDrift(drift))
	}
	current, err := a.git.CurrentBranch()
	if err != nil {
		return "", err
	}
	if current != commit.Branch {
		return "", fmt.Errorf("temporary split branch %q is not checked out", commit.Branch)
	}
	if err := a.git.Run("branch", "-m", commit.FinalBranch); err != nil {
		return "", err
	}
	return a.finishSplitCommitBranchName(state, progress)
}

func (a *App) finishSplitOperation(state State, progress splitJournalProgress) error {
	operation := state.Operation
	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	top := progress.Branches[len(progress.Branches)-1]
	if current != top {
		return fmt.Errorf("split in progress for %q; switch to %q before finishing split", progress.Target, top)
	}
	originalRefs := map[string]string{}
	for ref, snapshot := range operation.Refs {
		if snapshot.Original.Exists {
			originalRefs[ref[len("refs/heads/"):]] = snapshot.Original.OID
		}
	}
	legacyState := State{
		Stacks: cloneStacks(progress.DraftStacks),
		Pending: &Pending{
			Branch:         progress.Target,
			Top:            progress.OriginalHead,
			OriginalHead:   progress.OriginalHead,
			OriginalBase:   progress.OriginalBase,
			Branches:       append([]string(nil), progress.Branches...),
			OriginalRefs:   originalRefs,
			OriginalStacks: cloneStacks(operation.OriginalStacks),
		},
	}
	nextState, directOps, rewritten, skipTops, err := splitFinalState(legacyState)
	if err != nil {
		return err
	}
	restackOps, err := RestackOpsAfterRewrites(nextState, rewritten, originalRefs, skipTops)
	if err != nil {
		return err
	}
	ops := append(directOps, restackOps...)
	if err := a.validateRebaseOpsUpdateable("split", current, nextState, originalRefs, ops); err != nil {
		return err
	}
	rebaseProgress := rebaseJournalProgress{ReturnBranch: current}
	for _, op := range ops {
		refs, err := a.rebaseMutationRefs(current, op, "")
		if err != nil {
			return err
		}
		step := journalRebaseStep{Op: op}
		for branch := range refs {
			ref := "refs/heads/" + branch
			if _, ok := operation.Refs[ref]; !ok {
				return fmt.Errorf("split rebase would move branch %q that was created outside the operation", branch)
			}
			step.RefNames = append(step.RefNames, ref)
		}
		sort.Strings(step.RefNames)
		rebaseProgress.Steps = append(rebaseProgress.Steps, step)
	}
	operation.DesiredStacks = cloneStacks(nextState.Stacks)
	operation.Phase = operationApplying
	operation.Active = nil
	if err := encodeRebaseProgress(operation, rebaseProgress); err != nil {
		return err
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.continueRebaseQueueOperation(state, continueOptions{})
}
