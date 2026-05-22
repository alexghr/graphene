package graphene

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *App) newBranch(args []string) error {
	opts, err := parseNewArgs(args)
	if err != nil {
		return err
	}

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		if state.Pending.Operation == "split" && len(state.Pending.Queue) == 0 {
			return a.newDuringSplit(opts, current, state)
		}
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}

	if opts.reuseCurrent {
		if opts.branch != "" {
			return fmt.Errorf("graphene new --reuse-current cannot use --branch")
		}
		if opts.base == "" {
			return fmt.Errorf("graphene new --reuse-current requires --base")
		}
		if opts.base == current {
			return fmt.Errorf("graphene new --reuse-current cannot use current branch %q as base", current)
		}
		if StateContainsName(state, current) {
			return fmt.Errorf("current branch %q is already recorded in graphene state", current)
		}
	}

	recordBase := current
	if opts.base != "" {
		if err := a.validateNewBase(opts.base); err != nil {
			return err
		}
		recordBase = opts.base
	}

	branch := opts.branch
	temp := false
	var cfg Config
	if opts.reuseCurrent {
		branch = current
	} else if branch == "" {
		cfg, err = LoadConfig(a.getenv)
		if err != nil {
			return err
		}
		if err := a.ensureBranchPrefixAvailable(cfg.BranchPrefix); err != nil {
			return err
		}
		branch = tempBranchName(os.Getpid(), time.Now().UnixNano())
		temp = true
	}

	if !opts.reuseCurrent {
		if err := a.git.Run("switch", "-c", branch); err != nil {
			return err
		}
	}

	commitGitArgs := append([]string{"commit"}, opts.commitArgs...)
	if err := a.git.Run(commitGitArgs...); err != nil {
		if !opts.reuseCurrent {
			a.cleanupFailedCommit(current, branch)
		}
		return err
	}

	if temp {
		subject, err := a.git.Output("log", "-1", "--format=%s")
		if err != nil {
			return err
		}
		branch, err = a.derivedBranchName(cfg, subject)
		if err != nil {
			return err
		}
		if err := a.git.Run("branch", "-m", branch); err != nil {
			return err
		}
	}

	if err := state.AddCommit(recordBase, branch); err != nil {
		return err
	}
	return a.git.WriteState(state)
}

func (a *App) split(args []string) error {
	target, err := parseSplitArgs(args)
	if err != nil {
		return err
	}

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	if target == "" {
		target = current
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}

	base, ok := BaseBranch(state, target)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", target)
	}
	count, err := a.commitCount(base, target)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("branch %q contains %d commits on top of %q; Graphene can only split a one-commit branch", target, count, base)
	}

	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("tracked changes would be mixed into split; stash or commit them before graphene split")
	}

	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if inProgress {
		return fmt.Errorf("rebase in progress; use graphene continue or graphene abort before graphene split")
	}

	originalHead, err := a.git.Output("rev-parse", "--verify", target+"^{commit}")
	if err != nil {
		return err
	}
	originalRefs := a.trackedBranchRefs(state)

	originalStacks := cloneStacks(state.Stacks)
	nextState, ok := TruncateStackAfterBranch(State{Stacks: cloneStacks(state.Stacks)}, target)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", target)
	}
	nextState.Pending = &Pending{
		Operation:      "split",
		Branch:         target,
		ReturnBranch:   current,
		Top:            originalHead,
		Branches:       []string{target},
		OriginalHead:   originalHead,
		OriginalBase:   base,
		OriginalRefs:   originalRefs,
		OriginalStacks: originalStacks,
	}

	if target != current {
		if err := a.git.Run("switch", target); err != nil {
			return err
		}
	}
	if err := a.git.WriteState(nextState); err != nil {
		return err
	}
	if err := a.git.Run("reset", "-N", base); err != nil {
		nextState.Pending = nil
		nextState.Stacks = state.Stacks
		_ = a.git.WriteState(nextState)
		return err
	}
	return nil
}

func (a *App) newDuringSplit(opts commitOptions, current string, state State) error {
	pending := state.Pending
	if pending == nil || pending.Operation != "split" {
		return fmt.Errorf("no split in progress")
	}
	if opts.base != "" {
		return fmt.Errorf("graphene new --base cannot be used during graphene split")
	}

	top := splitTop(pending)
	if current != top {
		return fmt.Errorf("split in progress for %q; switch to %q before graphene new", pending.Branch, top)
	}

	targetCount, err := a.commitCount(pending.OriginalBase, pending.Branch)
	if err != nil {
		return err
	}
	if opts.reuseCurrent {
		if opts.branch != "" {
			return fmt.Errorf("graphene new --reuse-current cannot use --branch")
		}
		if current != pending.Branch {
			return fmt.Errorf("only the first split commit can use --reuse-current")
		}
		if targetCount != 0 {
			return fmt.Errorf("the first split commit already exists; use graphene new without --reuse-current for the next split part")
		}

		commitGitArgs := append([]string{"commit"}, opts.commitArgs...)
		if err := a.git.Run(commitGitArgs...); err != nil {
			return err
		}
		return a.finishSplitIfClean(state)
	}
	if targetCount == 0 {
		return fmt.Errorf("the first split commit must use graphene new --reuse-current")
	}

	recordBase := current
	branch := opts.branch
	temp := false
	var cfg Config
	if branch == "" {
		cfg, err = LoadConfig(a.getenv)
		if err != nil {
			return err
		}
		if err := a.ensureBranchPrefixAvailable(cfg.BranchPrefix); err != nil {
			return err
		}
		branch = tempBranchName(os.Getpid(), time.Now().UnixNano())
		temp = true
	}

	if err := a.git.Run("switch", "-c", branch); err != nil {
		return err
	}

	commitGitArgs := append([]string{"commit"}, opts.commitArgs...)
	if err := a.git.Run(commitGitArgs...); err != nil {
		a.cleanupFailedCommit(current, branch)
		return err
	}

	if temp {
		subject, err := a.git.Output("log", "-1", "--format=%s")
		if err != nil {
			return err
		}
		branch, err = a.derivedBranchName(cfg, subject)
		if err != nil {
			return err
		}
		if err := a.git.Run("branch", "-m", branch); err != nil {
			return err
		}
	}

	if err := state.AddCommit(recordBase, branch); err != nil {
		return err
	}
	state.Pending.Branches = append(state.Pending.Branches, branch)
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.finishSplitIfClean(state)
}

func (a *App) finishSplitIfClean(state State) error {
	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return nil
	}
	return a.finishSplit(state)
}

