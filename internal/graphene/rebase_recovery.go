package graphene

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	recoveryReady    = "ready"
	recoveryApplying = "applying"
	recoveryConflict = "conflict"
	recoveryAborting = "aborting"
	recoveryDeleting = "deleting"
)

type recoveryState struct {
	Snapshot    string            `json:"snapshot"`
	Phase       string            `json:"phase"`
	Expected    map[string]string `json:"expected"`
	Base        string            `json:"base"`
	BaseHead    string            `json:"baseHead"`
	FastForward string            `json:"fastForward,omitempty"`
	Onto        string            `json:"onto,omitempty"`
	AcceptRisk  bool              `json:"acceptRisk,omitempty"`
}

func (a *App) loadRebaseSnapshot(p *Pending) (operationSnapshot, error) {
	r := p.Recovery
	snapshot, err := a.git.readSnapshot(r.Snapshot)
	if err != nil {
		return snapshot, err
	}
	worktree, err := a.git.WorktreeID()
	if err != nil {
		return snapshot, err
	}
	if worktree != snapshot.Worktree {
		return snapshot, fmt.Errorf("resume this operation from its original worktree %s", snapshot.Worktree)
	}
	_, ownsOriginal := r.Expected[p.Branch]
	if (p.Operation != "restack" && p.Operation != "sync") || p.Branch != snapshot.Branch || !ownsOriginal || p.ReturnBranch == "" || r.BaseHead == "" || snapshot.Refs[r.Base] == "" {
		return snapshot, fmt.Errorf("invalid pending rebase recovery state")
	}
	switch r.Phase {
	case recoveryReady, recoveryApplying, recoveryConflict, recoveryAborting, recoveryDeleting:
	default:
		return snapshot, fmt.Errorf("unknown recovery phase %q", r.Phase)
	}
	for _, op := range p.Queue {
		if r.Expected[op.Top] == "" || op.Upstream == "" || (op.Onto != r.Base && r.Expected[op.Onto] == "") {
			return snapshot, fmt.Errorf("invalid pending rebase queue")
		}
	}
	return snapshot, nil
}

func activeRebaseBranch(p *Pending) string {
	if p.Recovery.Phase == recoveryReady {
		return ""
	}
	if p.Recovery.FastForward != "" {
		if p.Operation == "sync" {
			return p.Recovery.Base
		}
		return p.Branch
	}
	if p.Recovery.Onto != "" && len(p.Queue) > 0 {
		return p.Queue[0].Top
	}
	return ""
}

