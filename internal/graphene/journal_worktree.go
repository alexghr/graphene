package graphene

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const untrackedSnapshotVersion = 1

const rawWorktreeArchiveMagic = "graphene-raw-worktree-v1\n"

const (
	rawWorktreeRegular          = "regular"
	rawWorktreeSymlink          = "symlink"
	rawWorktreeMissing          = "missing"
	rawWorktreeDirectory        = "directory"
	rawWorktreeMissingDirectory = "missing-directory"
)

type untrackedSnapshot struct {
	Version int                     `json:"version"`
	Files   []untrackedSnapshotFile `json:"files"`
}

type untrackedSnapshotFile struct {
	Path     string `json:"path"`
	Artifact string `json:"artifact"`
}

type loadedOperationArtifacts struct {
	Index       []byte
	Staged      []byte
	Worktree    []byte
	SharedIndex []byte
	Untracked   []loadedUntrackedFile
	RawWorktree *rawWorktreeSnapshot
}

type loadedUntrackedFile struct {
	Path  string
	Patch []byte
}

type rawWorktreeSnapshot struct {
	Files []rawWorktreeFile
}

type rawWorktreeFile struct {
	Path    string
	Kind    string
	Mode    uint32
	Tracked bool
	Data    []byte
}

type rawWorktreeCandidate struct {
	Path    string
	Tracked bool
}

func (a *App) operationWorktreeFingerprint() (string, error) {
	git, err := a.recoveryGit()
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	head, err := git.OutputBytes("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	addFingerprintValue(digest, "head", head)
	branch, err := git.Output("symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		if !isGitExit(err, 1) {
			return "", err
		}
		branch = ""
	}
	addFingerprintValue(digest, "symbolic-head", []byte(branch))
	index, err := git.GitPath("index")
	if err != nil {
		return "", err
	}
	if err := addFingerprintFile(digest, "index", index); err != nil {
		return "", fmt.Errorf("fingerprint git index: %w", err)
	}
	shared, err := git.Output("rev-parse", "--shared-index-path")
	if err != nil {
		return "", err
	}
	if shared != "" {
		if !filepath.IsAbs(shared) {
			shared = filepath.Join(filepath.Dir(index), shared)
		}
		if err := addFingerprintFile(digest, "shared-index", filepath.Clean(shared)); err != nil {
			return "", fmt.Errorf("fingerprint shared index: %w", err)
		}
	} else {
		addFingerprintValue(digest, "shared-index", nil)
	}
	paths, err := git.OutputBytes("ls-files", "--cached", "--others", "--exclude-standard", "--full-name", "-z", "--")
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	var worktreePaths []string
	directories := map[string]rawWorktreeFile{}
	for _, path := range splitNullPaths(paths) {
		if seen[path] {
			continue
		}
		seen[path] = true
		if !safeWorktreePath(path) {
			return "", fmt.Errorf("cannot fingerprint unsafe worktree path %q", path)
		}
		parents, _, err := rawWorktreeParentDirectories(git.Dir, path)
		if err != nil {
			return "", fmt.Errorf("fingerprint worktree path %q: %w", path, err)
		}
		for _, directory := range parents {
			directories[directory.Path] = directory
		}
		worktreePaths = append(worktreePaths, path)
	}
	directoryPaths := make([]string, 0, len(directories))
	for path := range directories {
		directoryPaths = append(directoryPaths, path)
	}
	sort.Strings(directoryPaths)
	for _, path := range directoryPaths {
		if err := addFingerprintFile(digest, "worktree-directory:"+path, filepath.Join(git.Dir, filepath.FromSlash(path))); err != nil {
			return "", fmt.Errorf("fingerprint worktree directory %q: %w", path, err)
		}
	}
	for _, path := range worktreePaths {
		if err := addFingerprintFile(digest, "worktree:"+path, filepath.Join(git.Dir, filepath.FromSlash(path))); err != nil {
			return "", fmt.Errorf("fingerprint worktree path %q: %w", path, err)
		}
	}
	submodules, err := a.snapshotRecoverableSyncSubmodules()
	if err != nil {
		return "", err
	}
	submoduleData, err := json.Marshal(submodules)
	if err != nil {
		return "", err
	}
	addFingerprintValue(digest, "submodules", submoduleData)
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func addFingerprintValue(digest hash.Hash, label string, data []byte) {
	fmt.Fprintf(digest, "%d:%s:%d:", len(label), label, len(data))
	_, _ = digest.Write(data)
}

func addFingerprintFile(digest hash.Hash, label, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		addFingerprintValue(digest, label+":missing", nil)
		return nil
	}
	if err != nil {
		return err
	}
	addFingerprintValue(digest, label+":mode", fmt.Appendf(nil, "%#o", info.Mode()))
	switch {
	case info.Mode().IsRegular():
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		addFingerprintValue(digest, label+":file", data)
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		addFingerprintValue(digest, label+":symlink", []byte(target))
	case info.IsDir():
		addFingerprintValue(digest, label+":directory", nil)
	default:
		return fmt.Errorf("unsupported file type %s", info.Mode().Type())
	}
	return nil
}