func (a *App) finishSplit(state State) error {
	pending := state.Pending
	if pending == nil || pending.Operation != "split" {
		return fmt.Errorf("no split in progress")
	}

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	top := splitTop(pending)
	if current != top {
		return fmt.Errorf("split in progress for %q; switch to %q before finishing split", pending.Branch, top)
	}

	nextState, directOps, rewritten, skipTops, err := splitFinalState(state)
	if err != nil {
		return err
	}
	restackOps, err := RestackOpsAfterRewrites(nextState, rewritten, pending.OriginalRefs, skipTops)
	if err != nil {
		return err
	}
	ops := append(directOps, restackOps...)
	if err := a.validateRebaseOpsUpdateable("split", current, nextState, pending.OriginalRefs, ops); err != nil {
		return err
	}

	if len(ops) == 0 {
		nextState.Pending = nil
		return a.git.WriteState(nextState)
	}

	state.Pending.ReturnBranch = current
	state.Pending.Queue = ops
	state.Pending.NextStacks = nextState.Stacks
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runPendingRebases(state)
}

func splitFinalState(state State) (State, []RebaseOp, []string, map[string]bool, error) {
	pending := state.Pending
	if pending == nil {
		return State{}, nil, nil, nil, fmt.Errorf("no split in progress")
	}
	target := pending.Branch
	top := splitTop(pending)

	original := State{Stacks: cloneStacks(pending.OriginalStacks)}
	loc, ok := original.BranchLocation(target)
	if !ok {
		return State{}, nil, nil, nil, fmt.Errorf("branch %q is not in original split state", target)
	}
	originalStack := original.Stacks[loc.StackIndex]
	suffix := append([]string(nil), originalStack.Branches[loc.BranchIndex+1:]...)

	nextState := State{Stacks: cloneStacks(state.Stacks)}
	if len(suffix) > 0 {
		topLoc, ok := nextState.BranchLocation(top)
		if !ok {
			return State{}, nil, nil, nil, fmt.Errorf("split top branch %q is not in graphene state", top)
		}
		stack := nextState.Stacks[topLoc.StackIndex]
		branches := make([]string, 0, len(stack.Branches)+len(suffix))
		branches = append(branches, stack.Branches[:topLoc.BranchIndex+1]...)
		branches = append(branches, suffix...)
		branches = append(branches, stack.Branches[topLoc.BranchIndex+1:]...)
		nextState.Stacks[topLoc.StackIndex] = Stack{Base: stack.Base, Branches: branches}
	}

	for i, stack := range nextState.Stacks {
		if stack.Base == target {
			nextState.Stacks[i].Base = top
		}
	}

	upstream := pending.OriginalHead
	var ops []RebaseOp
	var rewritten []string
	skipTops := map[string]bool{}
	if len(suffix) > 0 {
		opTop := suffix[len(suffix)-1]
		ops = append(ops, RebaseOp{
			Onto:     top,
			Upstream: upstream,
			Top:      opTop,
		})
		rewritten = append(rewritten, suffix...)
		skipTops[opTop] = true
	}

	for _, stack := range original.Stacks {
		if stack.Base != target || len(stack.Branches) == 0 {
			continue
		}
		opTop := stack.Branches[len(stack.Branches)-1]
		ops = append(ops, RebaseOp{
			Onto:     top,
			Upstream: upstream,
			Top:      opTop,
		})
		rewritten = append(rewritten, stack.Branches...)
		skipTops[opTop] = true
	}

	return nextState, ops, rewritten, skipTops, nil
}

func splitTop(pending *Pending) string {
	if pending == nil {
		return ""
	}
	if len(pending.Branches) > 0 {
		return pending.Branches[len(pending.Branches)-1]
	}
	return pending.Branch
}

type squashSelection struct {
	StackIndex int
	Start      int
	End        int
	Base       string
	Bottom     string
	Top        string
	Branches   []string
	Removed    []string
	Suffix     []string
}

func (a *App) squash(args []string) error {
	opts, err := parseSquashArgs(args)
	if err != nil {
		return err
	}

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}

	selection, err := squashRange(state, current, opts.count)
	if err != nil {
		return err
	}
	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("tracked changes would prevent squash; stash or commit them before graphene squash")
	}
	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if inProgress {
		return fmt.Errorf("rebase in progress; use graphene continue or graphene abort before graphene squash")
	}
	if err := a.validateSquashShape(selection); err != nil {
		return err
	}
	if err := a.validateSquashBranchesAvailable(current, selection); err != nil {
		return err
	}

	oldRefs := a.trackedBranchRefs(state)
	topRef := oldRefs[selection.Top]
	if topRef == "" {
		return fmt.Errorf("missing old ref for %q", selection.Top)
	}
	baseRef, err := a.git.Output("rev-parse", "--verify", selection.Base+"^{commit}")
	if err != nil {
		return err
	}

	nextState, ops, err := squashFinalState(state, selection, oldRefs)
	if err != nil {
		return err
	}
	if err := a.validateRebaseOpsUpdateable("squash", current, nextState, oldRefs, ops); err != nil {
		return err
	}

	commitArgs, cleanup, err := a.squashCommitArgs(opts, selection.Branches)
	if err != nil {
		return err
	}
	defer cleanup()

	if current != selection.Bottom {
		if err := a.git.Run("switch", selection.Bottom); err != nil {
			return err
		}
	}
	restore := func(cause error) error {
		if restoreErr := a.restoreOriginalRewrite(State{Stacks: cloneStacks(state.Stacks)}, oldRefs, selection.Bottom, current); restoreErr != nil {
			return fmt.Errorf("%w; additionally failed to restore original squash state: %v", cause, restoreErr)
		}
		return cause
	}
	if err := a.git.Run("reset", "--hard", topRef); err != nil {
		return restore(err)
	}
	if err := a.git.Run("reset", "--soft", baseRef); err != nil {
		return restore(err)
	}
	if err := a.git.Run(commitArgs...); err != nil {
		return restore(err)
	}

	if len(ops) == 0 {
		if err := a.deleteBranches(selection.Removed); err != nil {
			return restore(err)
		}
		if err := a.git.WriteState(nextState); err != nil {
			return restore(err)
		}
		return nil
	}

	state.Pending = &Pending{
		Operation:      "squash",
		Branch:         selection.Bottom,
		ReturnBranch:   selection.Bottom,
		Queue:          ops,
		Top:            selection.Top,
		Branches:       append([]string(nil), selection.Removed...),
		NextStacks:     nextState.Stacks,
		OriginalRefs:   oldRefs,
		OriginalStacks: cloneStacks(state.Stacks),
	}
	if err := a.git.WriteState(state); err != nil {
		return restore(err)
	}
	return a.runPendingRebases(state)
}

