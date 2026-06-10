package graphene

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	graphenestackedprs "github.com/alexghr/graphene/skills/graphene-stacked-prs"
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
			base, err := a.inferReuseCurrentBase(current)
			if err != nil {
				return err
			}
			opts.base = base
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

	if err := a.stageRequestedChanges(opts); err != nil {
		return err
	}

	branch := opts.branch
	temp := false
	var cfg Config
	if opts.reuseCurrent {
		branch = current
	} else if branch == "" {
		cfg, err = a.loadConfig()
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

func (a *App) inferReuseCurrentBase(current string) (string, error) {
	branches, err := a.git.LocalBranchesPointingAt("HEAD")
	if err != nil {
		return "", err
	}

	var candidates []string
	for _, branch := range branches {
		if branch != current {
			candidates = append(candidates, branch)
		}
	}
	if len(candidates) != 1 {
		return "", fmt.Errorf("graphene new --reuse-current requires --base")
	}
	return candidates[0], nil
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
	pending, err := a.pendingForCurrentWorktree(Pending{
		Operation:      "split",
		Branch:         target,
		ReturnBranch:   current,
		Top:            originalHead,
		Branches:       []string{target},
		OriginalHead:   originalHead,
		OriginalBase:   base,
		OriginalRefs:   originalRefs,
		OriginalStacks: originalStacks,
	})
	if err != nil {
		return err
	}
	nextState.Pending = pending

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
		if err := a.stageRequestedChanges(opts); err != nil {
			return err
		}
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
		cfg, err = a.loadConfig()
		if err != nil {
			return err
		}
		if err := a.ensureBranchPrefixAvailable(cfg.BranchPrefix); err != nil {
			return err
		}
		branch = tempBranchName(os.Getpid(), time.Now().UnixNano())
		temp = true
	}

	if err := a.stageRequestedChanges(opts); err != nil {
		return err
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
	Base          string
	Bottom        string
	Top           string
	Branches      []string
	Removed       []string
	Suffixes      []squashSuffix
	HandledStacks map[int]bool
}

type squashSuffix struct {
	Upstream string
	Branches []string
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

	pending, err := a.pendingForCurrentWorktree(Pending{
		Operation:      "squash",
		Branch:         selection.Bottom,
		ReturnBranch:   selection.Bottom,
		Queue:          ops,
		Top:            selection.Top,
		Branches:       append([]string(nil), selection.Removed...),
		NextStacks:     nextState.Stacks,
		OriginalRefs:   oldRefs,
		OriginalStacks: cloneStacks(state.Stacks),
	})
	if err != nil {
		return restore(err)
	}
	state.Pending = pending
	if err := a.git.WriteState(state); err != nil {
		return restore(err)
	}
	return a.runPendingRebases(state)
}

func squashRange(state State, current string, count int) (squashSelection, error) {
	if count < 2 {
		return squashSelection{}, fmt.Errorf("squash count must be at least 2")
	}
	_, ok := state.BranchLocation(current)
	if !ok {
		return squashSelection{}, fmt.Errorf("branch %q is not in a graphene stack", current)
	}
	path := BranchesThroughCurrent(state, current)
	if len(path) < count {
		return squashSelection{}, fmt.Errorf("cannot squash %d branches ending at %q; only %d tracked branches are available in this stack path", count, current, len(path))
	}

	start := len(path) - count
	base := ""
	if start > 0 {
		base = path[start-1]
	} else if visibleBase, ok := BaseBranch(state, path[0]); ok {
		base = visibleBase
	}
	branches := append([]string(nil), path[start:]...)
	removed := append([]string(nil), branches[1:]...)
	suffixes, handledStacks := squashSuffixes(state, removed)

	return squashSelection{
		Base:          base,
		Bottom:        branches[0],
		Top:           current,
		Branches:      branches,
		Removed:       removed,
		Suffixes:      suffixes,
		HandledStacks: handledStacks,
	}, nil
}

func squashSuffixes(state State, removed []string) ([]squashSuffix, map[int]bool) {
	deleted := map[string]bool{}
	for _, branch := range removed {
		deleted[branch] = true
	}

	handledStacks := map[int]bool{}
	var suffixes []squashSuffix
	for stackIndex, stack := range state.Stacks {
		lastDeleted := -1
		for branchIndex, branch := range stack.Branches {
			if deleted[branch] {
				lastDeleted = branchIndex
				handledStacks[stackIndex] = true
				continue
			}
			if lastDeleted >= 0 {
				suffixes = append(suffixes, squashSuffix{
					Upstream: stack.Branches[lastDeleted],
					Branches: append([]string(nil), stack.Branches[branchIndex:]...),
				})
				break
			}
		}
	}
	return suffixes, handledStacks
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

	var ops []RebaseOp
	rewritten := []string{selection.Bottom}
	skipTops := map[string]bool{}
	for _, suffix := range selection.Suffixes {
		if len(suffix.Branches) == 0 {
			continue
		}
		upstream := oldRefs[suffix.Upstream]
		if upstream == "" {
			return State{}, nil, fmt.Errorf("missing old ref for %q", suffix.Upstream)
		}
		top := suffix.Branches[len(suffix.Branches)-1]
		ops = append(ops, RebaseOp{
			Onto:     selection.Bottom,
			Upstream: upstream,
			Top:      top,
		})
		rewritten = append(rewritten, suffix.Branches...)
		skipTops[top] = true
	}

	for i, dependent := range state.Stacks {
		if selection.HandledStacks[i] || !deleted[dependent.Base] || len(dependent.Branches) == 0 {
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
	if !opts.noEdit {
		args = append(args, "--edit")
	}
	args = append(args, "-F", path)
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
	opts, err := parseAmendArgs(args)
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

	if err := a.stageRequestedChanges(opts); err != nil {
		return err
	}
	if !opts.stageAll && !opts.stageUpdate {
		unstaged, err := a.git.HasUnstagedChanges()
		if err != nil {
			return err
		}
		if unstaged {
			staged, err := a.git.HasStagedChanges()
			if err != nil {
				return err
			}
			if !staged {
				return fmt.Errorf("unstaged changes are not included by graphene amend; stage them first or use -a/--all or -u/--update")
			}
		}
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

	commitGitArgs := append([]string{"commit", "--amend"}, opts.commitArgs...)
	if err := a.git.Run(commitGitArgs...); err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}

	pending, err := a.pendingForCurrentWorktree(Pending{
		Operation:    "amend",
		Branch:       current,
		ReturnBranch: current,
		Queue:        ops,
	})
	if err != nil {
		return err
	}
	state.Pending = pending
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
	if err := a.validateRestackBase(base); err != nil {
		return err
	}

	wholePath, ok := VisibleStackPath(state, current)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
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

	baseRef, err := a.git.Output("rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return err
	}

	path := wholePath
	oldBase := ""
	var nextState State
	if restackBaseInPath(base, baseRef, wholePath, oldRefs) {
		stack, index, ok := StackSuffix(state, current)
		if !ok {
			return fmt.Errorf("branch %q is not in a graphene stack", current)
		}
		path = append([]string(nil), stack.Branches[index:]...)
		oldBase, ok = BaseBranch(state, path[0])
		if !ok {
			return fmt.Errorf("branch %q is not in a graphene stack", path[0])
		}
		nextState, _, ok = ReparentBranch(state, current, base)
		if !ok {
			return fmt.Errorf("cannot restack %q onto %q", current, base)
		}
	} else {
		var ok bool
		oldBase, ok = BaseBranch(state, path[0])
		if !ok {
			return fmt.Errorf("branch %q is not in a graphene stack", path[0])
		}
		nextState, ok = ReparentStackPath(state, wholePath, base)
		if !ok {
			return fmt.Errorf("cannot restack %q onto %q", current, base)
		}
	}

	headUpdated, err := a.updateCurrentBranchFromUpstream(current, oldHead)
	if err != nil {
		return err
	}
	oldBaseRef := oldRefs[oldBase]
	if oldBaseRef == "" {
		oldBaseRef, err = a.git.Output("rev-parse", "--verify", oldBase+"^{commit}")
		if err != nil {
			return err
		}
	}

	if oldBaseRef == baseRef && !headUpdated {
		return a.git.WriteState(nextState)
	}

	currentIndex := branchIndex(path, current)
	if currentIndex < 0 {
		return fmt.Errorf("branch %q is not in selected restack path", current)
	}
	top := path[len(path)-1]
	var ops []RebaseOp
	var rewritten []string
	skipTops := map[string]bool{}
	if oldBaseRef != baseRef {
		ops = append(ops, RebaseOp{
			Onto:     base,
			Upstream: oldBaseRef,
			Top:      current,
		})
		rewritten = append(rewritten, path...)
		skipTops[top] = true
		if currentIndex+1 < len(path) {
			upstream := oldRefs[current]
			if upstream == "" {
				return fmt.Errorf("missing old ref for %q", current)
			}
			ops = append(ops, RebaseOp{
				Onto:     current,
				Upstream: upstream,
				Top:      top,
			})
		}
	} else if headUpdated {
		rewritten = append(rewritten, current)
	}
	if len(rewritten) > 0 {
		restackOps, err := RestackOpsAfterRewrites(nextState, rewritten, oldRefs, skipTops)
		if err != nil {
			return err
		}
		ops = append(ops, restackOps...)
	}
	if err := a.validateRebaseOpsUpdateable("restack", current, nextState, oldRefs, ops); err != nil {
		return err
	}
	if len(ops) == 0 {
		return a.git.WriteState(nextState)
	}

	pending, err := a.pendingForCurrentWorktree(Pending{
		Operation:    "restack",
		Branch:       current,
		ReturnBranch: current,
		Queue:        ops,
		NextStacks:   nextState.Stacks,
	})
	if err != nil {
		return err
	}
	state.Pending = pending
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runPendingRebases(state)
}

func restackBaseInPath(base, baseRef string, path []string, oldRefs map[string]string) bool {
	for _, branch := range path {
		if branch == base || oldRefs[branch] == baseRef {
			return true
		}
	}
	return false
}

func branchIndex(branches []string, branch string) int {
	for i, candidate := range branches {
		if candidate == branch {
			return i
		}
	}
	return -1
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
	opts, err := parseForgetArgs(args)
	if err != nil {
		return err
	}

	branch := opts.branch
	if branch == "" {
		current, err := a.git.CurrentBranch()
		if err != nil {
			return err
		}
		branch = current
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil && !opts.force {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}

	state, ok := RemoveStackThroughBranch(state, branch)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", branch)
	}
	if opts.force {
		state.Pending = nil
	}
	return a.git.WriteState(state)
}

func (a *App) deleteBranch(args []string) error {
	opts, err := parseDeleteArgs(args)
	if err != nil {
		return err
	}

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	if opts.branch == "" {
		opts.branch = current
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}
	if opts.stack {
		return a.deleteBranchStack(opts.branch, current, state)
	}

	branch := opts.branch
	if !state.ContainsBranch(branch) {
		return fmt.Errorf("branch %q is not in a graphene stack", branch)
	}

	children := newStackGraph(state).children[branch]
	if len(children) > 0 {
		return fmt.Errorf("branch %q has tracked descendants; delete or restack them before graphene delete", branch)
	}

	exists, err := a.git.BranchExists(branch)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("branch %q does not exist", branch)
	}

	base, ok := BaseBranch(state, branch)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", branch)
	}
	if current == branch {
		baseExists, err := a.git.BranchExists(base)
		if err != nil {
			return err
		}
		if !baseExists {
			return fmt.Errorf("base branch %q does not exist; switch away from %q before graphene delete", base, branch)
		}
		if err := a.git.Run("switch", base); err != nil {
			return err
		}
	} else {
		checkedOut, err := a.git.BranchCheckedOut(branch)
		if err != nil {
			return err
		}
		if checkedOut {
			return fmt.Errorf("branch %q is checked out in another worktree; switch that worktree away from the branch before graphene delete", branch)
		}
	}

	if err := a.git.Run("branch", "-D", branch); err != nil {
		return err
	}
	nextState := RemoveBranches(State{Stacks: cloneStacks(state.Stacks)}, []string{branch})
	return a.git.WriteState(nextState)
}

func (a *App) deleteBranchStack(branch, current string, state State) error {
	if !state.ContainsBranch(branch) {
		return fmt.Errorf("branch %q is not in a graphene stack", branch)
	}

	branches := stackSubtreeBranches(state, branch)
	if len(branches) == 0 {
		return fmt.Errorf("branch %q is not in a graphene stack", branch)
	}

	deleting := map[string]bool{}
	for _, branch := range branches {
		deleting[branch] = true
		exists, err := a.git.BranchExists(branch)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("branch %q does not exist", branch)
		}
	}

	if deleting[current] {
		base, ok := BaseBranch(state, branch)
		if !ok {
			return fmt.Errorf("branch %q is not in a graphene stack", branch)
		}
		baseExists, err := a.git.BranchExists(base)
		if err != nil {
			return err
		}
		if !baseExists {
			return fmt.Errorf("base branch %q does not exist; switch away from %q before graphene delete --stack", base, current)
		}
		if err := a.git.Run("switch", base); err != nil {
			return err
		}
	}

	for _, branch := range branches {
		checkedOut, err := a.git.BranchCheckedOut(branch)
		if err != nil {
			return err
		}
		if checkedOut {
			return fmt.Errorf("branch %q is checked out in another worktree; switch that worktree away from the branch before graphene delete --stack", branch)
		}
	}

	for _, branch := range branches {
		if err := a.git.Run("branch", "-D", branch); err != nil {
			return err
		}
	}
	nextState := RemoveBranches(State{Stacks: cloneStacks(state.Stacks)}, branches)
	return a.git.WriteState(nextState)
}

