# graphene

Install

```
VERSION=v0.6.1 && curl -Lo $HOME/.local/bin/graphene https://github.com/alexghr/graphene/releases/download/$VERSION/graphene-$VERSION-linux-amd64
```

For shorter commands, add `alias gn=graphene` to your shell config.

Commands

Graphene requires Git 2.38 or newer

- `graphene commit [-b <branch>] [git commit args...]`: creates a new branch from the current branch, runs `git commit` with the remaining args, and records the branch in the stack. Without `-b`, the branch name comes from the commit subject.
- `graphene amend [git commit args...]`: runs `git commit --amend`, then restacks dependent branches with `git rebase --update-refs`.
- `graphene rebase`: checks out the stack base, runs `git pull --ff-only`, then rebases the stack up to the current branch and returns to that branch.
- `graphene continue`: continues the current Git rebase and then continues any queued Graphene restacks.
- `graphene abort`: aborts the current Git rebase and clears Graphene's pending rebase state.
- `graphene rm [-f|--force]`: removes Graphene tracking for the current stack up to the current branch. It does not delete Git branches.
- `graphene update`: pulls the stack base, drops already-merged branches by diff, deletes those local branches, and rebases the remaining stack.
- `graphene push [remote] [git push flags...]`: runs `git push` for the current branch and the branches it depends on, then prints PR links.
- `graphene pushf [remote] [git push flags...]`: runs `git push --force-with-lease` for the current branch and tracked branches above it, then prints PR links.
- `graphene pr [remote]`: prints pull request creation links for the current branch and the branches it depends on. It does not push.
- `graphene graph`: prints the tracked stack graph.
- `graphene version`: prints the Graphene version and the Git version.
