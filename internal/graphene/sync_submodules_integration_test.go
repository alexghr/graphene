package graphene

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type syncSubmoduleTransitionFixture struct {
	repo            testRepo
	app             *App
	submoduleDir    string
	initialCommit   string
	targetCommit    string
	initialRoot     string
	targetRoot      string
	initialBranch   string
	initialRootName string
}

func TestSyncSubmoduleJournalBackupsProtectAndReleaseOriginalCommits(t *testing.T) {
	t.Parallel()
	fixture := newSyncSubmoduleTransitionFixture(t)
	initial, err := fixture.app.snapshotCleanSyncSubmodules()
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := fixture.app.git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("sync", worktree, fixture.initialRootName, fixture.initialRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	backups, err := planSyncSubmoduleBackups(operation, initial)
	if err != nil {
		t.Fatal(err)
	}
	progress := syncJournalProgress{InitialSubmodules: initial, SubmoduleBackups: backups}
	if err := fixture.app.installSyncSubmoduleBackups(progress, true); err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("submodule backups = %#v, want one", backups)
	}
	backup := backups[0]
	if got := runGit(t, fixture.submoduleDir, "rev-parse", backup.Ref); got != fixture.initialCommit {
		t.Fatalf("backup ref = %s, want initial commit %s", got, fixture.initialCommit)
	}

	runGit(t, fixture.submoduleDir, "switch", "--detach", fixture.targetCommit)
	runGit(t, fixture.submoduleDir, "update-ref", "-d", backup.Ref, fixture.initialCommit)
	if err := fixture.app.installSyncSubmoduleBackups(progress, false); err != nil {
		t.Fatalf("reinstall missing journal backup after transition: %v", err)
	}
	if err := fixture.app.verifySyncSubmoduleBackups(progress); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, fixture.submoduleDir, "rev-parse", backup.Ref); got != fixture.initialCommit {
		t.Fatalf("reinstalled backup ref = %s, want initial commit %s", got, fixture.initialCommit)
	}

	if err := fixture.app.removeSyncSubmoduleBackups(progress); err != nil {
		t.Fatal(err)
	}
	if refExists(t, fixture.submoduleDir, backup.Ref) {
		t.Fatalf("submodule backup %s remains after cleanup", backup.Ref)
	}
}

func TestSyncSubmoduleTransitionRefusesIgnoredTrackedTarget(t *testing.T) {
	t.Parallel()
	fixture := newSyncSubmoduleTransitionFixture(t)

	initial, err := fixture.app.snapshotCleanSyncSubmodules()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.git.RunOperationSwitch("main"); err != nil {
		t.Fatal(err)
	}
	local := []byte("keep this ignored content\n")
	writeFile(t, fixture.submoduleDir, "collision.txt", string(local))

	target, err := fixture.app.planSyncSubmoduleTarget(initial, fixture.targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.app.restoreSyncSubmodules(target, initial)
	if err == nil || !strings.Contains(err.Error(), "ignored path \"collision.txt\" would be overwritten") {
		t.Fatalf("restoreSyncSubmodules error = %v, want ignored-file collision", err)
	}
	if got := runGit(t, fixture.submoduleDir, "rev-parse", "HEAD"); got != fixture.initialCommit {
		t.Fatalf("submodule HEAD = %s after refused transition, want %s", got, fixture.initialCommit)
	}
	if got := currentBranch(t, fixture.submoduleDir); got != fixture.initialBranch {
		t.Fatalf("submodule branch = %q after refused transition, want %q", got, fixture.initialBranch)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.submoduleDir, "collision.txt")); err != nil {
		t.Fatal(err)
	} else if string(got) != string(local) {
		t.Fatalf("collision.txt = %q after refused transition, want %q", got, local)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.submoduleDir, "version.txt")); err != nil {
		t.Fatal(err)
	} else if string(got) != "one\n" {
		t.Fatalf("version.txt = %q after refused transition, want original bytes", got)
	}
}

