package graphene

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestRestackPreflightsTemporaryIgnoredFile(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "rpc/a", "temporary file\n", "One")
	runGit(t, repo.dir, "rm", "rpc/a")
	runGit(t, repo.dir, "commit", "-m", "remove temporary file")
	runGit(t, repo.dir, "switch", "-c", "target", "main")
	commitFile(t, repo.dir, "target-file", "target\n", "target")
	runGit(t, repo.dir, "switch", "stack/one")
	writeFile(t, repo.dir, ".git/info/exclude", "/rpc/\n")
	writeFile(t, repo.dir, "rpc/a", "precious ignored file\n")
	before := readState(t, repo.dir)
	refs := runGit(t, repo.dir, "show-ref")
	if code, _, stderr := repo.runGraphene(t, "restack", "target"); code == 0 || !strings.Contains(stderr, `path "rpc/a"`) {
		t.Fatalf("restack missed temporary replay path: %d: %s", code, stderr)
	}
	if !reflect.DeepEqual(readState(t, repo.dir), before) || runGit(t, repo.dir, "show-ref") != refs {
		t.Fatal("preflight failure changed refs or state")
	}
	if data, err := os.ReadFile(filepath.Join(repo.dir, "rpc/a")); err != nil || string(data) != "precious ignored file\n" {
		t.Fatalf("ignored file = %q, error = %v", data, err)
	}
}

func TestCheckoutPathRisks(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	writeFile(t, repo.dir, ".git/info/exclude", "*.ignored\n")
	for i, tc := range []struct {
		name, local, affected string
		symlink, safe         bool
	}{
		{name: "file", local: "local", affected: "local"},
		{name: "ignored file", local: "local.ignored", affected: "local.ignored"},
		{name: "directory", local: "local/precious", affected: "local"},
		{name: "ancestor file", local: "local", affected: "local/new"},
		{name: "ancestor symlink", local: "local", affected: "local/new", symlink: true},
		{name: "new sibling", local: "local/precious", affected: "local/new", safe: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := fmt.Sprintf("case-%d/", i)
			local, affected := prefix+tc.local, prefix+tc.affected
			if tc.symlink {
				if err := os.MkdirAll(filepath.Join(repo.dir, prefix), 0700); err != nil {
					t.Fatal(err)
				}
				outside := t.TempDir()
				writeFile(t, outside, "precious", "local edits\n")
				if err := os.Symlink(filepath.Join(outside, "precious"), filepath.Join(repo.dir, local)); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, repo.dir, local, "local edits\n")
			}
			risks, err := (Git{Dir: repo.dir}).checkoutPathRisks(checkoutPaths{affected: false}, "")
			if err != nil {
				t.Fatal(err)
			}
			var want []nestedRepositoryRisk
			if !tc.safe {
				want = []nestedRepositoryRisk{{path: affected}}
			}
			if !slices.Equal(risks, want) {
				t.Fatalf("risks = %#v, want %#v", risks, want)
			}
			if data, err := os.ReadFile(filepath.Join(repo.dir, local)); err != nil || string(data) != "local edits\n" {
				t.Fatalf("local file = %q, error = %v", data, err)
			}
		})
	}
}

func TestSyncPreflightsUntrackedPathsBeforeMutation(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	actor := cloneConfiguredRepo(t, remote, "main")
	commitFile(t, actor, "local", "upstream\n", "upstream file")
	runGit(t, actor, "push", "origin", "main")
	writeFile(t, repo.dir, "local", "local edits\n")
	before := readState(t, repo.dir)
	refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	if code, _, stderr := repo.runGraphene(t, "sync", "--force"); code == 0 || !strings.Contains(stderr, `path "local"`) {
		t.Fatalf("sync missed collision: %d: %s", code, stderr)
	}
	if !reflect.DeepEqual(readState(t, repo.dir), before) || runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads") != refs {
		t.Fatal("preflight failure mutated refs or stack state")
	}
	if data, err := os.ReadFile(filepath.Join(repo.dir, "local")); err != nil || string(data) != "local edits\n" {
		t.Fatalf("local file = %q, error = %v", data, err)
	}
}

