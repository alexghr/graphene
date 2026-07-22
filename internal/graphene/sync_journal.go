package graphene

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	syncComponentPending     = "pending"
	syncComponentRunning     = "running"
	syncComponentRollingBack = "rolling-back"
	syncComponentSucceeded   = "succeeded"
	syncComponentFailed      = "failed"
)

type syncJournalProgress struct {
	Base                      string                 `json:"base"`
	OriginalBranch            string                 `json:"originalBranch"`
	ReturnBranch              string                 `json:"returnBranch,omitempty"`
	ReturnRef                 string                 `json:"returnRef,omitempty"`
	BaseOld                   string                 `json:"baseOld"`
	BaseNew                   string                 `json:"baseNew"`
	BaseUpdate                bool                   `json:"baseUpdate"`
	BaseDone                  bool                   `json:"baseDone"`
	Components                []syncJournalComponent `json:"components"`
	NextComponent             int                    `json:"nextComponent"`
	InitialSubmodules         []syncSubmodule        `json:"initialSubmodules,omitempty"`
	SubmoduleBackups          []syncSubmoduleBackup  `json:"submoduleBackups,omitempty"`
	BaselineHead              string                 `json:"baselineHead,omitempty"`
	BaselineTargetSubmodules  []syncSubmodule        `json:"baselineTargetSubmodules,omitempty"`
	BaselineSubmodulesPlanned bool                   `json:"baselineSubmodulesPlanned,omitempty"`
	BaselineSubmodule         []syncSubmodule        `json:"baselineSubmodules,omitempty"`
	SubmodulesPrepared        bool                   `json:"submodulesPrepared,omitempty"`
	ReturnHead                string                 `json:"returnHead,omitempty"`
	ReturnTargetSubmodules    []syncSubmodule        `json:"returnTargetSubmodules,omitempty"`
	ReturnSubmodulesPlanned   bool                   `json:"returnSubmodulesPlanned,omitempty"`
	ReturnSubmodulesPrepared  bool                   `json:"returnSubmodulesPrepared,omitempty"`
	BaseChanges               []BaseChange           `json:"baseChanges,omitempty"`
}

type syncJournalComponent struct {
	Names      []string   `json:"names"`
	Branches   []string   `json:"branches"`
	Deleted    []string   `json:"deleted,omitempty"`
	Ops        []RebaseOp `json:"ops,omitempty"`
	RefNames   []string   `json:"refNames,omitempty"`
	Foreground bool       `json:"foreground,omitempty"`
	Status     string     `json:"status"`
	NextOp     int        `json:"nextOp"`
	FailureTop string     `json:"failureTop,omitempty"`
}

func (c syncJournalComponent) runtime() syncComponent {
	branches := make(map[string]bool, len(c.Branches))
	for _, branch := range c.Branches {
		branches[branch] = true
	}
	return syncComponent{
		Names:      append([]string(nil), c.Names...),
		Branches:   branches,
		Deleted:    append([]string(nil), c.Deleted...),
		Ops:        append([]RebaseOp(nil), c.Ops...),
		Foreground: c.Foreground,
	}
}

func encodeSyncProgress(operation *OperationJournal, progress syncJournalProgress) error {
	data, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	operation.Progress = data
	return nil
}

