package graphene

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (g Git) requireNoGitOperation() error {
	for _, name := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "sequencer"} {
		path, err := g.GitPath(name)
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("finish or abort the existing Git operation (%s) first", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (g Git) snapshotIndex(index []byte) (Git, func(), error) {
	root, err := g.Output("rev-parse", "--show-toplevel")
	if err != nil {
		return Git{}, nil, err
	}
	file, err := os.CreateTemp("", "graphene-index-*")
	if err != nil {
		return Git{}, nil, err
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	if _, err := file.Write(index); err != nil {
		_ = file.Close()
		cleanup()
		return Git{}, nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return Git{}, nil, err
	}
	g.Dir = root
	g.indexFile = file.Name()
	return g, cleanup, nil
}

func (g Git) captureSnapshotWorktree(snapshot *operationSnapshot) error {
	paths, err := g.snapshotPaths()
	if err != nil {
		return err
	}
	path, err := g.GitPath("index")
	if err != nil {
		return err
	}
	index, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	private, cleanup, err := g.snapshotIndex(index)
	if err != nil {
		return err
	}
	defer cleanup()
	indexTree, err := private.Output("write-tree")
	if err != nil {
		return err
	}
	if len(paths) > 0 {
		var input bytes.Buffer
		for _, path := range paths {
			input.WriteString(":(top,literal)" + path)
			input.WriteByte(0)
		}
		if _, err := private.outputWithInput(&input, "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return err
		}
	}
	if err := private.requireSnapshotIndex(); err != nil {
		return err
	}
	worktreeTree, err := private.Output("write-tree")
	if err != nil {
		return err
	}
	after, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(index, after) {
		return fmt.Errorf("Git index changed while capturing the snapshot; retry the operation")
	}
	snapshot.Index = index
	snapshot.IndexTree = indexTree
	snapshot.WorktreeTree = worktreeTree
	return nil
}

func (g Git) requireSnapshotIndex() error {
	_, err := g.snapshotPaths()
	return err
}

func (g Git) snapshotPaths() ([]string, error) {
	root, err := g.Output("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	g.Dir = root
	shared, err := g.Output("rev-parse", "--shared-index-path")
	if err != nil {
		return nil, err
	}
	if shared != "" {
		return nil, fmt.Errorf("worktree snapshots do not support split indexes; run git update-index --no-split-index first")
	}
	files, err := g.OutputBytes("ls-files", "--stage", "-v", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for file := range bytes.SplitSeq(files, []byte{0}) {
		if len(file) == 0 {
			continue
		}
		header, path, ok := bytes.Cut(file, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("cannot read snapshot index entry %q", file)
		}
		fields := strings.Fields(string(header))
		if len(fields) != 4 {
			return nil, fmt.Errorf("cannot read snapshot index entry %q", file)
		}
		if fields[3] != "0" {
			return nil, fmt.Errorf("cannot snapshot %q: unmerged index entry; resolve and stage the conflict first", path)
		}
		if file[0] == 'S' || file[0] == 's' {
			return nil, fmt.Errorf("cannot snapshot %q: skip-worktree flag is set; sparse checkouts are not supported", path)
		}
		if file[0] >= 'a' && file[0] <= 'z' {
			return nil, fmt.Errorf("cannot snapshot %q: assume-unchanged flag is set; clear it before retrying", path)
		}
		if file[0] != 'H' {
			return nil, fmt.Errorf("cannot snapshot %q: unsupported index flag %q", path, file[:1])
		}
		if fields[1] != "160000" {
			paths = append(paths, string(path))
		}
	}
	untracked, err := g.OutputBytes("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for path := range strings.SplitSeq(string(untracked), "\x00") {
		// Git lists nested repositories as directories, including unborn repos.
		if path != "" && !strings.HasSuffix(path, "/") {
			paths = append(paths, path)
		}
	}
	repositories, err := g.nestedRepositories("")
	if err != nil {
		return nil, err
	}
	var input bytes.Buffer
	for _, path := range paths {
		for repository := range repositories {
			if pathsOverlap(path, repository) {
				return nil, nestedRepositoryCollision(repository, path)
			}
		}
		input.WriteString(path)
		input.WriteByte(0)
	}
	attrs, err := g.outputWithInput(&input, "check-attr", "-z", "--stdin", "filter", "working-tree-encoding")
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(attrs, []byte{0})
	for i := 0; i+2 < len(fields); i += 3 {
		value := string(fields[i+2])
		if value != "unspecified" && value != "unset" {
			return nil, fmt.Errorf("worktree snapshots do not support %s on %q", fields[i+1], fields[i])
		}
	}
	return paths, nil
}

func (g Git) preflightSnapshotWorktree(snapshot operationSnapshot) error {
	if err := g.checkNestedRepositories(snapshot.IndexTree, snapshot.WorktreeTree); err != nil {
		return err
	}
	if err := g.requireSnapshotIndex(); err != nil {
		return err
	}
	private, cleanup, err := g.snapshotIndex(snapshot.Index)
	if err != nil {
		return err
	}
	defer cleanup()
	indexTree, err := private.Output("write-tree")
	if err != nil {
		return err
	}
	if indexTree != snapshot.IndexTree {
		return fmt.Errorf("saved index does not match the snapshot tree")
	}
	target, err := private.OutputBytes("ls-tree", "-r", "--name-only", "-z", snapshot.WorktreeTree)
	if err != nil {
		return err
	}
	owned := map[string]bool{}
	for path := range strings.SplitSeq(string(target), "\x00") {
		if path != "" {
			owned[path] = true
		}
	}
	for _, ignored := range []bool{false, true} {
		args := []string{"ls-files", "--others", "--exclude-standard", "--full-name", "-z"}
		if ignored {
			args = append(args, "--ignored")
		}
		rooted := g
		rooted.Dir = private.Dir
		out, err := rooted.OutputBytes(args...)
		if err != nil {
			return err
		}
		for path := range strings.SplitSeq(string(out), "\x00") {
			path = strings.TrimSuffix(path, "/")
			if path == "" || owned[path] {
				continue
			}
			for target := range owned {
				if strings.HasPrefix(path, target+"/") || strings.HasPrefix(target, path+"/") {
					return fmt.Errorf("untracked path %q would be overwritten restoring the snapshot; move it aside first", path)
				}
			}
		}
	}
	return nil
}

func (g Git) restoreSnapshotWorktree(snapshot operationSnapshot) error {
	if err := g.Run("-c", "submodule.recurse=false", "read-tree", "--reset", "-u", snapshot.WorktreeTree); err != nil {
		return err
	}
	path, err := g.GitPath("index")
	if err != nil {
		return err
	}
	// read-tree has finished with the live index. Replace it atomically so an
	// interruption cannot leave a truncated index that blocks a second rollback.
	file, err := os.CreateTemp(filepath.Dir(path), "graphene-index-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(snapshot.Index); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return g.OutputErr("symbolic-ref", "HEAD", "refs/heads/"+snapshot.Branch)
}
