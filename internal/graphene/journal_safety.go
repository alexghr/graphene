package graphene

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (a *App) prepareOperationWorktree(operation *OperationJournal) (resultErr error) {
	if operation.WorktreePolicy == "" || operation.WorktreePolicy == worktreeRestoreNone {
		return nil
	}
	artifactDir, err := a.operationArtifactDir(operation)
	if err != nil {
		return err
	}
	defer func() {
		if resultErr == nil {
			return
		}
		if cleanupErr := removeOperationArtifactDirectory(artifactDir); cleanupErr != nil {
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()
	if err := a.git.RequireNoGitOperation(); err != nil {
		return err
	}
	if err := a.git.RequireRecoverableWorktree(); err != nil {
		return err
	}
	before, err := a.operationWorktreeFingerprint()
	if err != nil {
		return err
	}
	if err := a.snapshotOperationRawWorktree(operation); err != nil {
		return err
	}
	if operation.WorktreePolicy == worktreeRestoreIndex {
		if err := a.snapshotOperationIndex(operation); err != nil {
			return err
		}
	}
	after, err := a.operationWorktreeFingerprint()
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("worktree or index changed while Graphene was taking its recovery snapshot; rerun the command")
	}
	operation.WorktreeFingerprint = after
	return nil
}

func cleanupUnpublishedOperationArtifacts(artifactDir string, publicationAttempted bool, resultErr *error) {
	if publicationAttempted {
		return
	}
	if cleanupErr := removeOperationArtifactDirectory(artifactDir); cleanupErr != nil {
		*resultErr = errors.Join(*resultErr, cleanupErr)
	}
}

func removeOperationArtifactDirectory(dir string) error {
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove unpublished operation artifacts: %w", err)
	}
	return nil
}

func (a *App) verifyOperationWorktreeFingerprint(operation *OperationJournal) error {
	if operation.WorktreePolicy == "" || operation.WorktreePolicy == worktreeRestoreNone || operation.WorktreeBoundaryCrossed {
		return nil
	}
	if operation.WorktreeFingerprint == "" {
		return nil
	}
	actual, err := a.operationWorktreeFingerprint()
	if err != nil {
		return err
	}
	if actual != operation.WorktreeFingerprint {
		return fmt.Errorf("worktree or index changed after Graphene took its recovery snapshot; rerun or abort the operation before continuing")
	}
	return nil
}

func (a *App) verifyUnpublishedOperationWorktree(operation *OperationJournal) error {
	if err := a.verifyOperationWorktreeFingerprint(operation); err != nil {
		_ = a.removeOperationArtifacts(operation)
		return err
	}
	if operation.WorktreePolicy != "" && operation.WorktreePolicy != worktreeRestoreNone && operation.WorktreeFingerprint != "" {
		operation.WorktreeBoundaryCrossed = true
	}
	return nil
}

func (a *App) crossOperationWorktreeBoundary(state State) error {
	operation := state.Operation
	if operation == nil || operation.WorktreePolicy == "" || operation.WorktreePolicy == worktreeRestoreNone || operation.WorktreeBoundaryCrossed {
		return nil
	}
	if operation.WorktreeFingerprint == "" {
		return nil
	}
	if err := a.verifyOperationWorktreeFingerprint(operation); err != nil {
		return err
	}
	operation.WorktreeBoundaryCrossed = true
	return a.git.WriteState(state)
}

func (g Git) RequireNoGitOperation() error {
	checks := []struct {
		path string
		name string
	}{
		{"rebase-merge", "rebase"},
		{"rebase-apply", "rebase or am"},
		{"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"},
		{"REVERT_HEAD", "revert"},
		{"sequencer", "sequencer"},
	}
	for _, check := range checks {
		path, err := g.GitPath(check.path)
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("cannot start graphene while a Git %s operation is in progress; finish or abort it first", check.name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect Git %s state: %w", check.name, err)
		}
	}
	return nil
}

func (g Git) RequireRecoverableWorktree() error {
	if err := g.RequireRecoverableWorktreeShallow(); err != nil {
		return err
	}
	rooted, err := g.Rooted()
	if err != nil {
		return err
	}
	return rooted.requireCleanInitializedSubmodules()
}