func (a *App) snapshotOperationUntracked(operation *OperationJournal) error {
	git, err := a.recoveryGit()
	if err != nil {
		return err
	}
	out, err := git.OutputBytes("ls-files", "--others", "--exclude-standard", "--full-name", "-z", "--")
	if err != nil {
		return err
	}
	_, err = a.snapshotOperationPaths(operation, splitNullPaths(out))
	return err
}

func (a *App) snapshotOperationRawWorktree(operation *OperationJournal) error {
	git, err := a.recoveryGit()
	if err != nil {
		return err
	}
	cached, err := git.OutputBytes("ls-files", "--cached", "--full-name", "-z", "--")
	if err != nil {
		return err
	}
	gitlinks, err := git.indexGitlinks()
	if err != nil {
		return err
	}
	skip := make(map[string]bool, len(gitlinks))
	for _, gitlink := range gitlinks {
		skip[gitlink.Path] = true
	}
	seen := map[string]bool{}
	var candidates []rawWorktreeCandidate
	for _, path := range splitNullPaths(cached) {
		if skip[path] || seen[path] {
			continue
		}
		seen[path] = true
		candidates = append(candidates, rawWorktreeCandidate{Path: path, Tracked: true})
	}
	others, err := git.OutputBytes("ls-files", "--others", "--exclude-standard", "--full-name", "-z", "--")
	if err != nil {
		return err
	}
	for _, path := range splitNullPaths(others) {
		if seen[path] {
			continue
		}
		seen[path] = true
		candidates = append(candidates, rawWorktreeCandidate{Path: path})
	}
	_, err = a.snapshotOperationRawPaths(operation, candidates, true)
	return err
}

func (a *App) snapshotOperationAddedPaths(operation *OperationJournal) (bool, error) {
	if operation.WorktreePolicy != worktreeRestoreHard && operation.WorktreePolicy != worktreeRestoreIndex {
		return false, nil
	}
	git, err := a.recoveryGit()
	if err != nil {
		return false, err
	}
	out, err := git.OutputBytes("diff", "--no-relative", "--no-ext-diff", "--no-textconv", "--default-prefix", "--name-only", "--diff-filter=A", "-z", operation.OriginalHead, "--")
	if err != nil {
		return false, err
	}
	paths := splitNullPaths(out)
	if operation.RawWorktreeArtifact != "" {
		candidates := make([]rawWorktreeCandidate, 0, len(paths))
		for _, path := range paths {
			candidates = append(candidates, rawWorktreeCandidate{Path: path})
		}
		return a.snapshotOperationRawPaths(operation, candidates, false)
	}
	return a.snapshotOperationPaths(operation, paths)
}