func stackSubtreeBranches(state State, root string) []string {
	graph := newStackGraph(state)
	if !graph.nodes[root] || !state.ContainsBranch(root) {
		return nil
	}

	var branches []string
	seen := map[string]bool{}
	var add func(string)
	add = func(branch string) {
		if branch == "" || seen[branch] {
			return
		}
		seen[branch] = true
		for _, child := range graph.children[branch] {
			add(child)
		}
		if state.ContainsBranch(branch) {
			branches = append(branches, branch)
		}
	}
	add(root)
	return branches
}

func (a *App) trackBranch(base, branch string) error {
	if branch == "" {
		var err error
		branch, err = a.git.CurrentBranch()
		if err != nil {
			return err
		}
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}

	nextState, err := TrackBranch(state, base, branch)
	if err != nil {
		return err
	}
	if err := a.validateTrackBranchesExist(base, branch); err != nil {
		return err
	}
	if err := a.updateTrackParentFromUpstream(base); err != nil {
		return err
	}
	if err := a.validateTrackBranchShape(base, branch); err != nil {
		return err
	}
	return a.git.WriteState(nextState)
}

func (a *App) track(args []string) error {
	base, branch, err := parseTrackArgs(args)
	if err != nil {
		return err
	}
	return a.trackBranch(base, branch)
}