func (g Git) RequireRecoverableWorktreeShallow() error {
	rooted, err := g.Rooted()
	if err != nil {
		return err
	}
	if out, err := rooted.OutputBytes("ls-files", "--unmerged", "-z", "--"); err != nil {
		return err
	} else if len(out) > 0 {
		return fmt.Errorf("cannot start graphene with unmerged index entries")
	}
	if err := rooted.requireNoUnsafeIndexFlags(""); err != nil {
		return err
	}
	return nil
}

func (g Git) requireNoUnsafeIndexFlags(prefix string) error {
	rooted, err := g.Rooted()
	if err != nil {
		return err
	}
	out, err := rooted.OutputBytes("ls-files", "-v", "-z", "--")
	if err != nil {
		return err
	}
	for record := range bytes.SplitSeq(out, []byte{0}) {
		if len(record) < 3 || record[1] != ' ' {
			continue
		}
		flag := record[0]
		if flag == 'S' || (flag >= 'a' && flag <= 'z') {
			path := prefix + string(record[2:])
			return fmt.Errorf("cannot start graphene while %q uses skip-worktree or assume-unchanged; clear the index flag first", path)
		}
	}
	return nil
}

func (g Git) requireCleanInitializedSubmodules() error {
	rooted, err := g.Rooted()
	if err != nil {
		return err
	}
	root := rooted.Dir
	gitlinks, err := rooted.gitlinkPaths()
	if err != nil {
		return err
	}
	for _, path := range gitlinks {
		submoduleDir := filepath.Join(root, filepath.FromSlash(path))
		submodule := Git{Dir: submoduleDir}
		submoduleRoot, err := submodule.Root()
		if err != nil || filepath.Clean(submoduleRoot) != filepath.Clean(submoduleDir) {
			continue
		}
		if err := submodule.requireNoUnsafeIndexFlags(path + "/"); err != nil {
			return err
		}
		out, err := submodule.OutputBytes("-c", "diff.ignoreSubmodules=none", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
		if err != nil {
			return err
		}
		if len(out) > 0 {
			return fmt.Errorf("cannot start graphene while initialized submodule %q has local changes", path)
		}
		if err := submodule.requireCleanInitializedSubmodules(); err != nil {
			return err
		}
	}
	return nil
}

func (g Git) gitlinkPaths() ([]string, error) {
	rooted, err := g.Rooted()
	if err != nil {
		return nil, err
	}
	out, err := rooted.OutputBytes("ls-files", "--stage", "-z", "--")
	if err != nil {
		return nil, err
	}
	var paths []string
	for record := range bytes.SplitSeq(out, []byte{0}) {
		meta, path, ok := bytes.Cut(record, []byte{'\t'})
		if !ok || !bytes.HasPrefix(meta, []byte("160000 ")) {
			continue
		}
		paths = append(paths, string(path))
	}
	sort.Strings(paths)
	return paths, nil
}

func (a *App) requireNoIgnoredRebaseCollision(op RebaseOp) error {
	paths, err := a.rebaseCheckoutPaths(op)
	if err != nil {
		return err
	}
	action := fmt.Sprintf("rebase of %q onto %q", op.Top, op.Onto)
	if err := a.requireNoIgnoredPathCollision(paths, action); err != nil {
		return err
	}
	return a.git.requireNoUntrackedDirectoryPathCollision(paths, action)
}

func (a *App) requireNoIgnoredTreeCollision(revision, action string) error {
	git, err := a.recoveryGit()
	if err != nil {
		return err
	}
	return git.RequireNoTreeCollision(revision, action)
}

func (a *App) rebaseCheckoutPaths(op RebaseOp) ([]string, error) {
	git, err := a.recoveryGit()
	if err != nil {
		return nil, err
	}
	tree, err := git.OutputBytes("ls-tree", "-r", "--name-only", "-z", op.Onto, "--")
	if err != nil {
		return nil, err
	}
	paths := splitNullPaths(tree)
	commits, err := git.Output("rev-list", op.Upstream+".."+op.Top)
	if err != nil {
		return nil, err
	}
	for commit := range strings.FieldsSeq(commits) {
		out, err := git.OutputBytes("diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "-z", commit, "--")
		if err != nil {
			return nil, err
		}
		paths = append(paths, splitNullPaths(out)...)
	}
	return paths, nil
}

func (a *App) requireNoIgnoredPathCollision(tracked []string, action string) error {
	git, err := a.recoveryGit()
	if err != nil {
		return err
	}
	return git.requireNoIgnoredPathCollision(tracked, action)
}

func (g Git) requireNoIgnoredPathCollision(tracked []string, action string) error {
	rooted, err := g.Rooted()
	if err != nil {
		return err
	}
	ignored, err := rooted.OutputBytes("ls-files", "--others", "--ignored", "--exclude-standard", "--full-name", "-z", "--")
	if err != nil {
		return err
	}
	for _, ignoredPath := range splitNullPaths(ignored) {
		for _, trackedPath := range tracked {
			if pathsOverlap(ignoredPath, trackedPath) {
				return fmt.Errorf("cannot perform %s because ignored path %q would be overwritten; move or preserve it first", action, ignoredPath)
			}
		}
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func (g Git) RequireNoUntrackedDirectoryCollision(revision, action string) error {
	rooted, err := g.Rooted()
	if err != nil {
		return err
	}
	paths, gitlinks, err := rooted.treePaths(revision)
	if err != nil {
		return err
	}
	return rooted.requireNoUntrackedDirectoryPathCollisionWithGitlinks(paths, gitlinks, action)
}

func (g Git) RequireNoTreeCollision(revision, action string) error {
	rooted, err := g.Rooted()
	if err != nil {
		return err
	}
	paths, gitlinks, err := rooted.treePaths(revision)
	if err != nil {
		return err
	}
	if err := rooted.requireNoIgnoredPathCollision(paths, action); err != nil {
		return err
	}
	return rooted.requireNoUntrackedDirectoryPathCollisionWithGitlinks(paths, gitlinks, action)
}

func (g Git) treePaths(revision string) ([]string, map[string]bool, error) {
	rooted, err := g.Rooted()
	if err != nil {
		return nil, nil, err
	}
	out, err := rooted.OutputBytes("ls-tree", "-r", "-z", revision, "--")
	if err != nil {
		return nil, nil, err
	}
	var paths []string
	gitlinks := map[string]bool{}
	for record := range bytes.SplitSeq(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, path, ok := bytes.Cut(record, []byte{'\t'})
		fields := bytes.Fields(meta)
		if !ok || len(fields) != 3 || len(path) == 0 {
			return nil, nil, fmt.Errorf("parse tree entry %q", record)
		}
		name := string(path)
		paths = append(paths, name)
		if string(fields[0]) == "160000" {
			gitlinks[name] = true
		}
	}
	return paths, gitlinks, nil
}

func (g Git) requireNoUntrackedDirectoryPathCollision(tracked []string, action string) error {
	return g.requireNoUntrackedDirectoryPathCollisionWithGitlinks(tracked, nil, action)
}

func (g Git) requireNoUntrackedDirectoryPathCollisionWithGitlinks(tracked []string, targetGitlinks map[string]bool, action string) error {
	rooted, err := g.Rooted()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	check := func(path string) error {
		if path == "" || path == "." || seen[path] {
			return nil
		}
		seen[path] = true
		info, err := os.Stat(filepath.Join(rooted.Dir, filepath.FromSlash(path)))
		if errors.Is(err, os.ErrNotExist) || err == nil && !info.IsDir() {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect worktree path %q: %w", path, err)
		}
		exact, err := rooted.OutputBytes("ls-files", "--stage", "-z", "--", path)
		if err != nil {
			return err
		}
		if bytes.HasPrefix(exact, []byte("160000 ")) && (targetGitlinks == nil || targetGitlinks[path]) {
			return nil
		}
		if targetGitlinks != nil && targetGitlinks[path] {
			dir := filepath.Join(rooted.Dir, filepath.FromSlash(path))
			submoduleRoot, rootErr := (Git{Dir: dir}).Root()
			if rootErr == nil && filepath.Clean(submoduleRoot) == filepath.Clean(dir) {
				return nil
			}
		}
		indexed, err := rooted.OutputBytes("ls-files", "-z", "--", path+"/")
		if err != nil {
			return err
		}
		if len(indexed) == 0 {
			return fmt.Errorf("cannot perform %s because untracked directory %q would be replaced; move or preserve it first", action, path)
		}
		return nil
	}
	for _, path := range tracked {
		if err := check(path); err != nil {
			return err
		}
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(path))); parent != "." && parent != ""; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			if err := check(parent); err != nil {
				return err
			}
		}
	}
	return nil
}
