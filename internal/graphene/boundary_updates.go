package graphene

import (
	"fmt"
	"strings"
)

// Legacy rebases use --update-refs, so a single queued step can move several
// branches. Save boundary destinations before Git starts, including conflicts.
func (a *App) prepareBoundaryUpdates(state *State) error {
	p := state.Pending
	if p.BoundaryUpdates != nil {
		return nil
	}
	op := p.Queue[0]
	refs, err := a.git.snapshotBranchRefs()
	if err != nil {
		return err
	}
	onto, err := a.git.Output("rev-parse", "--verify", op.Onto+"^{commit}")
	if err != nil {
		return err
	}
	commits, err := a.git.Output("rev-list", op.Upstream+".."+refs[op.Top])
	if err != nil {
		return err
	}
	inRange := map[string]bool{}
	for commit := range strings.FieldsSeq(commits) {
		inRange[commit] = true
	}
	planned := *state
	if p.NextStacks != nil {
		planned = State{Stacks: p.NextStacks, Boundaries: p.NextBoundaries}
	}
	boundaries := map[string]string{}
	trackedRefs := map[string]string{}
	for _, branch := range StateRefNames(planned) {
		if !planned.ContainsBranch(branch) || !inRange[refs[branch]] {
			continue
		}
		trackedRefs[branch] = refs[branch]
		var boundary string
		if _, saved := planned.Boundaries[branch]; saved {
			boundary, err = a.resolveBranchBoundary(planned, branch, refs)
			if err != nil {
				return err
			}
		} else {
			parent, _ := BaseBranch(planned, branch)
			boundary = refs[parent]
			// The direct parent may already have been rewritten by an earlier step.
			if parent == op.Onto {
				boundary = op.Upstream
			}
			count, err := a.commitCount(boundary, refs[branch])
			if err != nil {
				return err
			}
			if count != 1 {
				continue
			}
		}
		boundaries[branch] = boundary
	}
	updates, err := planBoundaryUpdates(boundaries, trackedRefs, inRange, op.Upstream, onto)
	if err != nil {
		return err
	}
	p.BoundaryUpdates = updates
	return a.git.WriteState(*state)
}

func planBoundaryUpdates(boundaries, refs map[string]string, inRange map[string]bool, upstream, onto string) (map[string]string, error) {
	replacements := map[string]string{upstream: onto}
	for branch, commit := range refs {
		if inRange[commit] {
			replacements[commit] = "refs/heads/" + branch
		}
	}
	updates := map[string]string{}
	for branch, boundary := range boundaries {
		if replacement, ok := replacements[boundary]; ok {
			updates[branch] = replacement
		} else if inRange[boundary] {
			return nil, fmt.Errorf("historical boundary for %q is inside the rebase range without a tracked ref; use graphene sync before retrying", branch)
		} else {
			updates[branch] = boundary
		}
	}
	return updates, nil
}

func (a *App) completePendingRebase(state *State) error {
	p := state.Pending
	updates := map[string]string{}
	for branch, ref := range p.BoundaryUpdates {
		commit, err := a.git.Output("rev-parse", "--verify", ref+"^{commit}")
		if err != nil {
			return err
		}
		ancestor, err := a.isAncestor(commit, "refs/heads/"+branch)
		if err != nil {
			return err
		}
		if !ancestor {
			return fmt.Errorf("rewritten boundary for %q is not an ancestor of the branch; use graphene abort", branch)
		}
		updates[branch] = commit
	}
	for branch, commit := range updates {
		if state.ContainsBranch(branch) {
			state.setBoundary(branch, commit)
		}
		if p.NextStacks != nil {
			if p.NextBoundaries == nil {
				p.NextBoundaries = map[string]string{}
			}
			p.NextBoundaries[branch] = commit
		}
	}
	p.BoundaryUpdates = nil
	p.Queue = p.Queue[1:]
	return a.git.WriteState(*state)
}
