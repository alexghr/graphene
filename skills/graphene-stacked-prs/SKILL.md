---
name: graphene-stacked-prs
description: Use when preparing stacked pull requests with the Graphene CLI, creating reviewable branches, walking stack branches, amending, splitting, squashing, restacking, or pushing stacked PR branches for review.
---

# Graphene Stacked PRs

Use Graphene for stacked PR work. Graphene repo and install instructions: https://github.com/alexghr/graphene#install.

## Setup

Graphene must be available on `PATH`. If it is missing and installing tools is allowed, read the Graphene README install section and follow those instructions.

Use `graphene` in commands. If the repo documents `gn`, treat it as an alias for `graphene`.

## Rules

- Split work into small, reviewable branches.
- Stage one logical change at a time.
- Use `graphene new -m "<message>"` instead of `git commit`.
- Use `graphene amend` instead of `git commit --amend` on stacked branches. Use `graphene amend --no-edit` when keeping the existing commit message.
- Use `graphene split` to break an existing one-commit stacked branch into smaller reviewable branches.
- Use `graphene squash` to combine the current branch with one or more direct ancestors. Use `--no-edit` to accept the generated squash message.
- Use `graphene sync` after the stack base changes or when stack branches may have landed upstream.
- After rewrite workflows such as amend, split, squash, or restack, inspect the stack and use `graphene sendf --dry-run` before pushing.
- Run `graphene graph` before pushing.
- Run `graphene send --dry-run` before pushing.
- Only run `graphene send` or `graphene sendf` when pushing is approved.
- Include the printed pull request URLs in the final response.

## Move Between Stack Branches

Before editing or amending a stacked branch, verify where you are:

```sh
git branch --show-current
graphene graph
```

Use Graphene stack navigation for tracked branches:

```sh
graphene go --next
graphene go --prev
graphene go --top
graphene go --bottom
```

Short flags are available as `-n`, `-p`, `-t`, and `-b`.

When Graphene reports multiple possible branches, choose from the numbered list it prints and rerun with that selector:

```sh
graphene go --next 2
graphene go -t2
```

If you need a branch outside Graphene's tracked stack graph, switch explicitly:

```sh
git switch <branch>
```

## Create A Stack

```sh
git add <files>
graphene new -m "First reviewable change"

git add <files>
graphene new -m "Second reviewable change"

graphene graph
graphene send --dry-run
```

After approval to push:

```sh
graphene send
```

## Sync A Stack

Run `graphene sync` after the stack base changes or when one or more stack branches may have landed upstream.

```sh
graphene sync
graphene graph
graphene sendf --dry-run
```

`graphene sync` can run from a tracked stack branch. It can also run from a stack base such as `main`; from a base branch, it syncs all stacks recorded with that exact base branch name.

If sync reports branches to retarget or rewrites stack branches, use `graphene sendf --dry-run` before pushing. Only force-with-lease push with `graphene sendf` after approval.

## Amend A Stacked Branch

Use `--no-edit` instead of `-m` when the existing commit message should be preserved.

```sh
git switch <branch>
git add <files>
graphene amend -m "Updated reviewable change"
graphene graph
graphene sendf --dry-run
```

To keep the existing commit message:

```sh
git switch <branch>
git add <files>
graphene amend --no-edit
graphene graph
graphene sendf --dry-run
```

After approval to force-with-lease push rewritten stacked branches:

```sh
graphene sendf
```

## Split A Stacked Branch

Use this when a tracked one-commit branch should become multiple reviewable branches. If no branch is given, Graphene splits the current branch.

```sh
graphene split [branch]
git add -p
graphene new --reuse-current -m "First split part"
git add -p
graphene new -m "Second split part"
graphene graph
graphene sendf --dry-run
```

The first split commit must use `graphene new --reuse-current`; later split parts use normal `graphene new`. When no tracked changes remain, Graphene restacks descendants onto the new split top.

## Squash Stacked Branches

Use this when adjacent stack branches should become one reviewable branch. `-c`/`--count` includes the current branch and defaults to `2`. Use `--no-edit` to accept Graphene's generated squash message without opening an editor.

```sh
graphene squash
graphene squash -c 3 -m "Combine related changes"
graphene graph
graphene sendf --dry-run
```

To use the generated squash message without editing:

```sh
graphene squash --no-edit
graphene graph
graphene sendf --dry-run
```

Graphene preserves the bottom branch name and restacks descendants onto the rewritten branch.

## Pending Operation Conflicts

If a pending Graphene operation stops for conflicts, resolve the conflicts, stage the fixes, then run:

```sh
graphene continue
```

To abandon the pending Graphene operation:

```sh
graphene abort
```
