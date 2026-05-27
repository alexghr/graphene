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
gn go up
gn go top 2
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
gn track --parent main
gn track --parent main feature/already-created
```

Run `gn help` or `gn help <command>` for full command details.

## Aliases

Graphene has Git-style aliases stored as config keys under `alias.<name>`. For example, you can create the common short commands yourself:

```
graphene config set --global alias.create new
graphene config set --global alias.modify amend
graphene config set --global alias.submit sendf
graphene config set --global alias.ss sendf --stack
graphene config set --global alias.up go up
graphene config set --global alias.down go down
graphene config set --global alias.top go top
graphene config set --global alias.bottom go bottom
graphene config set --global alias.log graph
graphene config set --global alias.tr track
```

Non-shell aliases are split like shell words and receive any extra arguments after the alias expansion. Shell aliases start with `!` and run through `sh`, with extra arguments appended the same way Git does:

```
graphene config set --global alias.save "!git add -A && graphene new -m"
```

## Config

Graphene settings are stored in Git config. Use `--global` for user-wide defaults and omit it for repository-local settings:

```
graphene config set --global branchPrefix stack
graphene config get branchPrefix
graphene config unset branchPrefix
```

For non-GitHub remotes, set a pull request URL template:

```
graphene config set prUrlTemplate "https://example.test/pr/${baseBranch}/${targetBranch}"
```

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
