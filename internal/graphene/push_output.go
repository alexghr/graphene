package graphene

import (
	"fmt"
	"strings"
)

type pushPlan struct {
	Remote         string
	Branches       []string
	Scope          string
	ForceWithLease bool
	DryRun         bool
}

func pushScope(state State, current string, stack bool) string {
	if state.ContainsBranch(current) {
		if stack {
			return "current branch, tracked ancestors, and descendants of the current branch (--stack)"
		}
		return "current branch and tracked ancestors"
	}
	for _, tracked := range state.Stacks {
		if tracked.Base == current && len(tracked.Branches) > 0 {
			if stack {
				return "tracked descendants of the current base branch (--stack); base excluded"
			}
			return "current base branch only"
		}
	}
	return "current branch only (untracked)"
}

func (a *App) printPushPlan(plan pushPlan) error {
	mode := "atomic"
	if plan.ForceWithLease {
		mode += ", force-with-lease"
	}
	if plan.DryRun {
		mode += " (dry run)"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Push plan:\n  Remote: %s\n  Mode: %s\n  Scope: %s\n  Branches:\n", plan.Remote, mode, plan.Scope)
	for _, branch := range plan.Branches {
		fmt.Fprintf(&out, "    %s\n", branch)
	}
	out.WriteByte('\n')
	_, err := fmt.Fprint(a.stdout, out.String())
	return err
}
