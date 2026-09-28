package graphene

import (
	"fmt"
	"strings"
)

func (a *App) writeBoundaryWarnings(state State, branches []string) error {
	if len(state.Boundaries) == 0 {
		return nil
	}
	refs, err := a.git.snapshotBranchRefs()
	if err != nil {
		return err
	}
	for _, branch := range branches {
		if _, saved := state.Boundaries[branch]; !saved || !state.ContainsBranch(branch) {
			continue
		}
		boundary, err := a.resolveBranchBoundary(state, branch, refs)
		if err != nil {
			fmt.Fprintf(a.stderr, "warning: %s\n", err)
			continue
		}
		parent, _ := BaseBranch(state, branch)
		if refs[parent] == "" {
			fmt.Fprintf(a.stderr, "warning: parent branch %q for %q is missing\n", parent, branch)
			continue
		}
		contained, err := a.isAncestor(boundary, refs[parent])
		if err != nil {
			return err
		}
		if !contained && !state.ContainsBranch(parent) {
			upstream, err := a.git.Output("for-each-ref", "--format=%(upstream)", "refs/heads/"+parent)
			if err != nil {
				return err
			}
			if strings.HasPrefix(upstream, "refs/remotes/") {
				exists, err := a.refExists(upstream)
				if err != nil {
					return err
				}
				if exists {
					contained, err = a.isAncestor(boundary, upstream)
					if err != nil {
						return err
					}
				}
			}
		}
		if !contained {
			fmt.Fprintf(a.stderr, "warning: %q needs sync: its historical parent is no longer in the known history of %q; switch to %q and run gn sync\n", branch, parent, branch)
		}
	}
	return nil
}
