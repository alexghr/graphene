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
	nextState, _, ok := ReparentBranch(State{Stacks: cloneStacks(state.Stacks)}, current, opts.base)
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
		upstream := refs[parent]
		if !state.ContainsBranch(parent) {
			var err error
			upstream, err = a.git.Output("merge-base", upstream, refs[branch])
			if err != nil {
				return err
			}
		} else {
			ancestor, err := a.isAncestor(upstream, refs[branch])
			if err != nil {
				return err
			}
			if !ancestor {
				return fmt.Errorf("parent %q is not an ancestor of %q; repair the stack before restacking", parent, branch)
			}
		}
		if branch == current && r.FastForward != "" {
			count, err := a.commitCount(upstream, r.FastForward)
			if err != nil {
				return err
			}
			if count > 1 {
				return fmt.Errorf("fetched branch %q contains %d commits on top of %q; Graphene expects one commit per stack branch. squash or drop the extra commits before restacking with --fetch", branch, count, parent)
			}
		}
		r.Expected[branch] = refs[branch]
		if branch == current && upstream == r.BaseHead && r.FastForward == "" {
			return nil
		}
		if branch != current || upstream != r.BaseHead {
			queue = append(queue, RebaseOp{Top: branch, Upstream: upstream, Onto: graph.parent[branch]})
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
	if len(queue) == 0 && r.FastForward == "" {
		return a.git.WriteState(nextState)
	}
	p := &Pending{
		Operation: "restack", Branch: current,
		ReturnBranch: current, Queue: queue, NextStacks: nextState.Stacks, Recovery: r,
	}
	if err := a.preflightRebaseRepositories(p, "HEAD", true); err != nil {
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
	p.Worktree = snapshot.Worktree
	state.Pending = p
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runSnapshotRebases(state)
}
