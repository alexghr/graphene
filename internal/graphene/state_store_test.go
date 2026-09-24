package graphene

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestReadStateFallsBackToLegacyConfig(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	want := State{
		Stacks: []Stack{{Base: "main", Branches: []string{"stack/one"}}},
		Pending: &Pending{
			Operation:    "restack",
			Branch:       "stack/one",
			OriginalHead: strings.Repeat("a", 40),
		},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.dir, "config", "--local", stateConfigKey, string(raw))

	got, err := (Git{Dir: repo.dir}).ReadState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadState() = %#v, want %#v", got, want)
	}
}

func TestWriteStateMigratesLegacyConfigAtomically(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	legacy := State{Stacks: []Stack{{Base: "main", Branches: []string{"stack/old"}}}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.dir, "config", "--local", stateConfigKey, string(raw))

	want := State{
		Stacks:  []Stack{{Base: "main", Branches: []string{"stack/new"}}},
		Pending: &Pending{Operation: "restack", Branch: "stack/new", OriginalHead: strings.Repeat("a", 40)},
	}
	git := Git{Dir: repo.dir}
	if err := git.WriteState(want); err != nil {
		t.Fatal(err)
	}

	if got := runGit(t, repo.dir, "config", "--local", "--get", stateConfigKey); got != stateMigrationSentinel {
		t.Fatalf("legacy config = %q, want %q", got, stateMigrationSentinel)
	}
	path, err := git.stateFilePath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatal(err)
	}
	if string(encoded["version"]) != "1" || string(encoded["stacks"]) == "null" {
		t.Fatalf("state file = %s", data)
	}
	if _, ok := encoded["operation"]; ok {
		t.Fatalf("state file unexpectedly contains journal state: %s", data)
	}
	if _, ok := encoded["migration"]; ok {
		t.Fatalf("completed state file still has migration marker: %s", data)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state mode = %o, want 600", got)
	}
	if got, err := git.ReadState(); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadState() = %#v, want %#v", got, want)
	}
}

