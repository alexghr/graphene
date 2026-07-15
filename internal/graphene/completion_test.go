package graphene

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCompletionStaticGrammar(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	tests := []struct {
		name string
		line string
		want []string
	}{
		{name: "root commands", line: "graphene co", want: []string{"completion", "config", "continue"}},
		{name: "documented alias root commands", line: "gn co", want: []string{"completion", "config", "continue"}},
		{name: "root flags", line: "graphene --", want: []string{"--help", "--version"}},
		{name: "config action", line: "graphene config s", want: []string{"set"}},
		{name: "aliases action", line: "graphene aliases i", want: []string{"import"}},
		{name: "go direction", line: "graphene go b", want: []string{"bottom"}},
		{name: "completion shell", line: "graphene completion z", want: []string{"zsh"}},
		{
			name: "empty command argument",
			line: "graphene new ",
			want: []string{
				"--all", "--base", "--branch", "--gpg-sign", "--help", "--message", "--no-edit", "--no-gpg-sign",
				"--no-verify", "--parent", "--reuse-current", "--update", "-a", "-b", "-h", "-m", "-u",
			},
		},
		{name: "command flags", line: "graphene new --no-", want: []string{"--no-edit", "--no-gpg-sign", "--no-verify"}},
		{name: "optional value flag", line: "graphene new --g", want: []string{"--gpg-sign"}},
		{name: "remote flag", line: "graphene send --r", want: []string{"--remote"}},
		{name: "escaped whitespace groups a value", line: `graphene new --message hello\ world --b`, want: []string{"--base", "--branch"}},
		{name: "help flag", line: "graphene delete -h", want: []string{"-h"}},
		{name: "parser-only negations stay hidden", line: "graphene sync --no-"},
		{name: "double dash ends flags", line: "graphene sync -- --"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := completionCandidatesFromCLI(t, repo.dir, tt.line); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("completion for %q = %#v, want %#v", tt.line, got, tt.want)
			}
		})
	}
}

func TestCompletionBranchDomains(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	runGit(t, repo.dir, "branch", "local/untracked")
	runGit(t, repo.dir, "switch", "-c", "tracked/live")
	writeFile(t, repo.dir, "tracked.txt", "tracked\n")
	runGit(t, repo.dir, "add", ".")
	runGit(t, repo.dir, "commit", "-m", "tracked change")
	runGit(t, repo.dir, "branch", "head-copy")

	err := (Git{Dir: repo.dir}).WriteState(State{Stacks: []Stack{{
		Base:     "main",
		Branches: []string{"tracked/live", "tracked/stale"},
	}}})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		line string
		want []string
	}{
		{name: "new base points at head", line: "graphene new --base h", want: []string{"head-copy"}},
		{name: "open quote in base value", line: `graphene new --base "he`, want: []string{"head-copy"}},
		{name: "new parent attached", line: "graphene new --parent=h", want: []string{"--parent=head-copy"}},
		{name: "split existing tracked", line: "graphene split tracked/", want: []string{"tracked/live"}},
		{name: "delete existing tracked", line: "graphene delete tracked/", want: []string{"tracked/live"}},
		{name: "forget includes stale tracked", line: "graphene forget tracked/", want: []string{"tracked/live", "tracked/stale"}},
		{name: "track parent local branch", line: "graphene track --parent lo", want: []string{"local/untracked"}},
		{name: "track parent attached", line: "graphene track --parent=lo", want: []string{"--parent=local/untracked"}},
		{name: "track optional branch", line: "graphene track --parent main lo", want: []string{"local/untracked"}},
		{name: "import local branch", line: "graphene import lo", want: []string{"local/untracked"}},
		{name: "restack local branch", line: "graphene restack --force lo", want: []string{"local/untracked"}},
		{name: "checkout existing branch", line: "graphene checkout lo", want: []string{"local/untracked"}},
		{name: "switch existing branch", line: "graphene switch lo", want: []string{"local/untracked"}},
		{name: "checkout new branch slot", line: "graphene checkout -b lo"},
		{name: "switch new branch slot", line: "graphene switch -c lo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := completionCandidatesFromCLI(t, repo.dir, tt.line); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("completion for %q = %#v, want %#v", tt.line, got, tt.want)
			}
		})
	}
}

