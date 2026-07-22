package graphene

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	worktreeRestoreHard   = "hard"
	worktreeRestoreIndex  = "index"
	worktreeRestoreNone   = "none"
	worktreeRestoreSwitch = "switch"
)

func (a *App) continueOperation(state State, opts continueOptions) error {
	operation := state.Operation
	if operation == nil {
		return fmt.Errorf("no graphene operation in progress")
	}
	if err := operation.validate(); err != nil {
		return err
	}
	if err := a.requireOperationWorktree(operation); err != nil {
		return err
	}
	if _, err := a.requireCompatibleGitOperation(operation); err != nil {
		return err
	}
	if operation.Phase != operationCleanup {
		if _, err := a.loadOperationArtifacts(operation); err != nil {
			return fmt.Errorf("validate operation recovery artifacts: %w", err)
		}
	}
	switch operation.Phase {
	case operationCleanup:
		return a.cleanupCommittedOperation(state)
	case operationCommitting:
		if operation.Kind == "sync" {
			progress, err := a.upgradeSyncSubmoduleBackups(state)
			if err != nil {
				return err
			}
			if err := a.installSyncSubmoduleBackups(progress, false); err != nil {
				return err
			}
		}
		if err := a.verifyOperationReadyToCommit(operation); err != nil {
			return err
		}
		state.Stacks = cloneStacks(operation.DesiredStacks)
		operation.Phase = operationCleanup
		if err := a.git.WriteState(state); err != nil {
			return err
		}
		return a.cleanupCommittedOperation(state)
	case operationRollingBack:
		return fmt.Errorf("%s abort is already in progress; rerun graphene abort", operation.Kind)
	}

	switch operation.Kind {
	case "sync":
		return a.continueSyncOperation(state, opts)
	case "restack", "amend", "squash", "new":
		return a.continueRebaseQueueOperation(state, opts)
	case "split":
		if operation.Phase == operationPreparing || operation.Phase == operationInteractive {
			return a.continueSplitOperation(state, opts)
		}
		return a.continueRebaseQueueOperation(state, opts)
	case "delete", "import", "track":
		return a.continueRefMutationOperation(state)
	default:
		return fmt.Errorf("cannot continue unsupported graphene operation %q", operation.Kind)
	}
}

