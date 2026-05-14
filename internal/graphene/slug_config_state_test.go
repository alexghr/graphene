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

func TestPushBranches(t *testing.T) {
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
		{current: "b", want: []string{"a", "b", "c"}},
		{current: "loose", want: []string{"loose"}},
	}

	for _, tt := range tests {
		if got := PushBranches(state, tt.current); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("PushBranches(%q) = %#v, want %#v", tt.current, got, tt.want)
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
