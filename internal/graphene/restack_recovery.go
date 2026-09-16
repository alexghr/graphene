package graphene

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	recoveryReady    = "ready"
	recoveryApplying = "applying"
	recoveryConflict = "conflict"
	recoveryAborting = "aborting"
)

type recoveryState struct {
	Snapshot    string            `json:"snapshot"`
	Phase       string            `json:"phase"`
	Expected    map[string]string `json:"expected"`
	Base        string            `json:"base"`
	BaseHead    string            `json:"baseHead"`
	FastForward string            `json:"fastForward,omitempty"`
	Onto        string            `json:"onto,omitempty"`
}

func (a *App) loadRestackSnapshot(p *Pending) (operationSnapshot, error) {
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
	if p.Operation != "restack" || p.Branch != snapshot.Branch || p.ReturnBranch != snapshot.Branch || r.Expected[p.Branch] == "" || r.BaseHead == "" || snapshot.Refs[r.Base] != r.BaseHead {
		return snapshot, fmt.Errorf("invalid pending restack recovery state")
	}
	switch r.Phase {
	case recoveryReady, recoveryApplying, recoveryConflict, recoveryAborting:
	default:
		return snapshot, fmt.Errorf("unknown recovery phase %q", r.Phase)
	}
	for _, op := range p.Queue {
		if r.Expected[op.Top] == "" || op.Upstream == "" || (op.Onto != r.Base && r.Expected[op.Onto] == "") {
			return snapshot, fmt.Errorf("invalid pending restack queue")
		}
	}
	return snapshot, nil
}

func activeRestackBranch(p *Pending) string {
	if p.Recovery.Phase == recoveryReady {
		return ""
	}
	if p.Recovery.FastForward != "" {
		return p.Branch
	}
	if p.Recovery.Onto != "" && len(p.Queue) > 0 {
		return p.Queue[0].Top
	}
	return ""
}

func (a *App) checkRestackRefs(p *Pending, snapshot operationSnapshot, abort bool) error {
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
	active := activeRestackBranch(p)
	if !abort && refs[r.Base] != r.BaseHead {
		return fmt.Errorf("restack target %q moved; use graphene abort and rerun", r.Base)
	}
	if current != "" && r.Expected[current] == "" {
		return fmt.Errorf("switch back to an operation branch before continuing or aborting restack")
	}
	for branch, expected := range r.Expected {
		if refs[branch] == "" {
			return fmt.Errorf("operation branch %q is missing", branch)
		}
		// Abort explicitly rolls back the active branch even when interruption
		// made its final tip unknown. Other branches must match a saved tip.
		if refs[branch] != expected && !(abort && (branch == active || refs[branch] == snapshot.Refs[branch])) {
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

func (a *App) requireRestackRebase(p *Pending) error {
	r := p.Recovery
	if r.FastForward != "" || r.Onto == "" || len(p.Queue) == 0 {
		return fmt.Errorf("Git rebase does not belong to this restack")
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
			return fmt.Errorf("cannot identify restack rebase: %w", err)
		}
		if strings.TrimSpace(string(data)) != want {
			return fmt.Errorf("Git rebase %s does not match this restack; refusing to change it", name)
		}
	}
	return nil
}

func (a *App) continueSnapshotRestack(state State) error {
	p := state.Pending
	snapshot, err := a.loadRestackSnapshot(p)
	if err != nil {
		return err
	}
	switch p.Recovery.Phase {
	case recoveryApplying:
		return fmt.Errorf("restack was interrupted during a Git step; use graphene abort and rerun")
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
		if err := a.checkRestackRefs(p, snapshot, false); err != nil {
			return err
		}
		if err := a.requireRestackRebase(p); err != nil {
			return err
		}
		p.Recovery.Phase = recoveryApplying
		if err := a.git.WriteState(state); err != nil {
			return err
		}
		if err := a.recordRestackResult(state, a.git.Run("rebase", "--continue")); err != nil {
			return err
		}
	}
	return a.runSnapshotRestack(state)
}

func (a *App) runSnapshotRestack(state State) error {
	p := state.Pending
	r := p.Recovery
	snapshot, err := a.loadRestackSnapshot(p)
	if err != nil {
		return err
	}
	for {
		if err := a.checkRestackRefs(p, snapshot, false); err != nil {
			return err
		}
		if err := a.git.requireNoGitOperation(); err != nil {
			return err
		}
		dirty, err := a.git.HasTrackedChanges()
		if err != nil {
			return err
		}
		if dirty {
			return fmt.Errorf("tracked changes would prevent restack; resolve them before continuing or use graphene abort")
		}
		if r.FastForward == "" && len(p.Queue) == 0 {
			if err := a.git.Run("switch", p.ReturnBranch); err != nil {
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
			current, err := a.git.CurrentBranch()
			if err != nil {
				return err
			}
			if current != p.Branch {
				return fmt.Errorf("switch to %q before continuing its fast-forward", p.Branch)
			}
			args = []string{"merge", "--ff-only", "--no-autostash", r.FastForward}
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
		if err := a.recordRestackResult(state, a.git.Run(args...)); err != nil {
			return err
		}
	}
}

func (a *App) recordRestackResult(state State, gitErr error) error {
	p := state.Pending
	r := p.Recovery
	if gitErr != nil {
		unmerged, err := a.git.Output("ls-files", "--unmerged", "--", ":/")
		if err == nil && unmerged != "" && a.requireRestackRebase(p) == nil {
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
	branch := activeRestackBranch(p)
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

func (a *App) abortSnapshotRestack(state State) error {
	p := state.Pending
	r := p.Recovery
	snapshot, err := a.loadRestackSnapshot(p)
	if err != nil {
		return err
	}
	if err := a.checkRestackRefs(p, snapshot, true); err != nil {
		return err
	}
	inRebase, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if inRebase {
		if err := a.requireRestackRebase(p); err != nil {
			return err
		}
	}
	active := activeRestackBranch(p)
	if active == "" {
		r.FastForward = ""
		r.Onto = ""
	}
	r.Phase = recoveryAborting
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	if inRebase {
		if err := a.git.Run("rebase", "--abort"); err != nil {
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
