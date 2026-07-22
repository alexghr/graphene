package graphene

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

type rebaseJournalProgress struct {
	Steps             []journalRebaseStep `json:"steps,omitempty"`
	Next              int                 `json:"next"`
	ReturnBranch      string              `json:"returnBranch,omitempty"`
	Commit            *journalCommit      `json:"commit,omitempty"`
	CommitPrepared    bool                `json:"commitPrepared,omitempty"`
	CommitPrepareStep int                 `json:"commitPrepareStep,omitempty"`
	CommitDone        bool                `json:"commitDone,omitempty"`
	FastForward       *journalFastForward `json:"fastForward,omitempty"`
	FastForwarded     bool                `json:"fastForwarded,omitempty"`
	DeleteBranches    []string            `json:"deleteBranches,omitempty"`
	New               *journalNewBranch   `json:"new,omitempty"`
}

type journalRebaseStep struct {
	Op       RebaseOp `json:"op"`
	RefNames []string `json:"refNames"`
}

type journalFastForward struct {
	Branch string `json:"branch"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

type journalCommit struct {
	Branch       string   `json:"branch"`
	Mode         string   `json:"mode"`
	StageAll     bool     `json:"stageAll,omitempty"`
	StageUpdate  bool     `json:"stageUpdate,omitempty"`
	ResetHard    string   `json:"resetHard,omitempty"`
	ResetSoft    string   `json:"resetSoft,omitempty"`
	CreateBranch bool     `json:"createBranch,omitempty"`
	Args         []string `json:"args"`
}

type journalNewBranch struct {
	RecordBase   string `json:"recordBase"`
	Derive       bool   `json:"derive,omitempty"`
	BranchPrefix string `json:"branchPrefix,omitempty"`
	FinalBranch  string `json:"finalBranch,omitempty"`
}

func encodeRebaseProgress(operation *OperationJournal, progress rebaseJournalProgress) error {
	data, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	operation.Progress = data
	return nil
}

func decodeRebaseProgress(operation *OperationJournal) (rebaseJournalProgress, error) {
	var progress rebaseJournalProgress
	decoder := json.NewDecoder(bytes.NewReader(operation.Progress))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&progress); err != nil {
		return progress, fmt.Errorf("parse pending %s: %w", operation.Kind, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return progress, fmt.Errorf("parse pending %s: %w", operation.Kind, err)
	}
	if progress.Next < 0 || progress.Next > len(progress.Steps) {
		return progress, fmt.Errorf("pending %s has invalid rebase index %d", operation.Kind, progress.Next)
	}
	if progress.ReturnBranch != "" && !validBranchArgument(progress.ReturnBranch) {
		return progress, fmt.Errorf("pending %s has invalid return branch %q", operation.Kind, progress.ReturnBranch)
	}
	for index, step := range progress.Steps {
		if !validJournalRebaseOp(step.Op) {
			return progress, fmt.Errorf("pending %s rebase %d is incomplete", operation.Kind, index)
		}
		if len(step.RefNames) == 0 {
			return progress, fmt.Errorf("pending %s rebase %d owns no refs", operation.Kind, index)
		}
		if !sort.StringsAreSorted(step.RefNames) {
			return progress, fmt.Errorf("pending %s rebase %d refs are not sorted", operation.Kind, index)
		}
		for _, ref := range step.RefNames {
			if _, ok := operation.Refs[ref]; !ok {
				return progress, fmt.Errorf("pending %s rebase %d does not own %s", operation.Kind, index, ref)
			}
		}
	}
	if progress.FastForward != nil {
		ff := progress.FastForward
		if !validBranchArgument(ff.Branch) || ff.Old == "" || ff.New == "" {
			return progress, fmt.Errorf("pending %s has an incomplete fast-forward", operation.Kind)
		}
		if !validObjectID(ff.Old) || !validObjectID(ff.New) {
			return progress, fmt.Errorf("pending %s has invalid fast-forward object ids", operation.Kind)
		}
		if _, ok := operation.Refs["refs/heads/"+ff.Branch]; !ok {
			return progress, fmt.Errorf("pending %s does not own fast-forward branch %q", operation.Kind, ff.Branch)
		}
	}
	if progress.Commit != nil {
		commit := progress.Commit
		if !validBranchArgument(commit.Branch) || len(commit.Args) == 0 {
			return progress, fmt.Errorf("pending %s has an invalid commit action", operation.Kind)
		}
		switch commit.Mode {
		case "amend":
			if operation.Kind != "amend" || commit.ResetHard != "" || commit.ResetSoft != "" || progress.CommitPrepareStep != 0 {
				return progress, fmt.Errorf("pending %s amend has invalid reset progress", operation.Kind)
			}
		case "squash":
			if operation.Kind != "squash" || commit.ResetHard == "" || commit.ResetSoft == "" || progress.CommitPrepareStep < 0 || progress.CommitPrepareStep > 2 {
				return progress, fmt.Errorf("pending %s squash has invalid reset progress", operation.Kind)
			}
			if progress.CommitPrepared && progress.CommitPrepareStep != 2 {
				return progress, fmt.Errorf("pending %s squash is prepared before its resets completed", operation.Kind)
			}
			if !validObjectID(commit.ResetHard) || !validObjectID(commit.ResetSoft) {
				return progress, fmt.Errorf("pending %s squash has invalid reset object ids", operation.Kind)
			}
		case "new":
			if operation.Kind != "new" || commit.ResetHard != "" || commit.ResetSoft != "" || progress.CommitPrepareStep < 0 || progress.CommitPrepareStep > 1 {
				return progress, fmt.Errorf("pending %s new commit has invalid preparation progress", operation.Kind)
			}
			if !commit.CreateBranch && progress.CommitPrepareStep != 0 {
				return progress, fmt.Errorf("pending %s reused branch has create progress", operation.Kind)
			}
		default:
			return progress, fmt.Errorf("pending %s has unsupported commit mode %q", operation.Kind, commit.Mode)
		}
		if err := validateJournalCommitArgs(commit.Mode, commit.Args); err != nil {
			return progress, err
		}
		if _, ok := operation.Refs["refs/heads/"+commit.Branch]; !ok {
			return progress, fmt.Errorf("pending %s does not own commit branch %q", operation.Kind, commit.Branch)
		}
		if progress.CommitDone && !progress.CommitPrepared {
			return progress, fmt.Errorf("pending %s completed its commit before preparing it", operation.Kind)
		}
	}
	if !sort.StringsAreSorted(progress.DeleteBranches) {
		return progress, fmt.Errorf("pending %s deleted branches are not sorted", operation.Kind)
	}
	for _, branch := range progress.DeleteBranches {
		if !validBranchArgument(branch) {
			return progress, fmt.Errorf("pending %s has invalid deleted branch %q", operation.Kind, branch)
		}
		if _, ok := operation.Refs["refs/heads/"+branch]; !ok {
			return progress, fmt.Errorf("pending %s does not own deleted branch %q", operation.Kind, branch)
		}
	}
	if progress.New != nil {
		if operation.Kind != "new" || progress.Commit == nil || progress.Commit.Mode != "new" || !validBranchArgument(progress.New.RecordBase) {
			return progress, fmt.Errorf("pending %s has invalid new-branch progress", operation.Kind)
		}
		if !progress.New.Derive && !validBranchArgument(progress.New.FinalBranch) {
			return progress, fmt.Errorf("pending new branch is missing its final name")
		}
		if progress.New.FinalBranch != "" && !validBranchArgument(progress.New.FinalBranch) {
			return progress, fmt.Errorf("pending new branch has invalid final name %q", progress.New.FinalBranch)
		}
	}
	return progress, nil
}

func validJournalRebaseOp(op RebaseOp) bool {
	return safeGitArgument(op.Onto) && safeGitArgument(op.Upstream) && validBranchArgument(op.Top)
}

func (a *App) startRebaseQueueOperation(
	state State,
	kind string,
	current string,
	returnBranch string,
	desiredStacks []Stack,
	observedBranches []string,
	plannedRefs map[string]string,
	ops []RebaseOp,
	fastForward *journalFastForward,
	topOverrides map[int]string,
	commit *journalCommit,
	snapshotIndex bool,
	deleteBranches []string,
	newBranch *journalNewBranch,
) (resultErr error) {
	if err := a.requirePlannedBranchRefs(plannedRefs); err != nil {
		return fmt.Errorf("cannot start %s: %w", kind, err)
	}
	worktree, err := a.git.WorktreeID()
	if err != nil {
		return err
	}
	head, err := a.git.Head()
	if err != nil {
		return err
	}
	operation, err := newOperationJournal(kind, worktree, current, head, state.Stacks)
	if err != nil {
		return err
	}
	artifactDir, err := a.operationArtifactDir(operation)
	if err != nil {
		return err
	}
	publicationAttempted := false
	defer func() {
		cleanupUnpublishedOperationArtifacts(artifactDir, publicationAttempted, &resultErr)
	}()
	operation.WorktreePolicy = worktreeRestoreHard
	if snapshotIndex {
		operation.WorktreePolicy = worktreeRestoreIndex
	}
	if err := a.prepareOperationWorktree(operation); err != nil {
		return err
	}
	originalRef := "refs/heads/" + current
	originalValue, err := a.git.RefValue(originalRef)
	if err != nil {
		return err
	}
	if !originalValue.Exists || originalValue.OID != head {
		return fmt.Errorf("cannot start %s because branch %q moved away from HEAD", kind, current)
	}
	operation.Refs[originalRef] = JournalRef{Original: originalValue, Expected: originalValue}
	if err := a.addOperationObservedBranches(operation, observedBranches, true); err != nil {
		return err
	}
	operation.DesiredStacks = cloneStacks(desiredStacks)
	progress := rebaseJournalProgress{
		ReturnBranch:   returnBranch,
		FastForward:    fastForward,
		Commit:         commit,
		DeleteBranches: append([]string(nil), deleteBranches...),
		New:            newBranch,
	}
	sort.Strings(progress.DeleteBranches)

	if commit != nil {
		ref := "refs/heads/" + commit.Branch
		value, err := a.git.RefValue(ref)
		if err != nil {
			return err
		}
		if commit.CreateBranch {
			if value.Exists {
				return fmt.Errorf("cannot start %s because new branch %q already exists", kind, commit.Branch)
			}
		} else if !value.Exists {
			return fmt.Errorf("cannot start %s because commit branch %q does not exist", kind, commit.Branch)
		}
		if snapshot, exists := operation.Refs[ref]; exists {
			if snapshot.Expected != value {
				return fmt.Errorf("cannot start %s because %q moved from %s to %s", kind, commit.Branch, formatRefValue(snapshot.Expected), formatRefValue(value))
			}
		} else {
			operation.Refs[ref] = JournalRef{Original: value, Expected: value}
		}
	}

	if fastForward != nil {
		ref := "refs/heads/" + fastForward.Branch
		value, err := a.git.RefValue(ref)
		if err != nil {
			return err
		}
		want := RefValue{Exists: true, OID: fastForward.Old}
		if value != want {
			return fmt.Errorf("cannot start %s because %q moved from %s to %s", kind, fastForward.Branch, fastForward.Old, formatRefValue(value))
		}
		if snapshot, exists := operation.Refs[ref]; exists {
			if snapshot.Expected != value {
				return fmt.Errorf("cannot start %s because %q moved from %s to %s", kind, fastForward.Branch, formatRefValue(snapshot.Expected), formatRefValue(value))
			}
		} else {
			operation.Refs[ref] = JournalRef{Original: value, Expected: value}
		}
	}

	for index, op := range ops {
		if err := a.addOperationObservedBranches(operation, []string{op.Onto, op.Upstream}, false); err != nil {
			return err
		}
		refs, err := a.rebaseMutationRefs(current, op, topOverrides[index])
		if err != nil {
			return err
		}
		step := journalRebaseStep{Op: op}
		for branch, oid := range refs {
			ref := "refs/heads/" + branch
			step.RefNames = append(step.RefNames, ref)
			if _, exists := operation.Refs[ref]; !exists {
				value := RefValue{Exists: true, OID: oid}
				operation.Refs[ref] = JournalRef{Original: value, Expected: value}
			}
		}
		sort.Strings(step.RefNames)
		progress.Steps = append(progress.Steps, step)
	}
	seenConfigs := map[string]bool{}
	for _, branch := range progress.DeleteBranches {
		ref := "refs/heads/" + branch
		if _, exists := operation.Refs[ref]; !exists {
			value, err := a.git.RefValue(ref)
			if err != nil {
				return err
			}
			if !value.Exists {
				return fmt.Errorf("cannot start %s because deleted branch %q does not exist", kind, branch)
			}
			operation.Refs[ref] = JournalRef{Original: value, Expected: value}
		}
		section := "branch." + branch
		if seenConfigs[section] {
			continue
		}
		seenConfigs[section] = true
		entries, err := a.git.BranchConfig(branch)
		if err != nil {
			return err
		}
		operation.Configs = append(operation.Configs, JournalConfig{
			Section:  section,
			Original: entries,
			Expected: append([]ConfigEntry(nil), entries...),
		})
	}
	if err := a.snapshotOperationValidationRefs(operation, operation.OriginalStacks, operation.DesiredStacks); err != nil {
		return err
	}
	if err := a.requirePlannedBranchRefs(plannedRefs); err != nil {
		return fmt.Errorf("cannot start %s: %w", kind, err)
	}
	if err := a.verifyUnpublishedOperationWorktree(operation); err != nil {
		return fmt.Errorf("cannot start %s: %w", kind, err)
	}
	if err := encodeRebaseProgress(operation, progress); err != nil {
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
	operation.Phase = operationApplying
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.continueRebaseQueueOperation(state, continueOptions{})
}

func (a *App) requirePlannedBranchRefs(planned map[string]string) error {
	for branch, oid := range planned {
		if oid == "" {
			continue
		}
		actual, err := a.git.RefValue("refs/heads/" + branch)
		if err != nil {
			return err
		}
		want := RefValue{Exists: true, OID: oid}
		if actual != want {
			return fmt.Errorf("planned branch %q moved from %s to %s", branch, oid, formatRefValue(actual))
		}
	}
	return nil
}

func (a *App) continueRebaseQueueOperation(state State, opts continueOptions) error {
	operation := state.Operation
	progress, err := decodeRebaseProgress(operation)
	if err != nil {
		return err
	}
	if operation.Phase == operationPreparing {
		if err := a.git.InstallOperationBackups(operation); err != nil {
			return err
		}
		operation.Phase = operationApplying
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	if err := a.crossOperationWorktreeBoundary(state); err != nil {
		return err
	}
	if progress.Commit != nil && !progress.CommitDone {
		if err := a.continueJournalCommit(state, &progress, opts); err != nil {
			return err
		}
	}
	if progress.FastForward != nil && !progress.FastForwarded {
		if err := a.continueJournalFastForward(state, &progress); err != nil {
			return err
		}
	}

	for progress.Next < len(progress.Steps) {
		step := progress.Steps[progress.Next]
		if operation.Active != nil {
			completed, err := a.recoverJournalRebase(state, progress, step, opts)
			if err != nil {
				return err
			}
			if !completed {
				return nil
			}
			progress, err = decodeRebaseProgress(operation)
			if err != nil {
				return err
			}
			continue
		}
		if err := a.startJournalRebase(state, progress, step); err != nil {
			return err
		}
		progress, err = decodeRebaseProgress(operation)
		if err != nil {
			return err
		}
	}
	if progress.ReturnBranch != "" {
		current, err := a.git.CurrentBranch()
		if err != nil {
			return err
		}
		if current != progress.ReturnBranch {
			if err := a.git.RunOperationSwitch(progress.ReturnBranch); err != nil {
				return err
			}
		}
	}
	if progress.New != nil {
		if err := a.finishJournalNewBranch(state, &progress); err != nil {
			return err
		}
	}
	if len(progress.DeleteBranches) > 0 {
		if err := a.removeOperationBranchConfigs(state, progress.DeleteBranches); err != nil {
			return err
		}
		if err := a.finishOperationRefDeletion(state, progress.DeleteBranches); err != nil {
			return err
		}
	}
	return a.commitOperation(state)
}

func (a *App) finishJournalNewBranch(state State, progress *rebaseJournalProgress) error {
	operation := state.Operation
	plan := progress.New
	if plan.FinalBranch == "" {
		subject, err := a.git.Output("log", "-1", "--format=%s", progress.Commit.Branch)
		if err != nil {
			return err
		}
		finalBranch, err := a.derivedBranchNameWithState(
			Config{BranchPrefix: plan.BranchPrefix},
			State{Stacks: cloneStacks(operation.OriginalStacks)},
			subject,
			map[string]bool{progress.Commit.Branch: true},
		)
		if err != nil {
			return err
		}
		finalRef := "refs/heads/" + finalBranch
		value, err := a.git.RefValue(finalRef)
		if err != nil {
			return err
		}
		if value.Exists {
			return fmt.Errorf("derived branch %q was created while graphene new was running", finalBranch)
		}
		operation.Refs[finalRef] = JournalRef{Original: RefValue{}, Expected: RefValue{}}
		plan.FinalBranch = finalBranch
		next := State{Stacks: cloneStacks(operation.OriginalStacks)}
		if err := next.AddCommit(plan.RecordBase, finalBranch); err != nil {
			return err
		}
		operation.DesiredStacks = cloneStacks(next.Stacks)
		if err := encodeRebaseProgress(operation, *progress); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	if progress.Commit.Branch == plan.FinalBranch {
		return nil
	}
	tempRef := "refs/heads/" + progress.Commit.Branch
	finalRef := "refs/heads/" + plan.FinalBranch
	tempSnapshot := operation.Refs[tempRef]
	finalSnapshot := operation.Refs[finalRef]
	if operation.Active == nil && !tempSnapshot.Expected.Exists && finalSnapshot.Expected.Exists {
		actual, err := a.git.RefValues([]string{tempRef, finalRef})
		if err != nil {
			return err
		}
		expected := map[string]RefValue{tempRef: {}, finalRef: finalSnapshot.Expected}
		if drift := refDrift(expected, actual); len(drift) > 0 {
			return fmt.Errorf("cannot finish checkpointed temporary branch rename:\n%s", formatRefDrift(drift))
		}
		current, err := a.git.CurrentBranch()
		if err != nil {
			return err
		}
		if current != plan.FinalBranch {
			if err := a.git.RunOperationSwitch(plan.FinalBranch); err != nil {
				return err
			}
		}
		return nil
	}
	tempValue := operation.Refs[tempRef].Expected
	before := map[string]RefValue{tempRef: tempValue, finalRef: {}}
	after := map[string]RefValue{tempRef: {}, finalRef: tempValue}
	const actionKind = "rename-branch"
	actionID := "rename-branch:" + progress.Commit.Branch + ":" + plan.FinalBranch
	refs := []string{tempRef, finalRef}
	actual, err := a.git.RefValues(refs)
	if err != nil {
		return err
	}
	if operation.Active == nil {
		if drift := refDrift(before, actual); len(drift) > 0 {
			return fmt.Errorf("cannot rename temporary branch because refs changed:\n%s", formatRefDrift(drift))
		}
		operation.Active = &JournalAction{ID: actionID, Kind: actionKind, RefsBefore: before, RefsAfter: after}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	} else if operation.Active.ID != actionID || operation.Active.Kind != actionKind {
		return fmt.Errorf("cannot rename temporary branch while journal action %q is active", operation.Active.ID)
	}
	if len(refDrift(after, actual)) == 0 {
		current, err := a.git.CurrentBranch()
		if err != nil {
			return err
		}
		if current != plan.FinalBranch {
			if err := a.git.RunOperationSwitch(plan.FinalBranch); err != nil {
				return err
			}
		}
		tempSnapshot := operation.Refs[tempRef]
		tempSnapshot.Expected = RefValue{}
		operation.Refs[tempRef] = tempSnapshot
		finalSnapshot := operation.Refs[finalRef]
		finalSnapshot.Expected = tempValue
		operation.Refs[finalRef] = finalSnapshot
		operation.Active = nil
		return a.git.WriteState(state)
	}
	if drift := refDrift(before, actual); len(drift) > 0 {
		return fmt.Errorf("cannot recover temporary branch rename:\n%s", formatRefDrift(drift))
	}
	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	if current != progress.Commit.Branch {
		return fmt.Errorf("temporary branch %q is not checked out", progress.Commit.Branch)
	}
	if err := a.git.Run("branch", "-m", plan.FinalBranch); err != nil {
		return err
	}
	return a.finishJournalNewBranch(state, progress)
}

func (a *App) continueJournalCommit(state State, progress *rebaseJournalProgress, opts continueOptions) error {
	operation := state.Operation
	commit := progress.Commit
	ref := "refs/heads/" + commit.Branch
	if !progress.CommitPrepared {
		if err := a.continueJournalCommitPreparation(state, progress); err != nil {
			return err
		}
	}
	snapshot := operation.Refs[ref]
	actual, err := a.git.RefValue(ref)
	if err != nil {
		return err
	}
	if operation.Active != nil {
		if operation.Active.Kind != "commit" || operation.Active.ID != "commit:"+commit.Branch {
			return fmt.Errorf("cannot finish %s commit while journal action %q is active", operation.Kind, operation.Active.ID)
		}
		before := operation.Active.RefsBefore[ref]
		if actual != before {
			if !opts.acceptCurrent {
				return fmt.Errorf("git may have completed %s; inspect %q, then run graphene continue --accept-current or graphene abort --force", operation.Active.ID, commit.Branch)
			}
			if err := a.validateJournalCommitResult(*commit, before, actual); err != nil {
				return err
			}
			return a.finishJournalCommit(state, progress, actual)
		}
	}
	if operation.Active == nil {
		if actual != snapshot.Expected {
			return fmt.Errorf("cannot commit %q because it moved from %s to %s", commit.Branch, formatRefValue(snapshot.Expected), formatRefValue(actual))
		}
		operation.Active = &JournalAction{
			ID:         "commit:" + commit.Branch,
			Kind:       "commit",
			RefsBefore: map[string]RefValue{ref: actual},
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	if current != commit.Branch && !(commit.Mode == "new" && commit.CreateBranch && progress.CommitPrepareStep == 0) {
		return fmt.Errorf("pending %s commit belongs to branch %q, currently on %q", operation.Kind, commit.Branch, current)
	}
	marker := fmt.Sprintf("graphene:%s:%s", operation.ID, operation.Active.ID)
	if err := a.git.RunWithEnv(map[string]string{"GIT_REFLOG_ACTION": marker}, commit.Args...); err != nil {
		currentValue, refErr := a.git.RefValue(ref)
		if refErr != nil {
			return fmt.Errorf("%w; additionally failed to inspect the commit ref: %v", err, refErr)
		}
		if currentValue == operation.Active.RefsBefore[ref] {
			fmt.Fprintf(a.stderr, "Graphene left the %s operation pending to preserve any commit-hook side effects; inspect the worktree, then retry with graphene continue or roll back with graphene abort.\n", operation.Kind)
			return err
		}
		fmt.Fprintf(a.stderr, "Graphene left the %s operation pending because the commit ref moved; inspect %q, then run graphene continue --accept-current or graphene abort --force.\n", operation.Kind, commit.Branch)
		return err
	}
	actual, err = a.git.RefValue(ref)
	if err != nil {
		return err
	}
	if err := a.validateJournalCommitResult(*commit, operation.Active.RefsBefore[ref], actual); err != nil {
		return err
	}
	return a.finishJournalCommit(state, progress, actual)
}

func (a *App) continueJournalCommitPreparation(state State, progress *rebaseJournalProgress) error {
	operation := state.Operation
	commit := progress.Commit
	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	if current != commit.Branch && !(commit.Mode == "new" && commit.CreateBranch && progress.CommitPrepareStep == 0) {
		if operation.Active != nil {
			return fmt.Errorf("cannot switch to %q while journal action %q is active", commit.Branch, operation.Active.ID)
		}
		if err := a.git.RunOperationSwitch(commit.Branch); err != nil {
			return err
		}
	}
	switch commit.Mode {
	case "amend":
		if operation.Active != nil {
			return fmt.Errorf("cannot prepare amend while journal action %q is active", operation.Active.ID)
		}
		opts := commitOptions{stageAll: commit.StageAll, stageUpdate: commit.StageUpdate}
		if err := a.stageRequestedChanges(opts); err != nil {
			return err
		}
	case "squash":
		for progress.CommitPrepareStep < 2 {
			mode := "--hard"
			target := commit.ResetHard
			if progress.CommitPrepareStep == 1 {
				mode = "--soft"
				target = commit.ResetSoft
			}
			if err := a.continueJournalCommitReset(state, progress, mode, target); err != nil {
				return err
			}
		}
	case "new":
		if commit.CreateBranch && progress.CommitPrepareStep == 0 {
			if err := a.continueJournalCreateBranch(state, progress); err != nil {
				return err
			}
		}
		opts := commitOptions{stageAll: commit.StageAll, stageUpdate: commit.StageUpdate}
		if err := a.stageRequestedChanges(opts); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported journal commit mode %q", commit.Mode)
	}
	progress.CommitPrepared = true
	if err := encodeRebaseProgress(operation, *progress); err != nil {
		return err
	}
	return a.git.WriteState(state)
}

func (a *App) continueJournalCreateBranch(state State, progress *rebaseJournalProgress) error {
	operation := state.Operation
	branch := progress.Commit.Branch
	ref := "refs/heads/" + branch
	before := RefValue{}
	after := RefValue{Exists: true, OID: operation.OriginalHead}
	actual, err := a.git.RefValue(ref)
	if err != nil {
		return err
	}
	const actionKind = "create-branch"
	actionID := "create-branch:" + branch
	if operation.Active == nil {
		if actual != before {
			return fmt.Errorf("cannot create %q because it now exists at %s", branch, formatRefValue(actual))
		}
		operation.Active = &JournalAction{
			ID:         actionID,
			Kind:       actionKind,
			RefsBefore: map[string]RefValue{ref: before},
			RefsAfter:  map[string]RefValue{ref: after},
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	} else if operation.Active.ID != actionID || operation.Active.Kind != actionKind {
		return fmt.Errorf("cannot create %q while journal action %q is active", branch, operation.Active.ID)
	}
	if actual == after {
		current, err := a.git.CurrentBranch()
		if err != nil {
			return err
		}
		if current != branch {
			if err := a.git.RunOperationSwitch(branch); err != nil {
				return err
			}
		}
		snapshot := operation.Refs[ref]
		snapshot.Expected = after
		operation.Refs[ref] = snapshot
		operation.Active = nil
		progress.CommitPrepareStep = 1
		if err := encodeRebaseProgress(operation, *progress); err != nil {
			return err
		}
		return a.git.WriteState(state)
	}
	if actual != before {
		return fmt.Errorf("cannot recover new branch %q at %s", branch, formatRefValue(actual))
	}
	if err := a.git.RunOperationSwitch("-c", branch, operation.OriginalHead); err != nil {
		return err
	}
	return a.continueJournalCreateBranch(state, progress)
}

func (a *App) continueJournalCommitReset(state State, progress *rebaseJournalProgress, mode, target string) error {
	operation := state.Operation
	ref := "refs/heads/" + progress.Commit.Branch
	snapshot := operation.Refs[ref]
	before := snapshot.Expected
	after := RefValue{Exists: true, OID: target}
	actionID := fmt.Sprintf("commit-reset-%d", progress.CommitPrepareStep)
	actual, err := a.git.RefValue(ref)
	if err != nil {
		return err
	}
	if operation.Active == nil {
		if actual != before {
			return fmt.Errorf("cannot prepare %s because %q moved from %s to %s", operation.Kind, progress.Commit.Branch, formatRefValue(before), formatRefValue(actual))
		}
		operation.Active = &JournalAction{
			ID:         actionID,
			Kind:       "reset",
			RefsBefore: map[string]RefValue{ref: before},
			RefsAfter:  map[string]RefValue{ref: after},
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	} else if operation.Active.ID != actionID || operation.Active.Kind != "reset" {
		return fmt.Errorf("cannot prepare %s while journal action %q is active", operation.Kind, operation.Active.ID)
	}
	if actual == after {
		snapshot.Expected = after
		operation.Refs[ref] = snapshot
		operation.Active = nil
		progress.CommitPrepareStep++
		if err := encodeRebaseProgress(operation, *progress); err != nil {
			return err
		}
		return a.git.WriteState(state)
	}
	if actual != before {
		return fmt.Errorf("cannot recover %s reset because %q is at %s", operation.Kind, progress.Commit.Branch, formatRefValue(actual))
	}
	if mode == "--hard" {
		if err := a.requireNoIgnoredTreeCollision(target, operation.Kind+" reset"); err != nil {
			return err
		}
	}
	if err := a.git.RunOperationReset(mode, target); err != nil {
		return err
	}
	return a.continueJournalCommitReset(state, progress, mode, target)
}

func (a *App) validateJournalCommitResult(commit journalCommit, before, after RefValue) error {
	if !before.Exists || !after.Exists {
		return fmt.Errorf("journaled %s commit changed branch existence unexpectedly", commit.Mode)
	}
	afterParents, err := a.git.Output("rev-list", "--parents", "-n", "1", after.OID)
	if err != nil {
		return err
	}
	afterFields := bytes.Fields([]byte(afterParents))
	if commit.Mode != "amend" {
		if commit.Mode != "squash" && commit.Mode != "new" {
			return fmt.Errorf("unsupported journal commit mode %q", commit.Mode)
		}
		if len(afterFields) != 2 {
			return fmt.Errorf("cannot accept current %q because the %s result is not a single-parent commit", commit.Branch, commit.Mode)
		}
		if string(afterFields[1]) != before.OID {
			return fmt.Errorf("cannot accept current %q because the %s commit has an unexpected parent", commit.Branch, commit.Mode)
		}
		return nil
	}
	beforeParents, err := a.git.Output("rev-list", "--parents", "-n", "1", before.OID)
	if err != nil {
		return err
	}
	beforeFields := bytes.Fields([]byte(beforeParents))
	if len(beforeFields) != len(afterFields) {
		return fmt.Errorf("cannot accept current %q because the amended commit has a different number of parents", commit.Branch)
	}
	_, beforeTail, _ := bytes.Cut([]byte(beforeParents), []byte(" "))
	_, afterTail, _ := bytes.Cut([]byte(afterParents), []byte(" "))
	if !bytes.Equal(beforeTail, afterTail) {
		return fmt.Errorf("cannot accept current %q because the amended commit has different parents", commit.Branch)
	}
	return nil
}

func (a *App) finishJournalCommit(state State, progress *rebaseJournalProgress, actual RefValue) error {
	operation := state.Operation
	ref := "refs/heads/" + progress.Commit.Branch
	snapshot := operation.Refs[ref]
	snapshot.Expected = actual
	operation.Refs[ref] = snapshot
	progress.CommitDone = true
	operation.Active = nil
	if err := encodeRebaseProgress(operation, *progress); err != nil {
		return err
	}
	return a.git.WriteState(state)
}

func (a *App) continueJournalFastForward(state State, progress *rebaseJournalProgress) error {
	operation := state.Operation
	ff := progress.FastForward
	ref := "refs/heads/" + ff.Branch
	before := RefValue{Exists: true, OID: ff.Old}
	after := RefValue{Exists: true, OID: ff.New}
	actual, err := a.git.RefValue(ref)
	if err != nil {
		return err
	}
	if actual == after {
		snapshot := operation.Refs[ref]
		snapshot.Expected = after
		operation.Refs[ref] = snapshot
		operation.Active = nil
		progress.FastForwarded = true
		if err := encodeRebaseProgress(operation, *progress); err != nil {
			return err
		}
		return a.git.WriteState(state)
	}
	if actual != before {
		return fmt.Errorf("cannot continue %s because %q moved from %s to %s", operation.Kind, ff.Branch, ff.Old, formatRefValue(actual))
	}
	if operation.Active == nil {
		operation.Active = &JournalAction{
			ID:         "fast-forward:" + ff.Branch,
			Kind:       "fast-forward",
			RefsBefore: map[string]RefValue{ref: before},
			RefsAfter:  map[string]RefValue{ref: after},
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	} else if operation.Active.Kind != "fast-forward" || operation.Active.ID != "fast-forward:"+ff.Branch {
		return fmt.Errorf("cannot fast-forward %q while journal action %q is active", ff.Branch, operation.Active.ID)
	}
	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	if current == ff.Branch {
		if err := a.requireNoIgnoredTreeCollision(ff.New, operation.Kind+" fast-forward"); err != nil {
			return err
		}
		if err := a.git.Run("-c", "submodule.recurse=false", "merge", "--ff-only", ff.New); err != nil {
			return err
		}
	} else if err := a.git.UpdateRefs([]refEdit{{Ref: ref, Old: before, New: after}}); err != nil {
		return err
	}
	return a.continueJournalFastForward(state, progress)
}

func (a *App) startJournalRebase(state State, progress rebaseJournalProgress, step journalRebaseStep) error {
	operation := state.Operation
	drift, _, err := a.git.OperationRefDrift(operation)
	if err != nil {
		return err
	}
	if len(drift) > 0 {
		return fmt.Errorf("cannot continue %s because operation-owned refs changed:\n%s", operation.Kind, formatRefDrift(drift))
	}
	inventory, err := a.git.LocalHeadRefValues()
	if err != nil {
		return err
	}
	refsBefore, err := a.git.RefValues(step.RefNames)
	if err != nil {
		return err
	}
	operation.Active = &JournalAction{
		ID:           fmt.Sprintf("rebase-%d", progress.Next),
		Kind:         "rebase",
		RefsBefore:   refsBefore,
		RefInventory: inventory,
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	marker := fmt.Sprintf("graphene:%s:%s", operation.ID, operation.Active.ID)
	if err := a.requireNoIgnoredRebaseCollision(step.Op); err != nil {
		return err
	}
	if err := a.git.RunWithEnv(map[string]string{"GIT_REFLOG_ACTION": marker}, "-c", "submodule.recurse=false", "rebase", "--update-refs", "--onto", step.Op.Onto, step.Op.Upstream, step.Op.Top); err != nil {
		return err
	}
	return a.finishJournalRebase(state, step)
}

func (a *App) recoverJournalRebase(state State, progress rebaseJournalProgress, step journalRebaseStep, opts continueOptions) (bool, error) {
	operation := state.Operation
	if operation.Active.Kind != "rebase" || operation.Active.ID != fmt.Sprintf("rebase-%d", progress.Next) {
		return false, fmt.Errorf("pending %s has unexpected journal action %q", operation.Kind, operation.Active.ID)
	}
	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return false, err
	}
	if inProgress {
		if err := a.continueCurrentRebase(); err != nil {
			return false, err
		}
		inProgress, err = a.git.RebaseInProgress()
		if err != nil || inProgress {
			return false, err
		}
		return true, a.finishJournalRebase(state, step)
	}
	actual, err := a.git.RefValues(step.RefNames)
	if err != nil {
		return false, err
	}
	if len(refDrift(operation.Active.RefsBefore, actual)) == 0 {
		operation.Active = nil
		if err := a.git.WriteState(state); err != nil {
			return false, err
		}
		return true, nil
	}
	if !opts.acceptCurrent {
		return false, fmt.Errorf("git may have completed %s; inspect the changed refs, then run graphene continue --accept-current or graphene abort --force", operation.Active.ID)
	}
	ancestor, err := a.isAncestor(step.Op.Onto, step.Op.Top)
	if err != nil {
		return false, err
	}
	if !ancestor {
		return false, fmt.Errorf("cannot accept current refs because %s is not descended from %s", step.Op.Top, shortSyncRef(step.Op.Onto))
	}
	return true, a.finishJournalRebase(state, step)
}

func (a *App) finishJournalRebase(state State, step journalRebaseStep) error {
	operation := state.Operation
	if operation.Active == nil || operation.Active.Kind != "rebase" {
		return fmt.Errorf("pending %s has no active rebase to finish", operation.Kind)
	}
	currentHeads, err := a.git.LocalHeadRefValues()
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, ref := range step.RefNames {
		allowed[ref] = true
	}
	for ref, before := range operation.Active.RefInventory {
		if after := currentHeads[ref]; before != after && !allowed[ref] {
			return fmt.Errorf("rebase unexpectedly moved %s from %s to %s", ref, formatRefValue(before), formatRefValue(after))
		}
	}
	for ref, after := range currentHeads {
		if _, existed := operation.Active.RefInventory[ref]; !existed && !allowed[ref] {
			return fmt.Errorf("rebase unexpectedly created %s at %s", ref, formatRefValue(after))
		}
	}
	actual, err := a.git.RefValues(step.RefNames)
	if err != nil {
		return err
	}
	for ref, value := range actual {
		snapshot := operation.Refs[ref]
		snapshot.Expected = value
		operation.Refs[ref] = snapshot
	}
	progress, err := decodeRebaseProgress(operation)
	if err != nil {
		return err
	}
	progress.Next++
	operation.Active = nil
	if err := encodeRebaseProgress(operation, progress); err != nil {
		return err
	}
	return a.git.WriteState(state)
}

func (a *App) rebaseMutationRefs(current string, op RebaseOp, topOverride string) (map[string]string, error) {
	localRefs, err := a.localBranchRefs()
	if err != nil {
		return nil, err
	}
	top := op.Top
	if topOverride != "" {
		top = topOverride
	}
	out, err := a.git.Output("rev-list", op.Upstream+".."+top)
	if err != nil {
		return nil, err
	}
	commits := map[string]bool{}
	for commit := range bytes.FieldsSeq([]byte(out)) {
		commits[string(commit)] = true
	}
	affected := map[string]string{}
	if oid := localRefs[op.Top]; oid != "" {
		affected[op.Top] = oid
	}
	for branch, oid := range localRefs {
		if branch == op.Top || !commits[oid] {
			continue
		}
		if branch != current {
			checkedOut, err := a.git.BranchCheckedOut(branch)
			if err != nil {
				return nil, err
			}
			if checkedOut {
				continue
			}
		}
		affected[branch] = oid
	}
	return affected, nil
}
