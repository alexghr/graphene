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
	if !state.ContainsBranch(current) {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
	}
	nextState, _, ok := ReparentBranch(cloneStackState(state), current, opts.base)
	if !ok {
		return fmt.Errorf("cannot restack %q onto %q", current, opts.base)
	}
	dirty, err := a.git.hasParentTrackedChanges()
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
		Expected: map[string]string{}, AcceptRisk: opts.acceptRisk,
	}
	if opts.fetch {
		fetched, err := a.fetchUpstream(opts.base)
		if err != nil {
			return err
		}
		r.BaseHead = fetched.Updated
	}
	graph := newStackGraph(nextState)
	var queue []RebaseOp
	var visit func(string, bool) error
	visit = func(branch string, parentRewritten bool) error {
		if _, seen := r.Expected[branch]; seen {
			return fmt.Errorf("branch %q appears more than once in the affected stack", branch)
		}
		upstream, err := a.resolveBranchBoundary(state, branch, refs)
		if err != nil {
			return err
		}
		nextState.setBoundary(branch, upstream)
		r.Expected[branch] = refs[branch]
		destination := refs[graph.parent[branch]]
		if branch == current {
			destination = r.BaseHead
		}
		rewrite := parentRewritten || upstream != destination
		if rewrite {
			if branch != current {
				if err := a.git.requireSnapshotBranchAvailable(branch); err != nil {
					return err
				}
			}
			queue = append(queue, RebaseOp{Top: branch, Upstream: upstream, Onto: graph.parent[branch]})
		}
		for _, child := range graph.children[branch] {
			if err := visit(child, rewrite); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(current, false); err != nil {
		return err
	}
	if len(queue) == 0 {
		return a.git.WriteState(nextState)
	}
	p := &Pending{
		Operation: "restack", Branch: current,
		RewriteBefore: a.captureRewriteSources(state, refs),
		ReturnBranch:  current, Queue: queue, NextStacks: nextState.Stacks, NextBoundaries: nextState.Boundaries, Recovery: r,
	}
	if err := a.preflightRebaseRepositories(p, "HEAD", true); err != nil {
		return err
	}
	id, err := a.git.captureSnapshot()
	if err != nil {
		return err
	}
	r.Snapshot = id
	snapshot, err := a.git.readSnapshot(id)
	if err != nil {
		return err
	}
	if snapshot.Branch != current || snapshot.Refs[r.Base] != refs[r.Base] {
		return fmt.Errorf("branches changed while preparing restack; retry")
	}
	for branch, oid := range r.Expected {
		if snapshot.Refs[branch] != oid {
			return fmt.Errorf("branch %q changed while preparing restack; retry", branch)
		}
	}
	p.Worktree = snapshot.Worktree
	state.Pending = p
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runSnapshotRebases(state)
}
