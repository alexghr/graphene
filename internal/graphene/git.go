package graphene

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Git struct {
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type GitError struct {
	Args     []string
	Code     int
	Stderr   string
	Streamed bool
}

func (e *GitError) Error() string {
	if strings.TrimSpace(e.Stderr) != "" {
		return strings.TrimSpace(e.Stderr)
	}
	return fmt.Sprintf("git %s exited with status %d", strings.Join(e.Args, " "), e.Code)
}

func (g Git) Run(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Dir
	cmd.Stdin = g.Stdin
	cmd.Stdout = g.Stdout
	cmd.Stderr = g.Stderr
	if err := cmd.Run(); err != nil {
		return gitCommandError(args, err, "", true)
	}
	return nil
}

func (g Git) Output(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", gitCommandError(args, err, stderr.String(), false)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func gitCommandError(args []string, err error, stderr string, streamed bool) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &GitError{
			Args:     append([]string(nil), args...),
			Code:     exitErr.ExitCode(),
			Stderr:   stderr,
			Streamed: streamed,
		}
	}
	return fmt.Errorf("run git %s: %w", strings.Join(args, " "), err)
}

func isGitExit(err error, code int) bool {
	var gitErr *GitError
	return errors.As(err, &gitErr) && gitErr.Code == code
}

func (g Git) CurrentBranch() (string, error) {
	branch, err := g.Output("branch", "--show-current")
	if err != nil {
		return "", err
	}
	if branch == "" {
		return "", fmt.Errorf("graphene requires a checked-out branch")
	}
	return branch, nil
}

func (g Git) Head() (string, error) {
	return g.Output("rev-parse", "HEAD")
}

func (g Git) BranchExists(branch string) (bool, error) {
	_, err := g.Output("show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if isGitExit(err, 1) {
		return false, nil
	}
	return false, err
}

func (g Git) LocalBranches() ([]string, error) {
	out, err := g.Output("for-each-ref", "--format=%(refname:strip=2)", "refs/heads")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (g Git) BranchCreateStatus(branch string) (string, error) {
	if strings.TrimSpace(branch) == "" || strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("invalid branch name %q", branch)
	}
	if _, err := g.Output("check-ref-format", "--branch", branch); err != nil {
		return "", fmt.Errorf("invalid branch name %q", branch)
	}

	branches, err := g.LocalBranches()
	if err != nil {
		return "", err
	}
	for _, existing := range branches {
		switch {
		case existing == branch:
			return "taken", nil
		case strings.HasPrefix(branch, existing+"/"):
			return "blocked", fmt.Errorf("cannot create branch %q because branch %q already exists", branch, existing)
		case strings.HasPrefix(existing, branch+"/"):
			return "taken", nil
		}
	}
	return "available", nil
}

func (g Git) UpstreamRemote(branch string) (string, error) {
	remote, err := g.Output("config", "--get", "branch."+branch+".remote")
	if err == nil {
		return remote, nil
	}
	if isGitExit(err, 1) {
		return "", nil
	}
	return "", err
}

func (g Git) RemoteURL(remote string) (string, error) {
	return g.Output("remote", "get-url", "--push", remote)
}

func (g Git) PRURLTemplate() (string, error) {
	template, err := g.Output("config", "--local", "--get", "graphene.prUrlTemplate")
	if err == nil {
		return template, nil
	}
	if isGitExit(err, 1) {
		return "", nil
	}
	return "", err
}

func (g Git) HasUpstream(branch string) (bool, error) {
	remote, err := g.Output("config", "--get", "branch."+branch+".remote")
	if err != nil {
		if isGitExit(err, 1) {
			return false, nil
		}
		return false, err
	}
	merge, err := g.Output("config", "--get", "branch."+branch+".merge")
	if err != nil {
		if isGitExit(err, 1) {
			return false, nil
		}
		return false, err
	}
	return remote != "" && merge != "", nil
}

func (g Git) SetUpstream(branch, remote string) error {
	if err := g.OutputErr("config", "branch."+branch+".remote", remote); err != nil {
		return err
	}
	return g.OutputErr("config", "branch."+branch+".merge", "refs/heads/"+branch)
}

func (g Git) OutputErr(args ...string) error {
	_, err := g.Output(args...)
	return err
}

func (g Git) RebaseInProgress() (bool, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		path, err := g.Output("rev-parse", "--git-path", name)
		if err != nil {
			return false, err
		}
		if !filepath.IsAbs(path) && g.Dir != "" {
			path = filepath.Join(g.Dir, path)
		}
		if existsDir(path) {
			return true, nil
		}
	}
	return false, nil
}

func (g Git) HasUnstagedChanges() (bool, error) {
	_, err := g.Output("diff", "--quiet", "--ignore-submodules", "--")
	if err == nil {
		return false, nil
	}
	if isGitExit(err, 1) {
		return true, nil
	}
	return false, err
}

func (g Git) HasTrackedChanges() (bool, error) {
	out, err := g.Output("status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

func tempBranchName(pid int, seq int64) string {
	return "graphene/tmp-" + strconv.Itoa(pid) + "-" + strconv.FormatInt(seq, 10)
}