func TestUnitEmptyStateSerializesStacksArray(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(newStateFile(State{}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"stacks":[]`) {
		t.Fatalf("empty state file = %s", data)
	}
}

func TestStateFileIsSharedByLinkedWorktrees(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	primary := Git{Dir: repo.dir}
	want := State{Stacks: []Stack{{Base: "main", Branches: []string{"stack/one"}}}}
	if err := primary.WriteState(want); err != nil {
		t.Fatal(err)
	}

	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, repo.dir, "worktree", "add", "-b", "linked", linked)
	secondary := Git{Dir: linked}
	primaryDir, err := primary.GrapheneDir()
	if err != nil {
		t.Fatal(err)
	}
	secondaryDir, err := secondary.GrapheneDir()
	if err != nil {
		t.Fatal(err)
	}
	if primaryDir != secondaryDir {
		t.Fatalf("GrapheneDir() = %q in linked worktree, want %q", secondaryDir, primaryDir)
	}
	if got, err := secondary.ReadState(); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(got, want) {
		t.Fatalf("linked ReadState() = %#v, want %#v", got, want)
	}

	next := State{Stacks: []Stack{{Base: "main", Branches: []string{"stack/two"}}}}
	if err := secondary.WriteState(next); err != nil {
		t.Fatal(err)
	}
	if got, err := primary.ReadState(); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(got, next) {
		t.Fatalf("primary ReadState() = %#v, want %#v", got, next)
	}
}

func TestReadStateRecoversInterruptedMigrationMarker(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	want := State{
		Stacks:  []Stack{{Base: "main", Branches: []string{"stack/one"}}},
		Pending: &Pending{Operation: "split", Branch: "stack/one"},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	marker := stateMigrationPrefix + base64.RawURLEncoding.EncodeToString(raw)
	runGit(t, repo.dir, "config", "--local", stateConfigKey, marker)

	got, err := (Git{Dir: repo.dir}).ReadState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadState() = %#v, want %#v", got, want)
	}

	next := State{Stacks: want.Stacks, Pending: want.Pending}
	if err := (Git{Dir: repo.dir}).WriteState(next); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, repo.dir, "config", "--local", "--get", stateConfigKey); got != stateMigrationSentinel {
		t.Fatalf("legacy config = %q, want sentinel", got)
	}
}

func TestReadStateFailsClosed(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "config", "--local", stateConfigKey, `{"stacks":[{"base":"stale","branches":["state"]}]}`)
	git := Git{Dir: repo.dir}
	path, err := git.stateFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"stacks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := git.ReadState(); err == nil || !strings.Contains(err.Error(), "unsupported graphene state version 2") {
		t.Fatalf("ReadState() error = %v, want invalid file to override legacy config", err)
	}
}

func TestUnitDecodeStateFileRejectsInvalidSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "malformed", data: `{`, want: "unexpected EOF"},
		{name: "unknown version", data: `{"version":2,"stacks":[]}`, want: "unsupported graphene state version 2"},
		{name: "missing stacks", data: `{"version":1}`, want: "missing stacks"},
		{name: "unknown field", data: `{"version":1,"stacks":[],"surprise":true}`, want: "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeStateFile([]byte(tt.data)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("decodeStateFile() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestUnitLegacyStateCodecPreservesPending(t *testing.T) {
	t.Parallel()
	want := State{
		Stacks: []Stack{{Base: "main", Branches: []string{"stack/one"}}},
		Pending: &Pending{
			Operation:    "sync",
			Branch:       "main",
			ReturnBranch: "main",
			Branches:     []string{"stack/one"},
		},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, value string
		marked      bool
	}{
		{name: "legacy config", value: string(raw)},
		{name: "interrupted marker", value: stateMigrationPrefix + base64.RawURLEncoding.EncodeToString(raw), marked: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeLegacyStateValue(tt.value)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("decodeLegacyStateValue() = %#v, %v; want %#v", got, err, want)
			}
			source, marked, err := legacyMigrationSource(tt.value, true)
			if err != nil || source != string(raw) || marked != tt.marked {
				t.Fatalf("legacyMigrationSource() = %q, %t, %v", source, marked, err)
			}
		})
	}
}

func TestUnitLegacyMigrationSourceBoundaries(t *testing.T) {
	t.Parallel()
	if source, marked, err := legacyMigrationSource("", false); err != nil || source != "{}" || marked {
		t.Fatalf("missing legacy config = %q, %t, %v", source, marked, err)
	}
	if _, _, err := legacyMigrationSource(stateMigrationSentinel, true); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("sentinel migration source error = %v, want missing state file", err)
	}
	if _, err := decodeLegacyStateValue(stateMigrationSentinel); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("sentinel decode error = %v, want missing state file", err)
	}
	if _, _, err := legacyMigrationSource(stateMigrationPrefix+"!", true); err == nil || !strings.Contains(err.Error(), "migration marker") {
		t.Fatalf("malformed migration marker error = %v", err)
	}
}

func TestReadStateRejectsMultipleLegacyValues(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "config", "--local", "--add", stateConfigKey, `{}`)
	runGit(t, repo.dir, "config", "--local", "--add", stateConfigKey, `{}`)
	if _, err := (Git{Dir: repo.dir}).ReadState(); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("ReadState() error = %v, want duplicate-value error", err)
	}
}

func TestUnitEnsureDurableDirCreatesAndSecuresDirectChild(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, grapheneStateDirName)

	if err := ensureDurableDir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureDurableDir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("existing directory mode = %o, want 700", got)
	}
}

func TestUnitEnsureDurableDirCreatesMissingParents(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dirs := []string{
		filepath.Join(parent, grapheneStateDirName),
		filepath.Join(parent, grapheneStateDirName, "artifacts"),
		filepath.Join(parent, grapheneStateDirName, "artifacts", "operation-id"),
	}

	if err := ensureDurableDir(dirs[len(dirs)-1], 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("mode of %s = %o, want 700", dir, got)
		}
	}
	if info, err := os.Stat(parent); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("existing parent mode = %o, want 755", got)
	}
}

func TestUnitEnsureDurableDirRejectsFile(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, grapheneStateDirName)
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureDurableDir(dir, 0o700); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("ensureDurableDir() error = %v, want non-directory error", err)
	}
}

func TestUnitEnsureDurableDirRejectsSymbolicLink(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, grapheneStateDirName)
	if err := os.Symlink(target, dir); err != nil {
		t.Skipf("cannot create symbolic link: %v", err)
	}
	if err := ensureDurableDir(dir, 0o700); err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Fatalf("ensureDurableDir() error = %v, want symbolic-link error", err)
	}
}

func TestUnitEnsureDurableDirHandlesConcurrentCreation(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, grapheneStateDirName)
	start := make(chan struct{})
	errs := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			<-start
			errs <- ensureDurableDir(dir, 0o700)
		})
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestWithStateLockRejectsConcurrentGoroutine(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- git.WithStateLock(func(Git) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	called := false
	err := git.WithStateLock(func(Git) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrStateLocked) {
		close(release)
		t.Fatalf("concurrent WithStateLock() = %v, want ErrStateLocked", err)
	}
	if called {
		close(release)
		t.Fatal("concurrent WithStateLock() called its callback")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStateLockCloseIsConcurrentSafe(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	lock, err := git.AcquireStateLock()
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	done := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			done <- lock.Close()
		}()
	}
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	next, err := git.AcquireStateLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteStateUsesExistingStateLock(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	git := Git{Dir: repo.dir}
	want := State{Stacks: []Stack{{Base: "main", Branches: []string{"stack/one"}}}}
	if err := git.WithStateLock(func(locked Git) error {
		if err := locked.WriteState(want); err != nil {
			return err
		}
		return locked.WithStateLock(func(nested Git) error {
			return nested.WriteState(want)
		})
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := git.ReadState(); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadState() = %#v, want %#v", got, want)
	}
}