type importBranch struct {
	Commit string
	Branch string
	Create bool
}

func (a *App) importStack(args []string) error {
	base, err := parseImportArgs(args)
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

	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if inProgress {
		return fmt.Errorf("rebase in progress; use graphene continue or graphene abort before graphene import")
	}

	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("tracked changes would prevent import; stash or commit them before graphene import")
	}

	baseExists, err := a.git.BranchExists(base)
	if err != nil {
		return err
	}
	if !baseExists {
		return fmt.Errorf("base branch %q does not exist", base)
	}

	baseRef := "refs/heads/" + base
	ancestor, err := a.isAncestor(baseRef, "HEAD")
	if err != nil {
		return err
	}
	if !ancestor {
		return fmt.Errorf("base branch %q is not an ancestor of HEAD", base)
	}

	commits, err := a.importCommits(base)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		return fmt.Errorf("base branch %q already points to HEAD", base)
	}
	if err := a.validateImportHistory(baseRef, commits); err != nil {
		return err
	}

	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	branches, err := a.planImportBranches(state, current, cfg, commits)
	if err != nil {
		return err
	}

	nextState := state
	parent := base
	for _, branch := range branches {
		nextState, err = TrackBranch(nextState, parent, branch.Branch)
		if err != nil {
			return err
		}
		parent = branch.Branch
	}

	var created []string
	for _, branch := range branches {
		if !branch.Create {
			continue
		}
		if err := a.git.OutputErr("branch", branch.Branch, branch.Commit); err != nil {
			a.cleanupCreatedBranches(created)
			return err
		}
		created = append(created, branch.Branch)
	}

	if err := a.validateImportedBranches(base, branches); err != nil {
		a.cleanupCreatedBranches(created)
		return err
	}
	return a.git.WriteState(nextState)
}

func (a *App) importCommits(base string) ([]string, error) {
	out, err := a.git.Output("rev-list", "--reverse", "--first-parent", "refs/heads/"+base+"..HEAD")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (a *App) validateImportHistory(base string, commits []string) error {
	parent := base
	for _, commit := range commits {
		ancestor, err := a.isAncestor(parent, commit)
		if err != nil {
			return err
		}
		if !ancestor {
			return fmt.Errorf("history from %s to HEAD is not linear; commit %s is not descended from %s", shortImportRef(base), shortSyncRef(commit), shortImportRef(parent))
		}

		count, err := a.commitCount(parent, commit)
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("commit %s contains %d commits on top of %s; Graphene can only import linear one-commit steps", shortSyncRef(commit), count, shortImportRef(parent))
		}
		parent = commit
	}
	return nil
}

func (a *App) planImportBranches(state State, current string, cfg Config, commits []string) ([]importBranch, error) {
	reserved := map[string]bool{}
	branches := make([]importBranch, 0, len(commits))
	for i, commit := range commits {
		branch, create, err := a.importBranchName(state, current, cfg, commit, i == len(commits)-1, reserved)
		if err != nil {
			return nil, err
		}
		reserved[branch] = true
		branches = append(branches, importBranch{
			Commit: commit,
			Branch: branch,
			Create: create,
		})
	}
	return branches, nil
}

func (a *App) importBranchName(state State, current string, cfg Config, commit string, head bool, reserved map[string]bool) (string, bool, error) {
	branches, err := a.git.LocalBranchesPointingAt(commit)
	if err != nil {
		return "", false, err
	}
	sort.Strings(branches)

	if head {
		for _, branch := range branches {
			if branch != current {
				continue
			}
			if StateContainsName(state, current) {
				return "", false, fmt.Errorf("current branch %q is already recorded in graphene state", current)
			}
			return current, false, nil
		}
	}

	var reusable []string
	for _, branch := range branches {
		if branch == "" || reserved[branch] || StateContainsName(state, branch) {
			continue
		}
		reusable = append(reusable, branch)
	}
	if len(reusable) == 1 {
		return reusable[0], false, nil
	}

	if err := a.ensureBranchPrefixAvailable(cfg.BranchPrefix); err != nil {
		return "", false, err
	}
	subject, err := a.git.Output("log", "-1", "--format=%s", commit)
	if err != nil {
		return "", false, err
	}
	branch, err := a.derivedBranchNameWithState(cfg, state, subject, reserved)
	if err != nil {
		return "", false, err
	}
	return branch, true, nil
}

func (a *App) validateImportedBranches(base string, branches []importBranch) error {
	parent := base
	for _, branch := range branches {
		if err := a.validateTrackBranch(parent, branch.Branch); err != nil {
			return err
		}
		parent = branch.Branch
	}
	return nil
}

func (a *App) cleanupCreatedBranches(branches []string) {
	for i := len(branches) - 1; i >= 0; i-- {
		_ = a.git.OutputErr("branch", "-D", branches[i])
	}
}

func shortImportRef(ref string) string {
	if strings.HasPrefix(ref, "refs/heads/") {
		return strings.TrimPrefix(ref, "refs/heads/")
	}
	return shortSyncRef(ref)
}

func (a *App) pendingForCurrentWorktree(pending Pending) (*Pending, error) {
	worktree, err := a.git.WorktreeID()
	if err != nil {
		return nil, err
	}
	pending.Worktree = worktree
	return &pending, nil
}

type syncSelection struct {
	Base        string
	Current     string
	BaseCurrent bool
	Paths       []syncPath
}

type syncPath struct {
	StackIndex         int
	Stack              Stack
	BranchLimit        int
	CurrentBranchIndex int
	SkipTops           []string
}

func syncSelectionForCurrent(state State, current string) (syncSelection, bool) {
	loc, ok := state.BranchLocation(current)
	if ok {
		stack, ok := state.StackAt(loc.StackIndex)
		if !ok {
			return syncSelection{}, false
		}
		branches := BranchesThroughCurrent(state, current)
		if len(branches) == 0 {
			return syncSelection{}, false
		}
		base := stack.Base
		if visibleBase, ok := BaseBranch(state, branches[0]); ok {
			base = visibleBase
		}
		return syncSelection{
			Base:    base,
			Current: current,
			Paths: []syncPath{{
				StackIndex:         loc.StackIndex,
				Stack:              Stack{Base: base, Branches: branches},
				BranchLimit:        len(branches),
				CurrentBranchIndex: len(branches) - 1,
				SkipTops:           stackTopsInBranches(state, branches),
			}},
		}, true
	}

	return syncSelectionForBase(state, current)
}

