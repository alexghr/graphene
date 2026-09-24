package graphene

import (
	"strings"
	"testing"
)

func TestStatefulCommandRejectsHeldStateLock(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	writeFile(t, repo.dir, "blocked.txt", "blocked\n")
	lock, err := (Git{Dir: repo.dir}).AcquireStateLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	code, _, stderr := repo.runGraphene(t, "new", "-m", "Blocked")
	if code == 0 || !strings.Contains(stderr, ErrStateLocked.Error()) {
		t.Fatalf("graphene new with held lock = (%d, %q), want state-lock error", code, stderr)
	}
	if got := currentBranch(t, repo.dir); got != "main" {
		t.Fatalf("branch after rejected command = %q, want main", got)
	}
}
