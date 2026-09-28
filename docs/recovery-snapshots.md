# Recovery snapshots

Restack and sync use these snapshot and rollback primitives. Other commands still use
their existing recovery paths.

A snapshot saves local branch tips and Graphene's stack metadata, including saved
historical boundaries. Restoring returns both topology and boundaries together;
the caller persists them before removing the snapshot. Older snapshots without
boundaries restore legacy metadata without inferring replacement boundaries. Operations
that change files can also save the original Git index and a worktree tree.
Capture uses a private index, leaving the user's staging untouched. Backup refs
keep the saved commits, trees and blobs reachable through Git garbage collection;
no snapshot commits are created.

The manifest lives in `<git-common-dir>/graphene/snapshots/<id>.json`, with backup
refs under `refs/graphene/snapshots/<id>/`. It uses the existing atomic JSON writer.
The recovery contract covers process interruptions, without promising recovery
from power loss or disk corruption.

## Integration contract

The caller holds the repository state lock through these steps:

1. Capture the snapshot before making changes.
2. Persist a pending operation that identifies the snapshot and the branches the
   operation may change, before starting the first mutation.
3. On completion, persist the final metadata and clear the pending operation,
   then remove the snapshot.
4. On abort, finish aborting any Git rebase owned by the operation, restore the
   snapshot, persist its returned stack metadata and clear the pending operation,
   then remove the snapshot.

Rollback checks the current worktree and branch tips before making changes. Only
branches named by the caller are restored; backing up a branch does not authorize
overwriting it. Ref updates use one Git transaction with expected old values.
Already restored tips are accepted so rollback can be retried after interruption.
Worktree and index restoration can also be retried while the snapshot remains.

The state lock coordinates Graphene processes, not arbitrary concurrent Git
commands or editors. The caller must establish which branch changes belong to
its pending operation; the snapshot alone cannot infer that after a crash.

## Scope

Pending operations retain separate original and planned boundary maps alongside
their stack topology. Split restores suffix metadata when reattaching the suffix;
squash and deletion prune metadata for removed branches. Abort restores the
original maps. Snapshot rebases record the frozen destination as the branch's new
boundary only after Git completes the step. Legacy queued rebases save boundary
destinations before starting Git, then resolve rewritten branch refs and persist
the resulting boundaries with queue advancement. A conflicted step leaves its
boundary metadata unchanged until continue succeeds.

Restack and sync persist a ready, applying, conflict or aborting phase. Each
fast-forward or rebase starts with an applying record and ends with a saved result.
An active rebase may continue only after a recorded conflict and when its Git
metadata matches the queued operation. Recorded successful steps can resume
without replaying them.
An applying record left after interruption requires abort and rerun. Abort may
restore that active branch from an unknown tip; other owned branches must match
their recorded or already-restored tips. This deliberately does not distinguish
an interrupted Git rewrite from a later manual edit to that same active branch.

Both commands rebase branches individually with automatic ref updates disabled.
They freeze the source boundaries and target commit, check branch tips between
steps, and retain commits that become empty. Completed branches and stack
metadata are restored together on abort. Backups are removed only after the
final or restored stack metadata has been persisted.

Sync and restack resolve source boundaries from saved metadata, validating that
each saved commit exists and is an ancestor of its branch. Legacy branches use
the shared resolver's local/upstream ancestry and reflog evidence. They freeze
the resolved commit in each queued rebase and record inferred boundaries only
with a successful state transition. Sync dry-run does not persist that inference.
A moved parent does not replace the saved source. Both planners inspect children
even when their parent needs no rewrite, so a child left behind by a direct Git
amend can be rebased from its own historical boundary.

Sync always uses the fetched base tip as its destination. It advances the local base only when that is a fast-forward and the branch is available in this worktree. Local-only commits are preserved when the local base is ahead or diverged. A base
checked out in another worktree stays untouched; affected branches rebase onto
the fetched commit. Applied branches remain until all rebases finish, then one
ref transaction deletes them after a persisted deleting phase. An interrupted
deletion requires abort and rerun; abort accepts either the saved tips or missing
refs for those planned deletions. A recorded deletion can continue to finalization.
Branch configuration is kept until the final stack metadata is saved, so abort
restores upstreams as well as deleted branches. Interrupted configuration cleanup
can leave unused entries after successful sync.

Restack uses the destination's captured local tip by default. With `--fetch`, it
fetches the destination's configured upstream and uses that commit without moving
the local destination. Recovery checks the local destination against its captured
local tip separately from the fetched rebase target. Existing pending restacks
with a current-branch fast-forward remain resumable.

Sync and restack with `--fetch` refresh the selected destination upstream's remote-tracking
ref as well as the private recovery ref. These updates also occur during sync
dry-runs and are not rolled back by abort.
Sync also refreshes existing upstream remote-tracking refs for surviving selected
stack branches, so subsequent force-with-lease pushes use the fetched tips. Remote-tracking
refs belonging only to unselected stacks remain unchanged.

Worktree snapshots preserve Git file content, staging and nonignored untracked
files. They are not filesystem archives: ignored files, timestamps and arbitrary
file permissions are not backed up. Unrelated untracked files are left in place;
directory/file collisions that could lose them block rollback.

Submodules are preserved as gitlinks: the parent index's recorded commit is saved,
not the submodule's checked-out commit. Nested repositories and linked worktrees
are excluded from file capture without requiring ignore entries. Their checkouts,
indexes and local files are outside the recovery boundary, including changes made
while an operation is paused. Snapshots do not back up objects inside nested
repositories.

Sync, restack, continue and abort disable recursive submodule updates. Dirty or
differently checked-out submodules do not block sync/restack, but staged parent
changes, including staged gitlink changes, still do. Committed gitlink changes are
rebased normally; additions do not initialize submodules, and removals leave
existing checkouts in place. Updating submodule checkouts remains an explicit
`git submodule update` operation.

Before changing files, recovery checks for nested repository collisions in the
current index, destination trees, and paths touched by commits to be replayed.
Git supplies the per-commit paths so temporary additions followed by removals are
included. A historical upstream boundary is not a destination; a branch already
contained in its rebase target needs no replay-path scan. This includes ignored
repositories and repositories created after capture. Gitlink-only changes at an
existing submodule path are allowed. The check is conservative and does not model
all paths Git's merge machinery might generate.

Sync and restack warn with the overlapping paths and refuse by default. Their
`--accept-risk` flag allows these forward-operation overwrites, which may destroy
nested files or local edits that abort cannot restore. Acceptance is recorded in
the pending recovery state and applies to subsequent `continue` calls for that
operation, not future operations. Sync dry-run prints warnings without requiring
acceptance. `--force` does not accept overwrite risk.

The flag does not bypass snapshot validation, dirty-parent checks, branch/ref
ownership checks, or rollback protection. Abort checks before invoking Git's
rebase abort as well as before restoring the snapshot, even after risk acceptance.
Move an obstructing repository aside and retry; the pending operation and snapshot
remain available. Existing checks for branches checked out in other worktrees
still apply, including worktrees nested inside this one.

Split indexes, unmerged entries, skip-worktree and assume-unchanged
flags, custom filters and working-tree encodings are rejected for worktree
snapshots, with paths identified where applicable. Filter and encoding checks
apply only to parent files being captured. The snapshot format is unchanged;
existing snapshots remain readable.

Branch configuration, reflogs and remote-tracking refs are outside this
snapshot format. Commands that change those must account for them separately.
Interrupted capture or cleanup can leave unused backup artifacts, but
does not move working branches.
