package graphene

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

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