func TestCompletionRemotes(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "remote", "add", "origin", "git@example.test:owner/repo.git")
	runGit(t, repo.dir, "remote", "add", "other", "git@example.test:owner/other.git")

	for _, line := range []string{"graphene send o", "graphene sendf o"} {
		if got, want := completionCandidatesFromCLI(t, repo.dir, line), []string{"origin", "other"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("completion for %q = %#v, want %#v", line, got, want)
		}
	}
	if got, want := completionCandidatesFromCLI(t, repo.dir, "graphene send --remote=orig"), []string{"--remote=origin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attached remote completion = %#v, want %#v", got, want)
	}
}

func TestCompletionConfiguredAliases(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	runGit(t, repo.dir, "branch", "local-target")
	runGit(t, repo.dir, "config", "graphene.alias.onto", "restack --force")
	runGit(t, repo.dir, "config", "graphene.alias.up", "go up")
	runGit(t, repo.dir, "config", "graphene.alias.boom", "!touch completion-shell-alias-ran")
	runGit(t, repo.dir, "config", "graphene.alias.sync", "graph")
	runGit(t, repo.dir, "config", "graphene.alias.checkout", "graph")
	runGit(t, repo.dir, "config", "graphene.alias.multiline", "graph\ngraphene.alias.phantom value")

	if got, want := completionCandidatesFromCLI(t, repo.dir, "graphene on"), []string{"onto"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("alias name completion = %#v, want %#v", got, want)
	}
	if got, want := completionCandidatesFromCLI(t, repo.dir, "graphene onto local"), []string{"local-target"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expanded alias completion = %#v, want %#v", got, want)
	}
	if got := completionCandidatesFromCLI(t, repo.dir, "graphene up "); len(got) != 0 {
		t.Fatalf("alias with consumed arguments completion = %#v, want none", got)
	}
	if got, want := completionCandidatesFromCLI(t, repo.dir, "graphene bo"), []string{"boom"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shell alias name completion = %#v, want %#v", got, want)
	}
	if got, want := completionCandidatesFromCLI(t, repo.dir, "graphene sy"), []string{"sync"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deduplicated completion = %#v, want %#v", got, want)
	}
	if got := completionCandidatesFromCLI(t, repo.dir, "graphene boom "); len(got) != 0 {
		t.Fatalf("shell alias argument completion = %#v, want none", got)
	}
	if got := completionCandidatesFromCLI(t, repo.dir, "graphene checkout local"); len(got) != 0 {
		t.Fatalf("Git passthrough alias override completion = %#v, want none", got)
	}
	if got := completionCandidatesFromCLI(t, repo.dir, "graphene phant"); len(got) != 0 {
		t.Fatalf("multiline alias value leaked a command = %#v", got)
	}
	if _, err := os.Stat(filepath.Join(repo.dir, "completion-shell-alias-ran")); !os.IsNotExist(err) {
		t.Fatalf("shell alias ran during completion: %v", err)
	}
}

func TestCompletionFilesystemContexts(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	var stdout, stderr bytes.Buffer
	app := NewApp(repo.dir, nil, &stdout, &stderr, os.Getenv)

	for _, line := range []string{
		"graphene skill --out ",
		"graphene aliases import ",
		"graphene checkout -- ",
	} {
		result := app.completionCandidates(line)
		if !result.files || result.fileFragment != "" {
			t.Fatalf("filesystem completion for %q = %#v", line, result)
		}
	}

	result := app.completionCandidates("graphene skill --out=comp")
	if !result.files || result.filePrefix != "--out=" || result.fileFragment != "comp" {
		t.Fatalf("attached filesystem completion = %#v", result)
	}
	if result := app.completionCandidates("graphene switch -- ma"); result.files {
		t.Fatalf("switch -- unexpectedly requested filesystem completion: %#v", result)
	}
}

func TestCompletionDoesNotLoadAliasFiles(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	writeFile(t, repo.dir, "aliases.gitconfig", "[graphene \"alias\"]\n\thidden = graph\n")
	runGit(t, repo.dir, "config", "graphene.aliasFile", "aliases.gitconfig")

	if got := completionCandidatesFromCLI(t, repo.dir, "graphene hid"); len(got) != 0 {
		t.Fatalf("alias-file completion = %#v, want none", got)
	}
}

func TestCompletionOutsideRepository(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if got, want := completionCandidatesFromCLI(t, dir, "graphene compl"), []string{"completion"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("static completion outside repository = %#v, want %#v", got, want)
	}
	if got := completionCandidatesFromCLI(t, dir, "graphene checkout ma"); len(got) != 0 {
		t.Fatalf("dynamic completion outside repository = %#v, want none", got)
	}
}

