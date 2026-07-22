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
	Dir       string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	stateLock *StateLock
}

type GitError struct {
	Args     []string
	Code     int
	Stderr   string
	Streamed bool
}

type gitVersion struct {
	Major int
	Minor int
	Patch int
}

var minimumGitVersion = gitVersion{Major: 2, Minor: 38}

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
	stdout, err := g.OutputBytes(args...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(stdout), "\n"), nil
}

func (g Git) OutputBytes(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, gitCommandError(args, err, stderr.String(), false)
	}
	return stdout.Bytes(), nil
}

func gitCommandError(args []string, err error, stderr string, streamed bool) error {
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
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

func (g Git) Root() (string, error) {
	root, err := g.Output("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("git returned non-absolute worktree root %q", root)
	}
	return filepath.Clean(root), nil
}

func (g Git) Rooted() (Git, error) {
	root, err := g.Root()
	if err != nil {
		return Git{}, err
	}
	g.Dir = root
	return g, nil
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

func (g Git) LocalBranchesPointingAt(rev string) ([]string, error) {
	out, err := g.Output("branch", "--format=%(refname:strip=2)", "--points-at", rev)
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

func (g Git) Upstream(branch string) (string, string, error) {
	remote, err := g.Output("config", "--get", "branch."+branch+".remote")
	if err != nil {
		if isGitExit(err, 1) {
			return "", "", nil
		}
		return "", "", err
	}
	merge, err := g.Output("config", "--get", "branch."+branch+".merge")
	if err != nil {
		if isGitExit(err, 1) {
			return "", "", nil
		}
		return "", "", err
	}
	return remote, merge, nil
}

func (g Git) RemoteURL(remote string) (string, error) {
	return g.Output("remote", "get-url", "--push", remote)
}

func (g Git) PRURLTemplate() (string, error) {
	template, err := g.Output("config", "--get", "graphene.prUrlTemplate")
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

func (g Git) BranchCheckedOut(branch string) (bool, error) {
	out, err := g.Output("worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	target := "refs/heads/" + branch
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimPrefix(line, "branch ") == target && strings.HasPrefix(line, "branch ") {
			return true, nil
		}
	}
	return false, nil
}

func (g Git) OutputErr(args ...string) error {
	_, err := g.Output(args...)
	return err
}

func (g Git) Version() (gitVersion, error) {
	out, err := g.Output("--version")
	if err != nil {
		return gitVersion{}, err
	}
	version, err := parseGitVersion(out)
	if err != nil {
		return gitVersion{}, err
	}
	return version, nil
}

func parseGitVersion(out string) (gitVersion, error) {
	for field := range strings.FieldsSeq(out) {
		if field == "" || field[0] < '0' || field[0] > '9' {
			continue
		}
		parts := strings.Split(field, ".")
		if len(parts) < 2 {
			break
		}
		major, ok := leadingInt(parts[0])
		if !ok {
			break
		}
		minor, ok := leadingInt(parts[1])
		if !ok {
			break
		}
		var patch int
		if len(parts) > 2 {
			patch, _ = leadingInt(parts[2])
		}
		return gitVersion{Major: major, Minor: minor, Patch: patch}, nil
	}
	return gitVersion{}, fmt.Errorf("parse git version from %q", out)
}

func leadingInt(s string) (int, bool) {
	var n int
	var ok bool
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			break
		}
		n = n*10 + int(ch-'0')
		ok = true
	}
	return n, ok
}

func (v gitVersion) less(other gitVersion) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	return v.Patch < other.Patch
}

func (v gitVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

func (g Git) RebaseInProgress() (bool, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		path, err := g.GitPath(name)
		if err != nil {
			return false, err
		}
		if existsDir(path) {
			return true, nil
		}
	}
	return false, nil
}

func (g Git) RunOperationSwitch(args ...string) error {
	if len(args) > 0 {
		target := args[len(args)-1]
		if !strings.HasPrefix(target, "-") {
			if err := g.RequireNoUntrackedDirectoryCollision(target, "branch switch"); err != nil {
				return err
			}
		}
	}
	command := []string{"-c", "submodule.recurse=false", "switch", "--no-overwrite-ignore", "--no-recurse-submodules"}
	return g.Run(append(command, args...)...)
}

func (g Git) RunOperationReset(args ...string) error {
	if len(args) >= 2 && (args[0] == "--hard" || args[0] == "--keep") {
		if err := g.RequireNoUntrackedDirectoryCollision(args[len(args)-1], "worktree reset"); err != nil {
			return err
		}
	}
	command := []string{"-c", "submodule.recurse=false", "reset", "--no-recurse-submodules"}
	return g.Run(append(command, args...)...)
}

func (g Git) RunOperationRebase(args ...string) error {
	command := []string{"-c", "submodule.recurse=false", "rebase"}
	return g.Run(append(command, args...)...)
}

func (g Git) WorktreeID() (string, error) {
	path, err := g.Output("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}

func (g Git) GitPath(name string) (string, error) {
	path, err := g.Output("rev-parse", "--path-format=absolute", "--git-path", name)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("git returned non-absolute path for %q: %q", name, path)
	}
	return filepath.Clean(path), nil
}

func (g Git) HasUnstagedChanges() (bool, error) {
	rooted, err := g.Rooted()
	if err != nil {
		return false, err
	}
	_, err = rooted.Output("diff", "--quiet", "--ignore-submodules", "--")
	if err == nil {
		return false, nil
	}
	if isGitExit(err, 1) {
		return true, nil
	}
	return false, err
}

func (g Git) HasStagedChanges() (bool, error) {
	rooted, err := g.Rooted()
	if err != nil {
		return false, err
	}
	_, err = rooted.Output("diff", "--cached", "--quiet", "--ignore-submodules", "--")
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

func (g Git) HasTrackedChangesIgnoringSubmodules() (bool, error) {
	rooted, err := g.Rooted()
	if err != nil {
		return false, err
	}
	out, err := rooted.Output("status", "--porcelain", "--untracked-files=no", "--ignore-submodules=all")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

func tempBranchName(pid int, seq int64) string {
	return "graphene/tmp-" + strconv.Itoa(pid) + "-" + strconv.FormatInt(seq, 10)
}
