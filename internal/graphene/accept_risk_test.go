package graphene

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func replayRiskRepo(t *testing.T) (testRepo, string) {
	t.Helper()
	repo := newTestRepo(t)
	commitFile(t, repo.dir, "a", "nested original\n", "nested file")
	createStackBranch(t, repo, "rpc/a", "temporary parent file\n", "One")
	runGit(t, repo.dir, "rm", "rpc/a")
	runGit(t, repo.dir, "commit", "-m", "remove temporary parent file")
	runGit(t, repo.dir, "switch", "-c", "target", "main")
	commitFile(t, repo.dir, "target-file", "target\n", "target")
	runGit(t, repo.dir, "switch", "stack/one")
	nested := filepath.Join(repo.dir, "rpc")
	runGit(t, repo.dir, "worktree", "add", "--detach", nested, "main")
	writeFile(t, nested, "a", "precious local edits\n")
	writeFile(t, repo.dir, ".git/info/exclude", "/rpc/\n")
	return repo, nested
}

func TestRestackReplayRiskRequiresAcceptance(t *testing.T) {
	t.Parallel()
	repo, nested := replayRiskRepo(t)
	beforeRefs := runGit(t, repo.dir, "show-ref")
	beforeState := readState(t, repo.dir)
	beforeNested := nestedState(t, nested)
	code, _, stderr := repo.runGraphene(t, "restack", "target")
	if code == 0 || !strings.Contains(stderr, `"rpc/a"`) || !strings.Contains(stderr, "--accept-risk") || !strings.Contains(stderr, "abort cannot restore") {
		t.Fatalf("expected risk warning, got %d: %s", code, stderr)
	}
	if runGit(t, repo.dir, "show-ref") != beforeRefs || !reflect.DeepEqual(readState(t, repo.dir), beforeState) || nestedState(t, nested) != beforeNested {
		t.Fatal("refused restack changed refs, state, or nested contents")
	}
	if data, err := os.ReadFile(filepath.Join(nested, "a")); err != nil || string(data) != "precious local edits\n" {
		t.Fatalf("nested edits = %q, error %v", data, err)
	}
	code, _, stderr = repo.runGraphene(t, "restack", "--accept-risk", "target")
	if code != 0 || !strings.Contains(stderr, `"rpc/a"`) || !strings.Contains(stderr, "abort cannot restore") {
		t.Fatalf("accepted restack returned %d: %s", code, stderr)
	}
	if readState(t, repo.dir).Pending != nil {
		t.Fatal("accepted restack left pending operation")
	}
}

func TestRestackHistoricalUpstreamIsNotDestination(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	commitFile(t, repo.dir, "rpc/a", "old parent file\n", "old parent file")
	runGit(t, repo.dir, "rm", "rpc/a")
	expectGrapheneOK(t, repo, "new", "-m", "Remove rpc")
	runGit(t, repo.dir, "branch", "target")
	nested := filepath.Join(repo.dir, "rpc")
	runGit(t, repo.dir, "worktree", "add", "--detach", nested, "target")
	before := nestedState(t, nested)
	expectGrapheneOK(t, repo, "restack", "target")
	if nestedState(t, nested) != before {
		t.Fatal("safe restack changed nested worktree")
	}
}

func TestSyncRiskPreviewAndAcceptance(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	actor := cloneConfiguredRepo(t, remote, "main")
	commitFile(t, actor, "rpc/remote", "remote\n", "remote path")
	runGit(t, actor, "push", "origin", "main")
	nested := filepath.Join(repo.dir, "rpc")
	runGit(t, repo.dir, "worktree", "add", "--detach", nested, "main")
	writeFile(t, repo.dir, ".git/info/exclude", "/rpc/\n")
	beforeRefs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	beforeState := readState(t, repo.dir)
	for _, args := range [][]string{{"sync", "--dry-run"}, {"sync", "--force"}} {
		code, _, stderr := repo.runGraphene(t, args...)
		if (code == 0) != (args[1] == "--dry-run") || !strings.Contains(stderr, `"rpc/remote"`) || !strings.Contains(stderr, "abort cannot restore") {
			t.Fatalf("%v returned %d: %s", args, code, stderr)
		}
		if runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads") != beforeRefs || !reflect.DeepEqual(readState(t, repo.dir), beforeState) {
			t.Fatal("preview/refusal moved local refs or state")
		}
	}
	code, _, stderr := repo.runGraphene(t, "sync", "--accept-risk")
	if code != 0 || !strings.Contains(stderr, `"rpc/remote"`) {
		t.Fatalf("accepted sync returned %d: %s", code, stderr)
	}
}

