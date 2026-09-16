package graphene

import (
	"fmt"
	"strings"
)

type upstreamUpdate struct {
	Branch  string
	Remote  string
	Merge   string
	Old     string
	Updated string
}

func (u upstreamUpdate) UpstreamName() string {
	return u.Remote + "/" + strings.TrimPrefix(u.Merge, "refs/heads/")
}

// Only sync and an explicit restack --fetch call this helper. The private ref
// keeps the fetched commit reachable if a subsequent rebase stops for conflicts.
func (a *App) fetchUpstream(branch string) (upstreamUpdate, error) {
	remote, merge, err := a.git.Upstream(branch)
	if err != nil {
		return upstreamUpdate{}, err
	}
	if remote == "" || merge == "" {
		return upstreamUpdate{}, fmt.Errorf("branch %q has no upstream; set one before fetching", branch)
	}
	if !strings.HasPrefix(merge, "refs/heads/") {
		return upstreamUpdate{}, fmt.Errorf("upstream for %q must name a branch under refs/heads/", branch)
	}
	if err := a.git.OutputErr("check-ref-format", merge); err != nil {
		return upstreamUpdate{}, err
	}
	old, err := a.git.Output("rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return upstreamUpdate{}, err
	}
	ref := "refs/graphene/fetch/" + branch
	if err := a.git.Run("fetch", "--no-write-fetch-head", "--no-tags", "--no-prune", "--no-prune-tags", "--no-recurse-submodules", "--refmap=", "--", remote, "+"+merge+":"+ref); err != nil {
		return upstreamUpdate{}, err
	}
	updated, err := a.git.Output("rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return upstreamUpdate{}, err
	}
	return upstreamUpdate{
		Branch: branch, Remote: remote, Merge: merge, Old: old, Updated: updated,
	}, nil
}
