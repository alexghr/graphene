package graphene

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type syncSubmoduleTransition struct {
	Target  syncSubmodule
	Allowed map[string]bool
}

type syncSubmoduleBackup struct {
	Path string `json:"path"`
	Head string `json:"head"`
	Ref  string `json:"ref"`
}

type gitlinkEntry struct {
	Path string
	OID  string
}

func planSyncSubmoduleBackups(operation *OperationJournal, initial []syncSubmodule) ([]syncSubmoduleBackup, error) {
	initial = append([]syncSubmodule(nil), initial...)
	sortSyncSubmodules(initial)
	if err := validateSyncSubmoduleSnapshot(initial); err != nil {
		return nil, err
	}
	backups := make([]syncSubmoduleBackup, 0, len(initial))
	for index, submodule := range initial {
		backups = append(backups, syncSubmoduleBackup{
			Path: submodule.Path,
			Head: submodule.Head,
			Ref:  fmt.Sprintf("refs/graphene/journal/%s/submodules/%04d", operation.ID, index),
		})
	}
	return backups, validateSyncSubmoduleBackups(operation, initial, backups)
}

func validateSyncSubmoduleBackups(operation *OperationJournal, initial []syncSubmodule, backups []syncSubmoduleBackup) error {
	if len(backups) == 0 {
		return nil
	}
	initial = append([]syncSubmodule(nil), initial...)
	sortSyncSubmodules(initial)
	if len(backups) != len(initial) {
		return fmt.Errorf("have %d backups for %d initialized submodules", len(backups), len(initial))
	}
	prefix := "refs/graphene/journal/" + operation.ID + "/submodules/"
	for index, backup := range backups {
		want := initial[index]
		wantRef := fmt.Sprintf("%s%04d", prefix, index)
		if backup.Path != want.Path || backup.Head != want.Head {
			return fmt.Errorf("backup %d does not match initialized submodule %q at %s", index, want.Path, want.Head)
		}
		if !safeWorktreePath(backup.Path) || !validObjectID(backup.Head) {
			return fmt.Errorf("invalid backup for initialized submodule %q", backup.Path)
		}
		if backup.Ref != wantRef || !validJournalRef(backup.Ref) {
			return fmt.Errorf("unsafe backup ref %q for initialized submodule %q", backup.Ref, backup.Path)
		}
	}
	return nil
}

