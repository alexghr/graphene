---
name: graphene-stacked-prs
description: Use when preparing stacked pull requests with the Graphene CLI, splitting work into reviewable branches, walking stack branches, amending stacked branches, restacking, or pushing stacked PR branches for review.
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
- Use `graphene amend` instead of `git commit --amend` on stacked branches.
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

## Amend A Stacked Branch

```sh
git switch <branch>
git add <files>
graphene amend -m "Updated reviewable change"
graphene graph
graphene sendf --dry-run
```

After approval to force-with-lease push rewritten stacked branches:

```sh
graphene sendf
```

## Rebase Conflicts

If a Graphene rebase stops for conflicts, resolve the conflicts, stage the fixes, then run:

```sh
graphene continue
```

To abandon the pending Graphene rebase:

```sh
graphene abort
```
