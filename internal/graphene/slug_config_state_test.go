package graphene

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSlugSubject(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "Fix API shape", want: "fix-api-shape"},
		{in: "  $$$  ", want: "change"},
		{in: "Hello__world 42", want: "hello-world-42"},
		{in: "One line\nBody line", want: "one-line"},
	}

	for _, tt := range tests {
		if got := SlugSubject(tt.in); got != tt.want {
			t.Fatalf("SlugSubject(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBranchNameAndCandidate(t *testing.T) {
	if got := BranchName("stack", "fix"); got != "stack/fix" {
		t.Fatalf("BranchName default = %q", got)
	}
	if got := BranchName("", "fix"); got != "fix" {
		t.Fatalf("BranchName empty prefix = %q", got)
	}
	if got := BranchName("/ag-stack/", "fix"); got != "ag-stack/fix" {
		t.Fatalf("BranchName trims slashes = %q", got)
	}
	if got := CandidateName("stack/fix", 3); got != "stack/fix-3" {
		t.Fatalf("CandidateName = %q", got)
	}
}

func TestLoadConfig(t *testing.T) {
	home := t.TempDir()
	env := func(key string) string {
		switch key {
		case "XDG_CONFIG_HOME":
			return filepath.Join(home, "xdg")
		case "HOME":
			return home
		default:
			return ""
		}
	}

	cfg, err := LoadConfig(env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BranchPrefix != defaultBranchPrefix {
		t.Fatalf("default prefix = %q", cfg.BranchPrefix)
	}

	configDir := filepath.Join(home, "xdg", "graphene")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"branchPrefix":""}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err = LoadConfig(env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BranchPrefix != "" {
		t.Fatalf("empty prefix should be preserved, got %q", cfg.BranchPrefix)
	}
}

func TestStateAddCommit(t *testing.T) {
	var state State
	if err := state.AddCommit("main", "stack/one"); err != nil {
		t.Fatal(err)
	}
	if err := state.AddCommit("stack/one", "stack/two"); err != nil {
		t.Fatal(err)
	}
	if err := state.AddCommit("stack/one", "stack/fork"); err != nil {
		t.Fatal(err)
	}

	want := []Stack{
		{Base: "main", Branches: []string{"stack/one", "stack/two"}},
		{Base: "stack/one", Branches: []string{"stack/fork"}},
	}
	if !reflect.DeepEqual(state.Stacks, want) {
		t.Fatalf("stacks = %#v, want %#v", state.Stacks, want)
	}
}

func TestBranchesThroughCurrent(t *testing.T) {
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b", "c"}},
		{Base: "b", Branches: []string{"d", "e"}},
		{Base: "e", Branches: []string{"f"}},
	}}

	tests := []struct {
		current string
		want    []string
	}{
		{current: "f", want: []string{"a", "b", "d", "e", "f"}},
		{current: "e", want: []string{"a", "b", "d", "e"}},
		{current: "b", want: []string{"a", "b"}},
		{current: "loose", want: []string{"loose"}},
	}

	for _, tt := range tests {
		if got := BranchesThroughCurrent(state, tt.current); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("BranchesThroughCurrent(%q) = %#v, want %#v", tt.current, got, tt.want)
		}
	}
}

func TestBranchesFromCurrent(t *testing.T) {
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b", "c"}},
		{Base: "b", Branches: []string{"d", "e"}},
		{Base: "e", Branches: []string{"f"}},
	}}

	tests := []struct {
		current string
		want    []string
	}{
		{current: "b", want: []string{"b", "c", "d", "e", "f"}},
		{current: "e", want: []string{"e", "f"}},
		{current: "loose", want: []string{"loose"}},
	}

	for _, tt := range tests {
		if got := BranchesFromCurrent(state, tt.current); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("BranchesFromCurrent(%q) = %#v, want %#v", tt.current, got, tt.want)
		}
	}
}

