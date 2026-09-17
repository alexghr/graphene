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

type syncFetchCache struct {
	Prefix  string
	Fetched map[string]map[string]bool
	Listed  map[string]map[string]bool
}

func (u upstreamUpdate) UpstreamName() string {
	return u.Remote + "/" + strings.TrimPrefix(u.Merge, "refs/heads/")
}

func (a *App) fetchUpstream(branch string) (upstreamUpdate, error) {
	return a.fetchUpstreamWithCache(branch, nil)
}

// Sync supplies a remote cache to reuse the fetch for upstream existence checks.
// The private base ref also keeps the fetched commit reachable during recovery.
func (a *App) fetchUpstreamWithCache(branch string, cache *syncFetchCache) (upstreamUpdate, error) {
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
	refspec := "+" + merge + ":" + ref
	if cache == nil {
		if err := a.git.Run("fetch", "--no-write-fetch-head", "--no-tags", "--no-prune", "--no-prune-tags", "--no-recurse-submodules", "--refmap=", "--", remote, refspec); err != nil {
			return upstreamUpdate{}, err
		}
	} else {
		refs, err := a.fetchRemoteBranches(remote, cache.Prefix, refspec)
		if err != nil {
			return upstreamUpdate{}, err
		}
		refs[merge] = true
		cache.Fetched[remote] = refs
	}
	updated, err := a.git.Output("rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return upstreamUpdate{}, err
	}
	return upstreamUpdate{
		Branch: branch, Remote: remote, Merge: merge, Old: old, Updated: updated,
	}, nil
}

// fetch a bunch of branch patterns in one go (e.g. fetch main and stack/*)
func (a *App) fetchRemoteBranches(remote, branchPrefix string, extraRefspecs ...string) (map[string]bool, error) {
	prefix := fmt.Sprintf("refs/graphene/remotes/%x/", remote)
	pattern := "refs/heads/" + branchPrefix + "*"
	if err := a.git.OutputErr("check-ref-format", "--refspec-pattern", pattern); err != nil {
		return nil, fmt.Errorf("invalid branch prefix %q", branchPrefix)
	}
	// Prune only our explicit private namespace, so deleted upstreams cannot
	// survive in the cache and user-configured refspecs cannot move local refs.
	args := []string{"fetch", "--no-write-fetch-head", "--no-tags", "--prune", "--no-prune-tags", "--no-recurse-submodules", "--refmap=", "--", remote, "+" + pattern + ":" + prefix + branchPrefix + "*"}
	args = append(args, extraRefspecs...)
	if err := a.git.Run(args...); err != nil {
		return nil, err
	}
	out, err := a.git.Output("for-each-ref", "--format=%(refname)", prefix+branchPrefix)
	if err != nil {
		return nil, err
	}
	refs := map[string]bool{}
	for ref := range strings.FieldsSeq(out) {
		refs["refs/heads/"+strings.TrimPrefix(ref, prefix)] = true
	}
	return refs, nil
}
