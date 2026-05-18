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

Push the current branch and the branches it depends on:

```
gn send
```

After amending a stacked branch, push the rewritten branch set with force-with-lease:

```
gn amend -m "Wire export into settings"
gn sendf
```

Stop tracking the current branch path without deleting any Git branches:

```
gn forget
```

Run `gn help` or `gn help <command>` for full command details.

## Agents

Graphene provides reusable agent instructions as a skill at [skills/graphene-stacked-prs](skills/graphene-stacked-prs).

Ask Codex to install the skill from:

```
https://github.com/alexghr/graphene/tree/main/skills/graphene-stacked-prs
```

Install it for Claude Code:

```
mkdir -p "$HOME/.claude/skills/graphene-stacked-prs"
curl -fL https://raw.githubusercontent.com/alexghr/graphene/main/skills/graphene-stacked-prs/SKILL.md -o "$HOME/.claude/skills/graphene-stacked-prs/SKILL.md"
```

Restart Codex or Claude Code after installing a new skill.

## Worktrees

Stack state is stored in the repository's local Git config under `graphene.state`, so linked worktrees for the same repository share the same stack graph.

When `gn sync` needs a newer base branch that is checked out in another worktree, Graphene fetches the upstream and rebases onto the fetched commit instead of switching to or updating the checked-out branch.
