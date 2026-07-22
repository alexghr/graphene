package graphene

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJournalStartRefusesExistingGitOperation(t *testing.T) {
	t.Parallel()

	t.Run("new during merge", func(t *testing.T) {
		repo := newTestRepo(t)
		runGit(t, repo.dir, "switch", "-c", "topic")
		commitFile(t, repo.dir, "topic.txt", "topic\n", "Topic")
		runGit(t, repo.dir, "switch", "main")
		runGit(t, repo.dir, "merge", "--no-commit", "--no-ff", "topic")

		code, _, stderr := repo.runGraphene(t, "new", "-m", "Do not commit merge")
		if code == 0 {
			t.Fatal("graphene new unexpectedly accepted an in-progress merge")
		}
		if !strings.Contains(stderr, "Git merge operation is in progress") {
			t.Fatalf("stderr = %q, want merge-in-progress error", stderr)
		}
		if state := readState(t, repo.dir); state.Operation != nil {
			t.Fatalf("operation = %#v, want none", state.Operation)
		}
		mergeHead, err := (Git{Dir: repo.dir}).GitPath("MERGE_HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(mergeHead); err != nil {
			t.Fatalf("pre-existing merge state was changed: %v", err)
		}
	})

	t.Run("amend during cherry-pick", func(t *testing.T) {
		repo := newTestRepo(t)
		runGit(t, repo.dir, "switch", "-c", "topic")
		commitFile(t, repo.dir, "file.txt", "topic\n", "Topic")
		runGit(t, repo.dir, "switch", "main")
		createStackBranch(t, repo, "file.txt", "stack\n", "Stack change")

		code, _, _ := runGitResult(t, repo.dir, "cherry-pick", "topic")
		if code == 0 {
			t.Fatal("cherry-pick unexpectedly succeeded without a conflict")
		}
		code, _, stderr := repo.runGraphene(t, "amend", "-m", "Do not amend cherry-pick")
		if code == 0 {
			t.Fatal("graphene amend unexpectedly accepted an in-progress cherry-pick")
		}
		if !strings.Contains(stderr, "Git cherry-pick operation is in progress") {
			t.Fatalf("stderr = %q, want cherry-pick-in-progress error", stderr)
		}
		if state := readState(t, repo.dir); state.Operation != nil {
			t.Fatalf("operation = %#v, want none", state.Operation)
		}
		cherryPickHead, err := (Git{Dir: repo.dir}).GitPath("CHERRY_PICK_HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(cherryPickHead); err != nil {
			t.Fatalf("pre-existing cherry-pick state was changed: %v", err)
		}
	})
}

func TestJournalStartRefusesUnsafeIndexFlags(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		flag string
	}{
		{name: "assume unchanged", flag: "--assume-unchanged"},
		{name: "skip worktree", flag: "--skip-worktree"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newTestRepo(t)
			runGit(t, repo.dir, "update-index", test.flag, "file.txt")
			writeFile(t, repo.dir, "ready.txt", "ready\n")
			runGit(t, repo.dir, "add", "ready.txt")

			code, _, stderr := repo.runGraphene(t, "new", "-m", "Unsafe index")
			if code == 0 {
				t.Fatalf("graphene new unexpectedly accepted %s", test.name)
			}
			if !strings.Contains(stderr, "skip-worktree or assume-unchanged") || !strings.Contains(stderr, "file.txt") {
				t.Fatalf("stderr = %q, want unsafe-index error for file.txt", stderr)
			}
			if state := readState(t, repo.dir); state.Operation != nil {
				t.Fatalf("operation = %#v, want none", state.Operation)
			}
			if got := currentBranch(t, repo.dir); got != "main" {
				t.Fatalf("branch = %q, want main", got)
			}
			if got := runGit(t, repo.dir, "status", "--short", "ready.txt"); got != "A  ready.txt" {
				t.Fatalf("staged input changed: %q", got)
			}
		})
	}
}