func stackTopsInBranches(state State, branches []string) []string {
	included := map[string]bool{}
	for _, branch := range branches {
		included[branch] = true
	}

	seen := map[string]bool{}
	var tops []string
	for _, stack := range state.Stacks {
		if len(stack.Branches) == 0 {
			continue
		}
		top := stack.Branches[len(stack.Branches)-1]
		if included[top] && !seen[top] {
			tops = append(tops, top)
			seen[top] = true
		}
	}
	return tops
}

func syncSelectionForBase(state State, current string) (syncSelection, bool) {
	var paths []syncPath
	for i, stack := range state.Stacks {
		if stack.Base != current || len(stack.Branches) == 0 {
			continue
		}
		paths = append(paths, syncPath{
			StackIndex:         i,
			Stack:              stack,
			BranchLimit:        len(stack.Branches),
			CurrentBranchIndex: -1,
		})
	}
	if len(paths) == 0 {
		return syncSelection{}, false
	}
	return syncSelection{
		Base:        current,
		Current:     current,
		BaseCurrent: true,
		Paths:       paths,
	}, true
}

func availableStackBases(state State) []string {
	seen := map[string]bool{}
	var bases []string
	for _, stack := range state.Stacks {
		if stack.Base == "" || len(stack.Branches) == 0 || seen[stack.Base] {
			continue
		}
		bases = append(bases, stack.Base)
		seen[stack.Base] = true
	}
	return bases
}

func formatAvailableStackBases(state State) string {
	bases := availableStackBases(state)
	if len(bases) == 0 {
		return ""
	}
	return " (available bases: " + strings.Join(bases, ", ") + ")"
}

func (s syncSelection) ContainsStack(index int) bool {
	for _, path := range s.Paths {
		if path.StackIndex == index {
			return true
		}
	}
	return false
}

func (s syncSelection) ReturnBranch(firstRemaining map[int]int) string {
	if s.BaseCurrent || len(s.Paths) == 0 {
		return s.Current
	}

	path := s.Paths[0]
	first := firstRemaining[path.StackIndex]
	if first <= path.CurrentBranchIndex {
		return s.Current
	}
	if first < len(path.Stack.Branches) {
		return path.Stack.Branches[first]
	}
	return ""
}

