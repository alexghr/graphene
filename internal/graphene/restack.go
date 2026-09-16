package graphene

import "fmt"

func (a *App) restack(args []string) error {
	opts, err := parseRestackArgs(args)
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
		return fmt.Errorf("pending operation exists; use graphene continue or graphene abort")
	}
	if err := a.git.requireNoGitOperation(); err != nil {
		return err
	}
	if err := a.validateRestackBase(opts.base); err != nil {
		return err
	}
	oldBase, ok := BaseBranch(state, current)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
	}
	nextState, _, ok := ReparentBranch(State{Stacks: cloneStacks(state.Stacks)}, current, opts.base)
	if !ok {
		return fmt.Errorf("cannot restack %q onto %q", current, opts.base)
	}
	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("tracked changes would prevent restack; stash or commit them before graphene restack")
	}
	refs, err := a.git.snapshotBranchRefs()
	if err != nil {
		return err
	}
	r := &recoveryState{
		Phase: recoveryReady, Base: opts.base, BaseHead: refs[opts.base],
		Expected: map[string]string{},
	}
	if opts.fetch {
		fetched, err := a.fetchUpstream(current)
		if err != nil {
			return err
		}
		advance, err := a.currentBranchNeedsFastForward(current, refs[current], fetched.Updated)
		if err != nil {
			return err
		}
		if advance {
			r.FastForward = fetched.Updated
		}
	}
	if refs[oldBase] == r.BaseHead && r.FastForward == "" {
		return a.git.WriteState(nextState)
	}

	graph := newStackGraph(nextState)
	var queue []RebaseOp
	var visit func(string) error
	visit = func(branch string) error {
		if _, seen := r.Expected[branch]; seen {
			return fmt.Errorf("branch %q appears more than once in the affected stack", branch)
		}
		if branch != current {
			if err := a.git.requireSnapshotBranchAvailable(branch); err != nil {
				return err
			}
		}
		parent, _ := BaseBranch(state, branch)
		if refs[branch] == "" || refs[parent] == "" {
			return fmt.Errorf("missing local branch or parent for %q", branch)
		}
		ancestor, err := a.isAncestor(refs[parent], refs[branch])
		if err != nil {
			return err
		}
		if !ancestor {
			return fmt.Errorf("parent %q is not an ancestor of %q; repair the stack before restacking", parent, branch)
		}
		r.Expected[branch] = refs[branch]
		if branch != current || refs[oldBase] != r.BaseHead {
			queue = append(queue, RebaseOp{Top: branch, Upstream: refs[parent], Onto: graph.parent[branch]})
		}
		for _, child := range graph.children[branch] {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(current); err != nil {
		return err
	}
	id, err := a.git.captureSnapshot(true)
	if err != nil {
		return err
	}
	r.Snapshot = id
	snapshot, err := a.git.readSnapshot(id)
	if err != nil {
		return err
	}
	if snapshot.Branch != current || snapshot.Refs[r.Base] != r.BaseHead {
		return fmt.Errorf("branches changed while preparing restack; retry")
	}
	for branch, oid := range r.Expected {
		if snapshot.Refs[branch] != oid {
			return fmt.Errorf("branch %q changed while preparing restack; retry", branch)
		}
	}
	state.Pending = &Pending{
		Operation: "restack", Worktree: snapshot.Worktree, Branch: current,
		ReturnBranch: current, Queue: queue, NextStacks: nextState.Stacks, Recovery: r,
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runSnapshotRestack(state)
}