func TestBashCompletionScript(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	code, script, stderr := repo.runGraphene(t, "completion", "bash")
	if code != 0 {
		t.Fatalf("graphene completion bash exited %d\nstdout:\n%s\nstderr:\n%s", code, script, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}

	check := exec.Command("bash", "-n")
	check.Stdin = strings.NewReader(script)
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, output)
	}

	sourceCommand := "source /dev/stdin\ncomplete -p graphene\ncomplete -p gn"
	if err := exec.Command("bash", "-c", "type complete >/dev/null 2>&1").Run(); err != nil {
		// Nix's non-interactive Bash omits completion builtins. A small stand-in
		// still verifies that sourcing the script registers both command names.
		sourceCommand = "complete() { printf '%s\\n' \"$*\"; }\nsource /dev/stdin"
	}
	source := exec.Command("bash", "-c", sourceCommand)
	source.Stdin = strings.NewReader(script)
	output, err := source.CombinedOutput()
	if err != nil {
		t.Fatalf("source completion script: %v\n%s", err, output)
	}
	registrations := string(output)
	if !strings.Contains(registrations, "graphene") || !strings.Contains(registrations, "gn") {
		t.Fatalf("completion registrations = %q", registrations)
	}

	code, stdout, stderr := repo.runGraphene(t, "help")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "graphene completion <bash|zsh>") {
		t.Fatalf("help does not advertise completion: (%d, %q, %q)", code, stdout, stderr)
	}

	code, stdout, stderr = repo.runGraphene(t, "completion", "fish")
	if code == 0 || stdout != "" || !strings.Contains(stderr, "usage: graphene completion <bash|zsh>") {
		t.Fatalf("unsupported shell = (%d, %q, %q)", code, stdout, stderr)
	}
}

func TestZshCompletionScript(t *testing.T) {
	repo := newTestRepo(t)
	runGit(t, repo.dir, "branch", "zsh-target")
	runGit(t, repo.dir, "remote", "add", "origin", "git@example.test:owner/repo.git")

	code, script, stderr := repo.runGraphene(t, "completion", "zsh")
	if code != 0 {
		t.Fatalf("graphene completion zsh exited %d\nstdout:\n%s\nstderr:\n%s", code, script, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}

	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "_graphene")
	writeFile(t, dir, "_graphene", script)

	t.Run("syntax", func(t *testing.T) {
		if output, err := exec.Command(zsh, "-n", scriptPath).CombinedOutput(); err != nil {
			t.Fatalf("zsh -n failed: %v\n%s", err, output)
		}
	})

	t.Run("registration", func(t *testing.T) {
		command := `compdef() { print -r -- "$*"; }; source "$1"`
		output, err := exec.Command(zsh, "-f", "-c", command, "--", scriptPath).CombinedOutput()
		if err != nil {
			t.Fatalf("source completion script: %v\n%s", err, output)
		}
		if registration := strings.TrimSpace(string(output)); registration != "_graphene graphene gn" {
			t.Fatalf("completion registration = %q", registration)
		}
	})

	t.Run("autoload", func(t *testing.T) {
		binDir := t.TempDir()
		writeExecutable(t, filepath.Join(binDir, "graphene"), `#!/bin/sh
test "$*" = "__complete graphene co" || exit 2
printf 'completion\n'
`)
		command := `
fpath=("$1" $fpath)
autoload -Uz _graphene
compadd() {
    local array_name="$2"
    print -rl -- "${(@P)array_name}"
}
BUFFER="graphene co"
CURSOR=${#BUFFER}
_graphene
`
		cmd := exec.Command(zsh, "-f", "-c", command, "--", dir)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("autoload completion script: %v\n%s", err, output)
		}
		if candidate := strings.TrimSpace(string(output)); candidate != "completion" {
			t.Fatalf("autoload completion candidate = %q", candidate)
		}
	})

	tests := []struct {
		name      string
		line      string
		want      []string
		wantFiles bool
	}{
		{
			name: "commands",
			line: "graphene co",
			want: []string{"completion", "config", "continue"},
		},
		{
			name: "flags",
			line: "graphene send --r",
			want: []string{"--remote"},
		},
		{
			name: "branches",
			line: "graphene checkout zsh-",
			want: []string{"zsh-target"},
		},
		{
			name: "attached branch value",
			line: "graphene new --base=zsh-",
			want: []string{"--base=zsh-target"},
		},
		{
			name: "attached remote value",
			line: "graphene send --remote=orig",
			want: []string{"--remote=origin"},
		},
		{
			name:      "separate path value",
			line:      "graphene skill --out comp",
			wantFiles: true,
		},
		{
			name: "attached path value is unsupported",
			line: "graphene skill --out=comp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backendOutput := completionOutputFromCLI(t, repo.dir, tt.line)
			got, files, fileOptionsPreserved := runZshCompletion(t, zsh, scriptPath, tt.line, backendOutput)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Zsh completion for %q = %#v, want %#v", tt.line, got, tt.want)
			}
			if files != tt.wantFiles {
				t.Fatalf("Zsh file completion for %q = %v, want %v", tt.line, files, tt.wantFiles)
			}
			if tt.wantFiles && !fileOptionsPreserved {
				t.Fatalf("Zsh file completion for %q reset completion-system options", tt.line)
			}
		})
	}
}

