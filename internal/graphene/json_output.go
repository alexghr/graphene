package graphene

import (
	"encoding/json"
	"fmt"
	"io"
)

const outputSchemaVersion = 1

type graphOutput struct {
	SchemaVersion int            `json:"schema_version"`
	CurrentBranch string         `json:"current_branch"`
	Branches      []branchOutput `json:"branches"`
	Pending       *pendingOutput `json:"pending"`
}

type branchOutput struct {
	Name    string `json:"name"`
	Parent  string `json:"parent"`
	Commit  string `json:"commit"`
	Tracked bool   `json:"tracked"`
}

type pendingOutput struct {
	Operation    string               `json:"operation"`
	Branch       string               `json:"branch"`
	ReturnBranch string               `json:"return_branch"`
	Phase        string               `json:"phase"`
	Rebases      []queuedRebaseOutput `json:"rebases"`
}

type queuedRebaseOutput struct {
	Branch   string `json:"branch"`
	Onto     string `json:"onto"`
	Upstream string `json:"upstream"`
}

type pushPlanOutput struct {
	SchemaVersion  int      `json:"schema_version"`
	CurrentBranch  string   `json:"current_branch"`
	Remote         string   `json:"remote"`
	Scope          string   `json:"scope"`
	Atomic         bool     `json:"atomic"`
	ForceWithLease bool     `json:"force_with_lease"`
	DryRun         bool     `json:"dry_run"`
	Branches       []string `json:"branches"`
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func publicPending(p *Pending) *pendingOutput {
	if p == nil {
		return nil
	}
	out := &pendingOutput{
		Operation: p.Operation, Branch: p.Branch, ReturnBranch: p.ReturnBranch,
		Rebases: make([]queuedRebaseOutput, 0, len(p.Queue)),
	}
	if p.Recovery != nil {
		out.Phase = p.Recovery.Phase
	}
	for _, op := range p.Queue {
		out.Rebases = append(out.Rebases, queuedRebaseOutput{Branch: op.Top, Onto: op.Onto, Upstream: op.Upstream})
	}
	return out
}

func (a *App) writeGraphJSON(state State, current string, stack bool) error {
	names := StateRefNames(state)
	if stack && (len(state.Stacks) != 0 || state.Pending != nil) {
		path, ok := VisibleStackPath(state, current)
		if !ok {
			return fmt.Errorf("branch %q is not in a graphene stack", current)
		}
		base, ok := BaseBranch(state, path[0])
		if !ok {
			return fmt.Errorf("branch %q is not in a graphene stack", current)
		}
		names = append([]string{base}, path...)
	}
	refs, err := a.git.localBranchCommits()
	if err != nil {
		return err
	}
	branches := make([]branchOutput, 0, len(names))
	for _, name := range names {
		parent, tracked := BaseBranch(state, name)
		branches = append(branches, branchOutput{Name: name, Parent: parent, Commit: refs[name], Tracked: tracked})
	}
	return writeJSON(a.stdout, graphOutput{
		SchemaVersion: outputSchemaVersion, CurrentBranch: current,
		Branches: branches, Pending: publicPending(state.Pending),
	})
}

func (a *App) writePushPlanJSON(plan pushPlan, current string) error {
	return writeJSON(a.stdout, pushPlanOutput{
		SchemaVersion: outputSchemaVersion, CurrentBranch: current, Remote: plan.Remote, Scope: plan.Scope,
		Atomic: true, ForceWithLease: plan.ForceWithLease, DryRun: plan.DryRun, Branches: plan.Branches,
	})
}
