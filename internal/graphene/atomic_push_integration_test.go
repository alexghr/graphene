package graphene

import (
	"reflect"
	"strings"
	"testing"
)

func TestSendRejectsAllBranchesAtomically(t *testing.T) {
	for _, tc := range []struct{ name, command, errorText string }{
		{"non-fast-forward", "send", "non-fast-forward"},
		{"stale lease", "sendf", "stale info"},
		{"unsupported server", "send", "does not support --atomic push"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, remote := newTestRepoWithOrigin(t)
			createStackBranch(t, repo, "one.txt", "one\n", "One")
			expectGrapheneOK(t, repo, "send", "origin")
			createStackBranch(t, repo, "two.txt", "two\n", "Two")
			if tc.name == "unsupported server" {
				runGit(t, remote, "config", "receive.advertiseAtomic", "false")
			} else {
				actor := cloneConfiguredRepo(t, remote, "stack/one")
				commitFile(t, actor, "collaborator.txt", "collaborator\n", "Collaborator change")
				runGit(t, actor, "push", "origin", "stack/one")
			}
			remoteBefore := runGit(t, remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
			localBefore := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
			stateBefore := readState(t, repo.dir)
			code, _, stderr := repo.runGraphene(t, tc.command, "origin")
			if code == 0 || !strings.Contains(stderr, tc.errorText) {
				t.Fatalf("%s result: %d, %s; want %s", tc.command, code, stderr, tc.errorText)
			}
			if got := runGit(t, remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != remoteBefore {
				t.Fatal("rejected push changed remote branches")
			}
			if got := runGit(t, repo.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != localBefore {
				t.Fatal("rejected push changed local branches")
			}
			if got := readState(t, repo.dir); !reflect.DeepEqual(got, stateBefore) {
				t.Fatal("rejected push changed stack state")
			}
			if upstream, err := (Git{Dir: repo.dir}).HasUpstream("stack/two"); err != nil || upstream {
				t.Fatalf("new branch upstream = %v, error = %v", upstream, err)
			}
		})
	}
}