func runZshCompletion(t *testing.T, zsh, completionScript, line, backendOutput string) ([]string, bool, bool) {
	t.Helper()

	dir := t.TempDir()
	trace := filepath.Join(dir, "trace")
	writeExecutable(t, filepath.Join(dir, "graphene"), `#!/bin/sh
printf '%s\n' "$*" > "$GRAPHENE_TEST_TRACE"
printf '%s' "$GRAPHENE_TEST_OUTPUT"
`)

	harness := `
compdef() { :; }
typeset -ga added
typeset -gi files_called=0
typeset -gi file_options_preserved=1
compadd() {
    local array_name
    while (( $# )); do
        case "$1" in
            -a)
                shift
                array_name="$1"
                added+=("${(@P)array_name}")
                ;;
            --)
                shift
                added+=("$@")
                break
                ;;
        esac
        shift
    done
    return 0
}
_files() {
    (( files_called++ ))
    [[ -o extendedglob && -o nullglob ]] || file_options_preserved=0
    return 0
}
source "$1"
setopt extendedglob nullglob
BUFFER="$GRAPHENE_TEST_LINE"
LBUFFER="$BUFFER"
CURSOR=${#BUFFER}
words=(${(z)BUFFER})
if [[ "$BUFFER" == *' ' ]]; then
    words+=("")
fi
CURRENT=${#words}
PREFIX="${words[CURRENT]}"
_graphene
print -r -- "files:$files_called"
print -r -- "file-options:$file_options_preserved"
for candidate in "${added[@]}"; do
    print -r -- "candidate:$candidate"
done
`
	cmd := exec.Command(zsh, "-f", "-c", harness, "--", completionScript)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GRAPHENE_TEST_TRACE="+trace,
		"GRAPHENE_TEST_LINE="+line,
		"GRAPHENE_TEST_OUTPUT="+backendOutput,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run Zsh completion for %q: %v\n%s", line, err, output)
	}

	called, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("read completion command trace: %v", err)
	}
	calledLine := strings.TrimSuffix(string(called), "\n")
	if want := "__complete " + line; calledLine != want {
		t.Fatalf("completion command = %q, want %q", calledLine, want)
	}

	var candidates []string
	files := false
	fileOptionsPreserved := false
	for _, outputLine := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		switch {
		case outputLine == "files:1":
			files = true
		case outputLine == "file-options:1":
			fileOptionsPreserved = true
		case strings.HasPrefix(outputLine, "candidate:"):
			candidates = append(candidates, strings.TrimPrefix(outputLine, "candidate:"))
		}
	}
	return candidates, files, fileOptionsPreserved
}

func completionCandidatesFromCLI(t *testing.T, dir, line string) []string {
	t.Helper()
	out := strings.TrimSuffix(completionOutputFromCLI(t, dir, line), "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func completionOutputFromCLI(t *testing.T, dir, line string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := NewApp(dir, nil, &stdout, &stderr, os.Getenv)
	code := app.Run([]string{"graphene", "__complete", line})
	if code != 0 {
		t.Fatalf("graphene __complete %q exited %d\nstdout:\n%s\nstderr:\n%s", line, code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("graphene __complete %q stderr = %q", line, stderr.String())
	}
	return stdout.String()
}