func (a *App) snapshotOperationRawPaths(operation *OperationJournal, candidates []rawWorktreeCandidate, createEmpty bool) (bool, error) {
	root, err := a.git.Root()
	if err != nil {
		return false, err
	}
	snapshot := rawWorktreeSnapshot{}
	if operation.RawWorktreeArtifact != "" {
		data, err := a.readVerifiedOperationArtifact(operation, operation.RawWorktreeArtifact)
		if err != nil {
			return false, err
		}
		snapshot, err = decodeRawWorktreeSnapshot(data)
		if err != nil {
			return false, err
		}
	}
	existing := make(map[string]string, len(snapshot.Files))
	for _, file := range snapshot.Files {
		existing[file.Path] = file.Kind
	}
	changed := operation.RawWorktreeArtifact == "" && createEmpty
	for _, candidate := range candidates {
		if existing[candidate.Path] != "" {
			continue
		}
		if !safeWorktreePath(candidate.Path) {
			return false, fmt.Errorf("cannot snapshot unsafe raw worktree path %q", candidate.Path)
		}
		blocked := false
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(candidate.Path))); parent != "." && parent != ""; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			if kind := existing[parent]; kind != "" && kind != rawWorktreeDirectory {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		directories, _, err := rawWorktreeParentDirectories(root, candidate.Path)
		if err != nil {
			return false, fmt.Errorf("inspect raw worktree path %q: %w", candidate.Path, err)
		}
		for _, directory := range directories {
			if existing[directory.Path] != "" {
				continue
			}
			snapshot.Files = append(snapshot.Files, directory)
			existing[directory.Path] = directory.Kind
			changed = true
		}
		file, exists, err := readRawWorktreeFile(root, candidate)
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		snapshot.Files = append(snapshot.Files, file)
		existing[file.Path] = file.Kind
		changed = true
	}
	if !changed {
		return false, nil
	}
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	data, err := encodeRawWorktreeSnapshot(snapshot)
	if err != nil {
		return false, err
	}
	if _, err := decodeRawWorktreeSnapshot(data); err != nil {
		return false, fmt.Errorf("validate raw worktree snapshot before publication: %w", err)
	}
	dir, err := a.operationArtifactDir(operation)
	if err != nil {
		return false, err
	}
	artifact := nextOperationArtifactName(operation, "worktree-raw-", ".tar")
	if err := writeAtomicFile(filepath.Join(dir, artifact), data, 0o600); err != nil {
		return false, fmt.Errorf("write raw worktree snapshot: %w", err)
	}
	operation.RawWorktreeArtifact = artifact
	recordOperationArtifact(operation, artifact, data)
	return true, nil
}

func readRawWorktreeFile(root string, candidate rawWorktreeCandidate) (rawWorktreeFile, bool, error) {
	parentsExist, err := ensureRawWorktreeParents(root, candidate.Path, false)
	if err != nil {
		return rawWorktreeFile{}, false, fmt.Errorf("inspect raw worktree path %q: %w", candidate.Path, err)
	}
	if !parentsExist {
		if candidate.Tracked {
			return rawWorktreeFile{Path: candidate.Path, Kind: rawWorktreeMissing, Tracked: true}, true, nil
		}
		return rawWorktreeFile{}, false, nil
	}
	path := filepath.Join(root, filepath.FromSlash(candidate.Path))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if candidate.Tracked {
			return rawWorktreeFile{Path: candidate.Path, Kind: rawWorktreeMissing, Tracked: true}, true, nil
		}
		return rawWorktreeFile{}, false, nil
	}
	if err != nil {
		return rawWorktreeFile{}, false, fmt.Errorf("inspect raw worktree path %q: %w", candidate.Path, err)
	}
	file := rawWorktreeFile{Path: candidate.Path, Mode: uint32(info.Mode().Perm()), Tracked: candidate.Tracked}
	switch {
	case info.Mode().IsRegular():
		file.Kind = rawWorktreeRegular
		file.Data, err = os.ReadFile(path)
	case info.Mode()&os.ModeSymlink != 0:
		file.Kind = rawWorktreeSymlink
		var target string
		target, err = os.Readlink(path)
		file.Data = []byte(target)
	default:
		return rawWorktreeFile{}, false, fmt.Errorf("cannot safely snapshot worktree path %q with type %s", candidate.Path, info.Mode().Type())
	}
	if err != nil {
		return rawWorktreeFile{}, false, fmt.Errorf("read raw worktree path %q: %w", candidate.Path, err)
	}
	return file, true, nil
}

func rawWorktreeParentDirectories(root, path string) ([]rawWorktreeFile, bool, error) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	current := root
	directories := make([]rawWorktreeFile, 0, len(parts)-1)
	for index, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			directories = append(directories, rawWorktreeFile{
				Path: strings.Join(parts[:index+1], "/"),
				Kind: rawWorktreeMissingDirectory,
			})
			return directories, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, false, fmt.Errorf("cannot access worktree path %q through non-directory parent %q", path, strings.Join(parts[:index+1], "/"))
		}
		directories = append(directories, rawWorktreeFile{
			Path: strings.Join(parts[:index+1], "/"),
			Kind: rawWorktreeDirectory,
			Mode: uint32(info.Mode().Perm()),
		})
	}
	return directories, true, nil
}

