package graphene

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

type refMutationProgress struct {
	RefNames       []string            `json:"refNames"`
	RefsAfter      map[string]RefValue `json:"refsAfter"`
	DeleteBranches []string            `json:"deleteBranches,omitempty"`
	SwitchBranch   string              `json:"switchBranch,omitempty"`
}

func encodeRefMutationProgress(operation *OperationJournal, progress refMutationProgress) error {
	data, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	operation.Progress = data
	return nil
}

func decodeRefMutationProgress(operation *OperationJournal) (refMutationProgress, error) {
	var progress refMutationProgress
	decoder := json.NewDecoder(bytes.NewReader(operation.Progress))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&progress); err != nil {
		return progress, fmt.Errorf("parse pending %s: %w", operation.Kind, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return progress, fmt.Errorf("parse pending %s: %w", operation.Kind, err)
	}
	if len(progress.RefNames) == 0 || !sort.StringsAreSorted(progress.RefNames) {
		return progress, fmt.Errorf("pending %s has invalid ref order", operation.Kind)
	}
	if progress.SwitchBranch != "" && !validBranchArgument(progress.SwitchBranch) {
		return progress, fmt.Errorf("pending %s has invalid switch branch %q", operation.Kind, progress.SwitchBranch)
	}
	for _, ref := range progress.RefNames {
		if _, ok := operation.Refs[ref]; !ok {
			return progress, fmt.Errorf("pending %s does not own %s", operation.Kind, ref)
		}
		value, ok := progress.RefsAfter[ref]
		if !ok {
			return progress, fmt.Errorf("pending %s has no target for %s", operation.Kind, ref)
		}
		if err := value.validate(ref); err != nil {
			return progress, err
		}
	}
	if len(progress.RefsAfter) != len(progress.RefNames) {
		return progress, fmt.Errorf("pending %s has unowned ref targets", operation.Kind)
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
	return progress, nil
}

func (a *App) startRefMutationOperation(
	state State,
	kind string,
	current string,
	desiredStacks []Stack,
	observedBranches []string,
	plannedRefs map[string]string,
	edits []refEdit,
	deleteBranches []string,
	switchBranch string,
	worktreePolicy string,
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
	operation.WorktreePolicy = worktreePolicy
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
	operation.DesiredStacks = cloneStacks(desiredStacks)
	if err := a.addOperationObservedBranches(operation, observedBranches, true); err != nil {
		return err
	}
	progress := refMutationProgress{
		RefsAfter:      map[string]RefValue{},
		DeleteBranches: append([]string(nil), deleteBranches...),
		SwitchBranch:   switchBranch,
	}
	sort.Strings(progress.DeleteBranches)
	edited := map[string]bool{}
	for _, edit := range edits {
		if edited[edit.Ref] {
			return fmt.Errorf("%s mutation contains duplicate ref %s", kind, edit.Ref)
		}
		edited[edit.Ref] = true
		actual, err := a.git.RefValue(edit.Ref)
		if err != nil {
			return err
		}
		if actual != edit.Old {
			return fmt.Errorf("cannot start %s because %s moved from %s to %s", kind, edit.Ref, formatRefValue(edit.Old), formatRefValue(actual))
		}
		if snapshot, exists := operation.Refs[edit.Ref]; exists {
			if snapshot.Expected != edit.Old {
				return fmt.Errorf("cannot start %s because observed %s was %s, want %s", kind, edit.Ref, formatRefValue(snapshot.Expected), formatRefValue(edit.Old))
			}
		} else {
			operation.Refs[edit.Ref] = JournalRef{Original: edit.Old, Expected: edit.Old}
		}
		progress.RefNames = append(progress.RefNames, edit.Ref)
		progress.RefsAfter[edit.Ref] = edit.New
	}
	sort.Strings(progress.RefNames)
	for _, branch := range progress.DeleteBranches {
		entries, err := a.git.BranchConfig(branch)
		if err != nil {
			return err
		}
		operation.Configs = append(operation.Configs, JournalConfig{
			Section:  "branch." + branch,
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
	if err := encodeRefMutationProgress(operation, progress); err != nil {
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
	return a.continueRefMutationOperation(state)
}

func (a *App) continueRefMutationOperation(state State) error {
	operation := state.Operation
	progress, err := decodeRefMutationProgress(operation)
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
	if progress.SwitchBranch != "" {
		current, err := a.git.CurrentBranch()
		if err != nil {
			return err
		}
		if current != progress.SwitchBranch {
			if operation.Active != nil {
				return fmt.Errorf("cannot switch to %q while journal action %q is active", progress.SwitchBranch, operation.Active.ID)
			}
			if err := a.git.RunOperationSwitch(progress.SwitchBranch); err != nil {
				return err
			}
		}
	}
	if len(progress.DeleteBranches) > 0 {
		if err := a.removeOperationBranchConfigs(state, progress.DeleteBranches); err != nil {
			return err
		}
		for _, branch := range progress.DeleteBranches {
			checkedOut, err := a.git.BranchCheckedOut(branch)
			if err != nil {
				return err
			}
			if checkedOut {
				return fmt.Errorf("branch %q is checked out in a worktree; switch that worktree away before continuing %s", branch, operation.Kind)
			}
		}
	}
	if err := a.continueRefMutationAction(state, progress); err != nil {
		return err
	}
	return a.commitOperation(state)
}

func (a *App) continueRefMutationAction(state State, progress refMutationProgress) error {
	operation := state.Operation
	before := map[string]RefValue{}
	for _, ref := range progress.RefNames {
		before[ref] = operation.Refs[ref].Expected
	}
	const actionID = "mutate-refs"
	if operation.Active == nil {
		actual, err := a.git.RefValues(progress.RefNames)
		if err != nil {
			return err
		}
		if drift := refDrift(before, actual); len(drift) > 0 {
			return fmt.Errorf("cannot continue %s because refs changed:\n%s", operation.Kind, formatRefDrift(drift))
		}
		operation.Active = &JournalAction{ID: actionID, Kind: "update-refs", RefsBefore: before, RefsAfter: progress.RefsAfter}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	} else if operation.Active.ID != actionID || operation.Active.Kind != "update-refs" {
		return fmt.Errorf("cannot mutate refs while journal action %q is active", operation.Active.ID)
	}
	actual, err := a.git.RefValues(progress.RefNames)
	if err != nil {
		return err
	}
	if len(refDrift(progress.RefsAfter, actual)) == 0 {
		if a.refMutationUpdatesCurrentBranch(operation, progress) {
			target := progress.RefsAfter[progress.RefNames[0]]
			if err := a.requireNoIgnoredTreeCollision(target.OID, operation.Kind+" worktree update"); err != nil {
				return err
			}
			if err := a.git.RunOperationReset("--keep", target.OID); err != nil {
				return err
			}
		}
		for _, ref := range progress.RefNames {
			snapshot := operation.Refs[ref]
			snapshot.Expected = progress.RefsAfter[ref]
			operation.Refs[ref] = snapshot
		}
		operation.Active = nil
		return a.git.WriteState(state)
	}
	if drift := refDrift(before, actual); len(drift) > 0 {
		return fmt.Errorf("cannot recover %s ref mutation:\n%s", operation.Kind, formatRefDrift(drift))
	}
	if a.refMutationUpdatesCurrentBranch(operation, progress) {
		target := progress.RefsAfter[progress.RefNames[0]]
		if err := a.requireNoIgnoredTreeCollision(target.OID, operation.Kind+" fast-forward"); err != nil {
			return err
		}
		if err := a.git.Run("-c", "submodule.recurse=false", "merge", "--ff-only", target.OID); err != nil {
			return err
		}
		return a.continueRefMutationAction(state, progress)
	}
	edits := make([]refEdit, 0, len(progress.RefNames))
	for _, ref := range progress.RefNames {
		edits = append(edits, refEdit{Ref: ref, Old: before[ref], New: progress.RefsAfter[ref]})
	}
	if err := a.git.UpdateRefs(edits); err != nil {
		return err
	}
	return a.continueRefMutationAction(state, progress)
}

func (a *App) refMutationUpdatesCurrentBranch(operation *OperationJournal, progress refMutationProgress) bool {
	if operation.Kind != "track" || len(progress.RefNames) != 1 || operation.OriginalBranch == "" {
		return false
	}
	return progress.RefNames[0] == "refs/heads/"+operation.OriginalBranch
}
