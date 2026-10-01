package graphene

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGraphJSONShowsScopedBranchesAndPending(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	createStackBranch(t, repo, "file.txt", "one\n", "One")
	one := runGit(t, repo.dir, "rev-parse", "HEAD")
	createStackBranch(t, repo, "file.txt", "two\n", "Two")
	two := runGit(t, repo.dir, "rev-parse", "HEAD")
	runGit(t, repo.dir, "switch", "main")
	createStackBranch(t, repo, "other.txt", "other\n", "Other")
	runGit(t, repo.dir, "switch", "stack/one")
	before := readState(t, repo.dir)
	code, stdout, stderr := repo.runGraphene(t, "graph", "--stack", "--json")
	var graph graphOutput
	if code != 0 || json.Unmarshal([]byte(stdout), &graph) != nil {
		t.Fatalf("graph JSON: %d, %s, %s", code, stdout, stderr)
	}
	want := []branchOutput{
		{Name: "main", Commit: runGit(t, repo.dir, "rev-parse", "main")},
		{Name: "stack/one", Parent: "main", Commit: one, Tracked: true},
		{Name: "stack/two", Parent: "stack/one", Commit: two, Tracked: true},
	}
	if graph.SchemaVersion != 1 || graph.CurrentBranch != "stack/one" || graph.Pending != nil || !reflect.DeepEqual(graph.Branches, want) {
		t.Fatalf("graph = %#v, want branches %#v", graph, want)
	}
	if !reflect.DeepEqual(readState(t, repo.dir), before) {
		t.Fatal("graph JSON changed state")
	}

	writeFile(t, repo.dir, "file.txt", "amended\n")
	runGit(t, repo.dir, "add", "file.txt")
	if code, _, stderr = repo.runGraphene(t, "amend", "--no-edit"); code == 0 || !strings.Contains(stderr, "could not apply") {
		t.Fatalf("expected rebase conflict: %d, %s", code, stderr)
	}
	before = readState(t, repo.dir)
	code, stdout, stderr = repo.runGraphene(t, "graph", "--json")
	if code != 0 || json.Unmarshal([]byte(stdout), &graph) != nil {
		t.Fatalf("pending JSON: %d, %s, %s", code, stdout, stderr)
	}
	pending := &pendingOutput{
		Operation: "amend", Branch: "stack/one", ReturnBranch: "stack/one",
		Rebases: []queuedRebaseOutput{{Branch: "stack/two", Onto: "stack/one", Upstream: one}},
	}
	if !reflect.DeepEqual(graph.Pending, pending) || len(graph.Branches) != 4 || strings.Contains(stdout, "rewriteBefore") {
		t.Fatalf("graph with pending operation = %#v", graph)
	}
	if !reflect.DeepEqual(readState(t, repo.dir), before) {
		t.Fatal("pending JSON changed state")
	}
}

func TestGraphJSONEmptyAndDetached(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	for _, current := range []string{"main", ""} {
		if current == "" {
			runGit(t, repo.dir, "switch", "--detach")
		}
		code, stdout, stderr := repo.runGraphene(t, "graph", "--json")
		var graph graphOutput
		if code != 0 || json.Unmarshal([]byte(stdout), &graph) != nil || graph.CurrentBranch != current || graph.Branches == nil || len(graph.Branches) != 0 || graph.Pending != nil {
			t.Fatalf("empty graph JSON: %d, %s, %s", code, stdout, stderr)
		}
	}
}