func squashRange(state State, current string, count int) (squashSelection, error) {
	if count < 2 {
		return squashSelection{}, fmt.Errorf("squash count must be at least 2")
	}
	loc, ok := state.BranchLocation(current)
	if !ok {
		return squashSelection{}, fmt.Errorf("branch %q is not in a graphene stack", current)
	}
	stack := state.Stacks[loc.StackIndex]
	if loc.BranchIndex+1 < count {
		return squashSelection{}, fmt.Errorf("cannot squash %d branches ending at %q; only %d tracked branches are available in this stack path", count, current, loc.BranchIndex+1)
	}

	start := loc.BranchIndex - count + 1
	base := stack.Base
	if start > 0 {
		base = stack.Branches[start-1]
	}
	branches := append([]string(nil), stack.Branches[start:loc.BranchIndex+1]...)
	return squashSelection{
		StackIndex: loc.StackIndex,
		Start:      start,
		End:        loc.BranchIndex,
		Base:       base,
		Bottom:     branches[0],
		Top:        current,
		Branches:   branches,
		Removed:    append([]string(nil), branches[1:]...),
		Suffix:     append([]string(nil), stack.Branches[loc.BranchIndex+1:]...),
	}, nil
}

func (a *App) validateSquashShape(selection squashSelection) error {
	parent := selection.Base
	for _, branch := range selection.Branches {
		count, err := a.commitCount(parent, branch)
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("branch %q contains %d commits on top of %q; Graphene can only squash one-commit stack branches", branch, count, parent)
		}
		parent = branch
	}
	return nil
}

func (a *App) validateSquashBranchesAvailable(current string, selection squashSelection) error {
	if selection.Bottom != current {
		checkedOut, err := a.git.BranchCheckedOut(selection.Bottom)
		if err != nil {
			return err
		}
		if checkedOut {
			return fmt.Errorf("branch %q is checked out in another worktree; switch that worktree away from the branch before graphene squash", selection.Bottom)
		}
	}
	for _, branch := range selection.Removed {
		if branch == current {
			continue
		}
		checkedOut, err := a.git.BranchCheckedOut(branch)
		if err != nil {
			return err
		}
		if checkedOut {
			return fmt.Errorf("branch %q is checked out in another worktree; switch that worktree away from the branch before graphene squash", branch)
		}
	}
	return nil
}

func squashFinalState(state State, selection squashSelection, oldRefs map[string]string) (State, []RebaseOp, error) {
	nextState := RemoveBranchesWithBase(State{Stacks: cloneStacks(state.Stacks)}, selection.Removed, selection.Bottom)
	deleted := map[string]bool{}
	for _, branch := range selection.Removed {
		deleted[branch] = true
	}

	upstream := oldRefs[selection.Top]
	if upstream == "" {
		return State{}, nil, fmt.Errorf("missing old ref for %q", selection.Top)
	}

	var ops []RebaseOp
	rewritten := []string{selection.Bottom}
	skipTops := map[string]bool{}
	if len(selection.Suffix) > 0 {
		top := selection.Suffix[len(selection.Suffix)-1]
		ops = append(ops, RebaseOp{
			Onto:     selection.Bottom,
			Upstream: upstream,
			Top:      top,
		})
		rewritten = append(rewritten, selection.Suffix...)
		skipTops[top] = true
	}

	for i, dependent := range state.Stacks {
		if i == selection.StackIndex || !deleted[dependent.Base] || len(dependent.Branches) == 0 {
			continue
		}
		upstream := oldRefs[dependent.Base]
		if upstream == "" {
			return State{}, nil, fmt.Errorf("missing old ref for %q", dependent.Base)
		}
		top := dependent.Branches[len(dependent.Branches)-1]
		ops = append(ops, RebaseOp{
			Onto:     selection.Bottom,
			Upstream: upstream,
			Top:      top,
		})
		rewritten = append(rewritten, dependent.Branches...)
		skipTops[top] = true
	}

	restackOps, err := RestackOpsAfterRewrites(nextState, rewritten, oldRefs, skipTops)
	if err != nil {
		return State{}, nil, err
	}
	ops = append(ops, restackOps...)
	return nextState, ops, nil
}

func (a *App) squashCommitArgs(opts squashOptions, branches []string) ([]string, func(), error) {
	args := []string{"commit"}
	if opts.messageSet {
		return append(args, opts.commitArgs...), func() {}, nil
	}

	message, err := a.defaultSquashMessage(branches)
	if err != nil {
		return nil, nil, err
	}
	path, err := a.git.GitPath("graphene/SQUASH_MSG")
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(path, []byte(message), 0o644); err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		_ = os.Remove(path)
	}
	args = append(args, opts.commitArgs...)
	args = append(args, "--edit", "-F", path)
	return args, cleanup, nil
}

func (a *App) defaultSquashMessage(branches []string) (string, error) {
	if len(branches) == 0 {
		return "", fmt.Errorf("no branches to squash")
	}
	message, err := a.git.Output("log", "-1", "--format=%B", branches[0])
	if err != nil {
		return "", err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Squashed changes"
	}
	if len(branches) == 1 {
		return message + "\n", nil
	}

	var b strings.Builder
	b.WriteString(message)
	b.WriteString("\n\nSquashed commits:\n")
	for _, branch := range branches[1:] {
		subject, err := a.git.Output("log", "-1", "--format=%s", branch)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n- %s", subject)
	}
	b.WriteByte('\n')
	return b.String(), nil
}

func (a *App) restoreOriginalRewrite(original State, refs map[string]string, resetBranch, returnBranch string) error {
	if resetBranch != "" {
		if err := a.git.Run("switch", "--force", resetBranch); err != nil {
			return err
		}
		if ref := refs[resetBranch]; ref != "" {
			if err := a.git.Run("reset", "--hard", ref); err != nil {
				return err
			}
		}
	}

	for branch, ref := range refs {
		if branch == "" || branch == resetBranch || ref == "" {
			continue
		}
		if err := a.git.OutputErr("update-ref", "refs/heads/"+branch, ref); err != nil {
			return err
		}
	}

	restored := State{Stacks: cloneStacks(original.Stacks)}
	if err := a.git.WriteState(restored); err != nil {
		return err
	}
	if returnBranch != "" && returnBranch != resetBranch {
		if err := a.git.Run("switch", returnBranch); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) amend(args []string) error {
	commitArgs, err := parseAmendArgs(args)
	if err != nil {
		return err
	}

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}

	oldRefs := a.stateRefs(state)
	oldHead, err := a.git.Head()
	if err != nil {
		return err
	}
	oldRefs[current] = oldHead

	ops, err := RestackOpsAfterRewrite(state, current, oldRefs)
	if err != nil {
		return err
	}
	if len(ops) > 0 {
		dirty, err := a.git.HasUnstagedChanges()
		if err != nil {
			return err
		}
		if dirty {
			return fmt.Errorf("unstaged changes would prevent restacking; stash or commit them before graphene amend")
		}
		if err := a.validateRebaseOpsUpdateable("amend", current, state, oldRefs, ops); err != nil {
			return err
		}
	}

	commitGitArgs := append([]string{"commit", "--amend"}, commitArgs...)
	if err := a.git.Run(commitGitArgs...); err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}

	state.Pending = &Pending{
		Operation:    "amend",
		Branch:       current,
		ReturnBranch: current,
		Queue:        ops,
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runPendingRebases(state)
}

