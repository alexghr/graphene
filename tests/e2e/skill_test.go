package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSkillInstallation(t *testing.T) {
	for _, target := range []string{"codex", "claude", "out"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			out := filepath.Join(home, "."+target, "skills", "graphene-stacked-prs", "SKILL.md")
			args := []string{"skill", "--" + target}
			if target == "out" {
				out = filepath.Join(home, "custom", "SKILL.md")
				args = []string{"skill", "--out", filepath.Dir(out)}
			}
			cmd := exec.Command(executable(t), args...)
			cmd.Env = append(os.Environ(), "HOME="+home)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("install: %v\n%s", err, output)
			}
			entrypoint, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			links := regexp.MustCompile(`\]\((references/[^)]+)\)`).FindAllSubmatch(entrypoint, -1)
			if len(links) == 0 {
				t.Fatal("installed skill has no reference links")
			}
			for _, link := range links {
				content, err := os.ReadFile(filepath.Join(filepath.Dir(out), string(link[1])))
				if err != nil || len(content) == 0 {
					t.Fatalf("reference %s: empty or unreadable: %v", link[1], err)
				}
			}
		})
	}
}

func TestSkillRequiresDirectoryDestination(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no destination", []string{"skill"}, "requires a destination"},
		{"stdout", []string{"skill", "--out", "-"}, "stdout output is not supported"},
		{"stdout equals", []string{"skill", "--out=-"}, "stdout output is not supported"},
		{"existing file", []string{"skill", "--out", "existing-file"}, "not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			file := filepath.Join(dir, "existing-file")
			if err := os.WriteFile(file, []byte("keep me"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable(t), tc.args...)
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), tc.want) {
				t.Fatalf("got %v, %s; want failure containing %q", err, output, tc.want)
			}
			content, err := os.ReadFile(file)
			if err != nil || string(content) != "keep me" {
				t.Fatalf("existing file changed: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("unexpected filesystem changes: %v", err)
			}
		})
	}
}
