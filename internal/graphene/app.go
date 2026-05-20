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

	gitVersion, err := a.git.Version()
	if err == nil && !isVersionCommand(command) && gitVersion.less(minimumGitVersion) {
		err = fmt.Errorf("graphene requires git >= %s; found git %s", minimumGitVersion, gitVersion)
	}

	if err == nil {
		if len(args) < 2 {
			a.usage(a.stderr)
			return 1
		}

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
		case "forget":
			if helpArgs(args[2:]) {
				a.commandUsage("forget", a.stdout)
				return 0
			}
			err = a.forget(args[2:])
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
				if !a.commandUsage(args[2], a.stdout) {
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
	if !(errors.As(err, &gitErr) && gitErr.Streamed) {
		fmt.Fprintln(a.stderr, err)
	}
	return errorExitCode(err)
}

func (a *App) usage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  graphene new [options]
  graphene amend [options]
  graphene continue
  graphene abort
  graphene forget [--force]
  graphene sync
  graphene send [--remote <remote>] [--stack] [--dry-run]
  graphene sendf [--remote <remote>] [--stack] [--dry-run]
  graphene restack <base>
  graphene graph
  graphene version

run "graphene help <command>" for command-specific help`)
}

func (a *App) commandUsage(command string, w io.Writer) bool {
	usages := map[string]string{
		"new": `usage: graphene new [options]

Commit staged changes on a new branch, or on the current branch with --reuse-current, and record it in the stack.

options:
  -b, --branch <branch>      use an explicit branch name
      --base <branch>        record the new branch as a child of this branch
      --reuse-current        commit on the current branch instead of creating one
  -m, --message <message>    use the given commit message
      --no-verify            bypass commit hooks
      --gpg-sign[=<key-id>]  GPG-sign the commit
      --no-gpg-sign          do not GPG-sign the commit`,
		"amend": `usage: graphene amend [options]

Amend the current commit and restack dependent branches.

options:
  -m, --message <message>    use the given commit message
      --no-verify            bypass commit hooks
      --gpg-sign[=<key-id>]  GPG-sign the commit
      --no-gpg-sign          do not GPG-sign the commit`,
		"continue": "usage: graphene continue\n\nContinue the current Git rebase and any queued Graphene restacks.",
		"abort":    "usage: graphene abort\n\nAbort the current Git rebase and clear queued Graphene restacks.",
		"forget": `usage: graphene forget [--force]

Remove Graphene tracking through the current branch without deleting Git branches.

options:
      --force  clear pending Graphene rebase state while forgetting`,
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
		"restack": "usage: graphene restack <base>\n\nMove the current branch onto another branch or commit-ish, then restack dependent branches.",
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
	return 1
}

func helpArgs(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help")
}

func isVersionCommand(command string) bool {
	return command == "version" || command == "--version" || command == "-v"
}