func TestSyncSubmoduleDirectRestoreReattachesOriginalBranch(t *testing.T) {
	t.Parallel()
	fixture := newSyncSubmoduleTransitionFixture(t)

	initial, err := fixture.app.snapshotCleanSyncSubmodules()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.git.RunOperationSwitch("main"); err != nil {
		t.Fatal(err)
	}
	target, err := fixture.app.planSyncSubmoduleTarget(initial, fixture.targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.restoreSyncSubmodules(target, initial); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, fixture.submoduleDir, "rev-parse", "HEAD"); got != fixture.targetCommit {
		t.Fatalf("submodule HEAD = %s after forward transition, want %s", got, fixture.targetCommit)
	}
	if got := runGit(t, fixture.submoduleDir, "branch", "--show-current"); got != "" {
		t.Fatalf("submodule branch = %q after forward transition, want detached HEAD", got)
	}

	if err := fixture.app.git.RunOperationSwitch(fixture.initialRootName); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.restoreSyncSubmodules(initial, target); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, fixture.submoduleDir, "rev-parse", "HEAD"); got != fixture.initialCommit {
		t.Fatalf("submodule HEAD = %s after restore, want %s", got, fixture.initialCommit)
	}
	if got := currentBranch(t, fixture.submoduleDir); got != fixture.initialBranch {
		t.Fatalf("submodule branch = %q after restore, want %q", got, fixture.initialBranch)
	}
}

func TestSyncSubmoduleRemovedGitlinkRefusalCanBeAborted(t *testing.T) {
	t.Parallel()
	fixture := newSyncSubmoduleTransitionFixture(t)

	initial, err := fixture.app.snapshotCleanSyncSubmodules()
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, fixture.repo.dir, "switch", "-c", "without-dependency")
	runGit(t, fixture.repo.dir, "rm", "--cached", "dependency")
	runGit(t, fixture.repo.dir, "commit", "-m", "Remove dependency gitlink")
	removedRoot := runGit(t, fixture.repo.dir, "rev-parse", "HEAD")
	if _, err := os.Stat(fixture.submoduleDir); err != nil {
		t.Fatalf("initialized submodule disappeared after removing gitlink: %v", err)
	}

	_, err = fixture.app.planSyncSubmoduleTarget(initial, removedRoot)
	if err == nil || !strings.Contains(err.Error(), "sync target removes initialized submodule \"dependency\"") {
		t.Fatalf("planSyncSubmoduleTarget error = %v, want removed-submodule refusal", err)
	}
	if got := runGit(t, fixture.submoduleDir, "rev-parse", "HEAD"); got != fixture.initialCommit {
		t.Fatalf("submodule HEAD = %s after refusal, want %s", got, fixture.initialCommit)
	}
	if got := currentBranch(t, fixture.submoduleDir); got != fixture.initialBranch {
		t.Fatalf("submodule branch = %q after refusal, want %q", got, fixture.initialBranch)
	}
	if err := fixture.app.git.RunOperationSwitch(fixture.initialRootName); err != nil {
		t.Fatalf("reintroduce gitlink with initialized directory: %v", err)
	}
	if err := fixture.app.restoreSyncSubmodules(initial); err != nil {
		t.Fatal(err)
	}
	oid, exists, err := fixture.app.git.gitlinkAtTree(fixture.initialRoot, "dependency")
	if err != nil {
		t.Fatal(err)
	}
	if !exists || oid != fixture.initialCommit {
		t.Fatalf("reintroduced gitlink = (%s, %t), want (%s, true)", oid, exists, fixture.initialCommit)
	}
	if got := runGit(t, fixture.submoduleDir, "rev-parse", "HEAD"); got != fixture.initialCommit {
		t.Fatalf("submodule HEAD = %s after gitlink reintroduction, want %s", got, fixture.initialCommit)
	}
	if got := currentBranch(t, fixture.submoduleDir); got != fixture.initialBranch {
		t.Fatalf("submodule branch = %q after gitlink reintroduction, want %q", got, fixture.initialBranch)
	}
}