func TestJournalStartRefusesDirtyInitializedSubmoduleDespiteIgnoreConfig(t *testing.T) {
	t.Parallel()

	submodule := t.TempDir()
	runGit(t, submodule, "init", "-b", "main")
	runGit(t, submodule, "config", "user.name", "Graphene Test")
	runGit(t, submodule, "config", "user.email", "graphene@example.test")
	runGit(t, submodule, "config", "commit.gpgsign", "false")
	commitFile(t, submodule, "dependency.txt", "clean\n", "Initial dependency")

	repo := newTestRepo(t)
	runGit(t, repo.dir, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "dependency")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "Add dependency")
	runGit(t, repo.dir, "config", "submodule.recurse", "true")
	runGit(t, repo.dir, "config", "submodule.dependency.ignore", "all")
	runGit(t, repo.dir, "config", "diff.ignoreSubmodules", "all")

	writeFile(t, filepath.Join(repo.dir, "dependency"), "dependency.txt", "precious local change\n")
	writeFile(t, repo.dir, "ready.txt", "ready\n")
	runGit(t, repo.dir, "add", "ready.txt")
	if got := runGit(t, repo.dir, "status", "--short"); got != "A  ready.txt" {
		t.Fatalf("ignore config did not hide the dirty submodule as expected: %q", got)
	}

	code, _, stderr := repo.runGraphene(t, "new", "-m", "Unsafe submodule")
	if code == 0 {
		t.Fatal("graphene new unexpectedly accepted a dirty initialized submodule")
	}
	if !strings.Contains(stderr, "initialized submodule \"dependency\" has local changes") {
		t.Fatalf("stderr = %q, want dirty-submodule error", stderr)
	}
	if state := readState(t, repo.dir); state.Operation != nil {
		t.Fatalf("operation = %#v, want none", state.Operation)
	}
	data, err := os.ReadFile(filepath.Join(repo.dir, "dependency", "dependency.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "precious local change\n" {
		t.Fatalf("submodule file = %q, want local change", data)
	}
}

