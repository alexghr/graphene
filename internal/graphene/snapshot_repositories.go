package graphene

import (
	"fmt"
	"slices"
	"strings"
)

func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func nestedRepositoryCollision(repository, path string) error {
	return fmt.Errorf("nested repository %q would be overwritten by parent path %q; move it aside and retry", repository, path)
}

type nestedRepositoryRisk struct {
	repository string
	path       string
}

func sortedRepositoryRisks(risks map[nestedRepositoryRisk]bool) []nestedRepositoryRisk {
	result := make([]nestedRepositoryRisk, 0, len(risks))
	for risk := range risks {
		result = append(result, risk)
	}
	slices.SortFunc(result, func(a, b nestedRepositoryRisk) int {
		if order := strings.Compare(a.repository, b.repository); order != 0 {
			return order
		}
		return strings.Compare(a.path, b.path)
	})
	return result
}

func (g Git) hasParentTrackedChanges() (bool, error) {
	for _, args := range [][]string{
		{"diff", "--cached", "--quiet", "--no-ext-diff", "--ignore-submodules=none", "--", ":/"},
		{"diff", "--quiet", "--no-ext-diff", "--ignore-submodules=all", "--", ":/"},
	} {
		if err := g.OutputErr(args...); err != nil {
			if isGitExit(err, 1) {
				return true, nil
			}
			return false, err
		}
	}
	return false, nil
}

func (g Git) runWithoutSubmodules(args ...string) error {
	return g.Run(append([]string{"-c", "submodule.recurse=false"}, args...)...)
}

func (a *App) rebaseRepositoryRisks(p *Pending, savedTree string) ([]nestedRepositoryRisk, error) {
	r := p.Recovery
	trees := []string{r.BaseHead, r.FastForward, r.Onto}
	if p.ReturnRef != "" {
		trees = append(trees, p.ReturnRef)
	} else if p.ReturnBranch != "" {
		trees = append(trees, "refs/heads/"+p.ReturnBranch)
	}
	paths, err := a.git.changedCheckoutPaths(trees...)
	if err != nil {
		return nil, err
	}
	for _, op := range p.Queue {
		top, onto := r.Expected[op.Top], r.Expected[op.Onto]
		if p.Operation == "restack" && op.Top == p.Branch && r.FastForward != "" {
			top = r.FastForward
		}
		if op.Onto == r.Base {
			onto = r.BaseHead
		}
		checkout, err := a.git.changedCheckoutPaths(top, onto)
		if err != nil {
			return nil, err
		}
		for path, gitlink := range checkout {
			previous, seen := paths[path]
			paths[path] = gitlink && (!seen || previous)
		}
		// Git can fast-forward to a target already containing the whole branch.
		// The historical upstream tree is not a checkout destination.
		contained, err := a.isAncestor(top, onto)
		if err != nil {
			return nil, err
		}
		if contained {
			continue
		}
		if err := a.git.replayCheckoutPaths(paths, op.Upstream, top); err != nil {
			return nil, err
		}
	}
	return a.git.checkoutPathRisks(paths, savedTree)
}

func (a *App) warnNestedRepositoryRisks(risks []nestedRepositoryRisk) {
	if len(risks) == 0 {
		return
	}
	for _, risk := range risks {
		if risk.repository == "" {
			fmt.Fprintf(a.stderr, "warning: parent changes may overwrite untracked or ignored path %q\n", risk.path)
			continue
		}
		fmt.Fprintf(a.stderr, "warning: parent changes touch %q inside or overlapping nested repository %q\n", risk.path, risk.repository)
	}
	fmt.Fprintln(a.stderr, "Local files or edits there may be overwritten or deleted. Graphene's abort cannot restore untracked files or nested repository contents.")
}

func (a *App) preflightRebaseRepositories(p *Pending, savedTree string, reportAccepted bool) error {
	risks, err := a.rebaseRepositoryRisks(p, savedTree)
	if err != nil || len(risks) == 0 {
		return err
	}
	if !p.Recovery.AcceptRisk || reportAccepted {
		a.warnNestedRepositoryRisks(risks)
	}
	if !p.Recovery.AcceptRisk {
		return fmt.Errorf("move the conflicting paths aside, or rerun %s with --accept-risk to allow possible overwrites; for a pending operation, abort before rerunning", p.Operation)
	}
	return nil
}
