package graphene

import "fmt"

func (a *App) planSnapshotSync(before, after State, selection syncSelection, refs map[string]string, baseHead string) ([]RebaseOp, error) {
	seen := map[string]bool{}
	for _, stack := range before.Stacks {
		for _, branch := range stack.Branches {
			if seen[branch] {
				return nil, fmt.Errorf("duplicate branch %q in stack state; repair the stack before syncing", branch)
			}
			seen[branch] = true
		}
	}
	oldGraph, graph := newStackGraph(before), newStackGraph(after)
	affected := map[string]bool{}
	for _, path := range selection.Paths {
		for _, branch := range syncPathAffectedBranches(path, oldGraph) {
			affected[branch] = true
		}
	}
	visited := map[string]bool{}
	var queue []RebaseOp
	var visit func(string, bool) error
	visit = func(branch string, parentRewritten bool) error {
		if !affected[branch] {
			return nil
		}
		// Git disallows cycles but we're iterating over Graphene state (which could be bugged)
		if visited[branch] {
			return fmt.Errorf("cycle in affected stack at %q", branch)
		}
		visited[branch] = true
		parent, nextParent := oldGraph.parent[branch], graph.parent[branch]
		upstream := refs[parent]
		if refs[branch] == "" || upstream == "" {
			return fmt.Errorf("missing local branch or parent for %q", branch)
		}
		if parent == selection.Base {
			based, err := a.isAncestor(baseHead, refs[branch])
			if err != nil {
				return err
			}
			if based {
				upstream = baseHead
			}
		}
		ancestor, err := a.isAncestor(upstream, refs[branch])
		if err != nil {
			return err
		}
		if !ancestor {
			return fmt.Errorf("parent %q is not an ancestor of %q; repair the stack before syncing", parent, branch)
		}
		if err := a.validateStackShapeFromBase(Stack{Branches: []string{branch}}, upstream, parent); err != nil {
			return err
		}
		rewrite := parentRewritten || parent != nextParent || (nextParent == selection.Base && upstream != baseHead)
		if rewrite {
			if branch != selection.Current {
				if err := a.git.requireSnapshotBranchAvailable(branch); err != nil {
					return err
				}
			}
			queue = append(queue, RebaseOp{Top: branch, Upstream: upstream, Onto: nextParent})
		}
		for _, child := range graph.children[branch] {
			if err := visit(child, rewrite); err != nil {
				return err
			}
		}
		return nil
	}
	for _, branch := range graph.children[selection.Base] {
		if err := visit(branch, false); err != nil {
			return nil, err
		}
	}
	for branch := range affected {
		if after.ContainsBranch(branch) && !visited[branch] {
			return nil, fmt.Errorf("affected branch %q is disconnected from sync base %q", branch, selection.Base)
		}
	}
	return queue, nil
}

func (a *App) startSnapshotSync(state State, p *Pending, fetched upstreamUpdate, refs map[string]string) error {
	base, current := fetched.Branch, p.Branch
	r := &recoveryState{Phase: recoveryReady, Base: base, BaseHead: fetched.Updated, Expected: map[string]string{current: refs[current]}}
	baseAvailable := base == current
	if !baseAvailable {
		checkedOut, err := a.git.BranchCheckedOut(base)
		if err != nil {
			return err
		}
		baseAvailable = !checkedOut
	}
	if baseAvailable {
		r.Expected[base] = fetched.Old
		if fetched.Old != fetched.Updated {
			r.FastForward = fetched.Updated
		}
	}
	for _, op := range p.Queue {
		r.Expected[op.Top] = refs[op.Top]
	}
	for _, branch := range p.Branches {
		r.Expected[branch] = refs[branch]
	}
	for branch, oid := range r.Expected {
		if oid == "" {
			return fmt.Errorf("missing local branch %q", branch)
		}
		if branch != current {
			if err := a.git.requireSnapshotBranchAvailable(branch); err != nil {
				return err
			}
		}
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
	if snapshot.Branch != current || snapshot.Refs[base] != fetched.Old {
		return fmt.Errorf("branches changed while preparing sync; retry")
	}
	for branch, oid := range r.Expected {
		if snapshot.Refs[branch] != oid {
			return fmt.Errorf("branch %q changed while preparing sync; retry", branch)
		}
	}
	p.Worktree = snapshot.Worktree
	p.Recovery = r
	if p.ReturnBranch == "" {
		p.ReturnBranch = base
		if !baseAvailable {
			p.ReturnRef = fetched.Updated
		}
	}
	state.Pending = p
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runSnapshotRebases(state)
}

func (a *App) finishSnapshotSync(state State) error {
	p := state.Pending
	r := p.Recovery
	if p.ReturnRef != "" {
		if err := a.git.Run("switch", "--detach", p.ReturnRef); err != nil {
			return err
		}
	} else if err := a.git.Run("switch", p.ReturnBranch); err != nil {
		return err
	}
	var edits []snapshotRefEdit
	for _, branch := range p.Branches {
		if r.Expected[branch] == "" {
			continue
		}
		if err := a.git.requireSnapshotBranchAvailable(branch); err != nil {
			return err
		}
		edits = append(edits, snapshotRefEdit{Ref: "refs/heads/" + branch, Old: r.Expected[branch]})
	}
	if len(edits) > 0 {
		r.Phase = recoveryDeleting
		if err := a.git.WriteState(state); err != nil {
			return err
		}
		if err := a.git.updateSnapshotRefs(edits); err != nil {
			return fmt.Errorf("branch deletion failed; use graphene abort and rerun: %w", err)
		}
		for _, branch := range p.Branches {
			r.Expected[branch] = ""
		}
		r.Phase = recoveryReady
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	state.Stacks = p.NextStacks
	state.Pending = nil
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	if err := a.git.removeSnapshot(r.Snapshot); err != nil {
		return err
	}
	// Keep configuration until rollback is no longer needed. Interrupted cleanup
	// can leave unused configuration, but restored branches retain their upstreams.
	if err := a.deleteBranchConfigs(p.Branches); err != nil {
		return fmt.Errorf("sync completed, but branch config cleanup failed: %w", err)
	}
	a.printSyncBaseChanges(p.BaseChanges)
	return nil
}