func TestSyncSubmodulePlannedTargetAllowsRestoreAfterPartialTransition(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	type dependency struct {
		path          string
		branch        string
		initialCommit string
		targetCommit  string
	}
	dependencies := []dependency{
		{path: "dependency-one", branch: "restore-one"},
		{path: "dependency-two", branch: "restore-two"},
	}
	for index := range dependencies {
		source, initial, target := newSyncSubmoduleHistory(t, dependencies[index].path)
		dependencies[index].initialCommit = initial
		dependencies[index].targetCommit = target
		runGit(t, repo.dir, "-c", "protocol.file.allow=always", "submodule", "add", source, dependencies[index].path)
		dir := filepath.Join(repo.dir, dependencies[index].path)
		runGit(t, dir, "switch", "-c", dependencies[index].branch, initial)
	}
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Add dependencies")
	initialRoot := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "branch", "initial-dependencies", initialRoot)
	for _, dependency := range dependencies {
		runGit(t, filepath.Join(repo.dir, dependency.path), "switch", "--detach", dependency.targetCommit)
		runGit(t, repo.dir, "add", dependency.path)
	}
	runGit(t, repo.dir, "commit", "-m", "Update dependencies")
	targetRoot := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "-c", "submodule.recurse=false", "switch", "initial-dependencies")
	for _, dependency := range dependencies {
		runGit(t, filepath.Join(repo.dir, dependency.path), "switch", dependency.branch)
	}

	app := NewApp(repo.dir, nil, io.Discard, io.Discard, os.Getenv)
	initial, err := app.snapshotCleanSyncSubmodules()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.git.RunOperationSwitch("main"); err != nil {
		t.Fatal(err)
	}
	transitions, err := app.planSyncSubmodulesForTree(initial, targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != len(dependencies) {
		t.Fatalf("planned transitions = %#v, want %d", transitions, len(dependencies))
	}
	plannedTarget := make([]syncSubmodule, 0, len(transitions))
	for _, transition := range transitions {
		plannedTarget = append(plannedTarget, transition.Target)
	}
	if err := app.applySyncSubmoduleTransitions(transitions[:1]); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, filepath.Join(repo.dir, transitions[0].Target.Path), "rev-parse", "HEAD"); got != transitions[0].Target.Head {
		t.Fatalf("partially transitioned submodule HEAD = %s, want %s", got, transitions[0].Target.Head)
	}

	if err := app.git.RunOperationSwitch("initial-dependencies"); err != nil {
		t.Fatal(err)
	}
	if err := app.restoreSyncSubmodules(initial, plannedTarget); err != nil {
		t.Fatalf("restore from partially applied planned target: %v", err)
	}
	for _, dependency := range dependencies {
		dir := filepath.Join(repo.dir, dependency.path)
		if got := runGit(t, dir, "rev-parse", "HEAD"); got != dependency.initialCommit {
			t.Fatalf("%s HEAD = %s after restore, want %s", dependency.path, got, dependency.initialCommit)
		}
		if got := currentBranch(t, dir); got != dependency.branch {
			t.Fatalf("%s branch = %q after restore, want %q", dependency.path, got, dependency.branch)
		}
	}
}

