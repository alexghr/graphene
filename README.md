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

Add Graphene to your shell startup file, define the `gn` alias used below, and
enable tab completion.

For Bash:

```
export PATH="$HOME/.local/bin:$PATH"
alias gn=graphene
source <(graphene completion bash)
```

For Zsh, initialize its completion system before sourcing Graphene's script:

```
export PATH="$HOME/.local/bin:$PATH"
alias gn=graphene
autoload -Uz compinit
compinit
source <(graphene completion zsh)
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

Sync all descendants from a base branch:

```
git checkout merge-train/spartan
gn sync -a
```

Preview the same sync first:

```
gn sync -a --dry-run
```

Sync only removes branches automatically when their commits are ancestors of the updated base or their patches are already present there. A deleted remote branch is not, by itself, proof that it was merged. If sync reports missing upstreams, preview the explicit assumption and verify every listed PR was merged before running it for real:

```
gn sync --assume-merged --dry-run
gn sync --assume-merged
```

Record an existing one-commit branch on top of a base branch:

```
gn track --parent main
gn track --parent main feature/already-created
```

Import a linear local commit series as a stack:

```
git checkout feature/multi-commit
gn import main
```

Run `gn help` or `gn help <command>` for full command details.

## Recovering interrupted operations

When a Graphene operation stops for conflicts, resolve them, stage the result, and continue:

```
gn continue
```

If Graphene cannot tell whether an interrupted action completed, inspect the current refs before explicitly accepting them as that action's result:

```
gn continue --accept-current
```

To abandon an operation before its commit point and restore its original local state, run `gn abort`. Graphene normally refuses to overwrite refs or branch configuration changed outside the operation. After reviewing the reported drift, `gn abort --force` permits that overwrite and preserves displaced data as recovery refs or artifacts.

Graphene journals mutating operations before changing refs, branch configuration, the index, or the worktree. If a process stops midway, rerun `gn continue` or `gn abort`; cleanup and rollback are restartable. If an earlier abort stopped during its destructive worktree-restoration step, inspect the worktree and use `gn abort --force` to explicitly resume it.

Once an operation journal exists, Graphene owns its recorded refs, config, index, worktree, and initialized submodule checkouts until the operation commits or aborts. Do not edit them with another process while recovery is pending; Graphene rejects drift where it can, and `abort --force` is the explicit escape hatch for intentionally replacing operation-owned state.

Repository state lives in `<git-common-dir>/graphene/state.json`, with journal artifacts and the repository-wide operation lock beside it. Existing `graphene.state` values in local Git config are migrated automatically and remain readable during an interrupted migration.

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

Graphene can import aliases from a Git config-format file. The repository includes a Graphite-compatibility file with common top-level command aliases:

```
graphene aliases import --global https://raw.githubusercontent.com/alexghr/graphene/main/aliases/graphite.gitconfig
graphene aliases import --global /path/to/graphene/aliases/graphite.gitconfig
```

Imported aliases are stored as normal `alias.<name>` config entries, so they work offline after import. For temporary use, set `GRAPHENE_ALIAS_FILE` to a local path list or a single HTTP(S) URL, or set `aliasFile` to a local path or HTTP(S) URL. Explicit `alias.<name>` config entries take precedence over alias files. Only import or load alias files from trusted URLs; they can define shell aliases.

Alias import refuses to overwrite existing aliases by default and lists the conflicting names. Rerun with `--force` to replace those aliases.

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

Stack state is stored under the repository's common Git directory, so linked worktrees share the same stack graph and operation journal. Only one mutating Graphene command runs at a time. An interrupted worktree operation must be continued or aborted from the worktree that owns it; disjoint `send` operations from another worktree remain available.

When `gn sync` needs a newer base branch that is checked out in another worktree, Graphene fetches the upstream and rebases onto the fetched commit instead of switching to or updating the checked-out branch.

If a worktree already has the branch you want to use checked out, commit on that branch and record it on top of another ref:

```
gn new --reuse-current --base main -m "implement foo"
```