func (a *App) restack(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return fmt.Errorf("usage: graphene restack <base>")
	}
	base := args[0]

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}

	oldBase, ok := BaseBranch(state, current)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
	}
	nextState, _, ok := ReparentBranch(state, current, base)
	if !ok {
		return fmt.Errorf("cannot restack %q onto %q", current, base)
	}

	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("tracked changes would prevent restack; stash or commit them before graphene restack")
	}

	oldRefs := a.stateRefs(state)
	oldHead, err := a.git.Head()
	if err != nil {
		return err
	}
	oldRefs[current] = oldHead
	oldBaseRef := oldRefs[oldBase]
	if oldBaseRef == "" {
		oldBaseRef, err = a.git.Output("rev-parse", "--verify", oldBase+"^{commit}")
		if err != nil {
			return err
		}
	}
	baseRef, err := a.git.Output("rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return err
	}

	if oldBaseRef == baseRef {
		return a.git.WriteState(nextState)
	}

	ops := []RebaseOp{{
		Onto:     base,
		Upstream: oldBaseRef,
		Top:      current,
	}}
	restackOps, err := RestackOpsAfterRewrite(nextState, current, oldRefs)
	if err != nil {
		return err
	}
	ops = append(ops, restackOps...)
	if err := a.validateRebaseOpsUpdateable("restack", current, nextState, oldRefs, ops); err != nil {
		return err
	}

	state.Pending = &Pending{
		Operation:    "restack",
		Branch:       current,
		ReturnBranch: current,
		Queue:        ops,
		NextStacks:   nextState.Stacks,
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runPendingRebases(state)
}

