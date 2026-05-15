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
	if err == nil && command != "version" && gitVersion.less(minimumGitVersion) {
		err = fmt.Errorf("graphene requires git >= %s; found git %s", minimumGitVersion, gitVersion)
	}

	if err == nil {
		if len(args) < 2 {
			a.usage()
			return 1
		}

		switch command {
		case "commit":
			err = a.commit(args[2:])
		case "amend":
			err = a.amend(args[2:])
		case "rebase":
			err = a.rebase(args[2:])
		case "continue":
			err = a.continueRebase(args[2:])
		case "abort":
			err = a.abortRebase(args[2:])
		case "rm":
			err = a.rm(args[2:])
		case "update":
			err = a.update(args[2:])
		case "push":
			err = a.push(args[2:])
		case "pushf":
			err = a.pushf(args[2:])
		case "pr":
			err = a.pr(args[2:])
		case "graph":
			err = a.graph(args[2:])
		case "version":
			err = a.version(args[2:], gitVersion)
		case "help", "-h", "--help":
			a.usage()
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

func (a *App) usage() {
	fmt.Fprintln(a.stderr, `usage:
  graphene commit [-b <branch>] [git commit args...]
  graphene amend [git commit args...]
  graphene rebase
  graphene continue
  graphene abort
  graphene rm [-f|--force]
  graphene update
  graphene push [remote] [git push flags...]
  graphene pushf [remote] [git push flags...]
  graphene pr [remote]
  graphene graph
  graphene version`)
}

func errorExitCode(err error) int {
	var gitErr *GitError
	if errors.As(err, &gitErr) && gitErr.Code > 0 {
		return gitErr.Code
	}
	return 1
}
