package graphene

import (
	"errors"
	"fmt"
	"io"
)

type App struct {
	git    Git
	getenv func(string) string
	stdout io.Writer
	stderr io.Writer
}

func NewApp(dir string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) *App {
	return &App{
		git: Git{
			Dir:    dir,
			Stdin:  stdin,
			Stdout: stdout,
			Stderr: stderr,
		},
		getenv: getenv,
		stdout: stdout,
		stderr: stderr,
	}
}

func (a *App) Run(args []string) int {
	command := ""
	if len(args) >= 2 {
		command = args[1]
	}
	shellAliasRan := false
	gitVersion, err := a.git.Version()
	if err == nil && !isVersionCommand(command) && gitVersion.less(minimumGitVersion) {
		err = fmt.Errorf("graphene requires git >= %s; found git %s", minimumGitVersion, gitVersion)
	}

	if err == nil {
		if len(args) < 2 {
			a.usage(a.stderr)
			return 1
		}

		expanded, expandErr := a.expandAliases(args)
		if expandErr != nil {
			err = expandErr
		} else if expanded.shell != nil {
			err = a.runShellAlias(*expanded.shell)
			shellAliasRan = true
		} else {
			args = expanded.args
			command = args[1]
		}
	}

	if err == nil && shellAliasRan {
		return 0
	}

	if err == nil {
		switch command {
		case "new":
			if helpArgs(args[2:]) {
				a.commandUsage("new", a.stdout)
				return 0
			}
			err = a.newBranch(args[2:])
		case "amend":
			if helpArgs(args[2:]) {
				a.commandUsage("amend", a.stdout)
				return 0
			}
			err = a.amend(args[2:])
		case "split":
			if helpArgs(args[2:]) {
				a.commandUsage("split", a.stdout)
				return 0
			}
			err = a.split(args[2:])
		case "squash":
			if helpArgs(args[2:]) {
				a.commandUsage("squash", a.stdout)
				return 0
			}
			err = a.squash(args[2:])
		case "skill", "agent-skill":
			if helpArgs(args[2:]) {
				a.commandUsage("skill", a.stdout)
				return 0
			}
			err = a.skill(args[2:])
		case "continue":
			if helpArgs(args[2:]) {
				a.commandUsage("continue", a.stdout)
				return 0
			}
			err = a.continueRebase(args[2:])
		case "abort":
			if helpArgs(args[2:]) {
				a.commandUsage("abort", a.stdout)
				return 0
			}
			err = a.abortRebase(args[2:])
		case "config":
			if helpArgs(args[2:]) {
				a.commandUsage("config", a.stdout)
				return 0
			}
			err = a.config(args[2:])
		case "forget":
			if helpArgs(args[2:]) {
				a.commandUsage("forget", a.stdout)
				return 0
			}
			err = a.forget(args[2:])
		case "track":
			if helpArgs(args[2:]) {
				a.commandUsage("track", a.stdout)
				return 0
			}
			err = a.track(args[2:])
		case "sync":
			if helpArgs(args[2:]) {
				a.commandUsage("sync", a.stdout)
				return 0
			}
			err = a.sync(args[2:])
		case "send":
			if helpArgs(args[2:]) {
				a.commandUsage("send", a.stdout)
				return 0
			}
			err = a.send(args[2:])
		case "sendf":
			if helpArgs(args[2:]) {
				a.commandUsage("sendf", a.stdout)
				return 0
			}
			err = a.sendf(args[2:])
		case "restack":
			if helpArgs(args[2:]) {
				a.commandUsage("restack", a.stdout)
				return 0
			}
			err = a.restack(args[2:])
		case "go":
			if helpArgs(args[2:]) {
				a.commandUsage("go", a.stdout)
				return 0
			}
			err = a.goBranch(args[2:])
		case "graph":
			if helpArgs(args[2:]) {
				a.commandUsage("graph", a.stdout)
				return 0
			}
			err = a.graph(args[2:])
		case "version", "--version", "-v":
			if command == "version" && helpArgs(args[2:]) {
				a.commandUsage("version", a.stdout)
				return 0
			}
			err = a.version(args[2:], gitVersion)
		case "help":
			if len(args) > 2 {
				command, err := a.helpCommand(args[2])
				if err != nil {
					break
				}
				if !a.commandUsage(command, a.stdout) {
					err = fmt.Errorf("unknown command %q", args[2])
					break
				}
				return 0
			}
			a.usage(a.stdout)
			return 0
		case "-h", "--help":
			a.usage(a.stdout)
			return 0
		default:
			err = fmt.Errorf("unknown command %q", command)
		}
	}

	if err == nil {
		return 0
	}

	var gitErr *GitError
	var aliasErr *shellAliasError
	if !(errors.As(err, &gitErr) && gitErr.Streamed) && !errors.As(err, &aliasErr) {
		fmt.Fprintln(a.stderr, err)
	}
	return errorExitCode(err)
}

func (a *App) usage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  graphene new [options]
  graphene amend [options]
  graphene split [branch]
  graphene squash [--count <n>]
  graphene continue
  graphene abort
  graphene config <get|set|unset> [--global|--local] <key> [value]
  graphene forget [--force]
  graphene track --parent <base> [branch]
  graphene sync
  graphene send [--remote <remote>] [--stack] [--dry-run]
  graphene sendf [--remote <remote>] [--stack] [--dry-run]
  graphene restack <base>
  graphene go <up|down|top|bottom> [number]
  graphene graph
  graphene skill [--codex|--claude|--out <path>]
  graphene version