func (a *App) sync(args []string) error {
	opts, err := parseSyncArgs(args)
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

	var selection syncSelection
	var ok bool
	if opts.all {
		if state.ContainsBranch(current) {
			return fmt.Errorf("graphene sync --all must be run from a stack base; %q is a tracked branch", current)
		}
		selection, ok = syncSelectionForBase(state, current)
		if !ok {
			return fmt.Errorf("graphene sync --all must be run from a stack base; %q is not a stack base%s", current, formatAvailableStackBases(state))
		}
	} else {
		selection, ok = syncSelectionForCurrent(state, current)
	}
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
	for _, path := range selection.Paths {
		if err := a.validateStackShape(path.Stack); err != nil {
			return err
		}
	}

	oldRefs := a.stateRefs(state)
	var baseRef string
	var basePlan syncBaseDryRun
	if opts.dryRun {
		baseRef, basePlan, err = a.fetchSyncBaseDryRun(selection.Base)
		if err != nil {
			return err
		}
	} else {
		baseRef, err = a.fetchSyncBase(selection.Base, current)
		if err != nil {
			return err
		}
	}

	deletedUpstreams, err := a.deletedSyncUpstreamBranches(selection)
	if err != nil {
		return err
	}

	var branches []string
	firstRemaining := map[int]int{}
	for _, path := range selection.Paths {
		applied, err := a.appliedPrefixBranches(baseRef, path.Stack.Branches[:path.BranchLimit], oldRefs)
		if err != nil {
			return err
		}
		removed := append([]string(nil), applied...)
		for _, branch := range path.Stack.Branches[len(applied):path.BranchLimit] {
			if !deletedUpstreams[branch] {
				break
			}
			removed = append(removed, branch)
		}
		firstRemaining[path.StackIndex] = len(removed)
		branches = append(branches, removed...)
	}

	nextState := RemoveBranchesWithBase(state, branches, selection.Base)
	baseChanges := branchBaseChanges(state, nextState)
	deleted := map[string]bool{}
	for _, branch := range branches {
		deleted[branch] = true
	}

	returnBranch := selection.ReturnBranch(firstRemaining)

	var ops []RebaseOp
	var rewritten []string
	skipTops := map[string]bool{}
	for _, path := range selection.Paths {
		first := firstRemaining[path.StackIndex]
		stack := path.Stack
		if first >= len(stack.Branches) {
			continue
		}
		predecessor := stack.Base
		if first > 0 {
			predecessor = stack.Branches[first-1]
		}
		upstream := oldRefs[predecessor]
		if upstream == "" {
			return fmt.Errorf("missing old ref for %q", predecessor)
		}

		topIndex := path.BranchLimit - 1
		if path.CurrentBranchIndex >= 0 && first > path.CurrentBranchIndex {
			topIndex = len(stack.Branches) - 1
		}
		top := stack.Branches[topIndex]
		ops = append(ops, RebaseOp{
			Onto:     baseRef,
			Upstream: upstream,
			Top:      top,
		})
		rewritten = append(rewritten, stack.Branches[first:topIndex+1]...)
		if topIndex == len(stack.Branches)-1 {
			skipTops[top] = true
		}
		for _, skipTop := range path.SkipTops {
			skipTops[skipTop] = true
		}
	}

	for i, dependent := range state.Stacks {
		if selection.ContainsStack(i) || !deleted[dependent.Base] || len(dependent.Branches) == 0 {
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

	if opts.dryRun {
		a.printSyncDryRun(basePlan, branches, baseChanges, ops, returnBranch, baseRef)
		return nil
	}

	if len(ops) == 0 {
		if returnBranch != "" {
			if err := a.git.Run("switch", returnBranch); err != nil {
				return err
			}
		} else if err := a.switchToBaseOrDetach(selection.Base, baseRef); err != nil {
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

	pending, err := a.pendingForCurrentWorktree(Pending{
		Operation:    "sync",
		Branch:       returnBranch,
		ReturnBranch: returnBranch,
		Queue:        ops,
		Branches:     branches,
		NextStacks:   nextState.Stacks,
		BaseChanges:  baseChanges,
	})
	if err != nil {
		return err
	}
	state.Pending = pending
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

func (a *App) skill(args []string) error {
	opts, err := parseSkillArgs(args)
	if err != nil {
		return err
	}
	out, err := a.skillOutPath(opts)
	if err != nil {
		return err
	}
	if out == "" || out == "-" {
		_, err := io.WriteString(a.stdout, graphenestackedprs.Content)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, []byte(graphenestackedprs.Content), 0o644); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "Wrote Graphene skill to %s\n", out)
	return err
}

func (a *App) skillOutPath(opts skillOptions) (string, error) {
	switch opts.target {
	case "":
		return opts.out, nil
	case "codex":
		home := a.getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("HOME is required for graphene skill --codex")
		}
		return filepath.Join(home, ".codex", "skills", "graphene-stacked-prs", "SKILL.md"), nil
	case "claude":
		home := a.getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("HOME is required for graphene skill --claude")
		}
		return filepath.Join(home, ".claude", "skills", "graphene-stacked-prs", "SKILL.md"), nil
	default:
		return "", fmt.Errorf("unsupported skill target %q", opts.target)
	}
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
	branches := BranchesThroughCurrent(state, current)
	if opts.stack {
		branches = BranchesThroughCurrentAndDescendants(state, current)
	}
	if len(branches) == 0 {
		return fmt.Errorf("no branch to send")
	}
	if err := a.validateSendAllowed(state, branches); err != nil {
		return err
	}
	remote := opts.remote
	if remote == "" {
		remote, err = a.pushRemote(current, branches)
		if err != nil {
			return err
		}
	}
	if forceWithLease {
		if err := a.validateForcePushPreservesRemoteDescendantPatches(remote, state, branches); err != nil {
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

func (a *App) validateForcePushPreservesRemoteDescendantPatches(remote string, state State, branches []string) error {
	if len(branches) < 2 {
		return nil
	}

	commitRefs := map[string]string{}
	commitRef := func(branch string) (string, error) {
		if ref := commitRefs[branch]; ref != "" {
			return ref, nil
		}
		ref, err := a.git.Output("rev-parse", "--verify", branch+"^{commit}")
		if err != nil {
			return "", err
		}
		commitRefs[branch] = ref
		return ref, nil
	}

	for _, branch := range branches {
		remoteRef := "refs/remotes/" + remote + "/" + branch
		if valid, err := a.validRefName(remoteRef); err != nil {
			return err
		} else if !valid {
			continue
		}

		remoteCommit, err := a.git.Output("rev-parse", "--verify", remoteRef+"^{commit}")
		if err != nil {
			if isGitExit(err, 1) {
				continue
			}
			return err
		}

		for _, descendant := range branches {
			if descendant == branch || !stateHasPath(state, branch, descendant) {
				continue
			}

			descendantCommit, err := commitRef(descendant)
			if err != nil {
				return err
			}
			remoteHasPatch, err := a.commitPatchAppliedTo(remoteCommit, descendantCommit)
			if err != nil {
				return err
			}
			if !remoteHasPatch {
				continue
			}

			localHasPatch, err := a.commitPatchAppliedTo(branch, descendantCommit)
			if err != nil {
				return err
			}
			if localHasPatch {
				continue
			}

			return fmt.Errorf("refusing to force-push %q because %s/%s already contains the patch from descendant %q", branch, remote, branch, descendant)
		}
	}
	return nil
}

func (a *App) validRefName(ref string) (bool, error) {
	_, err := a.git.Output("check-ref-format", "--normalize", ref)
	if err == nil {
		return true, nil
	}
	if isGitExit(err, 1) {
		return false, nil
	}
	return false, err
}

func (a *App) commitPatchAppliedTo(upstream, commit string) (bool, error) {
	ancestor, err := a.isAncestor(commit, upstream)
	if err != nil {
		return false, err
	}
	if ancestor {
		return true, nil
	}

	out, err := a.git.Output("cherry", upstream, commit)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != commit {
			continue
		}
		return fields[0] == "-", nil
	}
	return false, nil
}

func (a *App) validateSendAllowed(state State, branches []string) error {
	if state.Pending == nil {
		return nil
	}

	currentWorktree, err := a.pendingBelongsToCurrentWorktree(state.Pending)
	if err != nil {
		return err
	}
	if currentWorktree {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}
	if branch := pendingBranchBeingRewritten(state, branches); branch != "" {
		return fmt.Errorf("pending rebase in another worktree is rewriting branch %q; use graphene continue or graphene abort there before graphene send", branch)
	}
	return nil
}

func (a *App) pendingBelongsToCurrentWorktree(pending *Pending) (bool, error) {
	if pending.Worktree == "" {
		return a.git.RebaseInProgress()
	}
	current, err := a.git.WorktreeID()
	if err != nil {
		return false, err
	}
	return filepath.Clean(pending.Worktree) == current, nil
}

func pendingBranchBeingRewritten(state State, branches []string) string {
	affected := pendingAffectedBranches(state)
	for _, branch := range branches {
		if affected[branch] {
			return branch
		}
	}
	return ""
}

func pendingAffectedBranches(state State) map[string]bool {
	affected := map[string]bool{}
	if state.Pending == nil {
		return affected
	}

	add := func(branch string) {
		if branch != "" && state.ContainsBranch(branch) {
			affected[branch] = true
		}
	}
	addStack := func(branch string) {
		loc, ok := state.BranchLocation(branch)
		if !ok {
			return
		}
		for _, stackBranch := range state.Stacks[loc.StackIndex].Branches {
			add(stackBranch)
		}
	}

	pending := state.Pending
	for _, branch := range []string{pending.Branch, pending.ReturnBranch, pending.Top} {
		add(branch)
		addStack(branch)
	}
	for _, branch := range pending.Branches {
		add(branch)
		addStack(branch)
	}
	for _, op := range pending.Queue {
		add(op.Top)
		addStack(op.Top)
	}
	return affected
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
	return a.derivedBranchNameWithState(cfg, state, subject, nil)
}

func (a *App) derivedBranchNameWithState(cfg Config, state State, subject string, reserved map[string]bool) (string, error) {
	base := BranchName(cfg.BranchPrefix, SlugSubject(subject))
	for n := 1; ; n++ {
		candidate := CandidateName(base, n)
		status, err := a.git.BranchCreateStatus(candidate)
		if err != nil {
			return "", err
		}
		if status == "available" && !StateContainsName(state, candidate) && !reserved[candidate] {
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

func (a *App) printSyncDryRun(base syncBaseDryRun, branches []string, changes []BaseChange, ops []RebaseOp, returnBranch, baseRef string) {
	fmt.Fprintf(a.stdout, "Dry run: sync %s\n", base.Branch)
	fmt.Fprintf(a.stdout, "  fetch: %s\n", base.UpstreamName())
	if base.Old == base.Updated {
		fmt.Fprintf(a.stdout, "  base: %s is up to date\n", base.Branch)
	} else {
		fmt.Fprintf(a.stdout, "  base: %s %s -> %s\n", base.Branch, shortSyncRef(base.Old), shortSyncRef(base.Updated))
	}

	if len(branches) == 0 {
		fmt.Fprintln(a.stdout, "  delete applied branches: none")
	} else {
		fmt.Fprintln(a.stdout, "  delete applied branches:")
		for _, branch := range branches {
			fmt.Fprintf(a.stdout, "    %s\n", branch)
		}
	}

	if len(changes) > 0 {
		fmt.Fprintln(a.stdout, "  retarget existing PRs:")
		for _, change := range changes {
			fmt.Fprintf(a.stdout, "    %s: %s -> %s\n", change.Branch, change.OldBase, change.NewBase)
		}
	}

	if len(ops) == 0 {
		fmt.Fprintln(a.stdout, "  rebase: none")
	} else {
		fmt.Fprintln(a.stdout, "  rebase:")
		for _, op := range ops {
			fmt.Fprintf(a.stdout, "    git rebase --update-refs --onto %s %s %s\n", shortSyncRef(op.Onto), shortSyncRef(op.Upstream), op.Top)
		}
	}

	if returnBranch != "" {
		fmt.Fprintf(a.stdout, "  return: %s\n", returnBranch)
	} else {
		fmt.Fprintf(a.stdout, "  return: detach at %s\n", shortSyncRef(baseRef))
	}
}

func (a *App) fetchSyncBase(base, current string) (string, error) {
	if base == current {
		return a.fetchCurrentBase(base)
	}
	return a.fetchBase(base)
}

func (a *App) updateTrackParentFromUpstream(base string) error {
	exists, err := a.git.BranchExists(base)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	remote, merge, err := a.git.Upstream(base)
	if err != nil {
		return err
	}
	if remote == "" || merge == "" {
		return nil
	}

	oldBase, err := a.git.Output("rev-parse", "--verify", "refs/heads/"+base+"^{commit}")
	if err != nil {
		return err
	}
	if err := a.git.Run("fetch", "--prune", remote); err != nil {
		return err
	}

	upstream := base + "@{upstream}"
	updatedBase, err := a.git.Output("rev-parse", "--verify", upstream+"^{commit}")
	if err != nil {
		return err
	}
	if oldBase == updatedBase {
		return nil
	}
	ancestor, err := a.isAncestor(oldBase, updatedBase)
	if err != nil {
		return err
	}
	if !ancestor {
		return fmt.Errorf("cannot fast-forward %q to %q; resolve the parent branch before tracking", base, upstream)
	}

	current, err := a.git.Output("branch", "--show-current")
	if err != nil {
		return err
	}
	if current == base {
		return a.git.Run("merge", "--ff-only", updatedBase)
	}

	checkedOut, err := a.git.BranchCheckedOut(base)
	if err != nil {
		return err
	}
	if checkedOut {
		return fmt.Errorf("branch %q is checked out in another worktree; switch that worktree away from the branch before graphene track", base)
	}
	return a.git.OutputErr("update-ref", "refs/heads/"+base, updatedBase, oldBase)
}

func (a *App) updateCurrentBranchFromUpstream(branch, oldHead string) (bool, error) {
	remote, merge, err := a.git.Upstream(branch)
	if err != nil {
		return false, err
	}
	if remote == "" || merge == "" {
		return false, nil
	}
	if err := a.git.Run("fetch", remote); err != nil {
		return false, err
	}

	upstream := branch + "@{upstream}"
	updatedHead, err := a.git.Output("rev-parse", "--verify", upstream+"^{commit}")
	if err != nil {
		return false, err
	}
	if oldHead == updatedHead {
		return false, nil
	}
	ancestor, err := a.isAncestor(updatedHead, oldHead)
	if err != nil {
		return false, err
	}
	if ancestor {
		return false, nil
	}
	ancestor, err = a.isAncestor(oldHead, updatedHead)
	if err != nil {
		return false, err
	}
	if !ancestor {
		return false, fmt.Errorf("cannot fast-forward %q to %q; resolve the branch before restacking", branch, upstream)
	}
	if err := a.git.Run("merge", "--ff-only", updatedHead); err != nil {
		return false, err
	}
	return true, nil
}

type syncBaseDryRun struct {
	Branch  string
	Remote  string
	Merge   string
	Old     string
	Updated string
}

func (b syncBaseDryRun) UpstreamName() string {
	if strings.HasPrefix(b.Merge, "refs/heads/") {
		return b.Remote + "/" + strings.TrimPrefix(b.Merge, "refs/heads/")
	}
	return b.Remote + " " + b.Merge
}

type fetchedBase struct {
	Old     string
	Updated string
}

func (a *App) fetchBaseUpdate(base string) (fetchedBase, error) {
	remote, merge, err := a.git.Upstream(base)
	if err != nil {
		return fetchedBase{}, err
	}
	if remote == "" || merge == "" {
		return fetchedBase{}, fmt.Errorf("branch %q has no upstream; set one before updating the stack", base)
	}

	oldBase, err := a.git.Output("rev-parse", "--verify", "refs/heads/"+base+"^{commit}")
	if err != nil {
		return fetchedBase{}, err
	}
	if err := a.git.Run("fetch", "--prune", remote); err != nil {
		return fetchedBase{}, err
	}

	upstream := base + "@{upstream}"
	updatedBase, err := a.git.Output("rev-parse", "--verify", upstream+"^{commit}")
	if err != nil {
		return fetchedBase{}, err
	}
	ancestor, err := a.isAncestor(oldBase, updatedBase)
	if err != nil {
		return fetchedBase{}, err
	}
	if !ancestor {
		return fetchedBase{}, fmt.Errorf("cannot fast-forward %q to %q; resolve the base branch before updating the stack", base, upstream)
	}
	return fetchedBase{Old: oldBase, Updated: updatedBase}, nil
}

func (a *App) deletedSyncUpstreamBranches(selection syncSelection) (map[string]bool, error) {
	deleted := map[string]bool{}
	seen := map[string]bool{}
	exists := map[string]bool{}

	for _, path := range selection.Paths {
		for _, branch := range path.Stack.Branches[:path.BranchLimit] {
			if seen[branch] {
				continue
			}
			seen[branch] = true

			remote, merge, err := a.git.Upstream(branch)
			if err != nil {
				return nil, err
			}
			if remote == "" || merge == "" {
				continue
			}

			key := remote + "\x00" + merge
			remoteHasRef, ok := exists[key]
			if !ok {
				remoteHasRef, err = a.remoteRefExists(remote, merge)
				if err != nil {
					return nil, err
				}
				exists[key] = remoteHasRef
			}
			if !remoteHasRef {
				deleted[branch] = true
			}
		}
	}

	return deleted, nil
}

func (a *App) remoteRefExists(remote, ref string) (bool, error) {
	_, err := a.git.Output("ls-remote", "--exit-code", remote, ref)
	if err == nil {
		return true, nil
	}
	if isGitExit(err, 2) {
		return false, nil
	}
	return false, err
}

func (a *App) fetchCurrentBase(base string) (string, error) {
	fetched, err := a.fetchBaseUpdate(base)
	if err != nil {
		return "", err
	}
	if fetched.Old == fetched.Updated {
		return base, nil
	}
	if err := a.git.Run("merge", "--ff-only", fetched.Updated); err != nil {
		return "", err
	}
	return base, nil
}

func (a *App) fetchBase(base string) (string, error) {
	fetched, err := a.fetchBaseUpdate(base)
	if err != nil {
		return "", err
	}
	if fetched.Old == fetched.Updated {
		return base, nil
	}

	checkedOut, err := a.git.BranchCheckedOut(base)
	if err != nil {
		return "", err
	}
	if checkedOut {
		return fetched.Updated, nil
	}
	if err := a.git.OutputErr("update-ref", "refs/heads/"+base, fetched.Updated, fetched.Old); err != nil {
		return "", err
	}
	return base, nil
}

func (a *App) fetchSyncBaseDryRun(base string) (string, syncBaseDryRun, error) {
	remote, merge, err := a.git.Upstream(base)
	if err != nil {
		return "", syncBaseDryRun{}, err
	}
	if remote == "" || merge == "" {
		return "", syncBaseDryRun{}, fmt.Errorf("branch %q has no upstream; set one before updating the stack", base)
	}

	oldBase, err := a.git.Output("rev-parse", "--verify", "refs/heads/"+base+"^{commit}")
	if err != nil {
		return "", syncBaseDryRun{}, err
	}
	tempRef := fmt.Sprintf("refs/graphene/dry-run/%d-%d", os.Getpid(), time.Now().UnixNano())
	// Fetch into a private ref so dry-run can inspect the new base without moving local or remote-tracking refs.
	if err := a.git.OutputErr("fetch", "--no-write-fetch-head", "--refmap=", remote, "+"+merge+":"+tempRef); err != nil {
		return "", syncBaseDryRun{}, err
	}
	updatedBase, refErr := a.git.Output("rev-parse", "--verify", tempRef+"^{commit}")
	cleanupErr := a.git.OutputErr("update-ref", "-d", tempRef)
	if refErr != nil {
		return "", syncBaseDryRun{}, refErr
	}
	if cleanupErr != nil {
		return "", syncBaseDryRun{}, cleanupErr
	}

	ancestor, err := a.isAncestor(oldBase, updatedBase)
	if err != nil {
		return "", syncBaseDryRun{}, err
	}
	if !ancestor {
		return "", syncBaseDryRun{}, fmt.Errorf("cannot fast-forward %q to %q; resolve the base branch before updating the stack", base, base+"@{upstream}")
	}

	baseRef := base
	if oldBase != updatedBase {
		baseRef = updatedBase
	}
	return baseRef, syncBaseDryRun{
		Branch:  base,
		Remote:  remote,
		Merge:   merge,
		Old:     oldBase,
		Updated: updatedBase,
	}, nil
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

func shortSyncRef(ref string) string {
	if len(ref) < 12 {
		return ref
	}
	for _, r := range ref {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return ref
		}
	}
	return ref[:12]
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

func (a *App) refExists(ref string) (bool, error) {
	_, err := a.git.Output("show-ref", "--verify", "--quiet", ref)
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

func (a *App) validateRestackBase(base string) error {
	exists, err := a.git.BranchExists(base)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	if remoteExists, err := a.refExists("refs/remotes/" + base); err != nil {
		return err
	} else if remoteExists {
		return fmt.Errorf("base %q is a remote-tracking ref; use a local branch name for graphene restack", base)
	}
	return fmt.Errorf("base branch %q does not exist", base)
}

func (a *App) validateTrackBranch(base, branch string) error {
	if err := a.validateTrackBranchesExist(base, branch); err != nil {
		return err
	}
	return a.validateTrackBranchShape(base, branch)
}

func (a *App) validateTrackBranchesExist(base, branch string) error {
	baseExists, err := a.git.BranchExists(base)
	if err != nil {
		return err
	}
	if !baseExists {
		return fmt.Errorf("base branch %q does not exist", base)
	}

	branchExists, err := a.git.BranchExists(branch)
	if err != nil {
		return err
	}
	if !branchExists {
		return fmt.Errorf("branch %q does not exist", branch)
	}
	return nil
}

func (a *App) validateTrackBranchShape(base, branch string) error {
	baseRef := "refs/heads/" + base
	branchRef := "refs/heads/" + branch
	ancestor, err := a.isAncestor(baseRef, branchRef)
	if err != nil {
		return err
	}
	if !ancestor {
		return fmt.Errorf("base branch %q is not an ancestor of branch %q", base, branch)
	}

	count, err := a.commitCount(baseRef, branchRef)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("branch %q contains %d commits on top of %q; Graphene can only track one-commit branches", branch, count, base)
	}
	return nil
}

type commitOptions struct {
	branch       string
	base         string
	reuseCurrent bool
	stageAll     bool
	stageUpdate  bool
	commitArgs   []string
}

type squashOptions struct {
	count      int
	commitArgs []string
	messageSet bool
	noEdit     bool
}

type skillOptions struct {
	out    string
	target string
}

type forgetOptions struct {
	force  bool
	branch string
}

type deleteOptions struct {
	stack  bool
	branch string
}

type syncOptions struct {
	all    bool
	dryRun bool
}

func parseNewArgs(args []string) (commitOptions, error) {
	return parseCommitOptions(args, true)
}

func parseAmendArgs(args []string) (commitOptions, error) {
	return parseCommitOptions(args, false)
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

func parseSyncArgs(args []string) (syncOptions, error) {
	var opts syncOptions
	for _, arg := range args {
		switch arg {
		case "-a", "--all":
			opts.all = true
		case "-n", "--dry-run":
			opts.dryRun = true
		default:
			return opts, fmt.Errorf("unsupported argument %q; supported sync options are -a/--all and -n/--dry-run", arg)
		}
	}
	return opts, nil
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
		case arg == "--no-edit":
			opts.commitArgs = append(opts.commitArgs, arg)
			opts.noEdit = true
		case arg == "--no-verify":
			opts.commitArgs = append(opts.commitArgs, arg)
		case arg == "--gpg-sign" || strings.HasPrefix(arg, "--gpg-sign=") || arg == "--no-gpg-sign":
			opts.commitArgs = append(opts.commitArgs, arg)
		default:
			return opts, fmt.Errorf("unsupported argument %q; supported squash options are -c/--count, -m/--message, --no-edit, --no-verify, --gpg-sign, and --no-gpg-sign", arg)
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
	branchFromFlag := false
	branchFromPosition := false
	baseFlag := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "-a" || arg == "--all":
			opts.stageAll = true
		case arg == "-u" || arg == "--update":
			opts.stageUpdate = true
		case shortCommitFlags(arg):
			message, err := parseShortCommitFlags(arg, &opts)
			if err != nil {
				return opts, err
			}
			if message {
				if i+1 >= len(args) {
					return opts, fmt.Errorf("missing message after %s", arg)
				}
				opts.commitArgs = append(opts.commitArgs, "-m", args[i+1])
				i++
			}
		case arg == "-b" || arg == "--branch":
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support %s", arg)
			}
			if opts.branch != "" {
				if branchFromPosition {
					return opts, fmt.Errorf("graphene new accepts either positional branch or -b/--branch, not both")
				}
				return opts, fmt.Errorf("new branch specified more than once")
			}
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("missing branch after %s", arg)
			}
			opts.branch = args[i+1]
			branchFromFlag = true
			i++
		case strings.HasPrefix(arg, "--branch="):
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support --branch")
			}
			if opts.branch != "" {
				if branchFromPosition {
					return opts, fmt.Errorf("graphene new accepts either positional branch or -b/--branch, not both")
				}
				return opts, fmt.Errorf("new branch specified more than once")
			}
			opts.branch = strings.TrimPrefix(arg, "--branch=")
			if opts.branch == "" {
				return opts, fmt.Errorf("missing branch after --branch")
			}
			branchFromFlag = true
		case arg == "--base" || arg == "--parent":
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support %s", arg)
			}
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, missingNewBase(arg)
			}
			if err := setNewBase(&opts, &baseFlag, arg, args[i+1]); err != nil {
				return opts, err
			}
			i++
		case strings.HasPrefix(arg, "--base=") || strings.HasPrefix(arg, "--parent="):
			flag, value, _ := strings.Cut(arg, "=")
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support %s", flag)
			}
			if value == "" {
				return opts, missingNewBase(flag)
			}
			if err := setNewBase(&opts, &baseFlag, flag, value); err != nil {
				return opts, err
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
		case arg == "--no-edit":
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
			if allowBranch && !strings.HasPrefix(arg, "-") {
				if opts.branch != "" {
					if branchFromFlag {
						return opts, fmt.Errorf("graphene new accepts either positional branch or -b/--branch, not both")
					}
					return opts, fmt.Errorf("graphene new accepts at most one branch")
				}
				opts.branch = arg
				branchFromPosition = true
				continue
			}
			return opts, unsupportedCommitArg(arg, allowBranch)
		}
	}
	return opts, nil
}

func missingNewBase(flag string) error {
	if flag == "--base" {
		return fmt.Errorf("missing base after --base")
	}
	return fmt.Errorf("missing branch after --parent")
}

func setNewBase(opts *commitOptions, baseFlag *string, flag, value string) error {
	if opts.base == "" {
		opts.base = value
		*baseFlag = flag
		return nil
	}
	if *baseFlag != flag && opts.base == value {
		return nil
	}
	if *baseFlag != flag {
		return fmt.Errorf("graphene new %s and %s specify different branches: %q and %q", *baseFlag, flag, opts.base, value)
	}
	return fmt.Errorf("base branch specified more than once")
}

func shortCommitFlags(arg string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) <= 2 {
		return false
	}
	for _, r := range strings.TrimPrefix(arg, "-") {
		switch r {
		case 'a', 'u', 'm':
		default:
			return false
		}
	}
	return true
}

func parseShortCommitFlags(arg string, opts *commitOptions) (bool, error) {
	message := false
	for _, r := range strings.TrimPrefix(arg, "-") {
		switch r {
		case 'a':
			opts.stageAll = true
		case 'u':
			opts.stageUpdate = true
		case 'm':
			if message {
				return false, fmt.Errorf("message flag specified more than once")
			}
			message = true
		default:
			return false, fmt.Errorf("unsupported argument %q", arg)
		}
	}
	return message, nil
}

func (a *App) stageRequestedChanges(opts commitOptions) error {
	switch {
	case opts.stageAll:
		return a.git.Run("add", "-A")
	case opts.stageUpdate:
		return a.git.Run("add", "-u")
	default:
		return nil
	}
}

func unsupportedCommitArg(arg string, allowBranch bool) error {
	supported := "-m/--message, --no-edit, --no-verify, --gpg-sign, and --no-gpg-sign"
	if allowBranch {
		supported = "-a/--all, -u/--update, -b/--branch, --base/--parent, --reuse-current, " + supported
	} else {
		supported = "-a/--all, -u/--update, " + supported
	}
	return fmt.Errorf("unsupported argument %q; supported commit options are %s", arg, supported)
}

func parseForgetArgs(args []string) (forgetOptions, error) {
	var opts forgetOptions
	for _, arg := range args {
		switch arg {
		case "-f", "--force":
			opts.force = true
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unsupported argument %q; usage: graphene forget [--force] [branch]", arg)
			}
			if opts.branch != "" {
				return opts, fmt.Errorf("graphene forget accepts at most one branch")
			}
			opts.branch = arg
		}
	}
	return opts, nil
}