func decodeSyncProgress(operation *OperationJournal) (syncJournalProgress, error) {
	var progress syncJournalProgress
	decoder := json.NewDecoder(bytes.NewReader(operation.Progress))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&progress); err != nil {
		return progress, fmt.Errorf("parse pending sync: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return progress, fmt.Errorf("parse pending sync: %w", err)
	}
	if progress.Base == "" || progress.OriginalBranch == "" {
		return progress, fmt.Errorf("pending sync is missing its base or original branch")
	}
	for name, branch := range map[string]string{"base": progress.Base, "original branch": progress.OriginalBranch} {
		if !validBranchArgument(branch) {
			return progress, fmt.Errorf("pending sync has invalid %s %q", name, branch)
		}
	}
	if progress.ReturnBranch != "" && !validBranchArgument(progress.ReturnBranch) {
		return progress, fmt.Errorf("pending sync has invalid return branch %q", progress.ReturnBranch)
	}
	if progress.BaseOld == "" || progress.BaseNew == "" {
		return progress, fmt.Errorf("pending sync is missing its base ref snapshot")
	}
	if !validObjectID(progress.BaseOld) || !validObjectID(progress.BaseNew) {
		return progress, fmt.Errorf("pending sync has invalid base object ids")
	}
	if progress.ReturnRef != "" && !validObjectID(progress.ReturnRef) {
		return progress, fmt.Errorf("pending sync has invalid return ref %q", progress.ReturnRef)
	}
	if progress.NextComponent < 0 || progress.NextComponent > len(progress.Components) {
		return progress, fmt.Errorf("pending sync has invalid component index %d", progress.NextComponent)
	}
	owned := map[string]int{}
	for _, submodules := range [][]syncSubmodule{
		progress.InitialSubmodules,
		progress.BaselineTargetSubmodules,
		progress.BaselineSubmodule,
		progress.ReturnTargetSubmodules,
	} {
		for _, submodule := range submodules {
			if !safeWorktreePath(submodule.Path) || !validObjectID(submodule.Head) {
				return progress, fmt.Errorf("pending sync has invalid submodule snapshot for %q", submodule.Path)
			}
			if submodule.Branch != "" && !validBranchArgument(submodule.Branch) {
				return progress, fmt.Errorf("pending sync has invalid submodule branch %q for %q", submodule.Branch, submodule.Path)
			}
		}
	}
	if err := validateSyncSubmoduleBackups(operation, progress.InitialSubmodules, progress.SubmoduleBackups); err != nil {
		return progress, fmt.Errorf("pending sync has invalid submodule backups: %w", err)
	}
	if progress.ReturnHead != "" && !validObjectID(progress.ReturnHead) {
		return progress, fmt.Errorf("pending sync has invalid return HEAD %q", progress.ReturnHead)
	}
	if progress.BaselineHead != "" && !validObjectID(progress.BaselineHead) {
		return progress, fmt.Errorf("pending sync has invalid baseline HEAD %q", progress.BaselineHead)
	}
	for i, component := range progress.Components {
		if component.Status == "" {
			progress.Components[i].Status = syncComponentPending
			component.Status = syncComponentPending
		}
		switch component.Status {
		case syncComponentPending, syncComponentRunning, syncComponentRollingBack, syncComponentSucceeded, syncComponentFailed:
		default:
			return progress, fmt.Errorf("pending sync component %d has invalid status %q", i, component.Status)
		}
		if component.NextOp < 0 || component.NextOp > len(component.Ops) {
			return progress, fmt.Errorf("pending sync component %d has invalid rebase index %d", i, component.NextOp)
		}
		for opIndex, op := range component.Ops {
			if !validJournalRebaseOp(op) {
				return progress, fmt.Errorf("pending sync component %d has invalid rebase %d", i, opIndex)
			}
		}
		for _, branch := range append(append([]string(nil), component.Branches...), component.Deleted...) {
			if !validBranchArgument(branch) {
				return progress, fmt.Errorf("pending sync component %d has invalid branch %q", i, branch)
			}
		}
		if i < progress.NextComponent && component.Status != syncComponentSucceeded && component.Status != syncComponentFailed {
			return progress, fmt.Errorf("pending sync component %d before the cursor is not complete", i)
		}
		if i > progress.NextComponent && component.Status != syncComponentPending {
			return progress, fmt.Errorf("pending sync component %d after the cursor has status %q", i, component.Status)
		}
		if component.Status == syncComponentSucceeded && component.NextOp != len(component.Ops) {
			return progress, fmt.Errorf("pending sync component %d succeeded before all rebases completed", i)
		}
		if !sort.StringsAreSorted(component.RefNames) {
			return progress, fmt.Errorf("pending sync component %d refs are not sorted", i)
		}
		for _, ref := range component.RefNames {
			if _, ok := operation.Refs[ref]; !ok {
				return progress, fmt.Errorf("pending sync component %d does not own %s", i, ref)
			}
			if owner, exists := owned[ref]; exists {
				return progress, fmt.Errorf("pending sync components %d and %d both own %s", owner, i, ref)
			}
			owned[ref] = i
		}
	}
	if operation.Active != nil && operation.Active.Kind == "rebase" {
		if progress.NextComponent >= len(progress.Components) {
			return progress, fmt.Errorf("pending sync has an active rebase after all components completed")
		}
		component := progress.Components[progress.NextComponent]
		if (component.Status != syncComponentRunning && component.Status != syncComponentRollingBack) || component.NextOp >= len(component.Ops) {
			return progress, fmt.Errorf("pending sync active rebase does not match the component cursor")
		}
	}
	return progress, nil
}

