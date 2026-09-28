# Historical branch boundaries

This document records the intended command contract. It is a design for the shared boundary implementation; the current command inventory is in [stack-base-inventory.md](stack-base-inventory.md).

## Branch names and commit IDs

The logical parent is a branch name used for stack topology, navigation, and PR targets. The historical boundary is the commit immediately before the changes owned by a tracked branch. The destination is the commit onto which those changes will be rebased. A moving logical parent must not silently change the historical boundary.

Persist the historical boundary for each tracked branch in Graphene state. Resolve destinations to commit IDs before rewriting. A boundary must exist and be an ancestor of its branch; do not replace an invalid saved boundary with a freshly guessed merge-base.

## Commands

| Command | Source boundary | Destination and result |
| --- | --- | --- |
| `new` | Current `HEAD`, captured before committing. | Commit on that exact parent, create or reuse the branch, and record its logical parent and boundary. Tracking options must not silently move or rebase the commit. |
| `squash -c N` | Saved boundary of the bottom selected branch. | Combine N adjacent tracked branches, each containing exactly one commit, into one commit on that boundary. Preserve the bottom branch name and top tree, update topology, and restack affected descendants. No sync prerequisite merely because the base moved. |
| `split` | Saved boundary of the selected branch. | Keep the command. Reset to the frozen boundary, create the split parts, record their boundaries, and restack affected descendants. |
| `sync` | Saved boundaries of affected branches. | Fetch remote state and rebase surviving branches onto the fetched base commit and their rewritten parents. Retain existing merged-branch handling and recovery guarantees. A rewritten remote base must not prevent syncing when the saved source boundaries remain valid. |
| `restack <base>` | Saved boundaries of affected branches. | Rebase onto the explicitly selected local destination tip and rewritten parents. With `--fetch`, fetch the destination branch's upstream and use its fetched commit instead. |
| `track`, `import` | Boundaries established by their explicit local-base contracts. | Record the established boundaries when recording branches. |
| `amend` | Preserve the amended branch's boundary. | Update boundaries of descendants that are rebased onto the rewritten parent. |
| `continue`, `abort` | Frozen pending-operation data. | Continue without re-inferring boundaries; abort restores the original boundary metadata with the refs and topology. |

Graph, navigation, and PR base selection continue to use logical branch names.

## Base membership

Distinguish an invalid boundary from a valid boundary that is no longer contained in the base's history. The latter signals that the stack needs syncing; sync must still be able to replay the branch's changes using that boundary.

A local base can remain behind after sync when it is checked out in another worktree. This is not evidence that upstream history was rewritten. Membership checks must account for the known remote base history rather than demanding that every saved boundary be contained in the local base tip. Ordinary local commands must not fetch just to check membership.

Show a visible needs-sync warning/status indicator and direct users to `gn sync` when the historical boundary is no longer contained in the known base history. This indication does not require a CLI override and does not itself block operations whose saved boundaries remain valid. Sync remains available to repair the stack.

## Sync destinations and local base refs

The fetched remote base commit is the sync destination even if the local base is ahead or diverged. Updating the local base ref is separate from rebasing the stack. Preserve local-only commits and respect other worktrees; do not require moving the local base in order to sync the stack.

Restack's default destination remains local. Change `restack --fetch <base>` to fetch the destination branch's configured upstream and rebase onto that fetched commit, instead of fetching and fast-forwarding the current stack branch. Update help, completions where applicable, documentation, and tests to reflect this intentional behavior change; do not add a separate `--fetch-base` option.

## State transitions and migration

Record new boundaries at branch creation or import, and update them when a rewrite succeeds. Removing a tracked branch removes its boundary record. Reparenting, squash, split, and descendant rewrites must update topology and boundaries together.

Pending operations must preserve both the original boundaries for rollback and the planned or completed boundaries for continuation. Snapshot and legacy rewrite recovery paths must account for the metadata; see [recovery-snapshots.md](recovery-snapshots.md). Dry runs must not persist migrated or planned boundaries.

Existing state lacks boundaries. A shared migration resolver may use local and configured upstream ancestry, with reflog evidence where available. Validate the inferred boundary against the one-commit branch contract. If the evidence is ambiguous, report the affected branch instead of treating unrelated history as stack changes. Once recorded, the saved boundary takes precedence over inference.

## Implementation order

1. Add boundary metadata, a shared resolver, and migration tests. Cover advanced or stale local bases, rewritten upstream history, and invalid or ambiguous saved boundaries.
2. Record and preserve boundaries through creation, tracking, import, state transformations, and recovery. Test successful rewrites, conflicts, continuation, and abort.
3. Convert squash and split to use the same frozen boundary for validation and rewriting. Fold the current squash-specific inference into the shared resolver.
4. Convert sync and restack to use saved source boundaries and explicit destination rules. Test sync after an upstream rewrite and sync with the local base held in another worktree.
5. Add the agreed needs-sync indication and destination-fetch interface, then update help, completions, README, and the embedded skill.

## Test and documentation scope

Follow [the test layers](testing.md): most new coverage belongs in fast unit tests without mocks. Codec compatibility, state transforms, selection, and decisions based on already-resolved history belong there. Extend existing persistence and recovery integration tests where possible. Add a small real-Git test only when ancestry, ref updates, or rebase behavior is the contract being checked; do not repeat those scenarios across commands unnecessarily.

E2E coverage is limited to the user's daily new/send/amend/sync/sendf and local restack workflows. Split and squash are occasional operations, so their coverage belongs in unit tests and narrowly scoped Git integration tests, not new E2E workflows.

Keep design decisions, migration details, recovery invariants, and the inventory in committed internal documentation under `docs/`. README and embedded skill updates should explain when to run commands, practical examples, and observable outcomes or constraints. Explain internals there only when users need that information to make a decision. For squash, the useful usage note is that advancing the base branch does not require syncing before squashing; state fields and ancestry inference belong here instead.