func parseDeleteArgs(args []string) (deleteOptions, error) {
	var opts deleteOptions
	for _, arg := range args {
		switch arg {
		case "-s", "--stack":
			opts.stack = true
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unsupported argument %q; usage: graphene delete [--stack] [branch]", arg)
			}
			if opts.branch != "" {
				return opts, fmt.Errorf("graphene delete accepts at most one branch")
			}
			opts.branch = arg
		}
	}
	return opts, nil
}

func parseTrackArgs(args []string) (string, string, error) {
	base := ""
	baseFlag := ""
	branch := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-p" || arg == "--parent" || arg == "--base":
			flag := canonicalTrackBaseFlag(arg)
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return "", "", fmt.Errorf("missing branch after %s", arg)
			}
			if err := setTrackBase(&base, &baseFlag, flag, args[i+1]); err != nil {
				return "", "", err
			}
			i++
		case strings.HasPrefix(arg, "--parent=") || strings.HasPrefix(arg, "--base="):
			flag, value, _ := strings.Cut(arg, "=")
			if value == "" {
				return "", "", fmt.Errorf("missing branch after %s", flag)
			}
			if err := setTrackBase(&base, &baseFlag, flag, value); err != nil {
				return "", "", err
			}
		case strings.HasPrefix(arg, "-"):
			return "", "", fmt.Errorf("unsupported argument %q; supported track options are -p/--parent and --base", arg)
		default:
			if branch != "" {
				return "", "", fmt.Errorf("graphene track accepts one branch")
			}
			branch = arg
		}
	}
	if base == "" {
		return "", "", fmt.Errorf("usage: graphene track (--parent|--base) <base> [branch]")
	}
	return base, branch, nil
}