func encodeRawWorktreeSnapshot(snapshot rawWorktreeSnapshot) ([]byte, error) {
	var data bytes.Buffer
	data.WriteString(rawWorktreeArchiveMagic)
	archive := tar.NewWriter(&data)
	for _, file := range snapshot.Files {
		header := &tar.Header{
			Name:     file.Path,
			Mode:     int64(file.Mode),
			Size:     int64(len(file.Data)),
			Typeflag: tar.TypeReg,
			Format:   tar.FormatPAX,
			PAXRecords: map[string]string{
				"graphene.kind":    file.Kind,
				"graphene.tracked": fmt.Sprintf("%t", file.Tracked),
			},
		}
		if err := archive.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("encode raw worktree path %q: %w", file.Path, err)
		}
		if len(file.Data) > 0 {
			if _, err := archive.Write(file.Data); err != nil {
				return nil, fmt.Errorf("encode raw worktree path %q: %w", file.Path, err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("finish raw worktree snapshot: %w", err)
	}
	return data.Bytes(), nil
}

func decodeRawWorktreeSnapshot(data []byte) (rawWorktreeSnapshot, error) {
	var snapshot rawWorktreeSnapshot
	if !bytes.HasPrefix(data, []byte(rawWorktreeArchiveMagic)) {
		return snapshot, fmt.Errorf("raw worktree snapshot has an invalid header")
	}
	reader := tar.NewReader(bytes.NewReader(data[len(rawWorktreeArchiveMagic):]))
	seen := map[string]bool{}
	kinds := map[string]string{}
	last := ""
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return snapshot, fmt.Errorf("parse raw worktree snapshot: %w", err)
		}
		if header.Typeflag != tar.TypeReg || !safeWorktreePath(header.Name) || seen[header.Name] || last != "" && header.Name <= last {
			return snapshot, fmt.Errorf("raw worktree snapshot contains invalid or unsorted path %q", header.Name)
		}
		kind := header.PAXRecords["graphene.kind"]
		trackedValue := header.PAXRecords["graphene.tracked"]
		if kind != rawWorktreeRegular && kind != rawWorktreeSymlink && kind != rawWorktreeMissing && kind != rawWorktreeDirectory && kind != rawWorktreeMissingDirectory {
			return snapshot, fmt.Errorf("raw worktree snapshot path %q has invalid kind %q", header.Name, kind)
		}
		if trackedValue != "true" && trackedValue != "false" {
			return snapshot, fmt.Errorf("raw worktree snapshot path %q has invalid tracked flag %q", header.Name, trackedValue)
		}
		if header.Mode < 0 || header.Mode > 0o777 {
			return snapshot, fmt.Errorf("raw worktree snapshot path %q has invalid mode %#o", header.Name, header.Mode)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			return snapshot, fmt.Errorf("read raw worktree snapshot path %q: %w", header.Name, err)
		}
		if kind == rawWorktreeMissing && (len(content) != 0 || trackedValue != "true") {
			return snapshot, fmt.Errorf("raw worktree snapshot has invalid missing path %q", header.Name)
		}
		if kind == rawWorktreeDirectory && (len(content) != 0 || trackedValue != "false") {
			return snapshot, fmt.Errorf("raw worktree snapshot has invalid directory %q", header.Name)
		}
		if kind == rawWorktreeMissingDirectory && (len(content) != 0 || trackedValue != "false") {
			return snapshot, fmt.Errorf("raw worktree snapshot has invalid missing directory %q", header.Name)
		}
		file := rawWorktreeFile{Path: header.Name, Kind: kind, Mode: uint32(header.Mode), Tracked: trackedValue == "true", Data: content}
		snapshot.Files = append(snapshot.Files, file)
		seen[file.Path] = true
		kinds[file.Path] = file.Kind
		last = file.Path
	}
	for _, file := range snapshot.Files {
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(file.Path))); parent != "." && parent != ""; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			if seen[parent] {
				if kinds[parent] == rawWorktreeDirectory {
					continue
				}
				if kinds[parent] == rawWorktreeMissingDirectory && (file.Kind == rawWorktreeMissing || file.Kind == rawWorktreeMissingDirectory) {
					continue
				}
				return snapshot, fmt.Errorf("raw worktree snapshot path %q overlaps %q", file.Path, parent)
			}
		}
	}
	canonical, err := encodeRawWorktreeSnapshot(snapshot)
	if err != nil {
		return snapshot, err
	}
	if !bytes.Equal(canonical, data) {
		return snapshot, fmt.Errorf("raw worktree snapshot is not canonical")
	}
	return snapshot, nil
}

