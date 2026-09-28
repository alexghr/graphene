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
| `track` | Boundary established by its explicit local-base contract. | Record the established boundary when recording the branch. |
| `import <base>` | Historical fork with an untracked base's known local/upstream history; captured local tip for a tracked parent. | Import only the feature range, preserve the existing commits, and record each imported branch's boundary. The range may contain multiple commits; do not apply the legacy resolver's one-commit requirement to the whole import. |
| `amend` | Preserve the amended branch's boundary. | Update boundaries of descendants that are rebased onto the rewritten parent. |
| `continue`, `abort` | Frozen pending-operation data. | Continue without re-inferring boundaries; abort restores the original boundary metadata with the refs and topology. |

Graph, navigation, and PR base selection continue to use logical branch names.

## Base membership

Distinguish an invalid boundary from a valid boundary that is no longer contained in the base's history. The latter signals that the stack needs syncing; sync must still be able to replay the branch's changes using that boundary.

A local base can remain behind after sync when it is checked out in another worktree. This is not evidence that upstream history was rewritten. Membership checks must account for the known remote base history rather than demanding that every saved boundary be contained in the local base tip. Ordinary local commands must not fetch just to check membership.

Show a visible needs-sync warning/status indicator and direct users to `gn sync` when the historical boundary is no longer contained in the known base history. This indication does not require a CLI override and does not itself block operations whose saved boundaries remain valid. Sync remains available to repair the stack.

`graph` reports these warnings on stderr after rendering the graph, scoped to displayed branches. A tracked parent's local history is authoritative; an untracked base can contain the boundary in either its local history or its configured remote-tracking upstream. Checking the union avoids treating a stale local base as a rewrite. Graph does not fetch, infer missing legacy boundaries, or persist metadata. Invalid saved boundaries produce a separate diagnostic without suggesting that sync can repair them. Pending operations show their existing recovery status instead of inspecting temporarily inconsistent branch history.

## Sync destinations and local base refs

The fetched remote base commit is the sync destination even if the local base is ahead or diverged. Updating the local base ref is separate from rebasing the stack. Preserve local-only commits and respect other worktrees; do not require moving the local base in order to sync the stack.

Restack's default destination remains local. Change `restack --fetch <base>` to fetch the destination branch's configured upstream and rebase onto that fetched commit, instead of fetching and fast-forwarding the current stack branch. Update help, completions where applicable, documentation, and tests to reflect this intentional behavior change; do not add a separate `--fetch-base` option.

## Import range investigation

An additional report from `aztec-labs-eng/aztec-node` showed `import main` creating stack branches for dozens of upstream commits. The reporter observed equal `main` and `origin/main` IDs afterwards, but their values at import time are unknown. Import does not move either existing base ref. The previous implementation imported every commit in local `main..HEAD`. A temporary-repository reproduction confirmed that when `main` is behind cached `origin/main`, this includes upstream commits as well as the user's changes. When both base refs point to the feature commit's direct parent, the same reproduction imports only that feature commit. These establish the stale-base failure mode, not the cause of the reported incident; equal base IDs alone do not establish their relationship to the feature tip.

Follow-up diagnostics show `main`, `origin/main`, `refs/graphene/fetch/main`, and `HEAD^` all at `8cd490c7a76ea4cc055e5d987d1b07a8c62cac6c`, with `HEAD` at `c92c0c9cfe4e5960bf0b9c6c6e5db9d679627c5d` and exactly one commit in `main..HEAD`. Neither a stale visible base nor a newer private fetched base explains the checkout in that state. The reporter confirmed the long graph appeared after import and also used `forget`; the ref values at import time remain unresolved. Forget removes tracking, not Git branches or commits. Treat this as an import report, not merely a graph-display issue. In particular, importing a one-commit range after forgetting a stack must produce a single tracked branch even when old branch refs remain. Do not infer the user's installed version or local history from the remote repository alone.

The checkout is a linked worktree, so another worktree may have moved the shared base refs between import and the follow-up diagnostics. The regression uses a linked feature worktree with a stale local main, imports multiple feature commits, then forgets the stack and advances main from the other worktree. Reimport must track only the remaining feature commit while preserving leftover refs.

Import now reuses shared ancestry collection and selects the candidate leaving the fewest feature commits, rejecting a tie between distinct closest candidates. This differs from legacy migration, which requires exactly one branch commit. A tracked parent remains an explicit local-tip boundary. Import freezes the chosen boundary and feature tip for enumeration and history validation, then checks that created or reused branches still point to their planned commits. It does not reread a moving base to validate the completed import. Import creates branch refs and records tracking metadata and historical boundaries without rewriting commits.

## State transitions and migration

New branches and split parts record the pre-commit HEAD. Explicit `new --base` selects the logical parent without requiring it to point at HEAD. Track records the captured local parent used for validation; import records its frozen boundary followed by each imported commit. Amend retains or resolves the amended branch's boundary before committing. Removing a tracked branch removes its boundary record.

