package graphene

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareOperationWorktreeCleansPartialArtifacts(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	worktree, err := git.WorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newOperationJournal("amend", worktree, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.WorktreePolicy = worktreeRestoreIndex
	index, err := git.GitPath("index")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	app := &App{git: git}
	if err := app.prepareOperationWorktree(operation); err == nil || !strings.Contains(err.Error(), "snapshot git index") {
		t.Fatalf("prepareOperationWorktree error = %v, want index snapshot failure", err)
	}
	assertNoOperationArtifactDirs(t, git)
}

func TestRefMutationValidationFailureCleansArtifacts(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	value := RefValue{Exists: true, OID: head}
	edit := refEdit{Ref: "refs/heads/main", Old: value, New: value}
	app := &App{git: git}
	err := app.startRefMutationOperation(
		State{}, "track", "main", nil, nil, map[string]string{"main": head},
		[]refEdit{edit, edit}, nil, "", worktreeRestoreHard,
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate ref") {
		t.Fatalf("startRefMutationOperation error = %v, want duplicate ref failure", err)
	}
	assertNoOperationArtifactDirs(t, git)
}

func TestSyncValidationFailureCleansArtifacts(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	head := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "branch", "stack/duplicate")
	state := State{Stacks: []Stack{{Base: "main", Branches: []string{"stack/duplicate"}}}}
	component := syncComponent{
		Names:    []string{"stack/duplicate"},
		Branches: map[string]bool{"stack/duplicate": true},
		Deleted:  []string{"stack/duplicate"},
	}
	app := &App{git: git}
	err := app.startSyncOperation(
		state, "main", "main", head, "main", head, head, false,
		[]syncComponent{component, component},
		map[string]string{"stack/duplicate": head},
		map[string]string{"main": head, "stack/duplicate": head},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "both own refs/heads/stack/duplicate") {
		t.Fatalf("startSyncOperation error = %v, want duplicate component ref failure", err)
	}
	assertNoOperationArtifactDirs(t, git)
}

func TestSplitValidationFailureCleansArtifacts(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	base := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "switch", "-c", "stack/target")
	original := commitFile(t, repo.dir, "target.txt", "one\n", "Target")
	replacement := commitFile(t, repo.dir, "replacement.txt", "two\n", "Replacement")
	runGit(t, repo.dir, "switch", "main")
	runGit(t, repo.dir, "update-ref", "refs/heads/stack/target", original, replacement)

	grapheneDir, err := git.GrapheneDir()
	if err != nil {
		t.Fatal(err)
	}
	artifactRoot := filepath.Join(grapheneDir, "artifacts")
	mutationDone := make(chan error, 1)
	watching := make(chan struct{})
	go func() {
		close(watching)
		deadline := time.Now().Add(5 * time.Second)
		for {
			entries, readErr := os.ReadDir(artifactRoot)
			if readErr == nil && len(entries) > 0 {
				break
			}
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				mutationDone <- readErr
				return
			}
			if time.Now().After(deadline) {
				mutationDone <- errors.New("timed out waiting for split recovery artifacts")
				return
			}
			time.Sleep(time.Millisecond)
		}
		cmd := exec.Command("git", "update-ref", "refs/heads/stack/target", replacement, original)
		cmd.Dir = repo.dir
		mutationDone <- cmd.Run()
	}()
	<-watching

	state := State{Stacks: []Stack{{Base: "main", Branches: []string{"stack/target"}}}}
	app := &App{git: git}
	err = app.startSplitOperation(state, "main", "stack/target", "main", base, original, state)
	if mutationErr := <-mutationDone; mutationErr != nil {
		t.Fatalf("move split target: %v", mutationErr)
	}
	if err == nil || !strings.Contains(err.Error(), `planned branch "stack/target" moved`) {
		t.Fatalf("startSplitOperation error = %v, want planned-ref validation failure", err)
	}
	assertNoOperationArtifactDirs(t, git)
}

func assertNoOperationArtifactDirs(t *testing.T, git Git) {
	t.Helper()
	dir, err := git.GrapheneDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "artifacts"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unpublished operation left artifact directories: %v", entries)
	}
}