func (a *App) restoreOperationRawWorktree(snapshot rawWorktreeSnapshot) error {
	root, err := a.git.Root()
	if err != nil {
		return err
	}
	directories := make([]rawWorktreeFile, 0)
	missingDirectories := make([]rawWorktreeFile, 0)
	for _, file := range snapshot.Files {
		switch file.Kind {
		case rawWorktreeDirectory:
			directories = append(directories, file)
		case rawWorktreeMissingDirectory:
			missingDirectories = append(missingDirectories, file)
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		leftDepth := strings.Count(directories[i].Path, "/")
		rightDepth := strings.Count(directories[j].Path, "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return directories[i].Path < directories[j].Path
	})
	for _, directory := range directories {
		if err := createRawWorktreeDirectory(root, directory.Path); err != nil {
			return err
		}
	}
	for _, tracked := range []bool{true, false} {
		for _, file := range snapshot.Files {
			if file.Kind == rawWorktreeDirectory || file.Kind == rawWorktreeMissingDirectory || file.Tracked != tracked {
				continue
			}
			parentsExist, err := ensureRawWorktreeParents(root, file.Path, file.Kind != rawWorktreeMissing)
			if err != nil {
				return err
			}
			if !parentsExist && file.Kind == rawWorktreeMissing {
				continue
			}
			path := filepath.Join(root, filepath.FromSlash(file.Path))
			if !file.Tracked {
				matches, err := rawWorktreeFileMatches(path, file)
				if err != nil {
					return fmt.Errorf("inspect pre-operation untracked path %q: %w", file.Path, err)
				}
				if matches {
					continue
				}
				if _, err := os.Lstat(path); err == nil {
					return fmt.Errorf("cannot restore pre-operation untracked file %q because a different path now exists there; move it aside and rerun graphene abort", file.Path)
				} else if !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("inspect pre-operation untracked path %q: %w", file.Path, err)
				}
			}
			if err := restoreRawWorktreeFile(path, file); err != nil {
				return fmt.Errorf("restore raw worktree path %q: %w", file.Path, err)
			}
		}
	}
	sort.Slice(missingDirectories, func(i, j int) bool {
		leftDepth := strings.Count(missingDirectories[i].Path, "/")
		rightDepth := strings.Count(missingDirectories[j].Path, "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return missingDirectories[i].Path > missingDirectories[j].Path
	})
	for _, directory := range missingDirectories {
		path := filepath.Join(root, filepath.FromSlash(directory.Path))
		if err := removeRawWorktreeDirectoryTree(path); err != nil {
			return fmt.Errorf("restore missing worktree directory %q: %w", directory.Path, err)
		}
	}
	for _, directory := range slices.Backward(directories) {
		path := filepath.Join(root, filepath.FromSlash(directory.Path))
		if err := chmodRawWorktreeDirectory(path, os.FileMode(directory.Mode)); err != nil {
			return fmt.Errorf("restore worktree directory mode for %q: %w", directory.Path, err)
		}
	}
	for _, file := range snapshot.Files {
		parentsExist, err := ensureRawWorktreeParents(root, file.Path, false)
		if err != nil {
			return err
		}
		if !parentsExist && file.Kind == rawWorktreeMissing {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		matches, err := rawWorktreeFileMatches(path, file)
		if err != nil {
			return fmt.Errorf("verify restored raw worktree path %q: %w", file.Path, err)
		}
		if !matches {
			return fmt.Errorf("restored raw worktree path %q does not match its journal snapshot", file.Path)
		}
	}
	return nil
}

func ensureRawWorktreeParents(root, path string, create bool) (bool, error) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	current := root
	for index, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if !create {
				return false, nil
			}
			if err := os.Mkdir(current, 0o755); err != nil {
				return false, fmt.Errorf("create worktree parent for %q: %w", path, err)
			}
			if err := syncDirectory(filepath.Dir(current)); err != nil {
				return false, err
			}
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect worktree parent for %q: %w", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("cannot access worktree path %q through non-directory parent %q", path, strings.Join(parts[:index+1], "/"))
		}
	}
	return true, nil
}

