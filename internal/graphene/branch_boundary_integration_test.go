package graphene

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestResolveSavedBoundaryDoesNotInferReplacement(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	base := runGit(t, repo.dir, "rev-parse", "main")
	runGit(t, repo.dir, "switch", "-c", "one")
	commitFile(t, repo.dir, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "switch", "main")
	other := commitFile(t, repo.dir, "other.txt", "other\n", "Other")
	app := &App{git: Git{Dir: repo.dir}}
	refs, err := app.git.snapshotBranchRefs()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, boundary, wantError string }{
		{"valid", base, ""},
		{"diverged", other, "not an ancestor"},
		{"missing object", strings.Repeat("f", 40), "not an available commit"},
		{"wrong object type", runGit(t, repo.dir, "rev-parse", "main^{tree}"), "not a commit"},
		{"empty saved value", "", "expected a full commit ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := State{
				Stacks:     []Stack{{Base: "main", Branches: []string{"one"}}},
				Boundaries: map[string]string{"one": tc.boundary},
			}
			got, err := app.resolveBranchBoundary(state, "one", refs)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("boundary = %q, error = %v; want %q", got, err, tc.wantError)
				}
			} else if err != nil || got != base {
				t.Fatalf("boundary = %q, error = %v; want %q", got, err, base)
			}
		})
	}
}

func TestResolveLegacyBoundaryUsesKnownHistoryWithoutWriting(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	initial := runGit(t, repo.dir, "rev-parse", "main")
	base := commitFile(t, repo.dir, "base.txt", "base\n", "Base")
	runGit(t, repo.dir, "switch", "-c", "one")
	commitFile(t, repo.dir, "one.txt", "one\n", "One")
	runGit(t, repo.dir, "remote", "add", "origin", t.TempDir())
	runGit(t, repo.dir, "update-ref", "refs/remotes/origin/main", base)
	runGit(t, repo.dir, "branch", "--set-upstream-to=origin/main", "main")
	runGit(t, repo.dir, "update-ref", "refs/heads/main", initial)
	// Make upstream the only evidence of where this stack started.
	runGit(t, repo.dir, "reflog", "expire", "--expire=now", "--all")
	app := &App{git: Git{Dir: repo.dir}}
	legacy := State{Stacks: []Stack{{Base: "main", Branches: []string{"one"}}}}
	if err := app.git.WriteState(legacy); err != nil {
		t.Fatal(err)
	}
	path, err := app.git.stateFilePath()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(want string) {
		t.Helper()
		refs, err := app.git.snapshotBranchRefs()
		if err != nil {
			t.Fatal(err)
		}
		got, err := app.resolveBranchBoundary(legacy, "one", refs)
		if want == "" {
			if err == nil || !strings.Contains(err.Error(), "cannot determine") {
				t.Fatalf("boundary = %q, error = %v; want refusal without historical evidence", got, err)
			}
		} else if err != nil || got != want {
			t.Fatalf("boundary = %q, error = %v; want %q", got, err, want)
		}
		if after := readState(t, repo.dir); !reflect.DeepEqual(after, legacy) {
			t.Fatalf("resolver changed state: %#v", after)
		}
	}
	resolve(base)
	runGit(t, repo.dir, "switch", "-c", "replacement", "main")
	rewritten := commitFile(t, repo.dir, "base.txt", "rewritten\n", "Rewritten base")
	runGit(t, repo.dir, "update-ref", "refs/heads/main", rewritten)
	runGit(t, repo.dir, "update-ref", "refs/remotes/origin/main", rewritten)
	resolve(base) // The old upstream tip is now available only through its reflog.
	runGit(t, repo.dir, "reflog", "expire", "--expire=now", "--all")
	resolve("")
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || legacy.Boundaries != nil {
		t.Fatal("boundary inference persisted or mutated legacy metadata")
	}
}