func (a *App) continueRebase(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("graphene continue does not accept arguments")
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending == nil || len(state.Pending.Queue) == 0 {
		if state.Pending != nil && state.Pending.Operation == "split" {
			return fmt.Errorf("split in progress; use graphene new to commit split parts or graphene abort")
		}
		inProgress, err := a.git.RebaseInProgress()
		if err != nil {
			return err
		}
		if !inProgress {
			return fmt.Errorf("no rebase in progress")
		}
		if err := a.continueCurrentRebase(); err != nil {
			return err
		}
		return a.clearPendingIfRebaseDone()
	}

	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if inProgress {
		if err := a.continueCurrentRebase(); err != nil {
			return err
		}
		inProgress, err = a.git.RebaseInProgress()
		if err != nil {
			return err
		}
		if inProgress {
			return nil
		}
		state.Pending.Queue = state.Pending.Queue[1:]
		if len(state.Pending.Queue) == 0 {
			return a.finishPendingRebases(state)
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	return a.runPendingRebases(state)
}

func (a *App) continueCurrentRebase() error {
	snapshot, err := a.snapshotRebaseMerge()
	if err != nil {
		return err
	}
	err = a.git.Run("rebase", "--continue")
	if err == nil {
		return nil
	}
	if snapshot == nil {
		return err
	}
	if restoreErr := a.restoreRebaseMergeAfterFailedContinue(snapshot); restoreErr != nil {
		return fmt.Errorf("git rebase --continue failed, and Graphene could not restore rebase state: %w", restoreErr)
	}
	return err
}

type rebaseFileSnapshot struct {
	mode os.FileMode
	data []byte
}

type rebaseMergeSnapshot struct {
	dir       string
	indexPath string
	index     rebaseFileSnapshot
	head      string
	files     map[string]rebaseFileSnapshot
}

func (a *App) snapshotRebaseMerge() (*rebaseMergeSnapshot, error) {
	rebaseDir, err := a.git.GitPath("rebase-merge")
	if err != nil {
		return nil, err
	}
	if !existsDir(rebaseDir) {
		return nil, nil
	}
	indexPath, err := a.git.GitPath("index")
	if err != nil {
		return nil, err
	}
	index, err := snapshotFile(indexPath)
	if err != nil {
		return nil, err
	}
	head, err := a.git.Head()
	if err != nil {
		return nil, err
	}
	snapshot := &rebaseMergeSnapshot{
		dir:       rebaseDir,
		indexPath: indexPath,
		index:     index,
		head:      head,
		files:     map[string]rebaseFileSnapshot{},
	}
	err = filepath.WalkDir(rebaseDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot snapshot non-regular rebase file %s", path)
		}
		rel, err := filepath.Rel(rebaseDir, path)
		if err != nil {
			return err
		}
		file, err := snapshotFile(path)
		if err != nil {
			return err
		}
		snapshot.files[rel] = file
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func snapshotFile(path string) (rebaseFileSnapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		return rebaseFileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return rebaseFileSnapshot{}, fmt.Errorf("cannot snapshot non-regular file %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return rebaseFileSnapshot{}, err
	}
	return rebaseFileSnapshot{mode: info.Mode().Perm(), data: data}, nil
}

func (a *App) restoreRebaseMergeAfterFailedContinue(snapshot *rebaseMergeSnapshot) error {
	head, err := a.git.Head()
	if err != nil {
		return err
	}
	if head != snapshot.head {
		return nil
	}
	if !existsDir(snapshot.dir) {
		return nil
	}
	return restoreRebaseMerge(snapshot)
}

func restoreRebaseMerge(snapshot *rebaseMergeSnapshot) error {
	entries, err := os.ReadDir(snapshot.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(snapshot.dir, entry.Name())
		if entry.IsDir() {
			return fmt.Errorf("cannot restore rebase state with nested directory %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	for name, file := range snapshot.files {
		path := filepath.Join(snapshot.dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, file.data, file.mode); err != nil {
			return err
		}
	}
	if err := os.WriteFile(snapshot.indexPath, snapshot.index.data, snapshot.index.mode); err != nil {
		return err
	}
	return nil
}

func (a *App) abortRebase(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("graphene abort does not accept arguments")
	}
	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if state.Pending != nil && state.Pending.Operation == "split" {
		return a.abortSplit(state, inProgress)
	}
	if state.Pending != nil && state.Pending.Operation == "squash" {
		return a.abortSquash(state, inProgress)
	}
	if inProgress {
		if err := a.git.Run("rebase", "--abort"); err != nil {
			return err
		}
	} else if state.Pending == nil {
		return fmt.Errorf("no rebase in progress")
	}
	state.Pending = nil
	return a.git.WriteState(state)
}

func (a *App) abortSplit(state State, rebaseInProgress bool) error {
	pending := state.Pending
	if pending == nil || pending.Operation != "split" {
		return fmt.Errorf("no split in progress")
	}
	if rebaseInProgress {
		if err := a.git.Run("rebase", "--abort"); err != nil {
			return err
		}
	}

	if pending.Branch != "" {
		if err := a.git.Run("switch", "--force", pending.Branch); err != nil {
			return err
		}
	}
	if pending.OriginalHead != "" {
		if err := a.git.Run("reset", "--hard", pending.OriginalHead); err != nil {
			return err
		}
	}

	for branch, ref := range pending.OriginalRefs {
		if branch == "" || branch == pending.Branch || ref == "" {
			continue
		}
		if err := a.git.OutputErr("update-ref", "refs/heads/"+branch, ref); err != nil {
			return err
		}
	}
	for _, branch := range pending.Branches {
		if branch == "" || branch == pending.Branch {
			continue
		}
		exists, err := a.git.BranchExists(branch)
		if err != nil {
			return err
		}
		if exists {
			if err := a.git.Run("branch", "-D", branch); err != nil {
				return err
			}
		}
	}

	state.Stacks = cloneStacks(pending.OriginalStacks)
	state.Pending = nil
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	if pending.ReturnBranch != "" && pending.ReturnBranch != pending.Branch {
		if err := a.git.Run("switch", pending.ReturnBranch); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) abortSquash(state State, rebaseInProgress bool) error {
	pending := state.Pending
	if pending == nil || pending.Operation != "squash" {
		return fmt.Errorf("no squash in progress")
	}
	if rebaseInProgress {
		if err := a.git.Run("rebase", "--abort"); err != nil {
			return err
		}
	}
	returnBranch := pending.Top
	if returnBranch == "" {
		returnBranch = pending.ReturnBranch
	}
	return a.restoreOriginalRewrite(State{Stacks: cloneStacks(pending.OriginalStacks)}, pending.OriginalRefs, pending.Branch, returnBranch)
}

func (a *App) forget(args []string) error {
	force, err := parseForgetArgs(args)
	if err != nil {
		return err
	}

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil && !force {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}

	state, ok := RemoveStackThroughCurrent(state, current)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
	}
	if force {
		state.Pending = nil
	}
	return a.git.WriteState(state)
}

func (a *App) sync(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("graphene sync does not accept arguments")
	}

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}

	loc, ok := state.BranchLocation(current)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
	}
	stack, ok := state.StackAt(loc.StackIndex)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
	}

	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("tracked changes would prevent sync; stash or commit them before graphene sync")
	}
	if err := a.validateStackShape(stack); err != nil {
		return err
	}

	oldRefs := a.stateRefs(state)
	baseRef, err := a.fetchBase(stack.Base)
	if err != nil {
		return err
	}

	branches, err := a.appliedPrefixBranches(baseRef, stack.Branches[:loc.BranchIndex+1], oldRefs)
	if err != nil {
		return err
	}

	firstRemaining := len(branches)
	nextState := RemoveBranchesWithBase(state, branches, stack.Base)
	baseChanges := branchBaseChanges(state, nextState)
	deleted := map[string]bool{}
	for _, branch := range branches {
		deleted[branch] = true
	}

	returnBranch := current
	if firstRemaining > loc.BranchIndex {
		returnBranch = ""
		if firstRemaining < len(stack.Branches) {
			returnBranch = stack.Branches[firstRemaining]
		}
	}

	var ops []RebaseOp
	var rewritten []string
	skipTops := map[string]bool{}
	if firstRemaining < len(stack.Branches) {
		predecessor := stack.Base
		if firstRemaining > 0 {
			predecessor = stack.Branches[firstRemaining-1]
		}
		upstream := oldRefs[predecessor]
		if upstream == "" {
			return fmt.Errorf("missing old ref for %q", predecessor)
		}

		topIndex := loc.BranchIndex
		if firstRemaining > loc.BranchIndex {
			topIndex = len(stack.Branches) - 1
		}
		top := stack.Branches[topIndex]
		ops = append(ops, RebaseOp{
			Onto:     baseRef,
			Upstream: upstream,
			Top:      top,
		})
		rewritten = append(rewritten, stack.Branches[firstRemaining:topIndex+1]...)
		if topIndex == len(stack.Branches)-1 {
			skipTops[top] = true
		}
	}

	for i, dependent := range state.Stacks {
		if i == loc.StackIndex || !deleted[dependent.Base] || len(dependent.Branches) == 0 {
			continue
		}
		if err := a.validateStackShape(dependent); err != nil {
			return err
		}
		upstream := oldRefs[dependent.Base]
		if upstream == "" {
			return fmt.Errorf("missing old ref for %q", dependent.Base)
		}
		top := dependent.Branches[len(dependent.Branches)-1]
		ops = append(ops, RebaseOp{
			Onto:     baseRef,
			Upstream: upstream,
			Top:      top,
		})
		rewritten = append(rewritten, dependent.Branches...)
		skipTops[top] = true
		if returnBranch == "" {
			returnBranch = dependent.Branches[0]
		}
	}

	restackOps, err := RestackOpsAfterRewrites(nextState, rewritten, oldRefs, skipTops)
	if err != nil {
		return err
	}
	ops = append(ops, restackOps...)
	if err := a.validateRebaseOpsUpdateable("sync", current, nextState, oldRefs, ops); err != nil {
		return err
	}

	if len(ops) == 0 {
		if returnBranch != "" {
			if err := a.git.Run("switch", returnBranch); err != nil {
				return err
			}
		} else if err := a.switchToBaseOrDetach(stack.Base, baseRef); err != nil {
			return err
		}
		if err := a.deleteBranches(branches); err != nil {
			return err
		}
		if err := a.git.WriteState(nextState); err != nil {
			return err
		}
		a.printSyncBaseChanges(baseChanges)
		return nil
	}

	state.Pending = &Pending{
		Operation:    "sync",
		Branch:       returnBranch,
		ReturnBranch: returnBranch,
		Queue:        ops,
		Branches:     branches,
		NextStacks:   nextState.Stacks,
		BaseChanges:  baseChanges,
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runPendingRebases(state)
}