func (a *App) checkRebaseRefs(p *Pending, snapshot operationSnapshot, abort bool) error {
	r := p.Recovery
	refs, err := a.git.snapshotBranchRefs()
	if err != nil {
		return err
	}
	current, err := a.git.Output("branch", "--show-current")
	if err != nil {
		return err
	}
	inRebase, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	active := activeRebaseBranch(p)
	baseExpected := snapshot.Refs[r.Base]
	if owned, ok := r.Expected[r.Base]; ok {
		baseExpected = owned
	}
	if !abort && refs[r.Base] != baseExpected {
		return fmt.Errorf("rebase target %q moved; use graphene abort and rerun", r.Base)
	}
	if _, owned := r.Expected[current]; current != "" && !owned {
		return fmt.Errorf("switch back to an operation branch before continuing or aborting")
	}
	for branch, expected := range r.Expected {
		// Abort explicitly rolls back the active branch even when interruption
		// made its final tip unknown. Other branches must match a saved tip.
		deleting := r.Phase == recoveryDeleting && slices.Contains(p.Branches, branch) && refs[branch] == ""
		if refs[branch] != expected && !(abort && (branch == active || deleting || refs[branch] == snapshot.Refs[branch])) {
			return fmt.Errorf("branch %q changed outside the operation; refusing to overwrite it", branch)
		}
		if branch == current || (inRebase && branch == active) {
			continue
		}
		if !abort || refs[branch] != snapshot.Refs[branch] || branch == snapshot.Branch {
			if err := a.git.requireSnapshotBranchAvailable(branch); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) requireSnapshotRebase(p *Pending) error {
	r := p.Recovery
	if r.FastForward != "" || r.Onto == "" || len(p.Queue) == 0 {
		return fmt.Errorf("Git rebase does not belong to this operation")
	}
	dir, err := a.git.GitPath("rebase-merge")
	if err != nil {
		return err
	}
	op := p.Queue[0]
	for name, want := range map[string]string{
		"head-name": "refs/heads/" + op.Top, "orig-head": r.Expected[op.Top], "onto": r.Onto,
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("cannot identify operation rebase: %w", err)
		}
		if strings.TrimSpace(string(data)) != want {
			return fmt.Errorf("Git rebase %s does not match this operation; refusing to change it", name)
		}
	}
	return nil
}

func (a *App) continueSnapshotRebases(state State) error {
	p := state.Pending
	snapshot, err := a.loadRebaseSnapshot(p)
	if err != nil {
		return err
	}
	switch p.Recovery.Phase {
	case recoveryApplying, recoveryDeleting:
		return fmt.Errorf("%s was interrupted during a Git step; use graphene abort and rerun", p.Operation)
	case recoveryAborting:
		return fmt.Errorf("rollback is in progress; rerun graphene abort")
	case recoveryConflict:
		inRebase, err := a.git.RebaseInProgress()
		if err != nil {
			return err
		}
		if !inRebase {
			return fmt.Errorf("the recorded rebase is no longer active; use graphene abort and rerun")
		}
		if err := a.checkRebaseRefs(p, snapshot, false); err != nil {
			return err
		}
		if err := a.requireSnapshotRebase(p); err != nil {
			return err
		}
		if err := a.preflightRebaseRepositories(p, snapshot.IndexTree, true); err != nil {
			return err
		}
		p.Recovery.Phase = recoveryApplying
		if err := a.git.WriteState(state); err != nil {
			return err
		}
		if err := a.recordRebaseResult(state, a.git.runWithoutSubmodules("rebase", "--continue")); err != nil {
			return err
		}
	}
	return a.runSnapshotRebases(state)
}

func (a *App) runSnapshotRebases(state State) error {
	p := state.Pending
	r := p.Recovery
	snapshot, err := a.loadRebaseSnapshot(p)
	if err != nil {
		return err
	}
	for {
		if err := a.checkRebaseRefs(p, snapshot, false); err != nil {
			return err
		}
		if err := a.git.requireNoGitOperation(); err != nil {
			return err
		}
		dirty, err := a.git.hasParentTrackedChanges()
		if err != nil {
			return err
		}
		if dirty {
			return fmt.Errorf("tracked changes would prevent %s; resolve them before continuing or use graphene abort", p.Operation)
		}
		if err := a.preflightRebaseRepositories(p, snapshot.IndexTree, false); err != nil {
			return err
		}
		if r.FastForward == "" && len(p.Queue) == 0 {
			if p.Operation == "sync" {
				return a.finishSnapshotSync(state)
			}
			if err := a.git.runWithoutSubmodules("switch", p.ReturnBranch); err != nil {
				return err
			}
			state.Stacks = p.NextStacks
			state.Pending = nil
			if err := a.git.WriteState(state); err != nil {
				return err
			}
			return a.git.removeSnapshot(r.Snapshot)
		}
		var args []string
		if r.FastForward != "" {
			branch := p.Branch
			if p.Operation == "sync" {
				branch = r.Base
			}
			current, err := a.git.CurrentBranch()
			if err != nil {
				return err
			}
			if current == branch {
				args = []string{"merge", "--ff-only", "--no-autostash", r.FastForward}
			} else {
				if err := a.git.requireSnapshotBranchAvailable(branch); err != nil {
					return err
				}
				args = []string{"update-ref", "refs/heads/" + branch, r.FastForward, r.Expected[branch]}
			}
		} else {
			op := p.Queue[0]
			r.Onto = r.Expected[op.Onto]
			if op.Onto == r.Base {
				r.Onto = r.BaseHead
			}
			// Move only this branch. Keep empty results so restack does not
			// silently remove a branch's commit; sync handles applied branches.
			args = []string{"rebase", "--merge", "--no-update-refs", "--no-autostash", "--reapply-cherry-picks", "--empty=keep", "--onto", r.Onto, op.Upstream, op.Top}
		}
		r.Phase = recoveryApplying
		if err := a.git.WriteState(state); err != nil {
			return err
		}
		if err := a.recordRebaseResult(state, a.git.runWithoutSubmodules(args...)); err != nil {
			return err
		}
	}
}

func (a *App) recordRebaseResult(state State, gitErr error) error {
	p := state.Pending
	r := p.Recovery
	if gitErr != nil {
		unmerged, err := a.git.Output("ls-files", "--unmerged", "--", ":/")
		if err == nil && unmerged != "" && a.requireSnapshotRebase(p) == nil {
			r.Phase = recoveryConflict
			if err := a.git.WriteState(state); err != nil {
				return err
			}
			return gitErr
		}
		return fmt.Errorf("Git step failed; use graphene abort and rerun: %w", gitErr)
	}
	if err := a.git.requireNoGitOperation(); err != nil {
		return err
	}
	branch := activeRebaseBranch(p)
	updated, err := a.git.Output("rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return err
	}
	if r.FastForward != "" {
		if updated != r.FastForward {
			return fmt.Errorf("fast-forward did not leave %q at its expected tip; use graphene abort", branch)
		}
		r.FastForward = ""
	} else {
		p.Queue = p.Queue[1:]
	}
	r.Expected[branch] = updated
	r.Onto = ""
	r.Phase = recoveryReady
	return a.git.WriteState(state)
}

func (a *App) abortSnapshotRebases(state State) error {
	p := state.Pending
	r := p.Recovery
	snapshot, err := a.loadRebaseSnapshot(p)
	if err != nil {
		return err
	}
	if err := a.checkRebaseRefs(p, snapshot, true); err != nil {
		return err
	}
	inRebase, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if inRebase {
		if err := a.requireSnapshotRebase(p); err != nil {
			return err
		}
	}
	active := activeRebaseBranch(p)
	trees := []string{snapshot.WorktreeTree}
	if inRebase {
		trees = append(trees, r.Expected[active])
	}
	if err := a.git.checkNestedRepositories(snapshot.IndexTree, trees...); err != nil {
		return err
	}
	if r.Phase == recoveryDeleting {
		refs, err := a.git.snapshotBranchRefs()
		if err != nil {
			return err
		}
		for _, branch := range p.Branches {
			r.Expected[branch] = refs[branch]
		}
	}
	if active == "" {
		r.FastForward = ""
		r.Onto = ""
	}
	r.Phase = recoveryAborting
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	if inRebase {
		if err := a.git.runWithoutSubmodules("rebase", "--abort"); err != nil {
			return err
		}
	}
	if active != "" {
		current, err := a.git.Output("rev-parse", "--verify", "refs/heads/"+active+"^{commit}")
		if err != nil {
			return err
		}
		r.Expected[active] = current
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	stacks, err := a.git.restoreSnapshot(r.Snapshot, r.Expected)
	if err != nil {
		return err
	}
	state.Stacks = stacks
	state.Pending = nil
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.git.removeSnapshot(r.Snapshot)
}
