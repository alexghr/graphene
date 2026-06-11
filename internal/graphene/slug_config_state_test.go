package graphene

import (
	"reflect"
	"testing"
)

func TestSlugSubject(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestStateAddCommit(t *testing.T) {
	t.Parallel()
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

func TestTrackBranchFoldsExistingChildPath(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{
		{Base: "a", Branches: []string{"b"}},
	}}

	got, err := TrackBranch(state, "z", "a")
	if err != nil {
		t.Fatal(err)
	}
	want := State{Stacks: []Stack{
		{Base: "z", Branches: []string{"a", "b"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
}

func TestTrackBranchAppendsToTrackedTip(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{
		{Base: "z", Branches: []string{"x"}},
		{Base: "a", Branches: []string{"b"}},
	}}

	got, err := TrackBranch(state, "x", "a")
	if err != nil {
		t.Fatal(err)
	}
	want := State{Stacks: []Stack{
		{Base: "z", Branches: []string{"x", "a", "b"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
}

func TestTrackBranchKeepsSiblingChildStacks(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{
		{Base: "a", Branches: []string{"b"}},
		{Base: "a", Branches: []string{"c"}},
	}}

	got, err := TrackBranch(state, "z", "a")
	if err != nil {
		t.Fatal(err)
	}
	want := State{Stacks: []Stack{
		{Base: "z", Branches: []string{"a", "b"}},
		{Base: "a", Branches: []string{"c"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
}

func TestTrackBranchRejectsCycles(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{
		{Base: "a", Branches: []string{"b"}},
	}}

	if _, err := TrackBranch(state, "b", "a"); err == nil {
		t.Fatal("TrackBranch allowed a branch to track onto its descendant")
	}
}

func TestBranchesThroughCurrent(t *testing.T) {
	t.Parallel()
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

func TestBranchesThroughCurrentAndDescendants(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b", "c"}},
		{Base: "main", Branches: []string{"d", "e"}},
		{Base: "main", Branches: []string{"f"}},
		{Base: "other", Branches: []string{"x"}},
	}}

	tests := []struct {
		current string
		want    []string
	}{
		{current: "main", want: []string{"a", "b", "c", "d", "e", "f"}},
		{current: "a", want: []string{"a", "b", "c"}},
		{current: "b", want: []string{"a", "b", "c"}},
		{current: "c", want: []string{"a", "b", "c"}},
		{current: "d", want: []string{"d", "e"}},
		{current: "e", want: []string{"d", "e"}},
		{current: "f", want: []string{"f"}},
	}
	for _, tt := range tests {
		if got := BranchesThroughCurrentAndDescendants(state, tt.current); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("BranchesThroughCurrentAndDescendants(%q) = %#v, want %#v", tt.current, got, tt.want)
		}
	}
	if got := BranchesThroughCurrentAndDescendants(state, "loose"); !reflect.DeepEqual(got, []string{"loose"}) {
		t.Fatalf("BranchesThroughCurrentAndDescendants(loose) = %#v", got)
	}
}

func TestRemoveStackThroughBranch(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b", "c"}},
		{Base: "a", Branches: []string{"d"}},
	}}

	got, ok := RemoveStackThroughBranch(state, "b")
	if !ok {
		t.Fatal("RemoveStackThroughBranch did not find b")
	}
	want := State{Stacks: []Stack{
		{Base: "b", Branches: []string{"c"}},
		{Base: "a", Branches: []string{"d"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %#v, want %#v", got, want)
	}

	got, ok = RemoveStackThroughBranch(got, "c")
	if !ok {
		t.Fatal("RemoveStackThroughBranch did not find c")
	}
	want = State{Stacks: []Stack{
		{Base: "a", Branches: []string{"d"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tip state = %#v, want %#v", got, want)
	}
}

func TestReparentBranch(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a", "b", "c"}},
		{Base: "a", Branches: []string{"d"}},
	}}

	got, moved, ok := ReparentBranch(state, "b", "d")
	if !ok {
		t.Fatal("ReparentBranch did not move b")
	}
	want := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"a"}},
		{Base: "a", Branches: []string{"d"}},
		{Base: "d", Branches: []string{"b", "c"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(moved, []string{"b", "c"}) {
		t.Fatalf("moved = %#v", moved)
	}
	if _, _, ok := ReparentBranch(state, "b", "c"); ok {
		t.Fatal("ReparentBranch allowed a branch to move onto its descendant")
	}
}

func TestBaseBranch(t *testing.T) {
	t.Parallel()
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

func TestStackGraphCandidates(t *testing.T) {
	t.Parallel()
	state := State{Stacks: []Stack{
		{Base: "main", Branches: []string{"one", "two", "three"}},
		{Base: "one", Branches: []string{"fork", "fork-top"}},
		{Base: "main", Branches: []string{"other"}},
	}}
	graph := newStackGraph(state)

	tests := []struct {
		current   string
		direction goDirection
		want      []string
	}{
		{current: "main", direction: goUp, want: []string{"one", "other"}},
		{current: "one", direction: goUp, want: []string{"two", "fork"}},
		{current: "one", direction: goTop, want: []string{"three", "fork-top"}},
		{current: "main", direction: goTop, want: []string{"three", "fork-top", "other"}},
		{current: "three", direction: goBottom, want: []string{"one"}},
		{current: "main", direction: goBottom, want: []string{"one", "other"}},
		{current: "fork", direction: goDown, want: []string{"one"}},
		{current: "one", direction: goDown, want: []string{"main"}},
		{current: "three", direction: goUp, want: nil},
		{current: "one", direction: goBottom, want: nil},
	}

	for _, tt := range tests {
		if got := graph.candidates(tt.current, tt.direction); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%s candidates from %q = %#v, want %#v", tt.direction, tt.current, got, tt.want)
		}
	}

	target, err := goTarget(state, "one", goTop, 2)
	if err != nil {
		t.Fatal(err)
	}
	if target != "fork-top" {
		t.Fatalf("selected target = %q, want fork-top", target)
	}
	if _, err := goTarget(state, "one", goTop, 0); err == nil {
		t.Fatal("goTarget accepted ambiguous top without selector")
	}

	rootState := State{Stacks: []Stack{
		{Base: "z", Branches: []string{"z-one"}},
		{Base: "a", Branches: []string{"a-one"}},
	}}
	if got := newStackGraph(rootState).roots(); !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Fatalf("roots = %#v, want %#v", got, []string{"a", "z"})
	}
}

func TestParseArgs(t *testing.T) {
	t.Parallel()
	newOpts, err := parseNewArgs([]string{"--branch=feature/exact", "--base=stack/parent", "--message=hi", "--no-edit", "--no-verify", "--gpg-sign=key", "--no-gpg-sign"})
	if err != nil {
		t.Fatal(err)
	}
	wantCommitArgs := []string{"--message=hi", "--no-edit", "--no-verify", "--gpg-sign=key", "--no-gpg-sign"}
	if newOpts.branch != "feature/exact" || newOpts.base != "stack/parent" || !reflect.DeepEqual(newOpts.commitArgs, wantCommitArgs) {
		t.Fatalf("parseNewArgs = %#v", newOpts)
	}

	// Regression for https://github.com/alexghr/graphene/issues/11.
	newParentOpts, err := parseNewArgs([]string{"--parent", "stack/parent", "-m", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if newParentOpts.base != "stack/parent" || !reflect.DeepEqual(newParentOpts.commitArgs, []string{"-m", "hi"}) {
		t.Fatalf("parseNewArgs --parent = %#v", newParentOpts)
	}
	newMatchingBaseOpts, err := parseNewArgs([]string{"--base=main", "--parent=main", "-m", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if newMatchingBaseOpts.base != "main" {
		t.Fatalf("parseNewArgs matching base aliases = %#v", newMatchingBaseOpts)
	}

	positionalNewOpts, err := parseNewArgs([]string{"feature/positional", "--message=hi"})
	if err != nil {
		t.Fatal(err)
	}
	if positionalNewOpts.branch != "feature/positional" || !reflect.DeepEqual(positionalNewOpts.commitArgs, []string{"--message=hi"}) {
		t.Fatalf("parseNewArgs positional branch = %#v", positionalNewOpts)
	}

	reuseOpts, err := parseNewArgs([]string{"--reuse-current", "--base", "main", "-m", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !reuseOpts.reuseCurrent || reuseOpts.base != "main" || !reflect.DeepEqual(reuseOpts.commitArgs, []string{"-m", "hi"}) {
		t.Fatalf("parseNewArgs reuse-current = %#v", reuseOpts)
	}

	amendOpts, err := parseAmendArgs([]string{"-m", "hi", "--no-edit", "--gpg-sign"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(amendOpts.commitArgs, []string{"-m", "hi", "--no-edit", "--gpg-sign"}) {
		t.Fatalf("parseAmendArgs = %#v", amendOpts)
	}

	squashOpts, err := parseSquashArgs([]string{"-c3", "--message=combined", "--no-edit", "--no-verify"})
	if err != nil {
		t.Fatal(err)
	}
	if squashOpts.count != 3 || !squashOpts.messageSet || !squashOpts.noEdit || !reflect.DeepEqual(squashOpts.commitArgs, []string{"--message=combined", "--no-edit", "--no-verify"}) {
		t.Fatalf("parseSquashArgs = %#v", squashOpts)
	}

	sendOpts, err := parseSendArgs([]string{"--stack", "--remote", "origin", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if sendOpts.remote != "origin" || !sendOpts.stack || !sendOpts.dryRun {
		t.Fatalf("parseSendArgs = %#v", sendOpts)
	}

	sendOpts, err = parseSendArgs([]string{"-s", "-n", "origin"})
	if err != nil {
		t.Fatal(err)
	}
	if sendOpts.remote != "origin" || !sendOpts.stack || !sendOpts.dryRun {
		t.Fatalf("parseSendArgs short flags = %#v", sendOpts)
	}

	sendOpts, err = parseSendArgs([]string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	if sendOpts.remote != "origin" || sendOpts.stack || sendOpts.dryRun {
		t.Fatalf("parseSendArgs positional remote = %#v", sendOpts)
	}
	sendOpts, err = parseSendArgs([]string{"-sn", "--no-stack", "origin"})
	if err != nil {
		t.Fatal(err)
	}
	if sendOpts.remote != "origin" || sendOpts.stack || !sendOpts.dryRun {
		t.Fatalf("parseSendArgs cluster override = %#v", sendOpts)
	}
	syncOpts, err := parseSyncArgs([]string{"--all"})
	if err != nil {
		t.Fatal(err)
	}
	if !syncOpts.all || syncOpts.dryRun {
		t.Fatalf("parseSyncArgs --all = %#v", syncOpts)
	}
	syncOpts, err = parseSyncArgs([]string{"-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !syncOpts.all || syncOpts.dryRun {
		t.Fatalf("parseSyncArgs -a = %#v", syncOpts)
	}
	syncOpts, err = parseSyncArgs([]string{"--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if syncOpts.all || !syncOpts.dryRun {
		t.Fatalf("parseSyncArgs --dry-run = %#v", syncOpts)
	}
	syncOpts, err = parseSyncArgs([]string{"-anf", "--dry-run=false"})
	if err != nil {
		t.Fatal(err)
	}
	if !syncOpts.all || syncOpts.dryRun || !syncOpts.force {
		t.Fatalf("parseSyncArgs cluster override = %#v", syncOpts)
	}
	restackOpts, err := parseRestackArgs([]string{"--force", "target"})
	if err != nil {
		t.Fatal(err)
	}
	if restackOpts.base != "target" || !restackOpts.local {
		t.Fatalf("parseRestackArgs --force = %#v", restackOpts)
	}
	restackOpts, err = parseRestackArgs([]string{"-f", "target"})
	if err != nil {
		t.Fatal(err)
	}
	if restackOpts.base != "target" || !restackOpts.local {
		t.Fatalf("parseRestackArgs -f = %#v", restackOpts)
	}
	graphOpts, err := parseGraphArgs([]string{"--stack"})
	if err != nil {
		t.Fatal(err)
	}
	if !graphOpts.stack {
		t.Fatalf("parseGraphArgs --stack = %#v", graphOpts)
	}
	goOpts, err := parseGoArgs([]string{"up", "2"})
	if err != nil {
		t.Fatal(err)
	}
	if goOpts.direction != goUp || goOpts.selector != 2 {
		t.Fatalf("parseGoArgs up = %#v", goOpts)
	}
	goOpts, err = parseGoArgs([]string{"--bottom=3"})
	if err != nil {
		t.Fatal(err)
	}
	if goOpts.direction != goBottom || goOpts.selector != 3 {
		t.Fatalf("parseGoArgs --bottom = %#v", goOpts)
	}
	goOpts, err = parseGoArgs([]string{"-t"})
	if err != nil {
		t.Fatal(err)
	}
	if goOpts.direction != goTop || goOpts.selector != 0 {
		t.Fatalf("parseGoArgs -t = %#v", goOpts)
	}
	goOpts, err = parseGoArgs([]string{"-u2"})
	if err != nil {
		t.Fatal(err)
	}
	if goOpts.direction != goUp || goOpts.selector != 2 {
		t.Fatalf("parseGoArgs -u2 = %#v", goOpts)
	}
	forgetOpts, err := parseForgetArgs([]string{"--force"})
	if err != nil {
		t.Fatal(err)
	}
	if !forgetOpts.force || forgetOpts.branch != "" {
		t.Fatal("parseForgetArgs did not detect --force")
	}
	forgetOpts, err = parseForgetArgs([]string{"stack/two"})
	if err != nil {
		t.Fatal(err)
	}
	if forgetOpts.force || forgetOpts.branch != "stack/two" {
		t.Fatalf("parseForgetArgs branch = %#v", forgetOpts)
	}
	forgetOpts, err = parseForgetArgs([]string{"--force", "stack/two"})
	if err != nil {
		t.Fatal(err)
	}
	if !forgetOpts.force || forgetOpts.branch != "stack/two" {
		t.Fatalf("parseForgetArgs --force branch = %#v", forgetOpts)
	}
	forgetOpts, err = parseForgetArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if forgetOpts.force || forgetOpts.branch != "" {
		t.Fatalf("parseForgetArgs no args = %#v", forgetOpts)
	}
	deleteOpts, err := parseDeleteArgs([]string{"stack/two"})
	if err != nil {
		t.Fatal(err)
	}
	if deleteOpts.stack || deleteOpts.branch != "stack/two" {
		t.Fatalf("parseDeleteArgs branch = %#v", deleteOpts)
	}
	deleteOpts, err = parseDeleteArgs([]string{"--stack", "stack/two"})
	if err != nil {
		t.Fatal(err)
	}
	if !deleteOpts.stack || deleteOpts.branch != "stack/two" {
		t.Fatalf("parseDeleteArgs --stack branch = %#v", deleteOpts)
	}
	deleteOpts, err = parseDeleteArgs([]string{"-s"})
	if err != nil {
		t.Fatal(err)
	}
	if !deleteOpts.stack || deleteOpts.branch != "" {
		t.Fatalf("parseDeleteArgs -s = %#v", deleteOpts)
	}
	deleteOpts, err = parseDeleteArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if deleteOpts.stack || deleteOpts.branch != "" {
		t.Fatalf("parseDeleteArgs no args = %#v", deleteOpts)
	}
	aliasNew, err := parseNewArgs([]string{"-am", "One", "--branch", "feature/one"})
	if err != nil {
		t.Fatal(err)
	}
	if !aliasNew.stageAll || aliasNew.branch != "feature/one" || !reflect.DeepEqual(aliasNew.commitArgs, []string{"-m", "One"}) {
		t.Fatalf("parseNewArgs alias form = %#v", aliasNew)
	}
	aliasAmend, err := parseAmendArgs([]string{"--update", "--message=One"})
	if err != nil {
		t.Fatal(err)
	}
	if !aliasAmend.stageUpdate || !reflect.DeepEqual(aliasAmend.commitArgs, []string{"--message=One"}) {
		t.Fatalf("parseAmendArgs alias form = %#v", aliasAmend)
	}
	dashBranchOpts, err := parseNewArgs([]string{"--", "-branch"})
	if err != nil {
		t.Fatal(err)
	}
	if dashBranchOpts.branch != "-branch" {
		t.Fatalf("parseNewArgs -- branch = %#v", dashBranchOpts)
	}
	base, branch, err := parseTrackArgs([]string{"--parent", "main", "feature/one"})
	if err != nil {
		t.Fatal(err)
	}
	if base != "main" || branch != "feature/one" {
		t.Fatalf("parseTrackArgs = base %q branch %q", base, branch)
	}
	// Regression for https://github.com/alexghr/graphene/issues/11.
	base, branch, err = parseTrackArgs([]string{"--base=main", "feature/one"})
	if err != nil {
		t.Fatal(err)
	}
	if base != "main" || branch != "feature/one" {
		t.Fatalf("parseTrackArgs --base = base %q branch %q", base, branch)
	}
	base, branch, err = parseTrackArgs([]string{"-p", "main", "--base", "main", "feature/one"})
	if err != nil {
		t.Fatal(err)
	}
	if base != "main" || branch != "feature/one" {
		t.Fatalf("parseTrackArgs matching parent aliases = base %q branch %q", base, branch)
	}
	aliasWords, err := splitAlias(`new --branch "feature one" -m hi`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(aliasWords, []string{"new", "--branch", "feature one", "-m", "hi"}) {
		t.Fatalf("splitAlias = %#v", aliasWords)
	}
	configOpts, err := parseConfigArgs([]string{"set", "alias.ss", "sendf", "--stack"})
	if err != nil {
		t.Fatal(err)
	}
	if configOpts.key != "alias.ss" || configOpts.value != "sendf --stack" {
		t.Fatalf("parseConfigArgs alias value = %#v", configOpts)
	}
	if !isBuiltinCommand("agent-skill") {
		t.Fatal("agent-skill is not treated as a builtin")
	}
	if command, err := (&App{}).helpCommand("agent-skill"); err != nil || command != "skill" {
		t.Fatalf("helpCommand(agent-skill) = %q, %v; want skill, nil", command, err)
	}
	skillOpts, err := parseSkillArgs([]string{"--out=SKILL.md"})
	if err != nil {
		t.Fatal(err)
	}
	if skillOpts.out != "SKILL.md" || skillOpts.target != "" {
		t.Fatalf("parseSkillArgs = %#v, want out SKILL.md", skillOpts)
	}
	skillOpts, err = parseSkillArgs([]string{"--out", "-"})
	if err != nil {
		t.Fatal(err)
	}
	if skillOpts.out != "-" || skillOpts.target != "" {
		t.Fatalf("parseSkillArgs --out - = %#v", skillOpts)
	}
	skillOpts, err = parseSkillArgs([]string{"--codex"})
	if err != nil {
		t.Fatal(err)
	}
	if skillOpts.target != "codex" || skillOpts.out != "" {
		t.Fatalf("parseSkillArgs --codex = %#v", skillOpts)
	}
	skillOpts, err = parseSkillArgs([]string{"--claude"})
	if err != nil {
		t.Fatal(err)
	}
	if skillOpts.target != "claude" || skillOpts.out != "" {
		t.Fatalf("parseSkillArgs --claude = %#v", skillOpts)
	}
	conflictTests := []struct {
		name string
		run  func() error
	}{
		{
			name: "new positional and flag branch",
			run: func() error {
				_, err := parseNewArgs([]string{"feature/one", "--branch", "feature/two", "-m", "bad"})
				return err
			},
		},
		{
			name: "new conflicting base aliases",
			run: func() error {
				_, err := parseNewArgs([]string{"--base", "main", "--parent", "other", "-m", "bad"})
				return err
			},
		},
		{
			name: "track conflicting base aliases",
			run: func() error {
				_, _, err := parseTrackArgs([]string{"--parent", "main", "--base", "other"})
				return err
			},
		},
	}
	for _, tt := range conflictTests {
		if err := tt.run(); err == nil {
			t.Fatalf("%s succeeded", tt.name)
		}
	}
}

func TestParseGitVersion(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestRenderGraphWithPending(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