func TestRemoveStackThroughCurrent(t *testing.T) {
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b", "c"}},
		{Base: "a", Branches: []string{"d"}},
	}}

	got, ok := RemoveStackThroughCurrent(state, "b")
	if !ok {
		t.Fatal("RemoveStackThroughCurrent did not find b")
	}
	want := State{Stacks: []Stack{
		{Base: "b", Branches: []string{"c"}},
		{Base: "a", Branches: []string{"d"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %#v, want %#v", got, want)
	}

	got, ok = RemoveStackThroughCurrent(got, "c")
	if !ok {
		t.Fatal("RemoveStackThroughCurrent did not find c")
	}
	want = State{Stacks: []Stack{
		{Base: "a", Branches: []string{"d"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tip state = %#v, want %#v", got, want)
	}
}

func TestBaseBranch(t *testing.T) {
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b", "c"}},
		{Base: "b", Branches: []string{"d", "e"}},
	}}

	tests := []struct {
		branch string
		want   string
		ok     bool
	}{
		{branch: "a", want: "main", ok: true},
		{branch: "b", want: "a", ok: true},
		{branch: "d", want: "b", ok: true},
		{branch: "e", want: "d", ok: true},
		{branch: "loose"},
	}

	for _, tt := range tests {
		got, ok := BaseBranch(state, tt.branch)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("BaseBranch(%q) = %q %v, want %q %v", tt.branch, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRebaseBaseBranch(t *testing.T) {
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b"}},
		{Base: "b", Branches: []string{"c"}},
	}}

	tests := []struct {
		current string
		want    string
		ok      bool
	}{
		{current: "a", want: "main", ok: true},
		{current: "b", want: "main", ok: true},
		{current: "c", want: "b", ok: true},
		{current: "main", want: "main", ok: true},
		{current: "loose"},
	}

	for _, tt := range tests {
		got, ok := RebaseBaseBranch(state, tt.current)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("RebaseBaseBranch(%q) = %q %v, want %q %v", tt.current, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseArgs(t *testing.T) {
	branch, commitArgs, err := parseCommitArgs([]string{"-b", "feature/exact", "-m", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if branch != "feature/exact" || !reflect.DeepEqual(commitArgs, []string{"-m", "hi"}) {
		t.Fatalf("parseCommitArgs = %q %#v", branch, commitArgs)
	}

	remote, provided, flags, err := parsePushArgs([]string{"origin", "--force-with-lease"})
	if err != nil {
		t.Fatal(err)
	}
	if remote != "origin" || !provided || !reflect.DeepEqual(flags, []string{"--force-with-lease"}) {
		t.Fatalf("parsePushArgs remote = %q %v %#v", remote, provided, flags)
	}
	remote, provided, flags, err = parsePushArgs([]string{"--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if remote != "" || provided || !reflect.DeepEqual(flags, []string{"--dry-run"}) {
		t.Fatalf("parsePushArgs flags = %q %v %#v", remote, provided, flags)
	}
	if !pushDryRun(flags) {
		t.Fatal("pushDryRun did not detect --dry-run")
	}
	force, err := parseRmArgs([]string{"--force"})
	if err != nil {
		t.Fatal(err)
	}
	if !force {
		t.Fatal("parseRmArgs did not detect --force")
	}
	force, err = parseRmArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if force {
		t.Fatal("parseRmArgs force = true for no args")
	}
	if _, err := parseRmArgs([]string{"--bad"}); err == nil {
		t.Fatal("parseRmArgs accepted --bad")
	}
	remote, err = parsePRArgs([]string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	if remote != "origin" {
		t.Fatalf("parsePRArgs remote = %q", remote)
	}
	if _, err := parsePRArgs([]string{"origin", "upstream"}); err == nil {
		t.Fatal("parsePRArgs accepted multiple remotes")
	}
	if _, _, _, err := parsePushArgs([]string{"--dry-run", "origin"}); err == nil {
		t.Fatal("parsePushArgs accepted trailing remote")
	}
	if _, _, _, err := parsePushArgs([]string{"--delete"}); err == nil {
		t.Fatal("parsePushArgs accepted --delete")
	}
	if _, _, err := parseCommitArgs([]string{"--amend", "-m", "bad"}); err != nil {
		t.Fatal(err)
	}
	if err := rejectCommitModeArgs([]string{"--amend", "-m", "bad"}); err == nil {
		t.Fatal("rejectCommitModeArgs accepted --amend")
	}
}

func TestParseGitVersion(t *testing.T) {
	tests := []struct {
		in   string
		want gitVersion
	}{
		{in: "git version 2.38.0", want: gitVersion{Major: 2, Minor: 38, Patch: 0}},
		{in: "git version 2.53.0", want: gitVersion{Major: 2, Minor: 53, Patch: 0}},
		{in: "git version 2.39.3 (Apple Git-146)", want: gitVersion{Major: 2, Minor: 39, Patch: 3}},
		{in: "git version 2.44.0.windows.1", want: gitVersion{Major: 2, Minor: 44, Patch: 0}},
	}

	for _, tt := range tests {
		got, err := parseGitVersion(tt.in)
		if err != nil {
			t.Fatalf("parseGitVersion(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Fatalf("parseGitVersion(%q) = %#v, want %#v", tt.in, got, tt.want)
		}
	}

	if _, err := parseGitVersion("git version unknown"); err == nil {
		t.Fatal("parseGitVersion accepted unknown version")
	}
}

func TestRestackOpsAfterRewrite(t *testing.T) {
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b"}},
		{Base: "a", Branches: []string{"c"}},
		{Base: "b", Branches: []string{"d"}},
	}}
	oldRefs := map[string]string{
		"main": "old-main",
		"a":    "old-a",
		"b":    "old-b",
		"c":    "old-c",
		"d":    "old-d",
	}

	ops, err := RestackOpsAfterRewrite(state, "a", oldRefs)
	if err != nil {
		t.Fatal(err)
	}
	want := []RebaseOp{
		{Onto: "a", Upstream: "old-a", Top: "b"},
		{Onto: "a", Upstream: "old-a", Top: "c"},
		{Onto: "b", Upstream: "old-b", Top: "d"},
	}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %#v, want %#v", ops, want)
	}
}

func TestRestackOpsUpToBranch(t *testing.T) {
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b", "c"}},
		{Base: "b", Branches: []string{"d"}},
	}}
	oldRefs := map[string]string{
		"main": "old-main",
		"b":    "old-b",
	}

	ops, err := RestackOpsUpToBranch(state, "main", "b", oldRefs)
	if err != nil {
		t.Fatal(err)
	}
	want := []RebaseOp{{Onto: "main", Upstream: "old-main", Top: "b"}}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %#v, want %#v", ops, want)
	}

	ops, err = RestackOpsUpToBranch(state, "b", "d", oldRefs)
	if err != nil {
		t.Fatal(err)
	}
	want = []RebaseOp{{Onto: "b", Upstream: "old-b", Top: "d"}}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("nested ops = %#v, want %#v", ops, want)
	}
}

func TestRenderGraphWithPending(t *testing.T) {
	state := State{
		Stacks: []Stack{
			{Base: "main", Branches: []string{"a", "b"}},
			{Base: "a", Branches: []string{"c"}},
			{Base: "b", Branches: []string{"d"}},
		},
		Pending: &Pending{
			Operation: "amend",
			Branch:    "a",
			Queue: []RebaseOp{
				{Onto: "a", Top: "b"},
				{Onto: "b", Top: "d"},
			},
		},
	}

	want := "" +
		"main\n" +
		"  `- a *\n" +
		"     |- b\n" +
		"     |  `- d\n" +
		"     `- c\n" +
		"pending amend: a\n" +
		"  next: rebase b onto a\n" +
		"  remaining: 2\n"
	if got := RenderGraph(state, "a"); got != want {
		t.Fatalf("RenderGraph = %q, want %q", got, want)
	}
}

func TestPullRequestURLs(t *testing.T) {
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"ag/base-change", "ag/head-change"}},
	}}

	tests := []string{
		"git@github.com:AztecProtocol/aztec-packages.git",
		"ssh://git@github.com/AztecProtocol/aztec-packages.git",
		"https://github.com/AztecProtocol/aztec-packages.git",
	}

	want := []PullRequestURL{
		{
			Branch: "ag/base-change",
			Base:   "main",
			URL:    "https://github.com/AztecProtocol/aztec-packages/compare/main...ag/base-change?expand=1",
		},
		{
			Branch: "ag/head-change",
			Base:   "ag/base-change",
			URL:    "https://github.com/AztecProtocol/aztec-packages/compare/ag/base-change...ag/head-change?expand=1",
		},
	}

	for _, remoteURL := range tests {
		got := PullRequestURLs("", remoteURL, state, []string{"ag/base-change", "ag/head-change"})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("PullRequestURLs(%q) = %#v, want %#v", remoteURL, got, want)
		}
	}

	if got := PullRequestURLs("", "/tmp/repo.git", state, []string{"ag/base-change"}); got != nil {
		t.Fatalf("PullRequestURLs(non-github) = %#v, want nil", got)
	}
}

func TestPullRequestURLsFromTemplate(t *testing.T) {
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"ag/base-change", "ag/head-change"}},
	}}

	got := PullRequestURLs(
		"https://example.com/pr/${baseBranch}/${targetBranch}",
		"",
		state,
		[]string{"ag/base-change", "ag/head-change"},
	)
	want := []PullRequestURL{
		{
			Branch: "ag/base-change",
			Base:   "main",
			URL:    "https://example.com/pr/main/ag/base-change",
		},
		{
			Branch: "ag/head-change",
			Base:   "ag/base-change",
			URL:    "https://example.com/pr/ag/base-change/ag/head-change",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PullRequestURLs(template) = %#v, want %#v", got, want)
	}
}