func TestAcceptedRiskPersistsButAbortStillProtectsRepositories(t *testing.T) {
	t.Parallel()
	repo, nested := replayRiskRepo(t)
	runGit(t, repo.dir, "switch", "target")
	commitFile(t, repo.dir, "file.txt", "target conflict\n", "target conflict")
	runGit(t, repo.dir, "switch", "stack/one")
	// Stage only this parent file; the ignored nested worktree is independent.
	writeFile(t, repo.dir, "file.txt", "topic conflict\n")
	runGit(t, repo.dir, "add", "file.txt")
	runGit(t, repo.dir, "commit", "-m", "topic conflict")
	code, _, stderr := repo.runGraphene(t, "restack", "--accept-risk", "target")
	pending := readState(t, repo.dir).Pending
	if code == 0 || pending == nil || pending.Recovery.Phase != recoveryConflict || !pending.Recovery.AcceptRisk {
		t.Fatalf("expected accepted pending conflict, got %d: %s", code, stderr)
	}
	// The original snapshot owns file.txt. A repository created there during
	// the conflict must still block abort, despite acceptance of forward risk.
	if err := os.Rename(filepath.Join(repo.dir, "file.txt"), filepath.Join(repo.dir, "saved-conflict")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.dir, "init", filepath.Join(repo.dir, "file.txt"))
	if code, _, stderr := repo.runGraphene(t, "abort"); code == 0 || !strings.Contains(stderr, `nested repository "file.txt"`) {
		t.Fatalf("accepted risk bypassed abort collision: %d: %s", code, stderr)
	}
	if err := os.Rename(filepath.Join(repo.dir, "file.txt"), filepath.Join(t.TempDir(), "moved")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo.dir, "file.txt", "resolved\n")
	runGit(t, repo.dir, "add", "file.txt")
	expectGrapheneOK(t, repo, "continue")
	if _, err := os.Stat(filepath.Join(nested, ".git")); err != nil {
		t.Fatal(err)
	}
}

func TestUnitAcceptRiskParsing(t *testing.T) {
	t.Parallel()
	for _, flags := range [][]string{{"--accept-risk"}, {"--accept-risk=true"}, {"--accept-risk", "--no-accept-risk"}, {"--accept-risk=false"}} {
		want := len(flags) == 1 && flags[0] != "--accept-risk=false"
		sync, err := parseSyncArgs(flags)
		if err != nil || sync.acceptRisk != want {
			t.Fatalf("sync %v = %#v, %v", flags, sync, err)
		}
		restack, err := parseRestackArgs(append([]string{"target"}, flags...))
		if err != nil || restack.acceptRisk != want {
			t.Fatalf("restack %v = %#v, %v", flags, restack, err)
		}
	}
}

func TestContinueRechecksReplayRisks(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "file.txt", "topic\n", "One")
	commitFile(t, repo.dir, "rpc/a", "temporary\n", "temporary path")
	runGit(t, repo.dir, "rm", "rpc/a")
	runGit(t, repo.dir, "commit", "-m", "remove temporary path")
	runGit(t, repo.dir, "switch", "-c", "target", "main")
	commitFile(t, repo.dir, "file.txt", "target\n", "target")
	runGit(t, repo.dir, "switch", "stack/one")
	code, _, stderr := repo.runGraphene(t, "restack", "target")
	pending := readState(t, repo.dir).Pending
	if code == 0 || pending == nil || pending.Recovery.Phase != recoveryConflict {
		t.Fatalf("expected conflict: %d: %s", code, stderr)
	}
	nested := filepath.Join(repo.dir, "rpc")
	runGit(t, repo.dir, "worktree", "add", "--detach", nested, "main")
	writeFile(t, nested, "a", "new local edits\n")
	writeFile(t, repo.dir, ".git/info/exclude", "/rpc/\n")
	writeFile(t, repo.dir, "file.txt", "resolved\n")
	runGit(t, repo.dir, "add", "file.txt")
	beforeState := readState(t, repo.dir)
	beforeRefs := runGit(t, repo.dir, "show-ref")
	code, _, stderr = repo.runGraphene(t, "continue")
	if code == 0 || !strings.Contains(stderr, `"rpc/a"`) || !strings.Contains(stderr, "--accept-risk") {
		t.Fatalf("continue missed new nested worktree: %d: %s", code, stderr)
	}
	if !reflect.DeepEqual(readState(t, repo.dir), beforeState) || runGit(t, repo.dir, "show-ref") != beforeRefs {
		t.Fatal("refused continue changed state or refs")
	}
	if data, err := os.ReadFile(filepath.Join(nested, "a")); err != nil || string(data) != "new local edits\n" {
		t.Fatalf("nested edits = %q, error %v", data, err)
	}
}