Snapshot rebases record their frozen destination after a successful step. Legacy `--update-refs` rebases persist boundary destinations before invoking Git: the old source boundary maps to the frozen destination, and intermediate tracked tips map to their rewritten refs. Continue resolves the same saved destinations; abort retains completed amend steps or restores the operation's original metadata according to its existing recovery contract. A rewritten boundary without a tracked ref cannot be mapped by this path and requires sync first. Sync/restack now resolve saved source boundaries; sync always selects the fetched upstream commit while ordinary restack selects the local destination tip. `restack --fetch` selects the destination upstream commit without moving its local branch. Sync fast-forwards a local base only when that preserves its history and the branch is available; ahead or diverged local bases stay intact.

Pending operations must preserve both the original boundaries for rollback and the planned or completed boundaries for continuation. Snapshot and legacy rewrite recovery paths must account for the metadata; see [recovery-snapshots.md](recovery-snapshots.md). Dry runs must not persist migrated or planned boundaries.

Existing state lacks boundaries. A shared migration resolver may use local and configured upstream ancestry, with reflog evidence where available. Validate the inferred boundary against the one-commit branch contract. If the evidence is ambiguous, report the affected branch instead of treating unrelated history as stack changes. Once recorded, the saved boundary takes precedence over inference.

`resolveBranchBoundary` takes captured local branch tips and returns a commit ID without fetching, changing refs, or writing state. A saved value must be a full commit ID naming an available commit that is an ancestor of the captured branch tip. An invalid saved value is an error, never a reason to infer a replacement. Commands separately validate how many commits they can operate on.

For legacy state, a tracked parent supplies its captured tip. An untracked root supplies common-ancestor and reflog fork-point candidates from its local branch and configured remote-tracking upstream. Only ancestor candidates leaving exactly one branch commit qualify; repeated evidence for the same commit is harmless, while missing or conflicting qualifying evidence is an error. Callers record the resolved boundary with a successful state transition, not during a read or dry run. Squash, split, sync, and restack now use this resolver. Sync validates surviving branches against their own source boundaries instead of counting from the fetched base, so rewritten upstream commits are not treated as feature commits. Successfully applied branches with saved boundaries are validated before deletion; legacy applied branches can be removed based on ancestry or patch equivalence without inventing a boundary. Both rebase planners compare the source with the destination even when the logical parent name is unchanged.

Squash captures local refs once and validates each selected branch against its resolved historical boundary. Every branch must contain exactly one commit, and each branch after the bottom must start at the preceding selected tip. A rewritten internal parent requires sync before squash, while a moved base below the selection does not. The combined commit uses the bottom boundary and the selected top tree. Split freezes its resolved boundary in pending `OriginalBase` and uses that commit for reset and split-part checks; older pending splits containing a branch name remain readable. Abort restores the original metadata rather than persisting inferred boundaries from the cancelled operation.

## Implementation sequence (completed)

1. Add boundary metadata, a shared resolver, and migration tests. Cover advanced or stale local bases, rewritten upstream history, and invalid or ambiguous saved boundaries.
2. Record and preserve boundaries through creation, tracking, import, state transformations, and recovery. Correct import's feature-range selection when the local base is stale. Test successful rewrites, conflicts, continuation, and abort.
3. Convert squash and split to use the same frozen boundary for validation and rewriting. Fold the current squash-specific inference into the shared resolver.
4. Convert sync and restack to use saved source boundaries and explicit destination rules. Test sync after an upstream rewrite and sync with the local base held in another worktree.
5. Add the agreed needs-sync indication and destination-fetch interface, then update help, completions, README, and the embedded skill.

## Test and documentation scope

Follow [the test layers](testing.md): most new coverage belongs in fast unit tests without mocks. Codec compatibility, state transforms, selection, and decisions based on already-resolved history belong there. Extend existing persistence and recovery integration tests where possible. Add a small real-Git test only when ancestry, ref updates, or rebase behavior is the contract being checked; do not repeat those scenarios across commands unnecessarily.

E2E coverage is limited to the user's daily new/send/amend/sync/sendf and local restack workflows. Split and squash are occasional operations, so their coverage belongs in unit tests and narrowly scoped Git integration tests, not new E2E workflows.

Keep design decisions, migration details, recovery invariants, and the inventory in committed internal documentation under `docs/`. README and embedded skill updates should explain when to run commands, practical examples, and observable outcomes or constraints. Explain internals there only when users need that information to make a decision. For squash, the useful usage note is that advancing the base branch does not require syncing before squashing; state fields and ancestry inference belong here instead.

## Follow-up backlog for 2026-09-28

- Completed: `send` and `sendf` push the selected branches with `git push --atomic`, including dry-runs. Rejections leave every selected remote branch unchanged. A server without atomic support fails without fallback. A single Git integration table covers non-fast-forward rejection, stale leases, and unsupported servers; existing daily E2Es cover successful sends and force-with-lease sends.

The final test/documentation review keeps candidate selection, boundary validation rules, map isolation, and squash adjacency in unit tests without mocks. Git integration fixtures cover ancestry, worktrees, persisted metadata, recovery, and push transactions. Existing daily E2Es assert saved boundaries after their workflows; no split or squash E2Es were added. User documentation describes command selection, outcomes, and recovery actions; persisted fields and inference/recovery algorithms remain in these internal notes.
