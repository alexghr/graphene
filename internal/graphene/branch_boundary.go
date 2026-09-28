package graphene

import (
	"fmt"
	"strings"
)

func (a *App) resolveBranchBoundary(state State, branch string, refs map[string]string) (string, error) {
	parent, ok := BaseBranch(state, branch)
	if !ok {
		return "", fmt.Errorf("branch %q is not in a graphene stack", branch)
	}
	head := refs[branch]
	if head == "" {
		return "", fmt.Errorf("missing local branch %q", branch)
	}
	if saved, exists := state.Boundaries[branch]; exists {
		if !isFullCommitID(saved) {
			return "", fmt.Errorf("invalid saved historical boundary for %q: expected a full commit ID", branch)
		}
		objectType, err := a.git.Output("cat-file", "-t", saved)
		if err != nil {
			return "", fmt.Errorf("saved historical boundary for %q is not an available commit: %w", branch, err)
		}
		if objectType != "commit" {
			return "", fmt.Errorf("saved historical boundary for %q names a %s, not a commit", branch, objectType)
		}
		ancestor, err := a.isAncestor(saved, head)
		if err != nil {
			return "", err
		}
		if !ancestor {
			return "", fmt.Errorf("saved historical boundary for %q is not an ancestor of the branch; restore its history before rewriting", branch)
		}
		return saved, nil
	}
	if refs[parent] == "" {
		return "", fmt.Errorf("missing local parent %q for %q", parent, branch)
	}
	candidates := []string{refs[parent]}
	if !state.ContainsBranch(parent) {
		var err error
		candidates, err = a.rootBoundaryCandidates(parent, refs[parent], head)
		if err != nil {
			return "", err
		}
	}
	evidence, err := a.inspectBoundaryCandidates(candidates, head)
	if err != nil {
		return "", err
	}
	return selectLegacyBoundary(branch, evidence)
}

func (a *App) inspectBoundaryCandidates(candidates []string, head string) ([]boundaryEvidence, error) {
	seen := map[string]bool{}
	var evidence []boundaryEvidence
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		ancestor, err := a.isAncestor(candidate, head)
		if err != nil {
			return nil, err
		}
		if !ancestor {
			continue
		}
		count, err := a.commitCount(candidate, head)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, boundaryEvidence{Commit: candidate, Commits: count})
	}
	return evidence, nil
}

func isFullCommitID(id string) bool {
	return (len(id) == 40 || len(id) == 64) && strings.Trim(id, "0123456789abcdef") == ""
}

type boundaryEvidence struct {
	Commit  string
	Commits int
}

func selectLegacyBoundary(branch string, candidates []boundaryEvidence) (string, error) {
	boundary := ""
	for _, candidate := range candidates {
		if candidate.Commits != 1 {
			continue
		}
		if boundary != "" && boundary != candidate.Commit {
			return "", fmt.Errorf("ambiguous historical boundary for %q; refusing to guess", branch)
		}
		boundary = candidate.Commit
	}
	if boundary == "" {
		return "", fmt.Errorf("cannot determine historical boundary for %q: parent history does not establish a one-commit branch", branch)
	}
	return boundary, nil
}

func (a *App) rootBoundaryCandidates(base, baseHead, branchHead string) ([]string, error) {
	references := []struct{ name, head string }{{"refs/heads/" + base, baseHead}}
	upstream, err := a.git.Output("for-each-ref", "--format=%(upstream)", "refs/heads/"+base)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(upstream, "refs/remotes/") {
		exists, err := a.refExists(upstream)
		if err != nil {
			return nil, err
		}
		if exists {
			head, err := a.git.Output("rev-parse", "--verify", upstream+"^{commit}")
			if err != nil {
				return nil, err
			}
			references = append(references, struct{ name, head string }{upstream, head})
		}
	}
	var candidates []string
	for _, ref := range references {
		common, err := a.git.Output("merge-base", "--all", ref.head, branchHead)
		if err != nil && !isGitExit(err, 1) {
			return nil, err
		}
		candidates = append(candidates, strings.Fields(common)...)
		// Reflogs can retain the original base after upstream history is rewritten.
		fork, err := a.git.Output("merge-base", "--fork-point", ref.name, branchHead)
		if err != nil && !isGitExit(err, 1) {
			return nil, err
		}
		candidates = append(candidates, strings.Fields(fork)...)
	}
	return candidates, nil
}
