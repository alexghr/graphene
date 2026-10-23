---
name: graphene-stacked-prs
description: "Manage stacked PR branches with Graphene: create, navigate, rewrite, sync, and publish stacks."
---

# Graphene Stacked PRs

Use `graphene` for stacked PR work (`gn` is an alias). If missing from `PATH` and installation is allowed, follow the [README install instructions](https://github.com/alexghr/graphene#install).

## Working rules

- Keep one logical commit per tracked branch. Use `graphene new` and `graphene amend` instead of Git commit commands.
- Run routine commands directly within the user's scope. Dry-runs are optional: use them for concrete uncertainty about push selection, remote configuration, sync deletions or retargets, or a requested preview. Once resolved, proceed without another preview unless relevant state changes.
- If an operation is rejected, resolve its diagnostic within scope. Do not bypass lease failures, pending operations, or collision checks with raw Git force pushes or manual state edits.

## Inspect and navigate

Before editing or amending a stack branch, check `git branch --show-current` and `graphene graph`. Graph does not fetch. `graphene graph --stack` shows the current branch's tracked ancestors and descendants, their base, and warnings. If a branch needs syncing after its parent changed, switch to it and run `graphene sync`.

Use `graphene graph --json` when programmatically checking branch relationships or pending operations; see [inspection details](references/inspection.md) for the output contract.

Use `graphene go up|down|top|bottom` (`-u|-d|-t|-b`) for tracked branches. If multiple choices are printed, rerun with their numbered selector, for example `graphene go up 2`. Use `git switch <branch>` outside the tracked graph.

## Create or import

Stage one logical change, then create a branch:

```sh
git add <files>
graphene new -m "First reviewable change"
```

Repeat for each stack layer. To commit on an existing worktree branch, use `graphene new --reuse-current --base <base>`; `<base>` is the intended stack base, such as `main`. `--base` records the parent without moving HEAD or the local base. It can be omitted when only one base matches.

To track an existing linear series without rewriting commits, run `graphene import <base>` from its tip. Import excludes commits known in the base's upstream history; it does not fetch. Fetch first if current upstream state is needed. Import onto a tracked parent starts at that parent's local tip.

## Rewrite

Amend staged changes and restack descendants:

```sh
git add <files>
graphene amend --no-edit
```

Use `-m "Updated message"` instead of `--no-edit` to change the message.

Split a tracked one-commit branch (current branch by default):

```sh
graphene split [branch]
git add -p
graphene new --reuse-current -m "First split part"
git add -p
graphene new -m "Second split part"
```

The first split commit requires `--reuse-current`. When no tracked changes remain, descendants are restacked onto the new top.

Use `graphene squash --no-edit` to combine the current branch and its parent, preserving the bottom branch name and restacking descendants. `-c <count>` selects more ancestors; `-m "Message"` supplies a message. Each selected branch must have one commit. A moved base does not require syncing before split or squash.

## Sync or restack

Run `graphene sync` after the stack base changes or stack branches may have landed upstream. It uses the fetched base. From a tracked branch it syncs that stack; from a base it syncs stacks recorded with that base name. Use `--all` only when intentionally syncing every descendant of the current untracked base.

Use `graphene restack <local-base>` to move the current branch and descendants onto a local branch's tip. Add `--fetch` to use that destination's fetched upstream; its local branch stays in place.

Read [sync details](references/sync.md) when upstreams are missing, considering `--assume-merged`, or investigating fetch scope and base divergence. A missing remote ref alone is not evidence of a merged PR.

## Publish

A request to open or update a PR authorizes necessary branch pushes, including force-with-lease pushes of stack branches rewritten for that task. Ask again only if the remote, branches, or action falls outside that scope. Without publishing authorization, finish local work before asking to push.

Inspect `graphene graph`, then use `graphene send` for new commits or `graphene sendf` after rewriting published history. Ordinary `send` rejects rewritten remote history. Add `--stack` to publish descendants rewritten by amend, sync, restack, split, or squash.

Both commands print the remote, mode, scope, and selected branches, then push atomically. A rejection leaves all selected remote branches unchanged; servers without atomic push support are rejected. Selection defaults to the current branch and tracked ancestors. `--stack` adds descendants; from an untracked base it selects descendants without pushing the base. An untracked branch without descendants is pushed by itself.

Return the printed review links. Read [inspection details](references/inspection.md) when interpreting rewrite patch comparisons.

## Recover

Resolve conflicts, stage fixes, and run `graphene continue`. To abandon a pending operation, run `graphene abort` from the worktree where it started. For an interrupted Git step with an unknown result, abort and rerun; retry abort if rollback was interrupted.

Read [recovery details](references/recovery.md) before relying on rollback guarantees or handling collisions, submodules, nested repositories, or `--accept-risk`. Untracked and ignored contents are not backed up. Use `--accept-risk` only with the user's acceptance of possible local data loss.