func (a *App) abortOperation(state State, opts abortOptions) error {
	operation := state.Operation
	if operation == nil {
		return fmt.Errorf("no graphene operation in progress")
	}
	if err := operation.validate(); err != nil {
		return err
	}
	if operation.Phase == operationPreparing {
		if err := prepareOperationBackups(operation); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	if operation.Phase == operationCleanup {
		return fmt.Errorf("%s already crossed its commit point; run graphene continue to finish cleanup", operation.Kind)
	}
	artifacts, err := a.loadOperationArtifacts(operation)
	if err != nil {
		return fmt.Errorf("validate operation recovery artifacts before abort: %w", err)
	}
	var syncProgress *syncJournalProgress
	if operation.Kind == "sync" {
		progress, err := a.upgradeSyncSubmoduleBackups(state)
		if err != nil {
			return err
		}
		syncProgress = &progress
		if !operation.WorktreeRestored {
			if err := a.installSyncSubmoduleBackups(progress, false); err != nil {
				return fmt.Errorf("protect original submodule commits before abort: %w", err)
			}
		}
	}
	if operation.Phase != operationRollingBack && !operation.WorktreeBoundaryCrossed {
		if err := a.verifyOperationWorktreeFingerprint(operation); err != nil && !opts.force {
			return fmt.Errorf("cannot abort safely: %w (use graphene abort --force to discard the post-snapshot changes)", err)
		}
	}
	resumingWorktreeRestore := operation.Phase == operationRollingBack && !operation.WorktreeRestored && operation.WorktreePolicy != "" && operation.WorktreePolicy != worktreeRestoreNone
	if resumingWorktreeRestore && !opts.force {
		return fmt.Errorf("the previous %s abort stopped during destructive rollback; inspect the worktree, then rerun graphene abort --force to resume", operation.Kind)
	}
	if err := a.requireOperationWorktree(operation); err != nil {
		return err
	}
	if !operation.WorktreeRestored {
		inProgress, err := a.requireCompatibleGitOperation(operation)
		if err != nil {
			return err
		}
		if operation.Kind == "sync" {
			progress, progressErr := decodeSyncProgress(operation)
			if progressErr != nil {
				return progressErr
			}
			if err := a.requireRecoverableSyncSubmodules(progress, !inProgress); err != nil {
				return fmt.Errorf("cannot safely restore the worktree: %w", err)
			}
		} else if inProgress {
			if err := a.git.requireCleanInitializedSubmodules(); err != nil {
				return fmt.Errorf("cannot safely restore the worktree: %w", err)
			}
		} else {
			if err := a.git.RequireRecoverableWorktree(); err != nil {
				return fmt.Errorf("cannot safely restore the worktree: %w", err)
			}
		}
	}
	if err := a.reconcileAbortActive(state, opts.force); err != nil {
		return err
	}

	drift, actual, err := a.git.OperationAbortRefDrift(operation)
	if err != nil {
		return err
	}
	configDrift, err := a.git.OperationConfigDrift(operation, operation.Phase == operationRollingBack)
	if err != nil {
		return err
	}
	if len(drift) > 0 && !opts.force {
		return fmt.Errorf("cannot abort because operation-owned refs changed:\n%s\ninspect the changes, then use graphene abort --force to preserve and overwrite them", formatRefDrift(drift))
	}
	if len(configDrift) > 0 && !opts.force {
		return fmt.Errorf("cannot abort because operation-owned config changed:\n%s\ninspect the changes, then use graphene abort --force to preserve and overwrite it", formatConfigDrift(configDrift))
	}
	if opts.force && len(drift) > 0 {
		if err := a.git.PreserveUnexpectedRefs(operation, onlyDriftedRefs(drift, actual)); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}

	if opts.force && len(configDrift) > 0 {
		if err := a.preserveUnexpectedConfigs(operation, configDrift); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}

	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if operation.WorktreePolicy == worktreeRestoreHard && operation.Kind != "split" && operation.Phase != operationRollingBack && !inProgress {
		var dirty bool
		var dirtyErr error
		if operation.Kind == "sync" {
			dirty, dirtyErr = a.git.HasTrackedChangesIgnoringSubmodules()
		} else {
			dirty, dirtyErr = a.git.HasTrackedChanges()
		}
		if dirtyErr != nil {
			return dirtyErr
		}
		if dirty && !opts.force {
			return fmt.Errorf("cannot abort because the worktree has tracked changes; preserve them or use graphene abort --force to discard them")
		}
	}
	if !operation.WorktreeRestored {
		changed, snapshotErr := a.snapshotOperationAddedPaths(operation)
		if snapshotErr != nil {
			return fmt.Errorf("snapshot added worktree paths before abort: %w", snapshotErr)
		}
		if changed {
			if err := a.git.WriteState(state); err != nil {
				return err
			}
			artifacts, err = a.loadOperationArtifacts(operation)
			if err != nil {
				return fmt.Errorf("validate expanded operation recovery artifacts before abort: %w", err)
			}
		}
	}

	if operation.Phase != operationRollingBack {
		operation.Phase = operationRollingBack
		if !operation.WorktreeRestored && operation.WorktreePolicy != "" && operation.WorktreePolicy != worktreeRestoreNone {
			operation.WorktreeRestoreStarted = true
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}

	if inProgress {
		if err := a.git.RunOperationRebase("--abort"); err != nil {
			return err
		}
	}

	postDrift, postActual, err := a.git.OperationAbortRefDrift(operation)
	if err != nil {
		return err
	}
	if len(postDrift) > 0 && !opts.force {
		return fmt.Errorf("cannot finish abort because operation-owned refs changed while aborting:\n%s\ninspect the changes, then use graphene abort --force to preserve and overwrite them", formatRefDrift(postDrift))
	}
	if opts.force && len(postDrift) > 0 {
		if err := a.git.PreserveUnexpectedRefs(operation, onlyDriftedRefs(postDrift, postActual)); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	if err := a.git.RestoreOperationRefs(operation, postActual); err != nil {
		return err
	}
	for ref, snapshot := range operation.Refs {
		snapshot.Expected = snapshot.Original
		operation.Refs[ref] = snapshot
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	if err := a.restoreOperationConfigs(state, opts.force); err != nil {
		return err
	}
	if !operation.WorktreeRestored {
		if !operation.WorktreeRestoreStarted {
			operation.WorktreeRestoreStarted = true
			if err := a.git.WriteState(state); err != nil {
				return err
			}
		}
		if err := a.restoreOperationWorktree(operation, artifacts); err != nil {
			return err
		}
		if operation.Kind == "sync" {
			progress, progressErr := decodeSyncProgress(operation)
			if progressErr != nil {
				return progressErr
			}
			if err := a.restoreSyncSubmodules(
				progress.InitialSubmodules,
				progress.BaselineTargetSubmodules,
				progress.BaselineSubmodule,
				progress.ReturnTargetSubmodules,
			); err != nil {
				return err
			}
		}
		operation.WorktreeRestored = true
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}

	state.Stacks = cloneStacks(operation.OriginalStacks)
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	if err := a.verifyAbortFinished(state, opts.force); err != nil {
		return err
	}
	if syncProgress != nil {
		if err := a.removeSyncSubmoduleBackups(*syncProgress); err != nil {
			return err
		}
	}
	if err := a.git.RemoveOperationBackups(operation); err != nil {
		return err
	}
	if err := a.removeOperationArtifacts(operation); err != nil {
		return err
	}
	recoveryRefs := make(map[string]string, len(operation.RecoveryRefs))
	maps.Copy(recoveryRefs, operation.RecoveryRefs)
	recoveryArtifact := operation.RecoveryArtifact
	state.Operation = nil
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	if len(recoveryRefs) > 0 {
		refs := make([]string, 0, len(recoveryRefs))
		for ref := range recoveryRefs {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		fmt.Fprintln(a.stdout, "Preserved displaced refs:")
		for _, ref := range refs {
			fmt.Fprintf(a.stdout, "  %s -> %s\n", ref, recoveryRefs[ref])
		}
	}
	if recoveryArtifact != "" {
		dir, err := a.git.GrapheneDir()
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "Preserved displaced config: %s\n", filepath.Join(dir, recoveryArtifact))
	}
	return nil
}

func (a *App) requireCompatibleGitOperation(operation *OperationJournal) (bool, error) {
	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return false, err
	}
	if inProgress {
		if operation.Active == nil || operation.Active.Kind != "rebase" {
			return false, fmt.Errorf("a Git rebase not owned by the pending %s operation is in progress; finish or abort it without Graphene first", operation.Kind)
		}
		return true, nil
	}
	if err := a.git.RequireNoGitOperation(); err != nil {
		return false, err
	}
	return false, nil
}

func (a *App) verifyAbortFinished(state State, force bool) error {
	operation := state.Operation
	for range 3 {
		drift, actual, err := a.git.OperationRefDrift(operation)
		if err != nil {
			return err
		}
		configDrift, err := a.git.OperationConfigDrift(operation, true)
		if err != nil {
			return err
		}
		if len(drift) == 0 && len(configDrift) == 0 {
			return nil
		}
		if !force {
			if len(drift) > 0 {
				return fmt.Errorf("cannot finish abort because operation-owned refs changed while restoring the worktree:\n%s\ninspect the changes, then use graphene abort --force to preserve and overwrite them", formatRefDrift(drift))
			}
			return fmt.Errorf("cannot finish abort because operation-owned config changed while restoring the worktree:\n%s\ninspect the changes, then use graphene abort --force to preserve and overwrite it", formatConfigDrift(configDrift))
		}
		if len(drift) > 0 {
			if err := a.git.PreserveUnexpectedRefs(operation, onlyDriftedRefs(drift, actual)); err != nil {
				return err
			}
			if err := a.git.WriteState(state); err != nil {
				return err
			}
			if err := a.git.RestoreOperationRefs(operation, actual); err != nil {
				return err
			}
		}
		if len(configDrift) > 0 {
			if err := a.preserveUnexpectedConfigs(operation, configDrift); err != nil {
				return err
			}
			if err := a.git.WriteState(state); err != nil {
				return err
			}
			if err := a.restoreOperationConfigs(state, true); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("cannot finish abort because operation-owned refs or config keep changing")
}

func (a *App) reconcileAbortActive(state State, force bool) error {
	operation := state.Operation
	if operation.Active == nil {
		return nil
	}
	if operation.Active.Kind == "delete-config" {
		section, ok := strings.CutPrefix(operation.Active.ID, "delete-config:")
		if !ok || section == "" {
			return fmt.Errorf("journaled config deletion has invalid action %q", operation.Active.ID)
		}
		for index := range operation.Configs {
			config := &operation.Configs[index]
			if config.Section != section {
				continue
			}
			branch, ok := strings.CutPrefix(section, "branch.")
			if !ok || branch == "" {
				return fmt.Errorf("journaled config deletion has invalid section %q", section)
			}
			actual, err := a.git.BranchConfig(branch)
			if err != nil {
				return err
			}
			if !equalConfigEntries(actual, config.Expected) && len(actual) != 0 {
				if !force {
					return fmt.Errorf("cannot attribute the current config for %q to journaled deletion %q; use graphene abort --force to preserve and overwrite it", branch, operation.Active.ID)
				}
			} else if len(actual) == 0 {
				config.Expected = nil
			}
			operation.Active = nil
			return a.git.WriteState(state)
		}
		return fmt.Errorf("journaled config deletion %q has no config snapshot", operation.Active.ID)
	}
	if operation.Active.Kind != "commit" {
		return nil
	}
	if len(operation.Active.RefsBefore) != 1 {
		return fmt.Errorf("journaled commit %q owns an invalid ref set", operation.Active.ID)
	}
	for ref, before := range operation.Active.RefsBefore {
		actual, err := a.git.RefValue(ref)
		if err != nil {
			return err
		}
		if actual == before {
			return nil
		}
		var commit *journalCommit
		if operation.Kind == "split" && operation.Phase == operationInteractive {
			progress, err := decodeSplitProgress(operation)
			if err != nil {
				return err
			}
			if progress.Commit != nil {
				commit = &journalCommit{Branch: progress.Commit.Branch, Mode: "new"}
			}
		} else {
			progress, err := decodeRebaseProgress(operation)
			if err != nil {
				return err
			}
			commit = progress.Commit
		}
		if commit == nil || ref != "refs/heads/"+commit.Branch {
			return fmt.Errorf("journaled commit %q does not match its progress", operation.Active.ID)
		}
		if err := a.validateJournalCommitResult(*commit, before, actual); err != nil {
			if force {
				return nil
			}
			return err
		}
		subject, err := a.git.Output("reflog", "show", "--format=%gs", "-1", ref)
		if err != nil {
			return err
		}
		marker := fmt.Sprintf("graphene:%s:%s", operation.ID, operation.Active.ID)
		if !strings.Contains(subject, marker) {
			if force {
				return nil
			}
			return fmt.Errorf("cannot attribute the current value of %s to journaled commit %q; use graphene abort --force to preserve and overwrite it", ref, operation.Active.ID)
		}
		snapshot := operation.Refs[ref]
		snapshot.Expected = actual
		operation.Refs[ref] = snapshot
		return a.git.WriteState(state)
	}
	return nil
}

func onlyDriftedRefs(drift []RefDrift, actual map[string]RefValue) map[string]RefValue {
	filtered := make(map[string]RefValue, len(drift))
	for _, item := range drift {
		filtered[item.Ref] = actual[item.Ref]
	}
	return filtered
}

func (a *App) restoreOperationConfigs(state State, force bool) error {
	operation := state.Operation
	for i := range operation.Configs {
		config := &operation.Configs[i]
		branch, ok := strings.CutPrefix(config.Section, "branch.")
		if !ok || branch == "" {
			return fmt.Errorf("invalid branch config journal section %q", config.Section)
		}
		actual, err := a.git.BranchConfig(branch)
		if err != nil {
			return err
		}
		if equalConfigEntries(actual, config.Original) {
			config.Expected = append([]ConfigEntry(nil), config.Original...)
			continue
		}
		allowed := equalConfigEntries(actual, config.Expected)
		if operation.Phase == operationRollingBack && configEntriesPrefix(actual, config.Original) {
			allowed = true
		}
		if !allowed && !force {
			return fmt.Errorf("cannot restore %s because its config changed while aborting", config.Section)
		}
		if !allowed && force {
			if err := a.preserveUnexpectedConfigs(operation, []ConfigDrift{{Section: config.Section, Expected: config.Expected, Actual: actual}}); err != nil {
				return err
			}
			if err := a.git.WriteState(state); err != nil {
				return err
			}
		}
		if err := a.git.RestoreBranchConfig(config.Section, config.Original, actual); err != nil {
			return err
		}
		actual, err = a.git.BranchConfig(branch)
		if err != nil {
			return err
		}
		if !equalConfigEntries(actual, config.Original) {
			return fmt.Errorf("restored config for %q does not match its journal snapshot", branch)
		}
		config.Expected = append([]ConfigEntry(nil), config.Original...)
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	return nil
}

func configEntriesPrefix(actual, expected []ConfigEntry) bool {
	if len(actual) > len(expected) {
		return false
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return false
		}
	}
	return true
}

func (a *App) commitOperation(state State) error {
	operation := state.Operation
	if operation == nil {
		return fmt.Errorf("no graphene operation to commit")
	}
	if err := a.verifyOperationReadyToCommit(operation); err != nil {
		return err
	}
	operation.Phase = operationCommitting
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	state.Stacks = cloneStacks(operation.DesiredStacks)
	operation.Phase = operationCleanup
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.cleanupCommittedOperation(state)
}

func (a *App) verifyOperationReadyToCommit(operation *OperationJournal) error {
	if _, err := a.loadOperationArtifacts(operation); err != nil {
		return fmt.Errorf("validate operation recovery artifacts before commit: %w", err)
	}
	if operation.Active != nil {
		return fmt.Errorf("cannot commit %s while journal action %q is active", operation.Kind, operation.Active.ID)
	}
	drift, _, err := a.git.OperationRefDrift(operation)
	if err != nil {
		return err
	}
	if len(drift) > 0 {
		return fmt.Errorf("cannot commit %s because operation-owned refs changed:\n%s", operation.Kind, formatRefDrift(drift))
	}
	if operation.ValidationRefsComplete {
		validationDrift, err := a.git.OperationValidationRefDrift(operation)
		if err != nil {
			return err
		}
		if len(validationDrift) > 0 {
			return fmt.Errorf("cannot commit %s because stack dependency refs changed:\n%s", operation.Kind, formatRefDrift(validationDrift))
		}
	}
	configDrift, err := a.git.OperationConfigDrift(operation, false)
	if err != nil {
		return err
	}
	if len(configDrift) > 0 {
		return fmt.Errorf("cannot commit %s because operation-owned config changed:\n%s", operation.Kind, formatConfigDrift(configDrift))
	}
	if operation.Kind == "sync" {
		progress, err := decodeSyncProgress(operation)
		if err != nil {
			return err
		}
		if len(progress.InitialSubmodules) > 0 && len(progress.SubmoduleBackups) == 0 && operation.Phase != operationCleanup {
			return fmt.Errorf("cannot commit sync before its submodule recovery refs are journaled")
		}
		if progress.ReturnSubmodulesPlanned || progress.ReturnHead != "" {
			if !progress.ReturnSubmodulesPlanned || !progress.ReturnSubmodulesPrepared || progress.ReturnHead == "" {
				return fmt.Errorf("cannot commit sync before return-branch submodules are fully prepared")
			}
			head, err := a.git.Head()
			if err != nil {
				return err
			}
			if head != progress.ReturnHead {
				return fmt.Errorf("cannot commit sync because worktree HEAD moved from return target %s to %s", progress.ReturnHead, head)
			}
			branch, err := a.git.Output("branch", "--show-current")
			if err != nil {
				return err
			}
			if progress.ReturnRef == "" && branch != progress.ReturnBranch {
				return fmt.Errorf("cannot commit sync because worktree moved from return branch %q to %q", progress.ReturnBranch, branch)
			}
			if progress.ReturnRef != "" && branch != "" && branch != progress.ReturnBranch {
				return fmt.Errorf("cannot commit sync because worktree moved from return branch %q to %q", progress.ReturnBranch, branch)
			}
			if err := a.verifySyncSubmoduleTarget(progress.ReturnTargetSubmodules); err != nil {
				return fmt.Errorf("cannot commit sync because return-branch submodules changed: %w", err)
			}
		}
		if err := a.verifySyncSubmoduleBackups(progress); err != nil {
			return fmt.Errorf("cannot commit sync because its submodule recovery refs changed: %w", err)
		}
	}
	if err := a.validateOperationDesiredStacks(operation); err != nil {
		return err
	}
	return nil
}

func (a *App) validateOperationDesiredStacks(operation *OperationJournal) error {
	ontoOIDs, err := a.operationScopedOntoOIDs(operation)
	if err != nil {
		return err
	}
	for _, stack := range operation.DesiredStacks {
		parent := stack.Base
		for _, branch := range stack.Branches {
			childSnapshot, owned := operation.Refs["refs/heads/"+branch]
			childChanged := owned && childSnapshot.Expected != childSnapshot.Original
			parentSnapshot, parentOwned := operation.Refs["refs/heads/"+parent]
			parentChanged := parentOwned && parentSnapshot.Expected != parentSnapshot.Original
			originalParent, hadOriginalParent := stackParent(operation.OriginalStacks, branch)
			edgeChanged := !hadOriginalParent || originalParent != parent
			plannedRewrite, err := a.operationSuccessfullyPlansBranch(operation, branch)
			if err != nil {
				return err
			}
			if childChanged || edgeChanged || parentChanged && plannedRewrite {
				parentValue, err := a.operationBranchValue(operation, parent)
				if err != nil {
					return err
				}
				childValue, err := a.operationBranchValue(operation, branch)
				if err != nil {
					return err
				}
				if !parentValue.Exists || !childValue.Exists {
					return fmt.Errorf("cannot commit %s because desired stack edge %q -> %q is missing a branch", operation.Kind, parent, branch)
				}
				candidates := map[string]bool{parentValue.OID: true}
				for oid := range ontoOIDs[branch] {
					candidates[oid] = true
				}
				distance, err := a.operationExpectedStackDistance(operation, branch)
				if err != nil {
					return err
				}
				validBase := false
				for oid := range candidates {
					valid, err := a.isLinearCommitDistance(oid, childValue.OID, distance)
					if err != nil {
						return err
					}
					if valid {
						validBase = true
						break
					}
				}
				if !validBase {
					distanceLabel := fmt.Sprintf("%d commits", distance)
					if distance == 1 {
						distanceLabel = "one commit"
					}
					return fmt.Errorf("cannot commit %s because branch %q is not exactly %s on top of its planned base %q", operation.Kind, branch, distanceLabel, parent)
				}
			}
			parent = branch
		}
	}
	return nil
}

func (a *App) operationScopedOntoOIDs(operation *OperationJournal) (map[string]map[string]bool, error) {
	type plannedRebase struct {
		op   RebaseOp
		refs []string
	}
	var rebases []plannedRebase
	if operation.Kind == "sync" {
		progress, err := decodeSyncProgress(operation)
		if err != nil {
			return nil, err
		}
		for _, component := range progress.Components {
			for _, op := range component.Ops {
				rebases = append(rebases, plannedRebase{op: op, refs: component.RefNames})
			}
		}
	} else if operation.Kind == "restack" || operation.Kind == "amend" || operation.Kind == "squash" || operation.Kind == "new" || (operation.Kind == "split" && operation.Phase != operationInteractive && operation.Phase != operationPreparing) {
		progress, err := decodeRebaseProgress(operation)
		if err != nil {
			return nil, err
		}
		for _, step := range progress.Steps {
			rebases = append(rebases, plannedRebase{op: step.Op, refs: step.RefNames})
		}
	}
	result := map[string]map[string]bool{}
	for _, rebase := range rebases {
		op := rebase.op
		affected := map[string]bool{}
		for _, ref := range rebase.refs {
			branch := strings.TrimPrefix(ref, "refs/heads/")
			if branch != ref {
				affected[branch] = true
			}
		}
		var onto string
		if validObjectID(op.Onto) {
			onto = op.Onto
		} else {
			value, err := a.operationBranchValue(operation, op.Onto)
			if err != nil {
				return nil, err
			}
			if value.Exists {
				onto = value.OID
			}
		}
		if onto == "" {
			continue
		}
		for _, stack := range operation.DesiredStacks {
			parent := stack.Base
			for _, branch := range stack.Branches {
				if affected[branch] && !affected[parent] {
					if result[branch] == nil {
						result[branch] = map[string]bool{}
					}
					result[branch][onto] = true
				}
				parent = branch
			}
		}
	}
	return result, nil
}

func (a *App) operationExpectedStackDistance(operation *OperationJournal, branch string) (int, error) {
	if operation.Kind != "restack" {
		return 1, nil
	}
	progress, err := decodeRebaseProgress(operation)
	if err != nil {
		return 0, err
	}
	if progress.FastForward == nil || progress.FastForward.Branch != branch {
		return 1, nil
	}
	parent, ok := stackParent(operation.OriginalStacks, branch)
	if !ok {
		return 0, fmt.Errorf("cannot validate fast-forwarded branch %q because its original stack parent is missing", branch)
	}
	parentValue, err := a.operationOriginalBranchValue(operation, parent)
	if err != nil {
		return 0, err
	}
	if !parentValue.Exists {
		return 0, fmt.Errorf("cannot validate fast-forwarded branch %q because original parent %q is missing", branch, parent)
	}
	out, err := a.git.Output("rev-list", "--no-merges", "--count", parentValue.OID+".."+progress.FastForward.New)
	if err != nil {
		return 0, err
	}
	distance, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || distance < 1 {
		return 0, fmt.Errorf("cannot validate fast-forwarded branch %q commit distance %q", branch, out)
	}
	return distance, nil
}

func stackParent(stacks []Stack, branch string) (string, bool) {
	for _, stack := range stacks {
		parent := stack.Base
		for _, candidate := range stack.Branches {
			if candidate == branch {
				return parent, true
			}
			parent = candidate
		}
	}
	return "", false
}

func (a *App) operationOriginalBranchValue(operation *OperationJournal, branch string) (RefValue, error) {
	if snapshot, ok := operation.Refs["refs/heads/"+branch]; ok {
		return snapshot.Original, nil
	}
	if value, ok := operation.ValidationRefs["refs/heads/"+branch]; ok {
		return value, nil
	}
	if operation.ValidationRefsComplete {
		return RefValue{}, fmt.Errorf("operation is missing its validation snapshot for branch %q", branch)
	}
	return a.git.RefValue("refs/heads/" + branch)
}

func (a *App) operationSuccessfullyPlansBranch(operation *OperationJournal, branch string) (bool, error) {
	ref := "refs/heads/" + branch
	if operation.Kind == "sync" {
		progress, err := decodeSyncProgress(operation)
		if err != nil {
			return false, err
		}
		for _, component := range progress.Components {
			if component.Status != syncComponentSucceeded {
				continue
			}
			if slices.Contains(component.RefNames, ref) {
				return true, nil
			}
		}
		return false, nil
	}
	switch operation.Kind {
	case "restack", "amend", "squash", "new":
	case "split":
		if operation.Phase == operationInteractive || operation.Phase == operationPreparing {
			return false, nil
		}
	default:
		return false, nil
	}
	progress, err := decodeRebaseProgress(operation)
	if err != nil {
		return false, err
	}
	for _, step := range progress.Steps {
		if slices.Contains(step.RefNames, ref) {
			return true, nil
		}
	}
	return false, nil
}

func (a *App) isLinearCommitDistance(parent, child string, distance int) (bool, error) {
	if distance < 1 || parent == child {
		return false, nil
	}
	ancestor, err := a.isAncestor(parent, child)
	if err != nil || !ancestor {
		return false, err
	}
	out, err := a.git.Output("rev-list", "--parents", parent+".."+child)
	if err != nil {
		return false, err
	}
	lines := strings.FieldsFunc(out, func(r rune) bool { return r == '\n' || r == '\r' })
	if len(lines) != distance {
		return false, nil
	}
	for _, line := range lines {
		if len(strings.Fields(line)) != 2 {
			return false, nil
		}
	}
	return true, nil
}

func (a *App) operationBranchValue(operation *OperationJournal, branch string) (RefValue, error) {
	if snapshot, ok := operation.Refs["refs/heads/"+branch]; ok {
		return snapshot.Expected, nil
	}
	if value, ok := operation.ValidationRefs["refs/heads/"+branch]; ok {
		return value, nil
	}
	if operation.ValidationRefsComplete {
		return RefValue{}, fmt.Errorf("operation is missing its validation snapshot for branch %q", branch)
	}
	return a.git.RefValue("refs/heads/" + branch)
}

func (a *App) cleanupCommittedOperation(state State) error {
	operation := state.Operation
	if operation == nil {
		return nil
	}
	if operation.Phase != operationCleanup {
		return fmt.Errorf("cannot clean up %s in phase %s", operation.Kind, operation.Phase)
	}
	if operation.Kind == "sync" {
		progress, err := decodeSyncProgress(operation)
		if err != nil {
			return err
		}
		if err := a.removeSyncSubmoduleBackups(progress); err != nil {
			return err
		}
	}
	if err := a.git.RemoveOperationBackups(operation); err != nil {
		return err
	}
	if err := a.removeOperationArtifacts(operation); err != nil {
		return err
	}
	state.Operation = nil
	return a.git.WriteState(state)
}

func (a *App) requireOperationWorktree(operation *OperationJournal) error {
	current, err := a.git.WorktreeID()
	if err != nil {
		return err
	}
	if filepath.Clean(current) != filepath.Clean(operation.Worktree) {
		return fmt.Errorf("pending %s belongs to worktree %s; continue or abort it there", operation.Kind, operation.Worktree)
	}
	return nil
}

func (a *App) restoreOperationWorktree(operation *OperationJournal, artifacts loadedOperationArtifacts) error {
	switch operation.WorktreePolicy {
	case "", worktreeRestoreNone:
		return nil
	case worktreeRestoreSwitch:
		if operation.OriginalBranch == "" {
			return nil
		}
		current, err := a.git.CurrentBranch()
		if err != nil {
			return err
		}
		if current == operation.OriginalBranch {
			return nil
		}
		return a.git.RunOperationSwitch(operation.OriginalBranch)
	case worktreeRestoreHard:
		if operation.OriginalHead == "" {
			return fmt.Errorf("%s operation is missing its original HEAD", operation.Kind)
		}
		if err := a.git.RunOperationSwitch("--force", "--detach", operation.OriginalHead); err != nil {
			return err
		}
		if artifacts.RawWorktree != nil {
			if err := a.restoreOperationRawWorktree(*artifacts.RawWorktree); err != nil {
				return err
			}
		} else {
			if err := a.restoreOperationUntracked(artifacts.Untracked); err != nil {
				return err
			}
		}
		return a.reattachOperationHead(operation)
	case worktreeRestoreIndex:
		if err := a.git.RunOperationSwitch("--force", "--detach", operation.OriginalHead); err != nil {
			return err
		}
		git, err := a.recoveryGit()
		if err != nil {
			return err
		}
		if len(artifacts.Staged) > 0 {
			applyMode := "--index"
			if artifacts.RawWorktree != nil {
				applyMode = "--cached"
			}
			if err := git.RunWithInput(string(artifacts.Staged), "apply", applyMode, "--whitespace=nowarn"); err != nil {
				return fmt.Errorf("restore staged object contents: %w", err)
			}
		}
		if err := a.restoreOperationIndex(operation, artifacts.Index, artifacts.SharedIndex); err != nil {
			return err
		}
		if artifacts.RawWorktree != nil {
			if err := a.restoreOperationRawWorktree(*artifacts.RawWorktree); err != nil {
				return err
			}
		} else {
			if err := a.restoreOperationWorktreePatch(artifacts.Worktree); err != nil {
				return err
			}
			if err := a.restoreOperationUntracked(artifacts.Untracked); err != nil {
				return err
			}
		}
		return a.reattachOperationHead(operation)
	default:
		return fmt.Errorf("unsupported worktree restore policy %q", operation.WorktreePolicy)
	}
}

func (a *App) reattachOperationHead(operation *OperationJournal) error {
	if operation.OriginalBranch == "" {
		return nil
	}
	ref := "refs/heads/" + operation.OriginalBranch
	actual, err := a.git.RefValue(ref)
	if err != nil {
		return err
	}
	want := RefValue{Exists: true, OID: operation.OriginalHead}
	if actual != want {
		return fmt.Errorf("cannot reattach HEAD because %s changed from %s to %s while aborting", ref, formatRefValue(want), formatRefValue(actual))
	}
	return a.git.OutputErr("symbolic-ref", "HEAD", ref)
}

func (a *App) recoveryGit() (Git, error) {
	root, err := a.git.Root()
	if err != nil {
		return Git{}, err
	}
	git := a.git
	git.Dir = root
	return git, nil
}

func (a *App) operationArtifactDir(operation *OperationJournal) (string, error) {
	if err := operation.validate(); err != nil {
		return "", err
	}
	dir, err := a.git.GrapheneDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "artifacts", operation.ID), nil
}

func (a *App) snapshotOperationIndex(operation *OperationJournal) error {
	index, err := a.git.GitPath("index")
	if err != nil {
		return err
	}
	data, err := os.ReadFile(index)
	if err != nil {
		return fmt.Errorf("snapshot git index: %w", err)
	}
	indexInfo, err := os.Stat(index)
	if err != nil {
		return fmt.Errorf("inspect git index mode: %w", err)
	}
	operation.IndexMode = uint32(indexInfo.Mode().Perm())
	dir, err := a.operationArtifactDir(operation)
	if err != nil {
		return err
	}
	if err := ensureDurableDir(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "index")
	if err := writeAtomicFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write index snapshot: %w", err)
	}
	operation.IndexArtifact = "index"
	recordOperationArtifact(operation, operation.IndexArtifact, data)
	git, err := a.recoveryGit()
	if err != nil {
		return err
	}
	diffArgs := []string{"diff", "--binary", "--full-index", "--no-relative", "--no-ext-diff", "--no-textconv", "--submodule=short", "--default-prefix"}
	stagedArgs := append(append([]string(nil), diffArgs...), "--cached", operation.OriginalHead, "--")
	staged, err := git.OutputBytes(stagedArgs...)
	if err != nil {
		return fmt.Errorf("snapshot staged worktree changes: %w", err)
	}
	if err := writeAtomicFile(filepath.Join(dir, "staged.patch"), staged, 0o600); err != nil {
		return fmt.Errorf("write staged worktree snapshot: %w", err)
	}
	operation.StagedArtifact = "staged.patch"
	recordOperationArtifact(operation, operation.StagedArtifact, staged)
	if operation.RawWorktreeArtifact == "" {
		worktreeArgs := append(append([]string(nil), diffArgs...), "--")
		patch, err := git.OutputBytes(worktreeArgs...)
		if err != nil {
			return fmt.Errorf("snapshot unstaged worktree changes: %w", err)
		}
		if err := writeAtomicFile(filepath.Join(dir, "worktree.patch"), patch, 0o600); err != nil {
			return fmt.Errorf("write worktree snapshot: %w", err)
		}
		operation.WorktreeArtifact = "worktree.patch"
		recordOperationArtifact(operation, operation.WorktreeArtifact, patch)
	}
	shared, err := git.Output("rev-parse", "--shared-index-path")
	if err != nil {
		return fmt.Errorf("inspect split index: %w", err)
	}
	if shared != "" {
		if !filepath.IsAbs(shared) {
			shared = filepath.Join(git.Dir, shared)
		}
		shared = filepath.Clean(shared)
		if filepath.Dir(shared) != filepath.Dir(index) || !strings.HasPrefix(filepath.Base(shared), "sharedindex.") {
			return fmt.Errorf("git returned unsafe shared-index path %q", shared)
		}
		sharedData, err := os.ReadFile(shared)
		if err != nil {
			return fmt.Errorf("snapshot shared index: %w", err)
		}
		sharedInfo, err := os.Stat(shared)
		if err != nil {
			return fmt.Errorf("inspect shared-index mode: %w", err)
		}
		operation.SharedIndexArtifact = "shared-index"
		operation.SharedIndexPath = filepath.Base(shared)
		operation.SharedIndexMode = uint32(sharedInfo.Mode().Perm())
		if err := writeAtomicFile(filepath.Join(dir, operation.SharedIndexArtifact), sharedData, 0o600); err != nil {
			return fmt.Errorf("write shared-index snapshot: %w", err)
		}
		recordOperationArtifact(operation, operation.SharedIndexArtifact, sharedData)
	}
	return nil
}

func (a *App) restoreOperationWorktreePatch(patch []byte) error {
	if len(patch) == 0 {
		return nil
	}
	git, err := a.recoveryGit()
	if err != nil {
		return err
	}
	if err := git.RunWithInput(string(patch), "apply", "--whitespace=nowarn"); err != nil {
		return fmt.Errorf("restore unstaged worktree changes: %w", err)
	}
	return nil
}

func (a *App) restoreOperationIndex(operation *OperationJournal, data, sharedData []byte) error {
	index, err := a.git.GitPath("index")
	if err != nil {
		return err
	}
	if operation.SharedIndexPath != "" {
		if len(sharedData) == 0 {
			return fmt.Errorf("shared-index recovery artifact is empty")
		}
		if err := replaceFileDurably(filepath.Join(filepath.Dir(index), operation.SharedIndexPath), sharedData, journalFileMode(operation.SharedIndexMode)); err != nil {
			return fmt.Errorf("restore shared index: %w", err)
		}
	}
	if err := replaceFileDurably(index, data, journalFileMode(operation.IndexMode)); err != nil {
		return fmt.Errorf("restore git index: %w", err)
	}
	return nil
}

func journalFileMode(mode uint32) os.FileMode {
	if mode == 0 {
		return 0o600
	}
	return os.FileMode(mode)
}

func replaceFileDurably(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".graphene-index-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func (a *App) removeOperationArtifacts(operation *OperationJournal) error {
	dir, err := a.operationArtifactDir(operation)
	if err != nil {
		return err
	}
	err = os.RemoveAll(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove operation artifacts: %w", err)
	}
	return nil
}
