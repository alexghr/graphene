package graphene

import "fmt"

func (a *App) resolveImportBoundary(state State, base, baseHead, head string) (string, error) {
	if state.ContainsBranch(base) {
		ancestor, err := a.isAncestor(baseHead, head)
		if err != nil {
			return "", err
		}
		if !ancestor {
			return "", fmt.Errorf("tracked base branch %q is not an ancestor of HEAD", base)
		}
		return baseHead, nil
	}
	candidates, err := a.rootBoundaryCandidates(base, baseHead, head)
	if err != nil {
		return "", err
	}
	evidence, err := a.inspectBoundaryCandidates(candidates, head)
	if err != nil {
		return "", err
	}
	return selectImportBoundary(base, evidence)
}

func selectImportBoundary(base string, candidates []boundaryEvidence) (string, error) {
	boundary, distance, ambiguous := "", 0, false
	for _, candidate := range candidates {
		if boundary == "" || candidate.Commits < distance {
			boundary, distance, ambiguous = candidate.Commit, candidate.Commits, false
		} else if candidate.Commits == distance && candidate.Commit != boundary {
			ambiguous = true
		}
	}
	if boundary == "" {
		return "", fmt.Errorf("cannot find a historical boundary between %q and HEAD", base)
	}
	if ambiguous {
		return "", fmt.Errorf("ambiguous import boundary for %q; refusing to guess", base)
	}
	return boundary, nil
}