run "graphene help <command>" for command-specific help`)
}

func (a *App) commandUsage(command string, w io.Writer) bool {
	usages := map[string]string{
		"new": `usage: graphene new [options]

Commit staged changes on a new branch, or on the current branch with --reuse-current, and record it in the stack.

options:
  -a, --all                  stage all changes before committing
  -u, --update               stage tracked-file updates before committing
  -b, --branch <branch>      use an explicit branch name
      --base <branch>        record the new branch as a child of this branch
      --reuse-current        commit on the current branch instead of creating one
  -m, --message <message>    use the given commit message
      --no-edit              use the selected commit message without opening an editor
      --no-verify            bypass commit hooks
      --gpg-sign[=<key-id>]  GPG-sign the commit
      --no-gpg-sign          do not GPG-sign the commit`,
		"amend": `usage: graphene amend [options]

Amend the current commit and restack dependent branches.

options:
  -a, --all                  stage all changes before amending
  -u, --update               stage tracked-file updates before amending
  -m, --message <message>    use the given commit message
      --no-edit              use the selected commit message without opening an editor
      --no-verify            bypass commit hooks
      --gpg-sign[=<key-id>]  GPG-sign the commit
      --no-gpg-sign          do not GPG-sign the commit`,
		"split": `usage: graphene split [branch]

Reset a tracked branch to its base so its commit can be recreated as multiple Graphene branches.

When no branch is given, split the current branch. Commit the first split part with:

  graphene new --reuse-current -m "First part"

Commit later split parts with graphene new. When no tracked changes remain, Graphene restacks the original descendants onto the new split top.`,
		"squash": `usage: graphene squash [options]

Squash the current branch and one or more direct ancestors into the bottom branch.

options:
  -c, --count <n>            number of branches to squash, including the current branch (default: 2)
  -m, --message <message>    use the given commit message
      --no-edit              use the generated squash message without opening an editor
      --no-verify            bypass commit hooks
      --gpg-sign[=<key-id>]  GPG-sign the commit
      --no-gpg-sign          do not GPG-sign the commit`,
		"skill": `usage: graphene skill [--codex|--claude|--out <path>]

Write Graphene's bundled agent skill to stdout or to the given path.

options:
      --codex       install to ~/.codex/skills/graphene-stacked-prs/SKILL.md
      --claude      install to ~/.claude/skills/graphene-stacked-prs/SKILL.md
      --out <path>  write SKILL.md to this path; use - for stdout`,
		"continue": "usage: graphene continue\n\nContinue the current Git rebase and any queued Graphene restacks.",
		"abort":    "usage: graphene abort\n\nAbort the current Git rebase and clear queued Graphene restacks.",
		"config": `usage: graphene config <get|set|unset> [--global|--local] <key> [value]

Read and write Graphene settings in Git config. Keys may be written with or without the graphene. prefix.

examples:
  graphene config set --global alias.up go up
  graphene config get branchPrefix
  graphene config set branchPrefix stack
  graphene config unset alias.up`,
		"forget": `usage: graphene forget [--force]

Remove Graphene tracking through the current branch without deleting Git branches.

options:
      --force  clear pending Graphene rebase state while forgetting`,
		"track": `usage: graphene track --parent <base> [branch]

Record an existing one-commit branch in the Graphene stack graph.

When branch is omitted, Graphene tracks the current branch. If the branch is already the base of child stacks, the first child path is folded into the new stack path.`,
		"sync": `usage: graphene sync

Fetch the stack base, drop already-applied branches on the current path, and restack affected children.

Branches detected as already applied upstream are removed from Graphene state and deleted locally.`,
		"send": `usage: graphene send [options] [remote]

Push the current branch and its dependency path, then print pull request URLs.

options:
      --remote <remote>  push to this remote
  -s, --stack            push the current dependency path and descendants
  -n, --dry-run          show what would be pushed without updating refs or upstreams`,
		"sendf": `usage: graphene sendf [options] [remote]

Force-with-lease push the same branch set selected by graphene send.

options:
      --remote <remote>  push to this remote
  -s, --stack            push the current dependency path and descendants
  -n, --dry-run          show what would be pushed without updating refs or upstreams`,
		"restack": "usage: graphene restack <base>\n\nMove the current branch onto another local branch, then restack dependent branches.",
		"go": `usage: graphene go <up|down|top|bottom> [number]

Switch to another branch in the tracked stack graph.

options:
  -t, --top [number]     switch to a leaf descendant
  -b, --bottom [number]  switch to the bottom branch in the current stack path
  -u, --up [number]      switch to a direct child branch
  -d, --down [number]    switch to the direct parent branch`,
		"graph":   "usage: graphene graph\n\nPrint the tracked stack graph.",
		"version": "usage: graphene version\n\nPrint the Graphene version and Git version.",
	}
	text, ok := usages[command]
	if !ok {
		return false
	}
	fmt.Fprintln(w, text)
	return true
}

func errorExitCode(err error) int {
	var gitErr *GitError
	if errors.As(err, &gitErr) && gitErr.Code > 0 {
		return gitErr.Code
	}
	var aliasErr *shellAliasError
	if errors.As(err, &aliasErr) && aliasErr.code > 0 {
		return aliasErr.code
	}
	return 1
}

func helpArgs(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help")
}

func isVersionCommand(command string) bool {
	return command == "version" || command == "--version" || command == "-v"
}