func createRawWorktreeDirectory(root, path string) error {
	parentsExist, err := ensureRawWorktreeParents(root, path, true)
	if err != nil {
		return err
	}
	if !parentsExist {
		return fmt.Errorf("cannot create worktree directory %q", path)
	}
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Lstat(fullPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(fullPath, 0o700); err != nil {
			return fmt.Errorf("create worktree directory %q: %w", path, err)
		}
		return syncDirectory(filepath.Dir(fullPath))
	}
	if err != nil {
		return fmt.Errorf("inspect worktree directory %q: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cannot restore worktree directory %q over a non-directory path", path)
	}
	return nil
}

func chmodRawWorktreeDirectory(path string, mode os.FileMode) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		_ = directory.Close()
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}

func rawWorktreeFileMatches(path string, file rawWorktreeFile) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return file.Kind == rawWorktreeMissing || file.Kind == rawWorktreeMissingDirectory, nil
	}
	if err != nil {
		return false, err
	}
	switch file.Kind {
	case rawWorktreeMissing:
		return false, nil
	case rawWorktreeRegular:
		if !info.Mode().IsRegular() || uint32(info.Mode().Perm()) != file.Mode {
			return false, nil
		}
		data, err := os.ReadFile(path)
		return bytes.Equal(data, file.Data), err
	case rawWorktreeSymlink:
		if info.Mode()&os.ModeSymlink == 0 {
			return false, nil
		}
		target, err := os.Readlink(path)
		return target == string(file.Data), err
	case rawWorktreeDirectory:
		return info.IsDir() && info.Mode()&os.ModeSymlink == 0 && uint32(info.Mode().Perm()) == file.Mode, nil
	case rawWorktreeMissingDirectory:
		return false, nil
	default:
		return false, fmt.Errorf("unsupported raw worktree kind %q", file.Kind)
	}
}

func restoreRawWorktreeFile(path string, file rawWorktreeFile) error {
	switch file.Kind {
	case rawWorktreeMissing:
		return removeRawWorktreePath(path)
	case rawWorktreeRegular:
		return replaceRawWorktreeRegular(path, file.Data, os.FileMode(file.Mode))
	case rawWorktreeSymlink:
		return replaceRawWorktreeSymlink(path, string(file.Data))
	default:
		return fmt.Errorf("unsupported raw worktree kind %q", file.Kind)
	}
}

func removeRawWorktreeDirectoryTree(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse to remove non-directory path")
	}
	var directories []string
	var inspect func(string) error
	inspect = func(directory string) error {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			child := filepath.Join(directory, entry.Name())
			info, err := os.Lstat(child)
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refuse to remove directory containing %q", child)
			}
			if err := inspect(child); err != nil {
				return err
			}
		}
		directories = append(directories, directory)
		return nil
	}
	if err := inspect(path); err != nil {
		return err
	}
	for _, directory := range directories {
		if err := os.Remove(directory); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(directory)); err != nil {
			return err
		}
	}
	return nil
}

func removeRawWorktreePath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("refuse to remove non-empty directory: %w", err)
		}
	} else if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func prepareRawWorktreeDestination(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("refuse to replace non-empty directory: %w", err)
		}
	}
	return nil
}

