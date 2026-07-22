package graphene

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestContinueMigratesConfigOnlyLegacyPendingState(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "switch", "main")

	legacy := readState(t, repo.dir)
	legacy.Pending = &Pending{
		Operation:    "sync",
		Branch:       "main",
		ReturnBranch: "main",
		Branches:     []string{"stack/one"},
	}
	installConfigOnlyLegacyState(t, repo.dir, legacy)

	expectGrapheneOK(t, repo, "continue")

	if refExists(t, repo.dir, "refs/heads/stack/one") {
		t.Fatal("stack/one still exists after continuing legacy sync")
	}
	got := readState(t, repo.dir)
	if len(got.Stacks) != 0 || got.Pending != nil || got.Operation != nil {
		t.Fatalf("state after continue = %#v, want completed empty state", got)
	}
	assertStateMigrationFinalized(t, repo.dir)
}

func TestAbortMigratesConfigOnlyLegacyPendingState(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	createStackBranch(t, repo, "one.txt", "one\n", "One")
	want := readState(t, repo.dir)
	legacy := want
	legacy.Pending = &Pending{
		Operation: "restack",
		Branch:    "stack/one",
	}
	installConfigOnlyLegacyState(t, repo.dir, legacy)

	expectGrapheneOK(t, repo, "abort")

	got := readState(t, repo.dir)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state after abort = %#v, want %#v", got, want)
	}
	assertStateMigrationFinalized(t, repo.dir)
}

func TestInterruptedMigrationUsesFileStateAndFinalizesConfig(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}

	legacy := State{Stacks: []Stack{{Base: "legacy-base", Branches: []string{"legacy/one"}}}}
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.dir, "config", "--local", "--replace-all", stateConfigKey, string(legacyJSON))

	want := State{Stacks: []Stack{{Base: "main", Branches: []string{"stack/file"}}}}
	path, err := git.stateFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStateFileAtomic(path, newStateFile(want, stateMigrationPending)); err != nil {
		t.Fatal(err)
	}

	got, err := git.ReadState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadState() during interrupted migration = %#v, want file state %#v", got, want)
	}

	if err := git.WriteState(got); err != nil {
		t.Fatal(err)
	}
	assertStateMigrationFinalized(t, repo.dir)
	if got := readState(t, repo.dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("state after migration finalization = %#v, want %#v", got, want)
	}
}

func installConfigOnlyLegacyState(t *testing.T, dir string, state State) {
	t.Helper()
	git := Git{Dir: dir}
	path, err := git.stateFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "config", "--local", "--replace-all", stateConfigKey, string(raw))
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file still exists after installing config-only fixture: %v", err)
	}
}

func assertStateMigrationFinalized(t *testing.T, dir string) {
	t.Helper()
	git := Git{Dir: dir}
	if got := runGit(t, dir, "config", "--local", "--get", stateConfigKey); got != stateMigrationSentinel {
		t.Fatalf("legacy state marker = %q, want %q", got, stateMigrationSentinel)
	}
	path, err := git.stateFilePath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := decodeStateFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if file.Migration != "" {
		t.Fatalf("state file migration = %q, want finalized state", file.Migration)
	}
}