func (a *App) send(args []string) error {
	return a.sendBranches(args, false)
}

func (a *App) sendf(args []string) error {
	return a.sendBranches(args, true)
}

func (a *App) sendBranches(args []string, forceWithLease bool) error {
	opts, err := parseSendArgs(args)
	if err != nil {
		return err
	}

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}
	branches := BranchesThroughCurrent(state, current)
	if opts.stack {
		branches = BranchesThroughCurrentAndDescendants(state, current)
	}
	if len(branches) == 0 {
		return fmt.Errorf("no branch to send")
	}
	remote := opts.remote
	if remote == "" {
		remote, err = a.pushRemote(current, branches)
		if err != nil {
			return err
		}
	}

	pushArgs := []string{"push"}
	if forceWithLease {
		pushArgs = append(pushArgs, "--force-with-lease")
	}
	if opts.dryRun {
		pushArgs = append(pushArgs, "--dry-run")
	}
	pushArgs = append(pushArgs, remote)
	pushArgs = append(pushArgs, branches...)
	if err := a.git.Run(pushArgs...); err != nil {
		return err
	}
	if opts.dryRun {
		return nil
	}

	for _, branch := range branches {
		hasUpstream, err := a.git.HasUpstream(branch)
		if err != nil {
			return err
		}
		if !hasUpstream {
			if err := a.git.SetUpstream(branch, remote); err != nil {
				return err
			}
		}
	}
	return a.printPullRequestURLs(remote, state, branches)
}

func (a *App) cleanupFailedCommit(original, branch string) {
	if original != "" {
		_ = a.git.OutputErr("switch", original)
	}
	if branch != "" {
		_ = a.git.OutputErr("branch", "-D", branch)
	}
}

func (a *App) derivedBranchName(cfg Config, subject string) (string, error) {
	state, err := a.git.ReadState()
	if err != nil {
		return "", err
	}
	base := BranchName(cfg.BranchPrefix, SlugSubject(subject))
	for n := 1; ; n++ {
		candidate := CandidateName(base, n)
		status, err := a.git.BranchCreateStatus(candidate)
		if err != nil {
			return "", err
		}
		if status == "available" && !StateContainsName(state, candidate) {
			return candidate, nil
		}
	}
}

func (a *App) ensureBranchPrefixAvailable(prefix string) error {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return nil
	}
	if _, err := a.git.Output("check-ref-format", "--branch", prefix+"/graphene-check"); err != nil {
		return fmt.Errorf("invalid branch prefix %q", prefix)
	}
	branches, err := a.git.LocalBranches()
	if err != nil {
		return err
	}
	for _, existing := range branches {
		if existing == prefix || strings.HasPrefix(prefix, existing+"/") {
			return fmt.Errorf("branch prefix %q conflicts with existing branch %q", prefix, existing)
		}
	}
	return nil
}

func (a *App) clearPendingIfRebaseDone() error {
	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if inProgress {
		return nil
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		return a.finishPendingRebases(state)
	}
	state.Pending = nil
	return a.git.WriteState(state)
}