func replaceRawWorktreeRegular(path string, data []byte, mode os.FileMode) error {
	if err := prepareRawWorktreeDestination(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".graphene-restore-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func replaceRawWorktreeSymlink(path, target string) error {
	if err := prepareRawWorktreeDestination(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".graphene-restore-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	if err := os.Remove(tempName); err != nil {
		return err
	}
	defer os.Remove(tempName)
	if err := os.Symlink(target, tempName); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func (a *App) snapshotOperationPaths(operation *OperationJournal, paths []string) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	root, err := a.git.Root()
	if err != nil {
		return false, err
	}
	dir, err := a.operationArtifactDir(operation)
	if err != nil {
		return false, err
	}
	snapshot := untrackedSnapshot{Version: untrackedSnapshotVersion}
	if operation.UntrackedArtifact != "" {
		data, err := a.readVerifiedOperationArtifact(operation, operation.UntrackedArtifact)
		if err != nil {
			return false, err
		}
		snapshot, err = decodeUntrackedSnapshot(data)
		if err != nil {
			return false, err
		}
	}
	existing := map[string]bool{}
	for _, file := range snapshot.Files {
		existing[file.Path] = true
	}
	changed := false
	for _, path := range paths {
		if existing[path] {
			continue
		}
		if !safeWorktreePath(path) {
			return false, fmt.Errorf("cannot snapshot unsafe untracked path %q", path)
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, fmt.Errorf("inspect untracked snapshot path %q: %w", path, err)
		}
		patch, err := a.git.untrackedFilePatch(root, path)
		if err != nil {
			return false, fmt.Errorf("snapshot untracked file %q: %w", path, err)
		}
		artifact := nextOperationArtifactName(operation, "untracked-", ".patch")
		if err := writeAtomicFile(filepath.Join(dir, artifact), patch, 0o600); err != nil {
			return false, fmt.Errorf("write untracked snapshot for %q: %w", path, err)
		}
		recordOperationArtifact(operation, artifact, patch)
		snapshot.Files = append(snapshot.Files, untrackedSnapshotFile{Path: path, Artifact: artifact})
		existing[path] = true
		changed = true
	}
	if !changed {
		return false, nil
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return false, fmt.Errorf("encode untracked snapshot: %w", err)
	}
	data = append(data, '\n')
	manifest := nextOperationArtifactName(operation, "untracked-", ".json")
	if err := writeAtomicFile(filepath.Join(dir, manifest), data, 0o600); err != nil {
		return false, fmt.Errorf("write untracked snapshot manifest: %w", err)
	}
	operation.UntrackedArtifact = manifest
	recordOperationArtifact(operation, manifest, data)
	return true, nil
}

func recordOperationArtifact(operation *OperationJournal, name string, data []byte) {
	if operation.ArtifactDigests == nil {
		operation.ArtifactDigests = map[string]string{}
	}
	digest := sha256.Sum256(data)
	operation.ArtifactDigests[name] = fmt.Sprintf("%x", digest)
}

func nextOperationArtifactName(operation *OperationJournal, prefix, suffix string) string {
	for index := 0; ; index++ {
		name := fmt.Sprintf("%s%04d%s", prefix, index, suffix)
		if operation.ArtifactDigests[name] == "" {
			return name
		}
	}
}

func (a *App) readVerifiedOperationArtifact(operation *OperationJournal, name string) ([]byte, error) {
	want := operation.ArtifactDigests[name]
	if want == "" {
		return nil, fmt.Errorf("operation artifact %q is missing its digest", name)
	}
	dir, err := a.operationArtifactDir(operation)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("read operation artifact %q: %w", name, err)
	}
	digest := sha256.Sum256(data)
	got := fmt.Sprintf("%x", digest)
	if !strings.EqualFold(got, want) {
		return nil, fmt.Errorf("operation artifact %q failed its integrity check", name)
	}
	return data, nil
}

func (a *App) loadOperationArtifacts(operation *OperationJournal) (loadedOperationArtifacts, error) {
	var loaded loadedOperationArtifacts
	if operation.WorktreeRestored {
		return loaded, nil
	}
	read := func(name string) ([]byte, error) {
		if name == "" {
			return nil, nil
		}
		return a.readVerifiedOperationArtifact(operation, name)
	}
	var err error
	if operation.WorktreePolicy == worktreeRestoreIndex {
		if operation.IndexArtifact == "" || operation.StagedArtifact == "" || operation.RawWorktreeArtifact == "" && operation.WorktreeArtifact == "" {
			return loaded, fmt.Errorf("%s operation is missing its index recovery artifacts", operation.Kind)
		}
		loaded.Index, err = read(operation.IndexArtifact)
		if err != nil {
			return loaded, err
		}
		loaded.Staged, err = read(operation.StagedArtifact)
		if err != nil {
			return loaded, err
		}
		loaded.Worktree, err = read(operation.WorktreeArtifact)
		if err != nil {
			return loaded, err
		}
		if operation.SharedIndexArtifact != "" {
			loaded.SharedIndex, err = read(operation.SharedIndexArtifact)
			if err != nil {
				return loaded, err
			}
		}
	}
	if operation.RawWorktreeArtifact != "" {
		data, err := read(operation.RawWorktreeArtifact)
		if err != nil {
			return loaded, err
		}
		snapshot, err := decodeRawWorktreeSnapshot(data)
		if err != nil {
			return loaded, err
		}
		loaded.RawWorktree = &snapshot
	}
	if operation.UntrackedArtifact == "" {
		return loaded, nil
	}
	data, err := read(operation.UntrackedArtifact)
	if err != nil {
		return loaded, err
	}
	snapshot, err := decodeUntrackedSnapshot(data)
	if err != nil {
		return loaded, err
	}
	for _, file := range snapshot.Files {
		patch, err := read(file.Artifact)
		if err != nil {
			return loaded, err
		}
		loaded.Untracked = append(loaded.Untracked, loadedUntrackedFile{Path: file.Path, Patch: patch})
	}
	return loaded, nil
}

