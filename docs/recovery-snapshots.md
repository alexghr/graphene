# Recovery snapshots

Restack and sync use these snapshot and rollback primitives. Other commands still use
their existing recovery paths; their integration is tracked in the local plan.

A snapshot saves local branch tips and Graphene's stack metadata. Operations
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

Restack and sync persist a ready, applying, conflict or aborting phase. Each Git mutation
starts with an applying record and ends with a saved result. Only a recorded
conflict whose Git rebase metadata matches the queued operation may continue.
An applying record left after interruption requires abort and rerun. Abort may
restore that active branch from an unknown tip; other owned branches must match
their recorded or already-restored tips. This deliberately does not distinguish
an interrupted Git rewrite from a later manual edit to that same active branch.

Both commands rebase branches individually with automatic ref updates disabled.
It freezes the source boundaries and target commit, checks branch tips between
steps, and retains commits that become empty. Completed branches and stack
metadata are restored together on abort. Backups are removed only after the
final or restored stack metadata has been persisted.

Sync freezes the fetched base tip before advancing any local branch. A base
checked out in another worktree stays untouched; affected branches rebase onto
the fetched commit. Applied branches remain until all rebases finish, then one
ref transaction deletes them after a persisted deleting phase. An interrupted
deletion requires abort and rerun; abort accepts either the saved tips or missing
refs for those planned deletions. A recorded deletion can continue to finalization.
Branch configuration is kept until the final stack metadata is saved, so abort
restores upstreams as well as deleted branches. Interrupted configuration cleanup
can leave unused entries after successful sync.

Worktree snapshots preserve Git file content, staging and nonignored untracked
files. They are not filesystem archives: ignored files, timestamps and arbitrary
file permissions are not backed up. Unrelated untracked files are left in place;
directory/file collisions that could lose them block rollback.

Submodules, split indexes, unmerged entries, skip-worktree and assume-unchanged
flags, custom filters and working-tree encodings are rejected for worktree
snapshots. Branch configuration, reflogs and remote-tracking refs are outside this
snapshot format. Later command layers must account for any changes they make to
those. Interrupted capture or cleanup can leave unused backup artifacts, but
does not move working branches.