func TestCleanRecoveryLeavesUntrackedEditsAlone(t *testing.T) {
	t.Parallel()
	repo, original, refs := restackConflict(t)
	id := readState(t, repo.dir).Pending.Recovery.Snapshot
	snapshot, err := (Git{Dir: repo.dir}).readSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Head != snapshot.Refs[snapshot.Branch] {
		t.Fatalf("recovery lost its original checkout: %#v", snapshot)
	}
	backups := runGit(t, repo.dir, "for-each-ref", "--format=%(refname)", snapshotRefPrefix(id))
	if strings.Contains(backups, "/index") || strings.Contains(backups, "/worktree") {
		t.Fatalf("recovery backed up file contents: %s", backups)
	}
	writeFile(t, repo.dir, "notes", "edited during conflict\n")
	writeFile(t, repo.dir, "new-notes", "created during conflict\n")
	expectGrapheneOK(t, repo, "abort")
	if !reflect.DeepEqual(readState(t, repo.dir), original) || runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads") != refs {
		t.Fatal("abort did not restore refs and metadata")
	}
	for path, want := range map[string]string{"notes": "edited during conflict\n", "new-notes": "created during conflict\n"} {
		if data, err := os.ReadFile(filepath.Join(repo.dir, path)); err != nil || string(data) != want {
			t.Fatalf("%s = %q, error = %v", path, data, err)
		}
	}
	assertRestackSnapshotRemoved(t, repo, id)
}

func TestAbortPreflightsNewUntrackedCollision(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	commitFile(t, repo.dir, "restore-me", "original\n", "original path")
	createStackBranch(t, repo, "file.txt", "topic\n", "One")
	runGit(t, repo.dir, "switch", "-c", "target", "main")
	runGit(t, repo.dir, "rm", "restore-me")
	commitFile(t, repo.dir, "file.txt", "target\n", "target")
	runGit(t, repo.dir, "switch", "stack/one")
	code, _, stderr := repo.runGraphene(t, "restack", "target")
	pending := readState(t, repo.dir).Pending
	if code == 0 || pending == nil || pending.Recovery == nil || pending.Recovery.Phase != recoveryConflict {
		t.Fatalf("expected conflict: %d: %s", code, stderr)
	}
	writeFile(t, repo.dir, "restore-me", "new local work\n")
	before := readState(t, repo.dir)
	refs := runGit(t, repo.dir, "show-ref")
	if code, _, stderr := repo.runGraphene(t, "abort"); code == 0 || !strings.Contains(stderr, `path "restore-me"`) {
		t.Fatalf("abort missed untracked collision: %d: %s", code, stderr)
	}
	if !reflect.DeepEqual(readState(t, repo.dir), before) || runGit(t, repo.dir, "show-ref") != refs {
		t.Fatal("refused abort changed refs or recovery state")
	}
	if data, err := os.ReadFile(filepath.Join(repo.dir, "restore-me")); err != nil || string(data) != "new local work\n" {
		t.Fatalf("local file = %q, error = %v", data, err)
	}
	if err := os.Rename(filepath.Join(repo.dir, "restore-me"), filepath.Join(t.TempDir(), "saved")); err != nil {
		t.Fatal(err)
	}
	expectGrapheneOK(t, repo, "abort")
	if data, err := os.ReadFile(filepath.Join(repo.dir, "restore-me")); err != nil || string(data) != "original\n" {
		t.Fatalf("restored file = %q, error = %v", data, err)
	}
}

func BenchmarkCheckoutPreflightIgnoredFiles(b *testing.B) {
	root := b.TempDir()
	g := Git{Dir: root}
	run := func(args ...string) string {
		b.Helper()
		out, err := g.Output(args...)
		if err != nil {
			b.Fatal(err)
		}
		return out
	}
	write := func(path, content string) {
		b.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			b.Fatal(err)
		}
	}
	run("init", "--quiet")
	run("config", "user.name", "Graphene Test")
	run("config", "user.email", "graphene@example.test")
	run("config", "commit.gpgsign", "false")
	write(".gitignore", "*.generated\n")
	run("add", ".gitignore")
	run("commit", "--quiet", "-m", "Initial")
	head := run("rev-parse", "HEAD")
	write("new-file", "upstream\n")
	run("add", "new-file")
	run("commit", "--quiet", "-m", "New file")
	target := run("rev-parse", "HEAD")
	run("read-tree", "--reset", "-u", head)
	for i := range 100 {
		dir := fmt.Sprintf("build-%03d", i)
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			b.Fatal(err)
		}
		for j := range 150 {
			write(filepath.Join(dir, fmt.Sprintf("%03d.generated", j)), "")
		}
	}
	for b.Loop() {
		if err := g.checkCheckoutPaths(head, target); err != nil {
			b.Fatal(err)
		}
	}
}
