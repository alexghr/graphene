# Sync and restack details

Use `graphene sync -a` or `graphene sync --all` when intentionally syncing every descendant from the current untracked base branch. When a preview is useful, use `graphene sync --dry-run` or `graphene sync -a --dry-run` to fetch and preview deletions, retargets, and rebases without moving local branches or changing Graphene state. Dry-run still contacts the remote, downloads objects, and updates private fetch refs and remote-tracking refs for the base and selected stack upstreams. These refreshed upstream refs provide the leases for subsequent `graphene sendf` pushes. Sync fetches the base upstream and branches under the configured `branchPrefix` (default `stack/*`) once per needed remote, reusing the cache across selected stacks. An empty prefix matches all branches. Upstreams outside the prefix still work, but require an additional remote listing, cached per remote for that sync, and individual fetches to refresh selected upstreams. Other remote-tracking refs and tags stay unchanged; fetching other stacks' branches into the private cache does not move their local branch pointers.

If sync reports that configured upstreams for unapplied branches no longer exist, run `graphene sync --dry-run --assume-merged` and verify every listed PR was merged before using the flag for a real sync. A missing remote ref alone is not merge evidence, so do not use this flag only to get past the safety check.

## Restack

Restack uses the named local branch's tip by default. Use `graphene restack --fetch <local-base>` to fetch that destination's configured upstream and rebase onto the fetched commit. The destination must have an upstream. Its local branch stays in place, even when ahead or diverged; the current stack branch's upstream is not fetched.

Only `sync` and `restack --fetch` fetch Git objects. `track` records an existing branch using local refs and does not advance its parent. Use `sync` to update a stack against its fetched upstream base. Local-only base commits are preserved but are not included in the synced stack; a base checked out in another worktree stays in place.