func (a *App) installSyncSubmoduleBackups(progress syncJournalProgress, requireInitial bool) error {
	if len(progress.SubmoduleBackups) == 0 {
		return nil
	}
	initial := make(map[string]syncSubmodule, len(progress.InitialSubmodules))
	for _, submodule := range progress.InitialSubmodules {
		initial[submodule.Path] = submodule
	}
	for _, backup := range progress.SubmoduleBackups {
		git, err := a.syncSubmoduleGit(backup.Path)
		if err != nil {
			return err
		}
		if requireInitial {
			if err := requireCleanSyncSubmodule(git, backup.Path); err != nil {
				return err
			}
			current, err := currentSyncSubmodule(git, backup.Path)
			if err != nil {
				return err
			}
			if current != initial[backup.Path] {
				return fmt.Errorf("initialized submodule %q changed before its journal backup was installed", backup.Path)
			}
		}
		if _, err := git.Output("cat-file", "-e", backup.Head+"^{commit}"); err != nil {
			return fmt.Errorf("journal backup target %s for initialized submodule %q is unavailable: %w", backup.Head, backup.Path, err)
		}
		actual, err := git.RefValue(backup.Ref)
		if err != nil {
			return err
		}
		want := RefValue{Exists: true, OID: backup.Head}
		if actual.Exists && actual != want {
			return fmt.Errorf("submodule journal backup %s moved from %s to %s", backup.Ref, backup.Head, actual.OID)
		}
		if !actual.Exists {
			if err := git.UpdateRefs([]refEdit{{Ref: backup.Ref, New: want}}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) verifySyncSubmoduleBackups(progress syncJournalProgress) error {
	for _, backup := range progress.SubmoduleBackups {
		git, err := a.syncSubmoduleGit(backup.Path)
		if err != nil {
			return err
		}
		actual, err := git.RefValue(backup.Ref)
		if err != nil {
			return err
		}
		want := RefValue{Exists: true, OID: backup.Head}
		if actual != want {
			return fmt.Errorf("submodule journal backup %s is %s, want %s", backup.Ref, formatRefValue(actual), formatRefValue(want))
		}
	}
	return nil
}

func (a *App) removeSyncSubmoduleBackups(progress syncJournalProgress) error {
	for _, backup := range progress.SubmoduleBackups {
		git, err := a.syncSubmoduleGit(backup.Path)
		if err != nil {
			return err
		}
		actual, err := git.RefValue(backup.Ref)
		if err != nil {
			return err
		}
		if !actual.Exists {
			continue
		}
		if actual.OID != backup.Head {
			return fmt.Errorf("submodule journal backup %s moved from %s to %s", backup.Ref, backup.Head, actual.OID)
		}
		if err := git.UpdateRefs([]refEdit{{Ref: backup.Ref, Old: actual}}); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) snapshotCleanSyncSubmodules() ([]syncSubmodule, error) {
	return a.snapshotSyncSubmodules(true)
}

func (a *App) snapshotRecoverableSyncSubmodules() ([]syncSubmodule, error) {
	return a.snapshotSyncSubmodules(false)
}

func (a *App) snapshotSyncSubmodules(requireGitlinkMatch bool) ([]syncSubmodule, error) {
	git, err := a.recoveryGit()
	if err != nil {
		return nil, err
	}
	var snapshot []syncSubmodule
	if err := snapshotCleanSyncSubmodules(git, git.Dir, "", requireGitlinkMatch, &snapshot); err != nil {
		return nil, err
	}
	sortSyncSubmodules(snapshot)
	return snapshot, nil
}

func snapshotCleanSyncSubmodules(git Git, root, prefix string, requireGitlinkMatch bool, snapshot *[]syncSubmodule) error {
	gitlinks, err := git.indexGitlinks()
	if err != nil {
		return err
	}
	for _, gitlink := range gitlinks {
		path := gitlink.Path
		if prefix != "" {
			path = prefix + "/" + path
		}
		if !safeWorktreePath(path) {
			return fmt.Errorf("cannot snapshot unsafe submodule path %q", path)
		}
		dir := filepath.Join(root, filepath.FromSlash(path))
		submodule := Git{Dir: dir, Stdout: git.Stdout, Stderr: git.Stderr}
		submoduleRoot, err := submodule.Root()
		if err != nil || filepath.Clean(submoduleRoot) != filepath.Clean(dir) {
			continue
		}
		if err := requireCleanSyncSubmodule(submodule, path); err != nil {
			return err
		}
		state, err := currentSyncSubmodule(submodule, path)
		if err != nil {
			return err
		}
		if requireGitlinkMatch && state.Head != gitlink.OID {
			return fmt.Errorf("submodule changes would prevent sync; restore %q to %s before graphene sync", path, gitlink.OID)
		}
		*snapshot = append(*snapshot, state)
		if err := snapshotCleanSyncSubmodules(submodule, root, path, requireGitlinkMatch, snapshot); err != nil {
			return err
		}
	}
	return nil
}

func (g Git) indexGitlinks() ([]gitlinkEntry, error) {
	rooted, err := g.Rooted()
	if err != nil {
		return nil, err
	}
	out, err := rooted.OutputBytes("ls-files", "--stage", "-z", "--")
	if err != nil {
		return nil, err
	}
	var entries []gitlinkEntry
	for record := range bytes.SplitSeq(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, path, ok := bytes.Cut(record, []byte{'\t'})
		fields := bytes.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("parse index entry %q", record)
		}
		if string(fields[0]) != "160000" || string(fields[2]) != "0" {
			continue
		}
		oid := string(fields[1])
		if !validObjectID(oid) {
			return nil, fmt.Errorf("submodule %q has invalid object id %q", path, oid)
		}
		entries = append(entries, gitlinkEntry{Path: string(path), OID: oid})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func requireCleanSyncSubmodule(git Git, path string) error {
	if err := git.RequireNoGitOperation(); err != nil {
		return fmt.Errorf("initialized submodule %q is not recoverable: %w", path, err)
	}
	if out, err := git.OutputBytes("ls-files", "--unmerged", "-z", "--"); err != nil {
		return err
	} else if len(out) > 0 {
		return fmt.Errorf("initialized submodule %q has unmerged index entries", path)
	}
	if err := git.requireNoUnsafeIndexFlags(path + "/"); err != nil {
		return err
	}
	out, err := git.OutputBytes("-c", "diff.ignoreSubmodules=all", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return err
	}
	if len(out) > 0 {
		return fmt.Errorf("initialized submodule %q has local changes", path)
	}
	return nil
}

func currentSyncSubmodule(git Git, path string) (syncSubmodule, error) {
	head, err := git.Head()
	if err != nil {
		return syncSubmodule{}, err
	}
	branch, err := git.Output("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if !isGitExit(err, 1) {
			return syncSubmodule{}, err
		}
		branch = ""
	}
	if branch != "" && !validBranchArgument(branch) {
		return syncSubmodule{}, fmt.Errorf("initialized submodule %q has invalid branch %q", path, branch)
	}
	return syncSubmodule{Path: path, Head: head, Branch: branch}, nil
}

func (a *App) requireSyncSubmoduleSnapshot(expected []syncSubmodule) error {
	actual, err := a.snapshotCleanSyncSubmodules()
	if err != nil {
		return err
	}
	if sameSyncSubmodules(actual, expected) {
		return nil
	}
	return fmt.Errorf("initialized submodules changed while sync was being planned; rerun graphene sync")
}

func (a *App) requireRecoverableSyncSubmodules(progress syncJournalProgress, checkRoot bool) error {
	if checkRoot {
		if err := a.git.RequireRecoverableWorktreeShallow(); err != nil {
			return err
		}
	}
	snapshots := [][]syncSubmodule{
		progress.InitialSubmodules,
		progress.BaselineTargetSubmodules,
		progress.BaselineSubmodule,
		progress.ReturnTargetSubmodules,
	}
	allowed := syncSubmoduleAllowedStates(snapshots...)
	paths := make([]string, 0, len(allowed))
	for path := range allowed {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		leftDepth := strings.Count(paths[i], "/")
		rightDepth := strings.Count(paths[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return paths[i] < paths[j]
	})
	for _, path := range paths {
		git, err := a.syncSubmoduleGit(path)
		if err != nil {
			return err
		}
		if err := requireCleanSyncSubmodule(git, path); err != nil {
			return err
		}
		current, err := currentSyncSubmodule(git, path)
		if err != nil {
			return err
		}
		if !allowed[path][syncSubmoduleStateKey(current)] {
			return fmt.Errorf("initialized submodule %q moved to %s on %q outside the pending sync", path, current.Head, current.Branch)
		}
	}
	return nil
}

func sameSyncSubmodules(left, right []syncSubmodule) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]syncSubmodule(nil), left...)
	right = append([]syncSubmodule(nil), right...)
	sortSyncSubmodules(left)
	sortSyncSubmodules(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortSyncSubmodules(submodules []syncSubmodule) {
	sort.Slice(submodules, func(i, j int) bool {
		leftDepth := strings.Count(submodules[i].Path, "/")
		rightDepth := strings.Count(submodules[j].Path, "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return submodules[i].Path < submodules[j].Path
	})
}

func (a *App) planSyncSubmoduleTarget(initial []syncSubmodule, rootTarget string, allowedSnapshots ...[]syncSubmodule) ([]syncSubmodule, error) {
	git, err := a.recoveryGit()
	if err != nil {
		return nil, err
	}
	if !validObjectID(rootTarget) {
		return nil, fmt.Errorf("cannot prepare submodules for invalid target %q", rootTarget)
	}
	if len(initial) == 0 {
		return []syncSubmodule{}, nil
	}
	actualRoot, err := git.Head()
	if err != nil {
		return nil, err
	}
	if actualRoot != rootTarget {
		return nil, fmt.Errorf("cannot prepare submodules because worktree HEAD moved from %s to %s", rootTarget, actualRoot)
	}
	transitions, err := a.planSyncSubmodulesForTree(initial, rootTarget, allowedSnapshots...)
	if err != nil {
		return nil, err
	}
	target := make([]syncSubmodule, 0, len(transitions))
	for _, transition := range transitions {
		target = append(target, transition.Target)
	}
	return target, nil
}

func (a *App) restoreSyncSubmodules(target []syncSubmodule, allowedSnapshots ...[]syncSubmodule) error {
	transitions, err := a.planSyncSubmodulesForSnapshot(target, allowedSnapshots...)
	if err != nil {
		return err
	}
	return a.applySyncSubmoduleTransitions(transitions)
}

func (a *App) verifySyncSubmoduleTarget(target []syncSubmodule) error {
	actual, err := a.snapshotCleanSyncSubmodules()
	if err != nil {
		return err
	}
	if !sameSyncSubmodules(actual, target) {
		return fmt.Errorf("submodule set changed while applying the sync transition")
	}
	return nil
}

func (a *App) planSyncSubmodulesForTree(initial []syncSubmodule, rootTarget string, allowedSnapshots ...[]syncSubmodule) ([]syncSubmoduleTransition, error) {
	rootGit, err := a.recoveryGit()
	if err != nil {
		return nil, err
	}
	initial = append([]syncSubmodule(nil), initial...)
	sortSyncSubmodules(initial)
	if err := validateSyncSubmoduleSnapshot(initial); err != nil {
		return nil, err
	}
	allowed := syncSubmoduleAllowedStates(append([][]syncSubmodule{initial}, allowedSnapshots...)...)
	targets := map[string]string{}
	known := make([]string, 0, len(initial))
	var transitions []syncSubmoduleTransition
	for _, original := range initial {
		parent := nearestSyncSubmoduleParent(original.Path, known)
		container := rootGit
		containerTree := rootTarget
		relative := original.Path
		if parent != "" {
			containerTree = targets[parent]
			if containerTree == "" {
				return nil, fmt.Errorf("sync target removes initialized submodule %q through its parent; deinitialize it before syncing", original.Path)
			}
			container = Git{Dir: filepath.Join(rootGit.Dir, filepath.FromSlash(parent)), Stdout: a.stdout, Stderr: a.stderr}
			relative = strings.TrimPrefix(original.Path, parent+"/")
		}
		target, exists, err := container.gitlinkAtTree(containerTree, relative)
		if err != nil {
			return nil, fmt.Errorf("resolve target for initialized submodule %q: %w", original.Path, err)
		}
		known = append(known, original.Path)
		if !exists {
			return nil, fmt.Errorf("sync target removes initialized submodule %q; deinitialize it before syncing", original.Path)
		}
		targets[original.Path] = target
		submodule, err := a.syncSubmoduleGit(original.Path)
		if err != nil {
			return nil, err
		}
		if err := ensureSyncSubmoduleCommit(submodule, target); err != nil {
			return nil, fmt.Errorf("prepare initialized submodule %q: %w", original.Path, err)
		}
		state := syncSubmodule{Path: original.Path, Head: target}
		if target == original.Head {
			state.Branch = original.Branch
		}
		transitions = append(transitions, syncSubmoduleTransition{Target: state, Allowed: allowed[original.Path]})
	}
	return transitions, nil
}

func (a *App) planSyncSubmodulesForSnapshot(target []syncSubmodule, allowedSnapshots ...[]syncSubmodule) ([]syncSubmoduleTransition, error) {
	target = append([]syncSubmodule(nil), target...)
	sortSyncSubmodules(target)
	if err := validateSyncSubmoduleSnapshot(target); err != nil {
		return nil, err
	}
	allowed := syncSubmoduleAllowedStates(append([][]syncSubmodule{target}, allowedSnapshots...)...)
	transitions := make([]syncSubmoduleTransition, 0, len(target))
	for _, state := range target {
		submodule, err := a.syncSubmoduleGit(state.Path)
		if err != nil {
			return nil, err
		}
		if err := ensureSyncSubmoduleCommit(submodule, state.Head); err != nil {
			return nil, fmt.Errorf("prepare initialized submodule %q: %w", state.Path, err)
		}
		transitions = append(transitions, syncSubmoduleTransition{Target: state, Allowed: allowed[state.Path]})
	}
	return transitions, nil
}

func validateSyncSubmoduleSnapshot(snapshot []syncSubmodule) error {
	seen := map[string]bool{}
	for _, submodule := range snapshot {
		if !safeWorktreePath(submodule.Path) || !validObjectID(submodule.Head) {
			return fmt.Errorf("invalid submodule snapshot for %q", submodule.Path)
		}
		if submodule.Branch != "" && !validBranchArgument(submodule.Branch) {
			return fmt.Errorf("invalid submodule branch %q for %q", submodule.Branch, submodule.Path)
		}
		if seen[submodule.Path] {
			return fmt.Errorf("submodule snapshot contains %q more than once", submodule.Path)
		}
		seen[submodule.Path] = true
	}
	return nil
}

func syncSubmoduleAllowedStates(snapshots ...[]syncSubmodule) map[string]map[string]bool {
	allowed := map[string]map[string]bool{}
	for _, snapshot := range snapshots {
		for _, state := range snapshot {
			if allowed[state.Path] == nil {
				allowed[state.Path] = map[string]bool{}
			}
			allowed[state.Path][syncSubmoduleStateKey(state)] = true
		}
	}
	return allowed
}

func syncSubmoduleStateKey(state syncSubmodule) string {
	return state.Head + "\x00" + state.Branch
}

func nearestSyncSubmoduleParent(path string, candidates []string) string {
	parent := ""
	for _, candidate := range candidates {
		if strings.HasPrefix(path, candidate+"/") && len(candidate) > len(parent) {
			parent = candidate
		}
	}
	return parent
}

func (g Git) gitlinkAtTree(tree, path string) (string, bool, error) {
	if !validObjectID(tree) || !safeWorktreePath(path) {
		return "", false, fmt.Errorf("invalid gitlink lookup %q in %q", path, tree)
	}
	out, err := g.OutputBytes("ls-tree", "-z", tree, "--", path)
	if err != nil {
		return "", false, err
	}
	if len(out) == 0 {
		return "", false, nil
	}
	records := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	if len(records) != 1 {
		return "", false, fmt.Errorf("tree contains multiple entries for %q", path)
	}
	meta, name, ok := bytes.Cut(records[0], []byte{'\t'})
	fields := bytes.Fields(meta)
	if !ok || len(fields) != 3 || string(name) != path {
		return "", false, fmt.Errorf("parse tree entry %q", records[0])
	}
	if string(fields[0]) != "160000" || string(fields[1]) != "commit" {
		return "", false, fmt.Errorf("target replaces the submodule path with tracked content")
	}
	oid := string(fields[2])
	if !validObjectID(oid) {
		return "", false, fmt.Errorf("tree has invalid gitlink object id %q", oid)
	}
	return oid, true, nil
}

func ensureSyncSubmoduleCommit(git Git, oid string) error {
	if _, err := git.Output("cat-file", "-e", oid+"^{commit}"); err == nil {
		return nil
	}
	_, _ = git.Output("-c", "submodule.recurse=false", "fetch")
	if _, err := git.Output("cat-file", "-e", oid+"^{commit}"); err == nil {
		return nil
	}
	remotes, err := git.Output("remote")
	if err != nil {
		return err
	}
	names := strings.Fields(remotes)
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("target commit %s is missing and the submodule has no remote", oid)
	}
	remote := names[0]
	for _, name := range names {
		if name == "origin" {
			remote = name
			break
		}
	}
	if err := git.Run("-c", "submodule.recurse=false", "fetch", remote, oid); err != nil {
		return err
	}
	if _, err := git.Output("cat-file", "-e", oid+"^{commit}"); err != nil {
		return fmt.Errorf("fetched submodule target %s is unavailable: %w", oid, err)
	}
	return nil
}

func (a *App) syncSubmoduleGit(path string) (Git, error) {
	if !safeWorktreePath(path) {
		return Git{}, fmt.Errorf("unsafe initialized submodule path %q", path)
	}
	root, err := a.git.Root()
	if err != nil {
		return Git{}, err
	}
	dir := filepath.Join(root, filepath.FromSlash(path))
	git := Git{Dir: dir, Stdout: a.stdout, Stderr: a.stderr}
	actual, err := git.Root()
	if err != nil {
		return Git{}, fmt.Errorf("initialized submodule %q is no longer available: %w", path, err)
	}
	if filepath.Clean(actual) != filepath.Clean(dir) {
		return Git{}, fmt.Errorf("initialized submodule %q no longer has its own worktree", path)
	}
	return git, nil
}

func (a *App) applySyncSubmoduleTransitions(transitions []syncSubmoduleTransition) error {
	for _, transition := range transitions {
		if err := a.preflightSyncSubmoduleTransition(transition); err != nil {
			return err
		}
	}
	for _, transition := range transitions {
		git, err := a.syncSubmoduleGit(transition.Target.Path)
		if err != nil {
			return err
		}
		if err := a.preflightSyncSubmoduleTransition(transition); err != nil {
			return err
		}
		current, err := currentSyncSubmodule(git, transition.Target.Path)
		if err != nil {
			return err
		}
		if current == transition.Target {
			continue
		}
		if transition.Target.Branch == "" {
			err = git.RunOperationSwitch("--detach", transition.Target.Head)
		} else {
			err = git.RunOperationSwitch(transition.Target.Branch)
		}
		if err != nil {
			return fmt.Errorf("transition initialized submodule %q: %w", transition.Target.Path, err)
		}
		after, err := currentSyncSubmodule(git, transition.Target.Path)
		if err != nil {
			return err
		}
		if after != transition.Target {
			return fmt.Errorf("initialized submodule %q moved to %s on %q, want %s on %q", transition.Target.Path, after.Head, after.Branch, transition.Target.Head, transition.Target.Branch)
		}
	}
	return nil
}

func (a *App) preflightSyncSubmoduleTransition(transition syncSubmoduleTransition) error {
	git, err := a.syncSubmoduleGit(transition.Target.Path)
	if err != nil {
		return err
	}
	if err := requireCleanSyncSubmodule(git, transition.Target.Path); err != nil {
		return err
	}
	current, err := currentSyncSubmodule(git, transition.Target.Path)
	if err != nil {
		return err
	}
	allowed := transition.Allowed[syncSubmoduleStateKey(current)] || current == transition.Target
	if !allowed {
		return fmt.Errorf("initialized submodule %q moved to %s on %q while sync was in progress; restore it or abort explicitly", transition.Target.Path, current.Head, current.Branch)
	}
	if transition.Target.Branch != "" {
		value, err := git.RefValue("refs/heads/" + transition.Target.Branch)
		if err != nil {
			return err
		}
		want := RefValue{Exists: true, OID: transition.Target.Head}
		if value != want {
			return fmt.Errorf("initialized submodule %q branch %q moved from %s to %s", transition.Target.Path, transition.Target.Branch, transition.Target.Head, formatRefValue(value))
		}
	}
	if err := git.RequireNoTreeCollision(transition.Target.Head, "submodule transition"); err != nil {
		return fmt.Errorf("cannot transition initialized submodule %q: %w", transition.Target.Path, err)
	}
	return nil
}