func (a *App) restoreOperationUntracked(files []loadedUntrackedFile) error {
	if len(files) == 0 {
		return nil
	}
	root, err := a.git.Root()
	if err != nil {
		return err
	}
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if _, err := os.Lstat(path); err == nil {
			matches, checkErr := untrackedPatchMatches(root, file.Patch)
			if checkErr != nil {
				return fmt.Errorf("verify untracked file %q before restore: %w", file.Path, checkErr)
			}
			if matches {
				continue
			}
			return fmt.Errorf("cannot restore pre-operation untracked file %q because a different path now exists there; move it aside and rerun graphene abort", file.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect untracked path %q: %w", file.Path, err)
		}
		git := a.git
		git.Dir = root
		if err := git.RunWithInput(string(file.Patch), "apply", "--whitespace=nowarn"); err != nil {
			return fmt.Errorf("restore untracked file %q: %w", file.Path, err)
		}
	}
	return nil
}

func decodeUntrackedSnapshot(data []byte) (untrackedSnapshot, error) {
	var snapshot untrackedSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshot, fmt.Errorf("parse untracked snapshot: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return snapshot, fmt.Errorf("parse untracked snapshot: %w", err)
	}
	if snapshot.Version != untrackedSnapshotVersion {
		return snapshot, fmt.Errorf("unsupported untracked snapshot version %d", snapshot.Version)
	}
	seenPaths := map[string]bool{}
	seenArtifacts := map[string]bool{}
	for _, file := range snapshot.Files {
		if !safeWorktreePath(file.Path) {
			return snapshot, fmt.Errorf("untracked snapshot contains unsafe path %q", file.Path)
		}
		if filepath.Base(file.Artifact) != file.Artifact || !strings.HasPrefix(file.Artifact, "untracked-") || !strings.HasSuffix(file.Artifact, ".patch") {
			return snapshot, fmt.Errorf("untracked snapshot contains unsafe artifact %q", file.Artifact)
		}
		if seenPaths[file.Path] {
			return snapshot, fmt.Errorf("untracked snapshot contains path %q more than once", file.Path)
		}
		if seenArtifacts[file.Artifact] {
			return snapshot, fmt.Errorf("untracked snapshot contains artifact %q more than once", file.Artifact)
		}
		seenPaths[file.Path] = true
		seenArtifacts[file.Artifact] = true
	}
	return snapshot, nil
}

func splitNullPaths(data []byte) []string {
	fields := bytes.Split(data, []byte{0})
	paths := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) > 0 {
			paths = append(paths, string(field))
		}
	}
	return paths
}

func safeWorktreePath(path string) bool {
	if path == "" || !utf8.ValidString(path) || filepath.IsAbs(path) || filepath.Clean(path) != filepath.FromSlash(path) {
		return false
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return parts[0] != ".git"
}

func (g Git) untrackedFilePatch(root, path string) ([]byte, error) {
	args := []string{"diff", "--no-index", "--binary", "--full-index", "--no-relative", "--no-ext-diff", "--no-textconv", "--default-prefix", "--", os.DevNull, path}
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return nil, fmt.Errorf("git diff reported no change")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		if stdout.Len() == 0 {
			return nil, fmt.Errorf("git diff produced an empty patch")
		}
		return stdout.Bytes(), nil
	}
	return nil, gitCommandError(args, err, stderr.String(), false)
}

func untrackedPatchMatches(root string, patch []byte) (bool, error) {
	args := []string{"apply", "--reverse", "--check", "--whitespace=nowarn"}
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Stdin = bytes.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, gitCommandError(args, err, stderr.String(), false)
}
