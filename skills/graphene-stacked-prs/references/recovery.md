# Recovery details

Amend abort retains the amended commit and any completed descendant branch rewrites; successful abort output reports retained or restored changes and the final checkout.

Sync and restack save branch tips and stack metadata before changing local branches. Their `abort` restores the original branch tips, stack metadata and tracked checkout, including completed rebases, base fast-forwards and branches deleted by sync. Run recovery from the worktree where the operation started.

Untracked and ignored files are not backed up. Abort leaves unrelated local files, including edits made to them while paused, in place. Abort keeps the latest fetched remote-tracking refs.

Submodules are preserved as gitlinks in the saved commits. Sync, restack, continue and abort do not recursively update submodule checkouts or nested repositories/worktrees. Their contents, staging and local edits are outside recovery. Untracked nested repositories need no ignore entry. Dirty submodules or different checkout commits do not block sync/restack, but staged gitlink changes in the parent do. Updating submodule checkouts remains an explicit `git submodule update` action.

Before changing files, Graphene checks affected checkout and replay paths for collisions with untracked files, ignored files and nested repositories. Checks run again on continue and abort. Sync dry-run shows warnings. Move conflicting paths aside, or use `sync --accept-risk` / `restack --accept-risk <base>` only when the user accepts possible loss of local files or edits; abort cannot restore those contents. Acceptance persists for that pending operation's continue calls only and does not force Git to overwrite paths it refuses to change. This conservative check does not simulate Git merges. `--force` does not accept this risk, and `--accept-risk` does not bypass other safeguards or abort collision checks. A refused abort keeps its pending operation and recovery data.

If Graphene reports an interrupted Git step with an unknown result, use `graphene abort` and rerun the original command. Do not guess whether the step completed or bypass the pending operation with manual ref/state edits. If rollback itself is interrupted, retry `graphene abort`.

These snapshot guarantees apply to sync and restack; do not assume other commands have the same recovery behavior.
