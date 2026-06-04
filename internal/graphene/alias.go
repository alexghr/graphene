package graphene

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const maxAliasDepth = 20

type aliasExpansion struct {
	args  []string
	shell *shellAlias
}

type shellAlias struct {
	name   string
	script string
	args   []string
}

type shellAliasError struct {
	name string
	code int
}

func (e *shellAliasError) Error() string {
	return fmt.Sprintf("alias %s exited with status %d", e.name, e.code)
}

func (a *App) expandAliases(args []string) (aliasExpansion, error) {
	expanded := append([]string(nil), args...)
	seen := map[string]bool{}

	for depth := 0; depth < maxAliasDepth; depth++ {
		if len(expanded) < 2 || isBuiltinCommand(expanded[1]) {
			return aliasExpansion{args: expanded}, nil
		}

		name := expanded[1]
		value, ok, err := a.aliasFor(name)
		if err != nil {
			return aliasExpansion{}, err
		}
		if !ok {
			return aliasExpansion{args: expanded}, nil
		}
		if seen[name] {
			return aliasExpansion{}, fmt.Errorf("alias loop detected at %q", name)
		}
		seen[name] = true

		if strings.HasPrefix(value, "!") {
			script := strings.TrimPrefix(value, "!")
			if strings.TrimSpace(script) == "" {
				return aliasExpansion{}, fmt.Errorf("alias %q has an empty shell command", name)
			}
			return aliasExpansion{shell: &shellAlias{
				name:   name,
				script: script,
				args:   append([]string(nil), expanded[2:]...),
			}}, nil
		}

		words, err := splitAlias(value)
		if err != nil {
			return aliasExpansion{}, fmt.Errorf("parse alias %q: %w", name, err)
		}
		if len(words) == 0 {
			return aliasExpansion{}, fmt.Errorf("alias %q expands to nothing", name)
		}

		next := []string{expanded[0]}
		next = append(next, words...)
		next = append(next, expanded[2:]...)
		expanded = next
	}

	return aliasExpansion{}, fmt.Errorf("alias expansion exceeded %d levels", maxAliasDepth)
}

func (a *App) aliasFor(name string) (string, bool, error) {
	if !validAliasName(name) {
		return "", false, nil
	}

	value, err := a.git.Output("config", "--get", "graphene.alias."+name)
	if err == nil {
		return value, true, nil
	}
	if !isGitExit(err, 1) {
		return "", false, err
	}

	return "", false, nil
}

func validAliasName(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func (a *App) helpCommand(command string) (string, error) {
	seen := map[string]bool{}

	for depth := 0; depth < maxAliasDepth; depth++ {
		if isBuiltinCommand(command) {
			return command, nil
		}
		value, ok, err := a.aliasFor(command)
		if err != nil {
			return "", err
		}
		if !ok || strings.HasPrefix(value, "!") {
			return command, nil
		}
		if seen[command] {
			return "", fmt.Errorf("alias loop detected at %q", command)
		}
		seen[command] = true

		words, err := splitAlias(value)
		if err != nil {
			return "", fmt.Errorf("parse alias %q: %w", command, err)
		}
		if len(words) == 0 {
			return "", fmt.Errorf("alias %q expands to nothing", command)
		}
		command = words[0]
	}

	return "", fmt.Errorf("alias expansion exceeded %d levels", maxAliasDepth)
}

func (a *App) runShellAlias(alias shellAlias) error {
	command := alias.script
	for _, arg := range alias.args {
		command += " " + shellQuote(arg)
	}

	cmdArgs := []string{"-c", command, alias.name}
	cmdArgs = append(cmdArgs, alias.args...)
	cmd := exec.Command("sh", cmdArgs...)
	cmd.Dir = a.git.Dir
	cmd.Stdin = a.git.Stdin
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &shellAliasError{name: alias.name, code: exitErr.ExitCode()}
		}
		return fmt.Errorf("run alias %s: %w", alias.name, err)
	}
	return nil
}

func splitAlias(value string) ([]string, error) {
	var words []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	haveWord := false

	for i := 0; i < len(value); i++ {
		c := value[i]
		if escaped {
			current.WriteByte(c)
			haveWord = true
			escaped = false
			continue
		}

		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				current.WriteByte(c)
			}
			haveWord = true
		case inDouble:
			switch c {
			case '"':
				inDouble = false
			case '\\':
				escaped = true
			default:
				current.WriteByte(c)
			}
			haveWord = true
		default:
			switch {
			case c == '\'':
				inSingle = true
				haveWord = true
			case c == '"':
				inDouble = true
				haveWord = true
			case c == '\\':
				escaped = true
				haveWord = true
			case aliasSpace(c):
				if haveWord {
					words = append(words, current.String())
					current.Reset()
					haveWord = false
				}
			default:
				current.WriteByte(c)
				haveWord = true
			}
		}
	}

	if escaped {
		return nil, fmt.Errorf("trailing backslash")
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unclosed quote")
	}
	if haveWord {
		words = append(words, current.String())
	}
	return words, nil
}

func aliasSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

func isBuiltinCommand(command string) bool {
	switch command {
	case "abort", "amend", "config", "continue", "delete", "forget", "go", "graph", "help", "new", "restack", "send", "sendf", "skill", "split", "squash", "sync", "track", "version", "-h", "--help", "-v", "--version":
		return true
	default:
		return false
	}
}