func (a *App) upgradeSyncSubmoduleBackups(state State) (syncJournalProgress, error) {
	operation := state.Operation
	progress, err := decodeSyncProgress(operation)
	if err != nil {
		return progress, err
	}
	if operation.Phase == operationCleanup || len(progress.InitialSubmodules) == 0 || len(progress.SubmoduleBackups) > 0 {
		return progress, nil
	}
	progress.SubmoduleBackups, err = planSyncSubmoduleBackups(operation, progress.InitialSubmodules)
	if err != nil {
		return progress, err
	}
	if err := encodeSyncProgress(operation, progress); err != nil {
		return progress, err
	}
	if err := a.git.WriteState(state); err != nil {
		return progress, err
	}
	return progress, nil
}

func (a *App) startSyncOperation(
	state State,
	current string,
	returnBranch string,
	returnRef string,
	base string,
	baseOld string,
	baseNew string,
	baseUpdate bool,
	components []syncComponent,
	originalRefs map[string]string,
	plannedRefs map[string]string,
	initialSubmodules []syncSubmodule,
) (resultErr error) {
	if err := a.requirePlannedBranchRefs(plannedRefs); err != nil {
		return fmt.Errorf("cannot start sync: %w", err)
	}
	worktree, err := a.git.WorktreeID()
	if err != nil {
		return err
	}
	head, err := a.git.Head()
	if err != nil {
		return err
	}
	operation, err := newOperationJournal("sync", worktree, current, head, state.Stacks)
	if err != nil {
		return err
	}
	operation.WorktreePolicy = worktreeRestoreHard
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
	currentRef := "refs/heads/" + current
	currentValue, err := a.git.RefValue(currentRef)
	if err != nil {
		return err
	}
	if !currentValue.Exists || currentValue.OID != head {
		return fmt.Errorf("cannot start sync because current branch %q moved away from HEAD", current)
	}
	operation.Refs[currentRef] = JournalRef{Original: currentValue, Expected: currentValue}
	if err := a.addOperationObservedBranches(operation, []string{returnBranch}, true); err != nil {
		return err
	}
	for branch, oid := range originalRefs {
		ref := "refs/heads/" + branch
		value := RefValue{Exists: true, OID: oid}
		if snapshot, exists := operation.Refs[ref]; exists && snapshot.Expected != value {
			return fmt.Errorf("cannot start sync because %q moved from %s to %s", branch, formatRefValue(snapshot.Expected), formatRefValue(value))
		}
		operation.Refs[ref] = JournalRef{Original: value, Expected: value}
	}
	baseRef := "refs/heads/" + base
	if _, exists := operation.Refs[baseRef]; !exists {
		value, err := a.git.RefValue(baseRef)
		if err != nil {
			return err
		}
		if !value.Exists {
			return fmt.Errorf("sync base %q no longer exists", base)
		}
		operation.Refs[baseRef] = JournalRef{Original: value, Expected: value}
	}

	progress := syncJournalProgress{
		Base:              base,
		OriginalBranch:    current,
		ReturnBranch:      returnBranch,
		ReturnRef:         returnRef,
		BaseOld:           baseOld,
		BaseNew:           baseNew,
		BaseUpdate:        baseUpdate,
		InitialSubmodules: append([]syncSubmodule(nil), initialSubmodules...),
	}
	progress.SubmoduleBackups, err = planSyncSubmoduleBackups(operation, initialSubmodules)
	if err != nil {
		return err
	}
	ownedByComponent := map[string]int{}
	for componentIndex, component := range components {
		componentRefs, err := a.syncMutationRefs(current, base, false, component.Deleted, component.Ops)
		if err != nil {
			return err
		}
		journalComponent := syncJournalComponent{
			Names:      append([]string(nil), component.Names...),
			Deleted:    append([]string(nil), component.Deleted...),
			Ops:        append([]RebaseOp(nil), component.Ops...),
			Foreground: component.Foreground,
			Status:     syncComponentPending,
		}
		for branch := range component.Branches {
			journalComponent.Branches = append(journalComponent.Branches, branch)
		}
		sort.Strings(journalComponent.Branches)
		for branch, oid := range componentRefs {
			ref := "refs/heads/" + branch
			if owner, exists := ownedByComponent[ref]; exists {
				return fmt.Errorf("sync components %d and %d both own %s", owner, componentIndex, ref)
			}
			ownedByComponent[ref] = componentIndex
			journalComponent.RefNames = append(journalComponent.RefNames, ref)
			if _, exists := operation.Refs[ref]; !exists {
				value := RefValue{Exists: true, OID: oid}
				operation.Refs[ref] = JournalRef{Original: value, Expected: value}
			}
		}
		sort.Strings(journalComponent.RefNames)
		progress.Components = append(progress.Components, journalComponent)
	}

	seenConfig := map[string]bool{}
	for _, component := range progress.Components {
		for _, branch := range component.Deleted {
			section := "branch." + branch
			if seenConfig[section] {
				continue
			}
			seenConfig[section] = true
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
	}
	if err := a.snapshotOperationValidationRefs(operation, operation.OriginalStacks); err != nil {
		return err
	}
	if err := a.requirePlannedBranchRefs(plannedRefs); err != nil {
		return fmt.Errorf("cannot start sync: %w", err)
	}
	if err := a.requireSyncSubmoduleSnapshot(initialSubmodules); err != nil {
		return fmt.Errorf("cannot start sync: %w", err)
	}
	if err := a.verifyUnpublishedOperationWorktree(operation); err != nil {
		return fmt.Errorf("cannot start sync: %w", err)
	}
	if err := encodeSyncProgress(operation, progress); err != nil {
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
	if err := a.installSyncSubmoduleBackups(progress, true); err != nil {
		return err
	}
	operation.Phase = operationApplying
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.continueSyncOperation(state, continueOptions{})
}

func (a *App) continueSyncOperation(state State, opts continueOptions) error {
	operation := state.Operation
	progress, err := a.upgradeSyncSubmoduleBackups(state)
	if err != nil {
		return err
	}
	if operation.Phase == operationPreparing {
		if err := a.git.InstallOperationBackups(operation); err != nil {
			return err
		}
		if err := a.installSyncSubmoduleBackups(progress, true); err != nil {
			return err
		}
		operation.Phase = operationApplying
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	if err := a.installSyncSubmoduleBackups(progress, false); err != nil {
		return err
	}
	if err := a.crossOperationWorktreeBoundary(state); err != nil {
		return err
	}
	if !progress.BaseDone {
		if err := a.continueSyncBaseAction(state, &progress); err != nil {
			return err
		}
	}
	if !progress.BaselineSubmodulesPlanned {
		current := operation.Refs["refs/heads/"+progress.OriginalBranch].Expected
		if !current.Exists {
			return fmt.Errorf("cannot prepare sync submodules because original branch %q is missing", progress.OriginalBranch)
		}
		progress.BaselineHead = current.OID
		progress.BaselineTargetSubmodules, err = a.planSyncSubmoduleTarget(progress.InitialSubmodules, progress.BaselineHead)
		if err != nil {
			return err
		}
		progress.BaselineSubmodulesPlanned = true
		if err := encodeSyncProgress(operation, progress); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	if !progress.SubmodulesPrepared {
		if len(progress.InitialSubmodules) > 0 {
			currentHead, err := a.git.Head()
			if err != nil {
				return err
			}
			if currentHead != progress.BaselineHead {
				return fmt.Errorf("cannot apply planned submodules because worktree HEAD moved from %s to %s", progress.BaselineHead, currentHead)
			}
		}
		if err := a.restoreSyncSubmodules(progress.BaselineTargetSubmodules, progress.InitialSubmodules); err != nil {
			return err
		}
		if err := a.verifySyncSubmoduleTarget(progress.BaselineTargetSubmodules); err != nil {
			return err
		}
		progress.BaselineSubmodule = append([]syncSubmodule(nil), progress.BaselineTargetSubmodules...)
		progress.SubmodulesPrepared = true
		if err := encodeSyncProgress(operation, progress); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}

componentLoop:
	for progress.NextComponent < len(progress.Components) {
		componentIndex := progress.NextComponent
		component := &progress.Components[progress.NextComponent]
		if component.Status == syncComponentSucceeded || component.Status == syncComponentFailed {
			progress.NextComponent++
			continue
		}
		if component.Status == syncComponentRollingBack {
			if err := a.rollbackSyncJournalComponent(state, &progress, component); err != nil {
				return err
			}
			continue
		}
		component.Status = syncComponentRunning
		if err := encodeSyncProgress(operation, progress); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}

		for component.NextOp < len(component.Ops) {
			op := component.Ops[component.NextOp]
			if operation.Active != nil {
				completed, err := a.recoverSyncRebaseAction(state, &progress, component, op, opts)
				if err != nil {
					return err
				}
				if !completed {
					return nil
				}
				refreshed, decodeErr := decodeSyncProgress(operation)
				if decodeErr != nil {
					return decodeErr
				}
				progress = refreshed
				if progress.NextComponent != componentIndex {
					continue componentLoop
				}
				component = &progress.Components[componentIndex]
				continue
			}
			if err := a.startSyncRebaseAction(state, progress, *component, op); err != nil {
				inProgress, progressErr := a.git.RebaseInProgress()
				if progressErr != nil {
					return progressErr
				}
				if !inProgress || component.Foreground {
					return err
				}
				component.Status = syncComponentRollingBack
				component.FailureTop = op.Top
				if encodeErr := encodeSyncProgress(operation, progress); encodeErr != nil {
					return encodeErr
				}
				if writeErr := a.git.WriteState(state); writeErr != nil {
					return writeErr
				}
				if rollbackErr := a.rollbackSyncJournalComponent(state, &progress, component); rollbackErr != nil {
					return fmt.Errorf("sync conflict in %q; automatic rollback failed: %v", component.runtime().Name(), rollbackErr)
				}
				break
			}
			refreshed, err := decodeSyncProgress(operation)
			if err != nil {
				return err
			}
			progress = refreshed
			component = &progress.Components[progress.NextComponent]
		}
		if component.Status == syncComponentRunning && component.NextOp == len(component.Ops) {
			component.Status = syncComponentSucceeded
			progress.NextComponent++
			if err := a.updateSyncDesiredState(operation, &progress); err != nil {
				return err
			}
			if err := a.git.WriteState(state); err != nil {
				return err
			}
		}
	}
	return a.finishSyncOperation(state, progress)
}

func (a *App) continueSyncBaseAction(state State, progress *syncJournalProgress) error {
	operation := state.Operation
	ref := "refs/heads/" + progress.Base
	expected := operation.Refs[ref].Expected
	actual, err := a.git.RefValue(ref)
	if err != nil {
		return err
	}
	target := expected
	if progress.BaseUpdate {
		target = RefValue{Exists: true, OID: progress.BaseNew}
	}
	if actual == target {
		progress.BaseDone = true
		operation.Active = nil
		snapshot := operation.Refs[ref]
		snapshot.Expected = target
		operation.Refs[ref] = snapshot
		if err := encodeSyncProgress(operation, *progress); err != nil {
			return err
		}
		return a.git.WriteState(state)
	}
	if actual != expected {
		return fmt.Errorf("cannot continue sync because base %q moved from %s to %s", progress.Base, formatRefValue(expected), formatRefValue(actual))
	}
	operation.Active = &JournalAction{
		ID:         "base-update",
		Kind:       "update-ref",
		RefsBefore: map[string]RefValue{ref: expected},
		RefsAfter:  map[string]RefValue{ref: target},
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	if progress.BaseUpdate {
		current, err := a.git.CurrentBranch()
		if err != nil {
			return err
		}
		if current == progress.Base {
			if err := a.requireNoIgnoredTreeCollision(progress.BaseNew, "sync base fast-forward"); err != nil {
				return err
			}
			if err := a.git.Run("-c", "submodule.recurse=false", "merge", "--ff-only", progress.BaseNew); err != nil {
				return err
			}
		} else if err := a.git.UpdateRefs([]refEdit{{Ref: ref, Old: expected, New: target}}); err != nil {
			return err
		}
	}
	return a.continueSyncBaseAction(state, progress)
}

func (a *App) startSyncRebaseAction(state State, progress syncJournalProgress, component syncJournalComponent, op RebaseOp) error {
	operation := state.Operation
	drift, _, err := a.git.OperationRefDrift(operation)
	if err != nil {
		return err
	}
	if len(drift) > 0 {
		return fmt.Errorf("cannot continue sync because operation-owned refs changed:\n%s", formatRefDrift(drift))
	}
	inventory, err := a.git.LocalHeadRefValues()
	if err != nil {
		return err
	}
	refsBefore, err := a.git.RefValues(component.RefNames)
	if err != nil {
		return err
	}
	operation.Active = &JournalAction{
		ID:           fmt.Sprintf("component-%d-rebase-%d", progress.NextComponent, component.NextOp),
		Kind:         "rebase",
		RefsBefore:   refsBefore,
		RefInventory: inventory,
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	marker := fmt.Sprintf("graphene:%s:%s", operation.ID, operation.Active.ID)
	if err := a.requireNoIgnoredRebaseCollision(op); err != nil {
		return err
	}
	if err := a.git.RunWithEnv(map[string]string{"GIT_REFLOG_ACTION": marker}, "-c", "submodule.recurse=false", "rebase", "--update-refs", "--onto", op.Onto, op.Upstream, op.Top); err != nil {
		return err
	}
	return a.finishSyncRebaseAction(state, component)
}

func (a *App) recoverSyncRebaseAction(state State, progress *syncJournalProgress, component *syncJournalComponent, op RebaseOp, opts continueOptions) (bool, error) {
	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return false, err
	}
	if inProgress {
		if !component.Foreground {
			component.Status = syncComponentRollingBack
			component.FailureTop = op.Top
			if err := encodeSyncProgress(state.Operation, *progress); err != nil {
				return false, err
			}
			if err := a.git.WriteState(state); err != nil {
				return false, err
			}
			return true, a.rollbackSyncJournalComponent(state, progress, component)
		}
		if err := a.continueCurrentRebase(); err != nil {
			return false, err
		}
		inProgress, err = a.git.RebaseInProgress()
		if err != nil || inProgress {
			return false, err
		}
		return true, a.finishSyncRebaseAction(state, *component)
	}

	actual, err := a.git.RefValues(component.RefNames)
	if err != nil {
		return false, err
	}
	if len(refDrift(state.Operation.Active.RefsBefore, actual)) == 0 {
		state.Operation.Active = nil
		if err := a.git.WriteState(state); err != nil {
			return false, err
		}
		return true, nil
	}
	if !opts.acceptCurrent {
		return false, fmt.Errorf("git may have completed %s; inspect the changed refs, then run graphene continue --accept-current or graphene abort --force", state.Operation.Active.ID)
	}
	ancestor, err := a.isAncestor(op.Onto, op.Top)
	if err != nil {
		return false, err
	}
	if !ancestor {
		return false, fmt.Errorf("cannot accept current refs because %s is not descended from %s", op.Top, shortSyncRef(op.Onto))
	}
	return true, a.finishSyncRebaseAction(state, *component)
}

func (a *App) finishSyncRebaseAction(state State, component syncJournalComponent) error {
	operation := state.Operation
	currentHeads, err := a.git.LocalHeadRefValues()
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, ref := range component.RefNames {
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
	actual, err := a.git.RefValues(component.RefNames)
	if err != nil {
		return err
	}
	for ref, value := range actual {
		snapshot := operation.Refs[ref]
		snapshot.Expected = value
		operation.Refs[ref] = snapshot
	}
	progress, err := decodeSyncProgress(operation)
	if err != nil {
		return err
	}
	journalComponent := &progress.Components[progress.NextComponent]
	journalComponent.NextOp++
	operation.Active = nil
	if err := encodeSyncProgress(operation, progress); err != nil {
		return err
	}
	return a.git.WriteState(state)
}

func (a *App) rollbackSyncJournalComponent(state State, progress *syncJournalProgress, component *syncJournalComponent) error {
	operation := state.Operation
	if component.Status != syncComponentRollingBack {
		return fmt.Errorf("cannot roll back sync component in status %q", component.Status)
	}
	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if inProgress {
		if err := a.git.RunOperationRebase("--abort"); err != nil {
			return err
		}
	}
	if err := a.git.RunOperationSwitch(progress.OriginalBranch); err != nil {
		return err
	}
	actual, err := a.git.RefValues(component.RefNames)
	if err != nil {
		return err
	}
	var edits []refEdit
	for _, ref := range component.RefNames {
		snapshot := operation.Refs[ref]
		want := snapshot.Original
		got := actual[ref]
		allowed := got == snapshot.Expected || got == snapshot.Original
		if operation.Active != nil {
			if before, ok := operation.Active.RefsBefore[ref]; ok && got == before {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("cannot roll back sync component because %s changed from %s to %s", ref, formatRefValue(snapshot.Expected), formatRefValue(got))
		}
		if got != want {
			edits = append(edits, refEdit{Ref: ref, Old: got, New: want})
		}
	}
	if err := a.git.UpdateRefs(edits); err != nil {
		return err
	}
	for _, ref := range component.RefNames {
		snapshot := operation.Refs[ref]
		snapshot.Expected = snapshot.Original
		operation.Refs[ref] = snapshot
	}
	if err := a.restoreSyncSubmodules(progress.BaselineSubmodule, progress.InitialSubmodules, progress.BaselineTargetSubmodules); err != nil {
		return err
	}
	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("worktree is not clean after automatic sync rollback")
	}
	component.Status = syncComponentFailed
	component.NextOp = 0
	progress.NextComponent++
	operation.Active = nil
	if err := a.updateSyncDesiredState(operation, progress); err != nil {
		return err
	}
	return a.git.WriteState(state)
}

func (a *App) updateSyncDesiredState(operation *OperationJournal, progress *syncJournalProgress) error {
	var deleted []string
	for _, component := range progress.Components {
		if component.Status == syncComponentSucceeded {
			deleted = append(deleted, component.Deleted...)
		}
	}
	original := State{Stacks: cloneStacks(operation.OriginalStacks)}
	next := RemoveBranchesWithBase(original, deleted, progress.Base)
	operation.DesiredStacks = cloneStacks(next.Stacks)
	progress.BaseChanges = branchBaseChanges(original, next)
	return encodeSyncProgress(operation, *progress)
}

func (a *App) finishSyncOperation(state State, progress syncJournalProgress) error {
	operation := state.Operation
	if err := a.updateSyncDesiredState(operation, &progress); err != nil {
		return err
	}
	if progress.ReturnBranch != "" {
		if progress.ReturnRef != "" {
			if err := a.switchToBaseOrDetach(progress.ReturnBranch, progress.ReturnRef); err != nil {
				return err
			}
		} else if err := a.git.RunOperationSwitch(progress.ReturnBranch); err != nil {
			return err
		}
	}

	var deleted []string
	for _, component := range progress.Components {
		if component.Status == syncComponentSucceeded {
			deleted = append(deleted, component.Deleted...)
		}
	}
	if err := a.removeOperationBranchConfigs(state, deleted); err != nil {
		return err
	}
	if err := a.finishOperationRefDeletion(state, deleted); err != nil {
		return err
	}
	if !progress.ReturnSubmodulesPlanned {
		returnHead, err := a.git.Head()
		if err != nil {
			return err
		}
		progress.ReturnHead = returnHead
		returnTarget, err := a.planSyncSubmoduleTarget(progress.InitialSubmodules, progress.ReturnHead, progress.BaselineSubmodule)
		if err != nil {
			return fmt.Errorf("sync completed, but planning submodules for the return branch failed: %w", err)
		}
		progress.ReturnTargetSubmodules = returnTarget
		progress.ReturnSubmodulesPlanned = true
		if err := encodeSyncProgress(operation, progress); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	if !progress.ReturnSubmodulesPrepared {
		currentHead, err := a.git.Head()
		if err != nil {
			return err
		}
		if currentHead != progress.ReturnHead {
			return fmt.Errorf("cannot align return submodules because worktree HEAD moved from %s to %s", progress.ReturnHead, currentHead)
		}
		if err := a.restoreSyncSubmodules(progress.ReturnTargetSubmodules, progress.InitialSubmodules, progress.BaselineTargetSubmodules, progress.BaselineSubmodule); err != nil {
			return fmt.Errorf("sync completed, but aligning submodules for the return branch failed: %w", err)
		}
		if err := a.verifySyncSubmoduleTarget(progress.ReturnTargetSubmodules); err != nil {
			return err
		}
		progress.ReturnSubmodulesPrepared = true
		if err := encodeSyncProgress(operation, progress); err != nil {
			return err
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	if err := a.verifySyncSubmoduleTarget(progress.ReturnTargetSubmodules); err != nil {
		return fmt.Errorf("cannot finish sync because return-branch submodules changed: %w", err)
	}
	baseChanges := append([]BaseChange(nil), progress.BaseChanges...)
	var succeeded []syncComponent
	var failures []syncComponentFailure
	for _, component := range progress.Components {
		switch component.Status {
		case syncComponentSucceeded:
			if len(component.Ops) > 0 || len(component.Deleted) > 0 {
				succeeded = append(succeeded, component.runtime())
			}
		case syncComponentFailed:
			failures = append(failures, syncComponentFailure{Component: component.runtime(), Top: component.FailureTop})
		}
	}
	if err := a.commitOperation(state); err != nil {
		return err
	}
	a.printSyncBaseChanges(baseChanges)
	if len(failures) == 0 {
		return nil
	}
	a.printSyncComponentSummary(succeeded, failures, progress.OriginalBranch, progress.Base)
	return fmt.Errorf("sync completed with %d stack component(s) restored unchanged", len(failures))
}

func (a *App) removeOperationBranchConfigs(state State, deleted []string) error {
	operation := state.Operation
	wanted := map[string]bool{}
	for _, branch := range deleted {
		wanted["branch."+branch] = true
	}
	for i := range operation.Configs {
		config := &operation.Configs[i]
		if !wanted[config.Section] {
			continue
		}
		branch, ok := strings.CutPrefix(config.Section, "branch.")
		if !ok || branch == "" {
			return fmt.Errorf("invalid branch config journal section %q", config.Section)
		}
		actual, err := a.git.BranchConfig(branch)
		if err != nil {
			return err
		}
		if len(config.Expected) == 0 && len(actual) == 0 {
			continue
		}
		actionID := "delete-config:" + config.Section
		if operation.Active == nil {
			if !equalConfigEntries(config.Expected, actual) {
				return fmt.Errorf("branch config for %q changed during %s", branch, operation.Kind)
			}
			operation.Active = &JournalAction{ID: actionID, Kind: "delete-config"}
			if err := a.git.WriteState(state); err != nil {
				return err
			}
		} else if operation.Active.ID != actionID || operation.Active.Kind != "delete-config" {
			return fmt.Errorf("cannot delete branch config while journal action %q is active", operation.Active.ID)
		}
		if len(actual) > 0 && !equalConfigEntries(config.Expected, actual) {
			return fmt.Errorf("branch config for %q changed during %s", branch, operation.Kind)
		}
		if len(actual) > 0 {
			if err := a.git.RemoveBranchConfig(branch); err != nil {
				return err
			}
		}
		actual, err = a.git.BranchConfig(branch)
		if err != nil {
			return err
		}
		if len(actual) != 0 {
			return fmt.Errorf("branch config for %q still exists after removal", branch)
		}
		config.Expected = nil
		operation.Active = nil
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) finishOperationRefDeletion(state State, deleted []string) error {
	operation := state.Operation
	before := map[string]RefValue{}
	after := map[string]RefValue{}
	refs := make([]string, 0, len(deleted))
	for _, branch := range deleted {
		ref := "refs/heads/" + branch
		snapshot, ok := operation.Refs[ref]
		if !ok {
			return fmt.Errorf("%s journal does not own deleted branch %q", operation.Kind, branch)
		}
		refs = append(refs, ref)
		before[ref] = snapshot.Expected
		after[ref] = RefValue{}
	}
	if len(refs) == 0 {
		return nil
	}
	sort.Strings(refs)
	const actionID = "delete-merged-refs"
	if operation.Active == nil {
		actual, err := a.git.RefValues(refs)
		if err != nil {
			return err
		}
		if drift := refDrift(before, actual); len(drift) > 0 {
			return fmt.Errorf("cannot delete merged branches because operation-owned refs changed:\n%s", formatRefDrift(drift))
		}
		operation.Active = &JournalAction{ID: actionID, Kind: "delete-refs", RefsBefore: before, RefsAfter: after}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	} else if operation.Active.ID != actionID || operation.Active.Kind != "delete-refs" {
		return fmt.Errorf("cannot delete branches during %s while journal action %q is active", operation.Kind, operation.Active.ID)
	}

	actual, err := a.git.RefValues(refs)
	if err != nil {
		return err
	}
	if len(refDrift(after, actual)) == 0 {
		for _, ref := range refs {
			snapshot := operation.Refs[ref]
			snapshot.Expected = RefValue{}
			operation.Refs[ref] = snapshot
		}
		operation.Active = nil
		return a.git.WriteState(state)
	}
	if drift := refDrift(before, actual); len(drift) > 0 {
		return fmt.Errorf("cannot finish deleting merged branches because refs are neither before nor after the journaled action:\n%s", formatRefDrift(drift))
	}
	for _, branch := range deleted {
		checkedOut, err := a.git.BranchCheckedOut(branch)
		if err != nil {
			return err
		}
		if checkedOut {
			return fmt.Errorf("branch %q is checked out in a worktree; switch that worktree away before continuing sync", branch)
		}
	}
	edits := make([]refEdit, 0, len(refs))
	for _, ref := range refs {
		edits = append(edits, refEdit{Ref: ref, Old: before[ref]})
	}
	if err := a.git.UpdateRefs(edits); err != nil {
		return err
	}
	return a.finishOperationRefDeletion(state, deleted)
}
