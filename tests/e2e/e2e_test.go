package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

var binary struct {
	sync.Once
	path string
	dir  string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if binary.dir != "" {
		_ = os.RemoveAll(binary.dir)
	}
	os.Exit(code)
}

func executable(t *testing.T) string {
	t.Helper()
	binary.Do(func() {
		binary.dir, binary.err = os.MkdirTemp("", "graphene-e2e-binary-")
		if binary.err != nil {
			return
		}
		binary.path = filepath.Join(binary.dir, "graphene")
		cmd := exec.Command("go", "build", "-o", binary.path, "./cmd/graphene")
		cmd.Dir = filepath.Join("..", "..")
		out, err := cmd.CombinedOutput()
		if err != nil {
			binary.err = fmt.Errorf("build Graphene: %w\n%s", err, out)
		}
	})
	if binary.err != nil {
		t.Fatal(binary.err)
	}
	return binary.path
}

type fixture struct {
	t      *testing.T
	dir    string
	remote string
	hooks  string
	env    []string
}

type stack struct {
	Base     string   `json:"base"`
	Branches []string `json:"branches"`
}

type state struct {
	Stacks  []stack        `json:"stacks"`
	Pending map[string]any `json:"pending"`
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "config")
	tmpl := filepath.Join(root, "template")
	hooks := filepath.Join(root, "hooks")
	for _, dir := range []string{config, tmpl, hooks} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "GIT_") && !strings.HasPrefix(key, "GRAPHENE_") && !strings.HasPrefix(key, "LC_") && key != "LANG" && key != "LANGUAGE" && key != "HOME" && key != "XDG_CONFIG_HOME" && key != "GPG_TTY" {
			env = append(env, entry)
		}
	}
	env = append(env, "HOME="+root, "XDG_CONFIG_HOME="+config,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(root, "missing-global-config"),
		"GIT_TEMPLATE_DIR="+tmpl, "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C", "LANGUAGE=C", "LANG=C")
	f := &fixture{t: t, dir: filepath.Join(root, "work"), remote: filepath.Join(root, "origin.git"), hooks: hooks, env: env}
	f.gitAt(root, "init", "--bare", f.remote)
	f.gitAt(root, "init", "-b", "main", f.dir)
	f.configure(f.dir)
	f.write("base.txt", "base\n")
	f.git("add", ".")
	f.git("commit", "-m", "Initial")
	f.git("remote", "add", "origin", f.remote)
	f.git("push", "-u", "origin", "main")
	f.gitAt(f.remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return f
}

func (f *fixture) configure(dir string) {
	f.gitAt(dir, "config", "user.name", "Graphene E2E")
	f.gitAt(dir, "config", "user.email", "graphene-e2e@example.test")
	f.gitAt(dir, "config", "commit.gpgsign", "false")
	f.gitAt(dir, "config", "core.hooksPath", f.hooks)
	f.gitAt(dir, "config", "core.editor", "true")
	f.gitAt(dir, "config", "rebase.updateRefs", "false")
}

func (f *fixture) command(dir, name string, args ...string) (string, error) {
	f.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, f.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *fixture) gitAt(dir string, args ...string) string {
	f.t.Helper()
	out, err := f.command(dir, "git", args...)
	if err != nil {
		f.t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return strings.TrimSpace(out)
}

func (f *fixture) git(args ...string) string { return f.gitAt(f.dir, args...) }

func (f *fixture) graph(args ...string) string {
	f.t.Helper()
	out, err := f.command(f.dir, executable(f.t), args...)
	if err != nil {
		f.t.Fatalf("graphene %v: %v\n%s\nrefs:\n%s\nstate: %s", args, err, out, f.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"), f.stateDiagnostic())
	}
	return out
}

func (f *fixture) reject(want string, args ...string) string {
	f.t.Helper()
	out, err := f.command(f.dir, executable(f.t), args...)
	if err == nil || !strings.Contains(out, want) {
		f.t.Fatalf("graphene %v: got error %v and output %q; want failure containing %q", args, err, out, want)
	}
	return out
}

func (f *fixture) write(name, content string) {
	f.t.Helper()
	path := filepath.Join(f.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(name string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) new(name string) string {
	f.t.Helper()
	f.write(name+".txt", name+"\n")
	f.git("add", name+".txt")
	f.graph("new", "--branch", "stack/"+name, "-m", name)
	return "stack/" + name
}

func (f *fixture) newDerived(name string) string {
	f.t.Helper()
	f.write(name+".txt", name+"\n")
	f.git("add", name+".txt")
	f.graph("new", "-m", name)
	expectSame(f.t, "derived branch", f.branch(), "stack/"+name)
	return f.branch()
}

func (f *fixture) oid(ref string) string    { return f.git("rev-parse", ref) }
func (f *fixture) parent(ref string) string { return f.oid(ref + "^") }

func (f *fixture) branch() string { return f.git("symbolic-ref", "--short", "HEAD") }

func (f *fixture) state() state {
	f.t.Helper()
	common := f.git("rev-parse", "--path-format=absolute", "--git-common-dir")
	b, err := os.ReadFile(filepath.Join(common, "graphene", "state.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) stateDiagnostic() string {
	common := f.git("rev-parse", "--path-format=absolute", "--git-common-dir")
	b, err := os.ReadFile(filepath.Join(common, "graphene", "state.json"))
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func (f *fixture) cleanState(want ...stack) {
	f.t.Helper()
	s := f.state()
	if s.Pending != nil || len(s.Stacks) != len(want) || (len(want) > 0 && !reflect.DeepEqual(s.Stacks, want)) {
		f.t.Fatalf("state = %+v, want stacks %+v without pending", s, want)
	}
}

func (f *fixture) assertParent(child, parent string) {
	f.t.Helper()
	if got, want := f.parent(child), f.oid(parent); got != want {
		f.t.Fatalf("%s parent = %s; want %s (%s)", child, got, parent, want)
	}
}

func (f *fixture) assertFiles(ref string, names ...string) {
	f.t.Helper()
	for _, name := range names {
		if got := f.git("show", ref+":"+name+".txt"); got != name {
			f.t.Fatalf("%s:%s.txt = %q; want %q", ref, name, got, name)
		}
	}
}

func (f *fixture) assertPatchFile(ref, name string) {
	f.t.Helper()
	expectSame(f.t, ref+" changed file", f.git("diff-tree", "--no-commit-id", "--name-only", "-r", ref), name+".txt")
}

func (f *fixture) remoteRefs() string {
	return f.gitAt(f.remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
}

func (f *fixture) clone() string {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), "actor")
	f.gitAt(filepath.Dir(dir), "clone", f.remote, dir)
	f.configure(dir)
	return dir
}

func (f *fixture) actorCommit(dir, file, content, message string) string {
	f.t.Helper()
	path := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.gitAt(dir, "add", file)
	f.gitAt(dir, "commit", "-m", message)
	return f.gitAt(dir, "rev-parse", "HEAD")
}

func branches(names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "stack/" + n
	}
	return out
}

func (f *fixture) assertRemoteEquals(names ...string) {
	f.t.Helper()
	for _, n := range names {
		if got, want := f.gitAt(f.remote, "rev-parse", "refs/heads/stack/"+n), f.oid("stack/"+n); got != want {
			f.t.Fatalf("remote stack/%s = %s, local = %s", n, got, want)
		}
	}
}

func expectSame(t *testing.T, label, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %q, want %q", label, got, want)
	}
}