func (a *App) runPendingRebases(state State) error {
	for state.Pending != nil && len(state.Pending.Queue) > 0 {
		op := state.Pending.Queue[0]
		if err := a.git.Run("rebase", "--update-refs", "--onto", op.Onto, op.Upstream, op.Top); err != nil {
			return err
		}
		state.Pending.Queue = state.Pending.Queue[1:]
		if len(state.Pending.Queue) == 0 {
			return a.finishPendingRebases(state)
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) finishPendingRebases(state State) error {
	returnBranch := ""
	var appliedBranches []string
	var baseChanges []BaseChange
	var nextStacks []Stack
	if state.Pending != nil {
		returnBranch = state.Pending.ReturnBranch
		nextStacks = state.Pending.NextStacks
		if state.Pending.Operation == "sync" || state.Pending.Operation == "squash" {
			appliedBranches = append([]string(nil), state.Pending.Branches...)
		}
		if state.Pending.Operation == "sync" {
			baseChanges = append([]BaseChange(nil), state.Pending.BaseChanges...)
		}
	}
	if returnBranch != "" {
		if err := a.git.Run("switch", returnBranch); err != nil {
			return err
		}
	}
	if len(appliedBranches) > 0 {
		if err := a.deleteBranches(appliedBranches); err != nil {
			return err
		}
		if nextStacks == nil {
			state = RemoveBranches(state, appliedBranches)
		}
	}
	if nextStacks != nil {
		state.Stacks = nextStacks
	}
	state.Pending = nil
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	a.printSyncBaseChanges(baseChanges)
	return nil
}

func (a *App) printSyncBaseChanges(changes []BaseChange) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintln(a.stdout, "Retarget existing PRs after sync:")
	for _, change := range changes {
		fmt.Fprintf(a.stdout, "  %s: %s -> %s\n", change.Branch, change.OldBase, change.NewBase)
	}
}

func (a *App) fetchBase(base string) (string, error) {
	remote, merge, err := a.git.Upstream(base)
	if err != nil {
		return "", err
	}
	if remote == "" || merge == "" {
		return "", fmt.Errorf("branch %q has no upstream; set one before updating the stack", base)
	}

	oldBase, err := a.git.Output("rev-parse", "--verify", "refs/heads/"+base+"^{commit}")
	if err != nil {
		return "", err
	}
	if err := a.git.Run("fetch", remote); err != nil {
		return "", err
	}

	upstream := base + "@{upstream}"
	updatedBase, err := a.git.Output("rev-parse", "--verify", upstream+"^{commit}")
	if err != nil {
		return "", err
	}
	ancestor, err := a.isAncestor(oldBase, updatedBase)
	if err != nil {
		return "", err
	}
	if !ancestor {
		return "", fmt.Errorf("cannot fast-forward %q to %q; resolve the base branch before updating the stack", base, upstream)
	}
	if oldBase == updatedBase {
		return base, nil
	}

	checkedOut, err := a.git.BranchCheckedOut(base)
	if err != nil {
		return "", err
	}
	if checkedOut {
		return updatedBase, nil
	}
	if err := a.git.OutputErr("update-ref", "refs/heads/"+base, updatedBase, oldBase); err != nil {
		return "", err
	}
	return base, nil
}

func (a *App) switchToBaseOrDetach(base, baseRef string) error {
	if err := a.git.OutputErr("switch", base); err == nil {
		return nil
	}
	return a.git.Run("switch", "--detach", baseRef)
}

func (a *App) deleteBranches(branches []string) error {
	for _, branch := range branches {
		exists, err := a.git.BranchExists(branch)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := a.git.Run("branch", "-D", branch); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) validateStackShape(stack Stack) error {
	parent := stack.Base
	for _, branch := range stack.Branches {
		count, err := a.commitCount(parent, branch)
		if err != nil {
			return err
		}
		if count > 1 {
			return fmt.Errorf("branch %q contains %d commits on top of %q; Graphene expects one commit per stack branch. squash or drop the extra commits before graphene sync", branch, count, parent)
		}
		parent = branch
	}
	return nil
}

func (a *App) validateRebaseOpsUpdateable(operation, current string, state State, oldRefs map[string]string, ops []RebaseOp) error {
	if len(ops) == 0 {
		return nil
	}

	checkedOut := map[string]bool{}
	for _, op := range ops {
		topRef := oldRefs[op.Top]
		if topRef == "" {
			var err error
			topRef, err = a.git.Output("rev-parse", "--verify", op.Top+"^{commit}")
			if err != nil {
				return err
			}
		}

		for _, branch := range StateRefNames(state) {
			if branch == current {
				continue
			}
			ref := oldRefs[branch]
			if ref == "" || ref == op.Upstream {
				continue
			}

			inRange, err := a.isAncestor(op.Upstream, ref)
			if err != nil {
				return err
			}
			if !inRange {
				continue
			}
			inRange, err = a.isAncestor(ref, topRef)
			if err != nil {
				return err
			}
			if !inRange {
				continue
			}

			isCheckedOut, ok := checkedOut[branch]
			if !ok {
				isCheckedOut, err = a.git.BranchCheckedOut(branch)
				if err != nil {
					return err
				}
				checkedOut[branch] = isCheckedOut
			}
			if isCheckedOut {
				return fmt.Errorf("branch %q is checked out in another worktree; switch that worktree away from the branch before graphene %s", branch, operation)
			}
		}
	}
	return nil
}

func (a *App) commitCount(base, branch string) (int, error) {
	out, err := a.git.Output("rev-list", "--count", base+".."+branch)
	if err != nil {
		return 0, err
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &count); err != nil {
		return 0, fmt.Errorf("parse commit count for %s..%s: %w", base, branch, err)
	}
	return count, nil
}

func (a *App) appliedPrefixBranches(base string, branches []string, oldRefs map[string]string) ([]string, error) {
	if len(branches) == 0 {
		return nil, nil
	}

	applied, err := a.appliedCommitRefs(base, branches[len(branches)-1])
	if err != nil {
		return nil, err
	}

	var prefix []string
	for _, branch := range branches {
		ref := oldRefs[branch]
		if ref == "" {
			break
		}
		if applied[ref] {
			prefix = append(prefix, branch)
			continue
		}
		ancestor, err := a.isAncestor(ref, base)
		if err != nil {
			return nil, err
		}
		if !ancestor {
			break
		}
		prefix = append(prefix, branch)
	}
	return prefix, nil
}

func (a *App) appliedCommitRefs(base, top string) (map[string]bool, error) {
	out, err := a.git.Output("cherry", base, top)
	if err != nil {
		return nil, err
	}

	applied := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "-" {
			continue
		}
		applied[fields[1]] = true
	}
	return applied, nil
}

func (a *App) isAncestor(ancestor, descendant string) (bool, error) {
	_, err := a.git.Output("merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if isGitExit(err, 1) {
		return false, nil
	}
	return false, err
}

func (a *App) stateRefs(state State) map[string]string {
	refs := map[string]string{}
	for _, name := range StateRefNames(state) {
		if ref, err := a.git.Output("rev-parse", "--verify", name); err == nil {
			refs[name] = ref
		}
	}
	return refs
}

func (a *App) trackedBranchRefs(state State) map[string]string {
	seen := map[string]bool{}
	refs := map[string]string{}
	for _, stack := range state.Stacks {
		for _, branch := range stack.Branches {
			if branch == "" || seen[branch] {
				continue
			}
			seen[branch] = true
			if ref, err := a.git.Output("rev-parse", "--verify", branch+"^{commit}"); err == nil {
				refs[branch] = ref
			}
		}
	}
	return refs
}

func (a *App) validateNewBase(base string) error {
	exists, err := a.git.BranchExists(base)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("base branch %q does not exist", base)
	}

	head, err := a.git.Head()
	if err != nil {
		return err
	}
	baseRef, err := a.git.Output("rev-parse", "--verify", "refs/heads/"+base+"^{commit}")
	if err != nil {
		return err
	}
	if baseRef != head {
		return fmt.Errorf("base branch %q does not point to current HEAD", base)
	}
	return nil
}

type commitOptions struct {
	branch       string
	base         string
	reuseCurrent bool
	commitArgs   []string
}

type squashOptions struct {
	count      int
	commitArgs []string
	messageSet bool
}

func parseNewArgs(args []string) (commitOptions, error) {
	return parseCommitOptions(args, true)
}

func parseAmendArgs(args []string) ([]string, error) {
	opts, err := parseCommitOptions(args, false)
	return opts.commitArgs, err
}

func parseSplitArgs(args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("usage: graphene split [branch]")
	}
	if len(args) == 0 {
		return "", nil
	}
	if strings.HasPrefix(args[0], "-") {
		return "", fmt.Errorf("unsupported argument %q; usage: graphene split [branch]", args[0])
	}
	return args[0], nil
}

func parseSquashArgs(args []string) (squashOptions, error) {
	opts := squashOptions{count: 2}
	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "-c" || arg == "--count":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("missing count after %s", arg)
			}
			count, err := parseSquashCount(args[i+1])
			if err != nil {
				return opts, err
			}
			opts.count = count
			i++
		case strings.HasPrefix(arg, "--count="):
			count, err := parseSquashCount(strings.TrimPrefix(arg, "--count="))
			if err != nil {
				return opts, err
			}
			opts.count = count
		case shortSquashCount(arg):
			count, err := parseSquashCount(strings.TrimPrefix(arg, "-c"))
			if err != nil {
				return opts, err
			}
			opts.count = count
		case arg == "-m" || arg == "--message":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("missing message after %s", arg)
			}
			opts.commitArgs = append(opts.commitArgs, "-m", args[i+1])
			opts.messageSet = true
			i++
		case strings.HasPrefix(arg, "--message="):
			opts.commitArgs = append(opts.commitArgs, arg)
			opts.messageSet = true
		case arg == "--no-verify":
			opts.commitArgs = append(opts.commitArgs, arg)
		case arg == "--gpg-sign" || strings.HasPrefix(arg, "--gpg-sign=") || arg == "--no-gpg-sign":
			opts.commitArgs = append(opts.commitArgs, arg)
		default:
			return opts, fmt.Errorf("unsupported argument %q; supported squash options are -c/--count, -m/--message, --no-verify, --gpg-sign, and --no-gpg-sign", arg)
		}
	}
	return opts, nil
}