func parseImportArgs(args []string) (string, error) {
	if len(args) != 1 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return "", fmt.Errorf("usage: graphene import <base>")
	}
	return args[0], nil
}

func canonicalTrackBaseFlag(flag string) string {
	if flag == "-p" {
		return "--parent"
	}
	return flag
}

func setTrackBase(base, baseFlag *string, flag, value string) error {
	if *base == "" {
		*base = value
		*baseFlag = flag
		return nil
	}
	if *baseFlag != flag && *base == value {
		return nil
	}
	if *baseFlag != flag {
		return fmt.Errorf("graphene track %s and %s specify different branches: %q and %q", *baseFlag, flag, *base, value)
	}
	return fmt.Errorf("graphene track accepts one %s branch", flag)
}

func parseSkillArgs(args []string) (skillOptions, error) {
	var opts skillOptions
	setDestination := func(name string) error {
		if opts.out != "" || opts.target != "" {
			return fmt.Errorf("graphene skill accepts one destination: --codex, --claude, or --out")
		}
		opts.target = name
		return nil
	}
	setOut := func(path string) error {
		if opts.out != "" || opts.target != "" {
			return fmt.Errorf("graphene skill accepts one destination: --codex, --claude, or --out")
		}
		opts.out = path
		return nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "--codex":
			if err := setDestination("codex"); err != nil {
				return skillOptions{}, err
			}
		case arg == "--claude":
			if err := setDestination("claude"); err != nil {
				return skillOptions{}, err
			}
		case arg == "--out":
			if i+1 >= len(args) || args[i+1] == "" || (strings.HasPrefix(args[i+1], "-") && args[i+1] != "-") {
				return skillOptions{}, fmt.Errorf("missing path after --out")
			}
			if err := setOut(args[i+1]); err != nil {
				return skillOptions{}, err
			}
			i++
		case strings.HasPrefix(arg, "--out="):
			out := strings.TrimPrefix(arg, "--out=")
			if out == "" {
				return skillOptions{}, fmt.Errorf("missing path after --out")
			}
			if err := setOut(out); err != nil {
				return skillOptions{}, err
			}
		default:
			return skillOptions{}, fmt.Errorf("unsupported argument %q; supported skill options are --codex, --claude, and --out", arg)
		}
	}
	return opts, nil
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