func TestSyncAlignsInitializedSubmoduleWithReturnedBranchTree(t *testing.T) {
	t.Parallel()
	source, initialCommit, targetCommit := newSyncSubmoduleHistory(t, "return-tree")
	repo := newTestRepo(t)
	runGit(t, repo.dir, "-c", "protocol.file.allow=always", "submodule", "add", source, "dependency")
	submoduleDir := filepath.Join(repo.dir, "dependency")
	runGit(t, submoduleDir, "switch", "-c", "restore-point", initialCommit)
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Add dependency")

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, repo.dir, "remote", "add", "origin", remote)
	runGit(t, repo.dir, "push", "-u", "origin", "main")

	writeFile(t, repo.dir, "stack.txt", "stack\n")
	runGit(t, repo.dir, "add", "stack.txt")
	expectGrapheneOK(t, repo, "new", "-m", "Return tree")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "--branch", "main", remote, other)
	runGit(t, other, "config", "user.name", "Graphene Test")
	runGit(t, other, "config", "user.email", "graphene@example.test")
	runGit(t, other, "config", "commit.gpgsign", "false")
	runGit(t, other, "update-index", "--cacheinfo", "160000,"+targetCommit+",dependency")
	runGit(t, other, "commit", "-m", "Update dependency")
	runGit(t, other, "push", "origin", "main")

	expectGrapheneOK(t, repo, "sync")
	if got := currentBranch(t, repo.dir); got != "stack/return-tree" {
		t.Fatalf("returned branch = %q, want stack/return-tree", got)
	}
	rootHead := runGit(t, repo.dir, "rev-parse", "HEAD")
	oid, exists, err := (Git{Dir: repo.dir}).gitlinkAtTree(rootHead, "dependency")
	if err != nil {
		t.Fatal(err)
	}
	if !exists || oid != targetCommit {
		t.Fatalf("returned tree gitlink = (%s, %t), want (%s, true)", oid, exists, targetCommit)
	}
	if got := runGit(t, submoduleDir, "rev-parse", "HEAD"); got != targetCommit {
		t.Fatalf("initialized submodule HEAD = %s after sync, want returned tree gitlink %s", got, targetCommit)
	}
	if status := runGit(t, repo.dir, "status", "--porcelain"); status != "" {
		t.Fatalf("status after sync = %q, want clean", status)
	}
	if state := readState(t, repo.dir); state.Operation != nil {
		t.Fatalf("operation remains after successful sync: %#v", state.Operation)
	}
}

func newSyncSubmoduleTransitionFixture(t *testing.T) syncSubmoduleTransitionFixture {
	t.Helper()
	source, initialCommit, targetCommit := newSyncSubmoduleHistory(t, "dependency")
	repo := newTestRepo(t)
	runGit(t, repo.dir, "-c", "protocol.file.allow=always", "submodule", "add", source, "dependency")
	submoduleDir := filepath.Join(repo.dir, "dependency")
	initialBranch := "restore-point"
	runGit(t, submoduleDir, "switch", "-c", initialBranch, initialCommit)
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Add dependency")
	initialRoot := runGit(t, repo.dir, "rev-parse", "HEAD")
	initialRootName := "initial-dependency"
	runGit(t, repo.dir, "branch", initialRootName, initialRoot)

	runGit(t, submoduleDir, "switch", "--detach", targetCommit)
	runGit(t, repo.dir, "add", "dependency")
	runGit(t, repo.dir, "commit", "-m", "Update dependency")
	targetRoot := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "-c", "submodule.recurse=false", "switch", initialRootName)
	runGit(t, submoduleDir, "switch", initialBranch)

	return syncSubmoduleTransitionFixture{
		repo:            repo,
		app:             NewApp(repo.dir, nil, io.Discard, io.Discard, os.Getenv),
		submoduleDir:    submoduleDir,
		initialCommit:   initialCommit,
		targetCommit:    targetCommit,
		initialRoot:     initialRoot,
		targetRoot:      targetRoot,
		initialBranch:   initialBranch,
		initialRootName: initialRootName,
	}
}

func newSyncSubmoduleHistory(t *testing.T, name string) (string, string, string) {
	t.Helper()
	source := t.TempDir()
	runGit(t, source, "init", "-b", "main")
	runGit(t, source, "config", "user.name", "Graphene Test")
	runGit(t, source, "config", "user.email", "graphene@example.test")
	runGit(t, source, "config", "commit.gpgsign", "false")
	writeFile(t, source, ".gitignore", "collision.txt\n")
	writeFile(t, source, "version.txt", "one\n")
	runGit(t, source, "add", ".")
	runGit(t, source, "commit", "-m", "Initial "+name)
	initial := runGit(t, source, "rev-parse", "HEAD")
	writeFile(t, source, "version.txt", "two\n")
	writeFile(t, source, "collision.txt", "target content\n")
	runGit(t, source, "add", "version.txt")
	runGit(t, source, "add", "-f", "collision.txt")
	runGit(t, source, "commit", "-m", "Target "+name)
	target := runGit(t, source, "rev-parse", "HEAD")
	return source, initial, target
}
