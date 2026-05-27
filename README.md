# graphene

This is a vibe-coded CLI tool to create stacked PRs.

## Requirements

This tool requires Git 2.38 or newer

## Install

```
mkdir -p "$HOME/.local/bin"
curl -fL -o "$HOME/.local/bin/graphene" "https://github.com/alexghr/graphene/releases/latest/download/graphene-$(uname -s)-$(uname -m)"
chmod +x "$HOME/.local/bin/graphene"
```

Add to shell init

```
export PATH="$HOME/.local/bin:$PATH"
alias gn=graphene
```

## Workflow

Create a stack by staging changes and committing each change with Graphene:

```
git add .
gn new -m "Add account export"

git add .
gn new -m "Wire export into settings"
```

Inspect the stack:

```
gn graph
```

Move through the stack:

```
gn go --next
gn go --top 2
```

Push the current branch and the branches it depends on:

```
gn send
```

After amending a stacked branch, push the rewritten branch set with force-with-lease:

```
gn amend -m "Wire export into settings"
gn sendf
```

Use `--no-edit` when you want to keep the existing commit message while amending:

```
gn amend --no-edit
```

Split the current stacked branch into multiple branches:

```
gn split
git add -p
gn new --reuse-current -m "Extract helper"
git add -p
gn new -m "Wire helper"
```

Squash the current branch into its parent, or squash a larger range ending at the current branch:

```
gn squash
gn squash -c 3 -m "Combine parser cleanup"
```

Use `--no-edit` with `gn squash` to accept the generated squash message without opening an editor.

Stop tracking the current branch path without deleting any Git branches:

```
gn forget
```

Record an existing one-commit branch on top of a base branch:

```
gn track main
gn track main feature/already-created
```

Run `gn help` or `gn help <command>` for full command details.

## Agents

Graphene provides reusable agent instructions as a skill at [skills/graphene-stacked-prs](skills/graphene-stacked-prs).

Install it for Codex:

```
graphene skill --codex
```

Install it for Claude Code:

```
graphene skill --claude
```

Restart Codex or Claude Code after installing a new skill.

## Worktrees

Stack state is stored in the repository's local Git config under `graphene.state`, so linked worktrees for the same repository share the same stack graph.

When `gn sync` needs a newer base branch that is checked out in another worktree, Graphene fetches the upstream and rebases onto the fetched commit instead of switching to or updating the checked-out branch.

If a worktree already has the branch you want to use checked out, commit on that branch and record it on top of another ref:

```
gn new --reuse-current --base main -m "implement foo"
```