func TestGraphJSONWithSymbolicLocalRef(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"empty", "legacy boundaries", "saved boundaries"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			want := []branchOutput{}
			if kind != "empty" {
				createStackBranch(t, repo, "one.txt", "one\n", "One")
				want = []branchOutput{
					{Name: "main", Commit: runGit(t, repo.dir, "rev-parse", "main")},
					{Name: "stack/one", Parent: "main", Commit: runGit(t, repo.dir, "rev-parse", "HEAD"), Tracked: true},
				}
				if kind == "legacy boundaries" {
					state := readState(t, repo.dir)
					state.Boundaries = nil
					if err := (Git{Dir: repo.dir}).WriteState(state); err != nil {
						t.Fatal(err)
					}
				}
			}
			runGit(t, repo.dir, "symbolic-ref", "refs/heads/alias", "refs/heads/main")
			before := readState(t, repo.dir)
			refs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname) %(symref)")
			for _, stack := range []bool{false, true} {
				args := []string{"graph"}
				if stack {
					args = append(args, "--stack")
				}
				expectGrapheneOK(t, repo, args...)
				code, stdout, stderr := repo.runGraphene(t, append(args, "--json")...)
				var graph graphOutput
				if code != 0 || json.Unmarshal([]byte(stdout), &graph) != nil || stderr != "" {
					t.Fatalf("graph JSON: %d, %s, %s", code, stdout, stderr)
				}
				if !reflect.DeepEqual(graph.Branches, want) || graph.Pending != nil {
					t.Fatalf("graph = %#v, want branches %#v and no pending operation", graph, want)
				}
			}
			if got := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname) %(symref)"); got != refs {
				t.Fatal("graph changed refs")
			}
			if !reflect.DeepEqual(readState(t, repo.dir), before) {
				t.Fatal("graph changed state")
			}
			if _, err := (Git{Dir: repo.dir}).snapshotBranchRefs(); err == nil || !strings.Contains(err.Error(), "snapshots require ordinary local branch refs") {
				t.Fatalf("snapshot ref reader accepted symbolic ref: %v", err)
			}
		})
	}
}

func TestSendJSONKeepsDryRunReadOnly(t *testing.T) {
	t.Parallel()
	repo, remote := newTestRepoWithOrigin(t)
	createStackBranch(t, repo, "one.txt", "one\n", "One")
	createStackBranch(t, repo, "two.txt", "two\n", "Two")
	runGit(t, repo.dir, "switch", "stack/one")
	before := readState(t, repo.dir)
	remoteRefs := runGit(t, remote, "for-each-ref", "--format=%(refname) %(objectname)")
	localRefs := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)")
	config := runGit(t, repo.dir, "config", "--local", "--list")
	writeExecutable(t, filepath.Join(repo.dir, ".git", "hooks", "pre-push"), "#!/bin/sh\nprintf 'pre-push diagnostic\\n'\n")
	for _, tc := range []struct {
		command string
		stack   bool
	}{
		{"send", false},
		{"sendf", true},
	} {
		args := []string{tc.command, "--dry-run", "--json"}
		wantBranches := []string{"stack/one"}
		if tc.stack {
			args = append(args, "--stack")
			wantBranches = append(wantBranches, "stack/two")
		}
		code, stdout, stderr := repo.runGraphene(t, args...)
		var plan pushPlanOutput
		if code != 0 || json.Unmarshal([]byte(stdout), &plan) != nil || !strings.Contains(stderr, "pre-push diagnostic") {
			t.Fatalf("push JSON: %d, %s, %s", code, stdout, stderr)
		}
		if plan.SchemaVersion != 1 || plan.Remote != "origin" || !plan.Atomic || !plan.DryRun || plan.ForceWithLease != (tc.command == "sendf") || plan.CurrentBranch != "stack/one" || !reflect.DeepEqual(plan.Branches, wantBranches) {
			t.Fatalf("push plan = %#v, want branches %v", plan, wantBranches)
		}
	}
	if got := runGit(t, remote, "for-each-ref", "--format=%(refname) %(objectname)"); got != remoteRefs {
		t.Fatal("JSON dry-run changed remote refs")
	}
	if got := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)"); got != localRefs {
		t.Fatal("JSON dry-run changed local refs")
	}
	if got := runGit(t, repo.dir, "config", "--local", "--list"); got != config || !reflect.DeepEqual(readState(t, repo.dir), before) {
		t.Fatal("JSON dry-run changed local state or upstreams")
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"send", "--json"}, "--json requires --dry-run"},
		{[]string{"send", "--dry-run", "--json", "missing-remote"}, "does not appear to be a git repository"},
	} {
		code, stdout, stderr := repo.runGraphene(t, tc.args...)
		if code == 0 || stdout != "" || !strings.Contains(stderr, tc.want) {
			t.Fatalf("expected failure without JSON for %v: %d, %s, %s", tc.args, code, stdout, stderr)
		}
	}
}