func TestJournalRebaseRefusesIgnoredFileCollision(t *testing.T) {
	t.Parallel()

	repo := newTestRepo(t)
	commitFile(t, repo.dir, ".gitignore", "collision.txt\n", "Ignore collision")
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	original := runGit(t, repo.dir, "rev-parse", "stack/one")

	runGit(t, repo.dir, "switch", "main")
	runGit(t, repo.dir, "switch", "-c", "newbase")
	writeFile(t, repo.dir, "collision.txt", "tracked by new base\n")
	runGit(t, repo.dir, "add", "-f", "collision.txt")
	runGit(t, repo.dir, "commit", "-m", "Track collision")
	runGit(t, repo.dir, "switch", "stack/one")
	writeFile(t, repo.dir, "collision.txt", "precious ignored bytes\n")

	code, _, stderr := repo.runGraphene(t, "restack", "--force", "newbase")
	if code == 0 {
		t.Fatal("graphene restack unexpectedly overwrote an ignored collision")
	}
	if !strings.Contains(stderr, "ignored path \"collision.txt\" would be overwritten") {
		t.Fatalf("stderr = %q, want ignored-collision error", stderr)
	}
	data, err := os.ReadFile(filepath.Join(repo.dir, "collision.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "precious ignored bytes\n" {
		t.Fatalf("ignored file = %q after refused rebase", data)
	}
	if got := runGit(t, repo.dir, "rev-parse", "stack/one"); got != original {
		t.Fatalf("stack/one = %s, want original %s", got, original)
	}
	inProgress, err := (Git{Dir: repo.dir}).RebaseInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if inProgress {
		t.Fatal("rebase started despite ignored collision")
	}
	if state := readState(t, repo.dir); state.Operation == nil || state.Operation.Kind != "restack" {
		t.Fatalf("operation = %#v, want recoverable restack journal", state.Operation)
	}

	expectGrapheneOK(t, repo, "abort")
	data, err = os.ReadFile(filepath.Join(repo.dir, "collision.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "precious ignored bytes\n" {
		t.Fatalf("ignored file = %q after abort", data)
	}
}

func TestJournalAbortRestoresIndexWithHostileDiffConfigFromSubdirectory(t *testing.T) {
	t.Parallel()

	repo := newTestRepo(t)
	writeFile(t, repo.dir, ".gitattributes", "*.bin diff=hostile\n")
	writeBytes(t, repo.dir, "nested/data.bin", []byte("base\x00bytes\n"))
	runGit(t, repo.dir, "add", ".gitattributes", "nested/data.bin")
	runGit(t, repo.dir, "commit", "-m", "Add binary fixture")
	createStackBranch(t, repo, "file.txt", "one\n", "One")
	createStackBranch(t, repo, "file.txt", "two\n", "Two")
	runGit(t, repo.dir, "switch", "stack/one")

	writeFile(t, repo.dir, "file.txt", "amended\n")
	writeBytes(t, repo.dir, "nested/data.bin", []byte("staged\x00bytes\n"))
	runGit(t, repo.dir, "add", "file.txt", "nested/data.bin")
	writeBytes(t, repo.dir, "nested/data.bin", []byte("unstaged\x00bytes\n"))
	runGit(t, repo.dir, "config", "diff.noprefix", "true")
	runGit(t, repo.dir, "config", "diff.relative", "true")
	runGit(t, repo.dir, "config", "diff.hostile.textconv", "sed 's/staged/TEXTCONV/g'")

	git := Git{Dir: repo.dir}
	wantIndexData, err := git.OutputBytes("show", ":nested/data.bin")
	if err != nil {
		t.Fatal(err)
	}
	wantIndexFile, err := git.OutputBytes("show", ":file.txt")
	if err != nil {
		t.Fatal(err)
	}
	wantWorktreeData, err := os.ReadFile(filepath.Join(repo.dir, "nested", "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	wantWorktreeFile, err := os.ReadFile(filepath.Join(repo.dir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	original := runGit(t, repo.dir, "rev-parse", "stack/one")

	fromNested := repo
	fromNested.dir = filepath.Join(repo.dir, "nested")
	code, _, stderr := fromNested.runGraphene(t, "amend", "-a", "-m", "One amended")
	if code == 0 {
		t.Fatal("graphene amend unexpectedly avoided the descendant conflict")
	}
	if stderr == "" {
		t.Fatal("conflicting amend failed without an error")
	}
	if state := readState(t, repo.dir); state.Operation == nil || state.Operation.Kind != "amend" {
		t.Fatalf("operation = %#v, want pending amend", state.Operation)
	}

	expectGrapheneOK(t, fromNested, "abort")
	if got := runGit(t, repo.dir, "rev-parse", "stack/one"); got != original {
		t.Fatalf("stack/one = %s after abort, want %s", got, original)
	}
	gotIndexData, err := git.OutputBytes("show", ":nested/data.bin")
	if err != nil {
		t.Fatal(err)
	}
	gotIndexFile, err := git.OutputBytes("show", ":file.txt")
	if err != nil {
		t.Fatal(err)
	}
	gotWorktreeData, err := os.ReadFile(filepath.Join(repo.dir, "nested", "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	gotWorktreeFile, err := os.ReadFile(filepath.Join(repo.dir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for label, values := range map[string][2][]byte{
		"index nested/data.bin":    {gotIndexData, wantIndexData},
		"index file.txt":           {gotIndexFile, wantIndexFile},
		"worktree nested/data.bin": {gotWorktreeData, wantWorktreeData},
		"worktree file.txt":        {gotWorktreeFile, wantWorktreeFile},
	} {
		if !bytes.Equal(values[0], values[1]) {
			t.Errorf("%s = %q, want exact bytes %q", label, values[0], values[1])
		}
	}
}

func TestJournalAbortAfterCrashRetainsUntrackedCollisionAndArtifacts(t *testing.T) {
	t.Parallel()

	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	original := runGit(t, repo.dir, "rev-parse", "main")
	writeFile(t, repo.dir, "draft.txt", "pre-operation draft\n")

	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("amend", worktree, "main", original, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	app := &App{git: git}
	if err := app.prepareOperationWorktree(operation); err != nil {
		t.Fatal(err)
	}
	value := RefValue{Exists: true, OID: original}
	operation.Refs["refs/heads/main"] = JournalRef{Original: value, Expected: value}
	operation.Phase = operationRollingBack
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}
	if err := git.WriteState(State{Operation: operation}); err != nil {
		t.Fatal(err)
	}
	artifactDir, err := app.operationArtifactDir(operation)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a crash after rollback detached HEAD but before restoring the
	// pre-operation untracked snapshot, followed by a different process placing
	// a new file at the same path.
	runGit(t, repo.dir, "switch", "--detach", original)
	writeFile(t, repo.dir, "draft.txt", "new occupant after crash\n")
	code, _, stderr := repo.runGraphene(t, "abort", "--force")
	if code == 0 {
		t.Fatal("graphene abort unexpectedly overwrote an untracked collision")
	}
	if !strings.Contains(stderr, "different path now exists there") {
		t.Fatalf("stderr = %q, want untracked-restore collision error", stderr)
	}
	data, err := os.ReadFile(filepath.Join(repo.dir, "draft.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new occupant after crash\n" {
		t.Fatalf("draft.txt = %q, want collision bytes preserved", data)
	}
	state := readState(t, repo.dir)
	if state.Operation == nil || state.Operation.ID != operation.ID {
		t.Fatalf("operation = %#v, want retained journal %q", state.Operation, operation.ID)
	}
	if state.Operation.Phase != operationRollingBack || state.Operation.WorktreeRestored {
		t.Fatalf("operation recovery checkpoint = (%q, %t), want rolling-back and not restored", state.Operation.Phase, state.Operation.WorktreeRestored)
	}
	for artifact := range operation.ArtifactDigests {
		if _, err := os.Stat(filepath.Join(artifactDir, artifact)); err != nil {
			t.Errorf("recovery artifact %q was not retained: %v", artifact, err)
		}
	}
}

func TestJournalAbortRefusesFreshEditFromPreRestoreCrashGap(t *testing.T) {
	t.Parallel()

	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	original := commitFile(t, repo.dir, "tracked.txt", "original\n", "Add tracked file")
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("amend", worktree, "main", original, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	app := &App{git: git}
	if err := app.prepareOperationWorktree(operation); err != nil {
		t.Fatal(err)
	}
	value := RefValue{Exists: true, OID: original}
	operation.Refs["refs/heads/main"] = JournalRef{Original: value, Expected: value}
	if err := git.InstallOperationBackups(operation); err != nil {
		t.Fatal(err)
	}

	// Simulate the old crash gap: rolling-back was durable, but the next
	// checkpoint saying destructive worktree restoration had started was not.
	operation.Phase = operationRollingBack
	operation.WorktreeRestoreStarted = false
	if err := git.WriteState(State{Operation: operation}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo.dir, "tracked.txt", "fresh edit after crash\n")

	code, _, stderr := repo.runGraphene(t, "abort")
	if code == 0 {
		t.Fatal("graphene abort unexpectedly overwrote a fresh post-crash edit")
	}
	if !strings.Contains(stderr, "stopped during destructive rollback") || !strings.Contains(stderr, "abort --force") {
		t.Fatalf("stderr = %q, want resumed-rollback force guard", stderr)
	}
	data, err := os.ReadFile(filepath.Join(repo.dir, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "fresh edit after crash\n" {
		t.Fatalf("tracked.txt after refused abort = %q, want fresh edit preserved", got)
	}
	state := readState(t, repo.dir)
	if state.Operation == nil || state.Operation.Phase != operationRollingBack || state.Operation.WorktreeRestoreStarted {
		t.Fatalf("state after refused abort = %#v, want original crash-gap checkpoint", state.Operation)
	}

	expectGrapheneOK(t, repo, "abort", "--force")
	data, err = os.ReadFile(filepath.Join(repo.dir, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "original\n" {
		t.Fatalf("tracked.txt after forced abort = %q, want original contents", got)
	}
}

func writeBytes(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
