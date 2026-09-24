package graphene

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestUnitSplitCompletionLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line string
		want []string
	}{
		{"graphene new ", []string{"graphene", "new", ""}},
		{`graphene new --message "hello world" --b`, []string{"graphene", "new", "--message", "hello world", "--b"}},
		{`graphene new --message hello\ world --b`, []string{"graphene", "new", "--message", "hello world", "--b"}},
		{`graphene new --base 'branch with spaces'`, []string{"graphene", "new", "--base", "branch with spaces"}},
		{`graphene new --base foo\`, []string{"graphene", "new", "--base", `foo\`}},
	}
	for _, tt := range tests {
		if got := splitCompletionLine(tt.line); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("splitCompletionLine(%q) = %#v, want %#v", tt.line, got, tt.want)
		}
	}
}

func TestUnitCompletionPositionals(t *testing.T) {
	t.Parallel()
	flags := completionValueFlags("new")
	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"--base", "main", "--message=hello", "topic"}, []string{"topic"}},
		{[]string{"--base=main", "-a", "topic"}, []string{"topic"}},
		{[]string{"--", "--base", "main"}, []string{"--base", "main"}},
	}
	for _, tt := range tests {
		if got := completionPositionals(tt.args, flags); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("completionPositionals(%#v) = %#v, want %#v", tt.args, got, tt.want)
		}
	}
}

func TestCompletionScriptsParse(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		shell, script string
	}{
		{"bash", bashCompletion},
		{"zsh", zshCompletion},
	} {
		t.Run(tt.shell, func(t *testing.T) {
			if _, err := exec.LookPath(tt.shell); err != nil {
				t.Skipf("%s is not installed", tt.shell)
			}
			cmd := exec.Command(tt.shell, "-n")
			cmd.Stdin = strings.NewReader(tt.script)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s completion syntax: %v\n%s", tt.shell, err, output)
			}
		})
	}
}