func shortSquashCount(arg string) bool {
	if !strings.HasPrefix(arg, "-c") || len(arg) == len("-c") {
		return false
	}
	for _, r := range strings.TrimPrefix(arg, "-c") {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseSquashCount(raw string) (int, error) {
	count, err := strconv.Atoi(raw)
	if err != nil || count < 2 {
		return 0, fmt.Errorf("invalid squash count %q; use 2, 3, ...", raw)
	}
	return count, nil
}

func parseCommitOptions(args []string, allowBranch bool) (commitOptions, error) {
	var opts commitOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "-b" || arg == "--branch":
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support %s", arg)
			}
			if opts.branch != "" {
				return opts, fmt.Errorf("new branch specified more than once")
			}
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("missing branch after %s", arg)
			}
			opts.branch = args[i+1]
			i++
		case strings.HasPrefix(arg, "--branch="):
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support --branch")
			}
			if opts.branch != "" {
				return opts, fmt.Errorf("new branch specified more than once")
			}
			opts.branch = strings.TrimPrefix(arg, "--branch=")
			if opts.branch == "" {
				return opts, fmt.Errorf("missing branch after --branch")
			}
		case arg == "--base":
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support --base")
			}
			if opts.base != "" {
				return opts, fmt.Errorf("base branch specified more than once")
			}
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("missing base after --base")
			}
			opts.base = args[i+1]
			i++
		case strings.HasPrefix(arg, "--base="):
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support --base")
			}
			if opts.base != "" {
				return opts, fmt.Errorf("base branch specified more than once")
			}
			opts.base = strings.TrimPrefix(arg, "--base=")
			if opts.base == "" {
				return opts, fmt.Errorf("missing base after --base")
			}
		case arg == "--reuse-current":
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support --reuse-current")
			}
			if opts.reuseCurrent {
				return opts, fmt.Errorf("reuse-current specified more than once")
			}
			opts.reuseCurrent = true
		case arg == "-m" || arg == "--message":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("missing message after %s", arg)
			}
			opts.commitArgs = append(opts.commitArgs, "-m", args[i+1])
			i++
		case strings.HasPrefix(arg, "--message="):
			opts.commitArgs = append(opts.commitArgs, arg)
		case arg == "--no-verify":
			opts.commitArgs = append(opts.commitArgs, arg)
		case arg == "--gpg-sign" || strings.HasPrefix(arg, "--gpg-sign=") || arg == "--no-gpg-sign":
			opts.commitArgs = append(opts.commitArgs, arg)
		case arg == "--amend":
			if allowBranch {
				return opts, fmt.Errorf("graphene new cannot use --amend; use graphene amend")
			}
			return opts, unsupportedCommitArg(arg, allowBranch)
		default:
			return opts, unsupportedCommitArg(arg, allowBranch)
		}
	}
	return opts, nil
}

func unsupportedCommitArg(arg string, allowBranch bool) error {
	supported := "-m/--message, --no-verify, --gpg-sign, and --no-gpg-sign"
	if allowBranch {
		supported = "-b/--branch, --base, --reuse-current, " + supported
	}
	return fmt.Errorf("unsupported argument %q; supported commit options are %s", arg, supported)
}

func parseForgetArgs(args []string) (bool, error) {
	var force bool
	for _, arg := range args {
		switch arg {
		case "-f", "--force":
			force = true
		default:
			return false, fmt.Errorf("graphene forget does not support %s", arg)
		}
	}
	return force, nil
}

type sendOptions struct {
	remote string
	stack  bool
	dryRun bool
}

func parseSendArgs(args []string) (sendOptions, error) {
	var opts sendOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "-s" || arg == "--stack":
			opts.stack = true
		case arg == "-n" || arg == "--dry-run":
			opts.dryRun = true
		case arg == "--remote":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("missing remote after --remote")
			}
			if opts.remote != "" {
				return opts, fmt.Errorf("graphene send accepts at most one remote")
			}
			opts.remote = args[i+1]
			i++
		case strings.HasPrefix(arg, "--remote="):
			if opts.remote != "" {
				return opts, fmt.Errorf("graphene send accepts at most one remote")
			}
			opts.remote = strings.TrimPrefix(arg, "--remote=")
			if opts.remote == "" {
				return opts, fmt.Errorf("missing remote after --remote")
			}
		case strings.HasPrefix(arg, "-"):
			return opts, fmt.Errorf("unsupported argument %q; supported send options are --remote, -s/--stack, and -n/--dry-run", arg)
		default:
			if opts.remote != "" {
				return opts, fmt.Errorf("graphene send accepts at most one remote")
			}
			opts.remote = arg
		}
	}
	return opts, nil
}

func (a *App) pushRemote(current string, branches []string) (string, error) {
	currentRemote, err := a.git.UpstreamRemote(current)
	if err != nil {
		return "", err
	}

	remote := currentRemote
	for _, branch := range branches {
		branchRemote, err := a.git.UpstreamRemote(branch)
		if err != nil {
			return "", err
		}
		if branchRemote == "" {
			continue
		}
		if remote == "" {
			remote = branchRemote
			continue
		}
		if branchRemote != remote {
			return "", fmt.Errorf("stack branches have mixed upstream remotes; pass a remote explicitly")
		}
	}
	if remote == "" {
		remote = "origin"
	}
	return remote, nil
}

func (a *App) printPullRequestURLs(remote string, state State, branches []string) error {
	template, err := a.git.PRURLTemplate()
	if err != nil {
		return err
	}

	var remoteURL string
	if template == "" {
		remoteURL, err = a.git.RemoteURL(remote)
		if err != nil {
			_, printErr := fmt.Fprintf(a.stdout, "No pull request URLs: remote %q has no push URL.\n", remote)
			return printErr
		}
	}

	urls := PullRequestURLs(template, remoteURL, state, branches)
	if len(urls) == 0 {
		_, err := fmt.Fprintf(a.stdout, "No pull request URLs for remote %q; set graphene.prUrlTemplate for non-GitHub remotes.\n", remote)
		return err
	}

	if _, err := fmt.Fprintln(a.stdout, "Pull request URLs:"); err != nil {
		return err
	}
	for _, pull := range urls {
		if _, err := fmt.Fprintf(a.stdout, "  %s into %s: %s\n", pull.Branch, pull.Base, pull.URL); err != nil {
			return err
		}
	}
	return nil
}
