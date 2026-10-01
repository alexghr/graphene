package graphene

import (
	"fmt"
	"slices"
	"strings"
)

func recoveryRefNames(refs map[string]string) []string {
	var branches []string
	for branch, ref := range refs {
		if branch != "" && ref != "" {
			branches = append(branches, branch)
		}
	}
	slices.Sort(branches)
	return branches
}

func (a *App) printRestoredAbort(operation string, refs map[string]string, removed []string, fetched bool) {
	fmt.Fprintf(a.stdout, "Aborted %s.\n", operation)
	if branches := recoveryRefNames(refs); len(branches) > 0 {
		fmt.Fprintf(a.stdout, "Restored original branch tips: %s.\n", strings.Join(branches, ", "))
	}
	fmt.Fprintln(a.stdout, "Restored stack layout and tracked checkout.")
	if len(removed) > 0 {
		fmt.Fprintf(a.stdout, "Removed split branches: %s.\n", strings.Join(removed, ", "))
	}
	if fetched {
		fmt.Fprintln(a.stdout, "Any fetched remote-tracking updates remain.")
	}
	a.printAbortCheckout()
}

func (a *App) printRetainedAbort(pending *Pending, state State, inProgress bool) {
	operation := "Git rebase"
	if pending != nil && pending.Operation != "" {
		operation = pending.Operation
	}
	fmt.Fprintf(a.stdout, "Aborted %s.\n", operation)
	if inProgress {
		fmt.Fprintln(a.stdout, "Undone: in-progress Git rebase.")
	}
	if pending != nil {
		if len(pending.Queue) > 0 {
			fmt.Fprintln(a.stdout, "Cancelled remaining queued rebases.")
		}
		if pending.Operation == "amend" {
			if pending.Branch != "" {
				fmt.Fprintf(a.stdout, "Retained amended commit on %s.\n", pending.Branch)
			}
			a.printRetainedDescendants(pending, state)
		} else {
			fmt.Fprintln(a.stdout, "Any completed branch changes remain.")
		}
	}
	a.printAbortCheckout()
}

func (a *App) printRetainedDescendants(pending *Pending, state State) {
	if len(pending.RewriteBefore) == 0 {
		fmt.Fprintln(a.stdout, "Any completed descendant rewrites remain; legacy pending state does not record which branches.")
		return
	}
	refs, err := a.git.snapshotBranchRefs()
	if err != nil {
		fmt.Fprintln(a.stdout, "Any completed descendant rewrites remain; their branch tips could not be read.")
		return
	}
	ancestors := BranchesThroughCurrent(state, pending.Branch)
	var retained []string
	unknown := false
	for _, branch := range BranchesThroughCurrentAndDescendants(state, pending.Branch) {
		if slices.Contains(ancestors, branch) {
			continue
		}
		before, ok := pending.RewriteBefore[branch]
		if !ok || before.Head == "" {
			unknown = true
			continue
		}
		if refs[branch] != "" && refs[branch] != before.Head {
			retained = append(retained, branch)
		}
	}
	if len(retained) > 0 {
		fmt.Fprintf(a.stdout, "Retained descendant branch changes: %s.\n", strings.Join(retained, ", "))
	} else if !unknown {
		fmt.Fprintln(a.stdout, "No completed descendant branch changes remain.")
	}
	if unknown {
		fmt.Fprintln(a.stdout, "Any other completed descendant rewrites remain; original tips are unavailable for some branches.")
	}
}

func (a *App) printAbortCheckout() {
	branch, err := a.git.Output("branch", "--show-current")
	if err == nil && branch != "" {
		fmt.Fprintf(a.stdout, "Checkout: %s.\n", branch)
		return
	}
	if err == nil {
		if head, err := a.git.Head(); err == nil {
			fmt.Fprintf(a.stdout, "Checkout: detached at %s.\n", head)
			return
		}
	}
	fmt.Fprintln(a.stdout, "Checkout could not be determined.")
}
