package graphene

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxAliasDepth = 20
const maxRemoteAliasFileBytes = 1 << 20

var aliasHTTPClient = &http.Client{Timeout: 5 * time.Second}

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

	value, ok, err := a.configAliasFor(name)
	if err != nil || ok {
		return value, ok, err
	}

	return a.aliasFileAliasFor(name)
}

func (a *App) configAliasFor(name string) (string, bool, error) {
	value, err := a.git.Output("config", "--get", "graphene.alias."+name)
	if err == nil {
		return value, true, nil
	}
	if !isGitExit(err, 1) {
		return "", false, err
	}

	return "", false, nil
}

func (a *App) aliasFileAliasFor(name string) (string, bool, error) {
	files, err := a.aliasFiles()
	if err != nil {
		return "", false, err
	}

	for _, file := range files {
		value, err := a.aliasFileValue(file, name)
		if err == nil {
			return value, true, nil
		}
		if !isGitExit(err, 1) {
			return "", false, err
		}
	}

	return "", false, nil
}

func (a *App) aliasFileValue(file, name string) (string, error) {
	path, cleanup, err := a.aliasFilePath(file)
	if err != nil {
		return "", err
	}
	defer cleanup()
	return a.git.Output("config", "--file", path, "--get", "graphene.alias."+name)
}

func (a *App) aliasFilePath(file string) (string, func(), error) {
	if remoteAliasFile(file) {
		return fetchRemoteAliasFile(file)
	}
	return a.resolveAliasFile(file), func() {}, nil
}

func remoteAliasFile(path string) bool {
	u, err := url.Parse(path)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && u.Host != ""
}

func fetchRemoteAliasFile(rawURL string) (string, func(), error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, fmt.Errorf("fetch alias file %s: %w", rawURL, err)
	}
	req.Header.Set("User-Agent", "graphene")

	resp, err := aliasHTTPClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("fetch alias file %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("fetch alias file %s: %s", rawURL, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteAliasFileBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("read alias file %s: %w", rawURL, err)
	}
	if len(data) > maxRemoteAliasFileBytes {
		return "", nil, fmt.Errorf("alias file %s is too large", rawURL)
	}

	tmp, err := os.CreateTemp("", "graphene-alias-*.gitconfig")
	if err != nil {
		return "", nil, fmt.Errorf("create alias file temp: %w", err)
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, fmt.Errorf("write alias file temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close alias file temp: %w", err)
	}
	return name, cleanup, nil
}

func (a *App) aliasFiles() ([]string, error) {
	var files []string
	if a.getenv != nil {
		files = appendAliasFiles(files, splitAliasFileList(a.getenv("GRAPHENE_ALIAS_FILE"))...)
	}

	out, err := a.git.Output("config", "--get-all", "graphene.aliasFile")
	if err == nil {
		files = appendAliasFiles(files, strings.Split(out, "\n")...)
		return files, nil
	}
	if !isGitExit(err, 1) {
		return nil, err
	}
	return files, nil
}

func splitAliasFileList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if remoteAliasFile(value) {
		return []string{value}
	}
	return filepath.SplitList(value)
}

func appendAliasFiles(files []string, paths ...string) []string {
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path != "" {
			files = append(files, path)
		}
	}
	return files
}

func (a *App) resolveAliasFile(path string) string {
	if (path == "~" || strings.HasPrefix(path, "~/")) && a.getenv != nil {
		if home := a.getenv("HOME"); home != "" {
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, path[2:])
			}
		}
	}
	if filepath.IsAbs(path) || a.git.Dir == "" {
		return path
	}
	return filepath.Join(a.git.Dir, path)
}

type aliasesOptions struct {
	action string
	scope  string
	force  bool
	source string
}

type aliasImportEntry struct {
	name  string
	value string
}

func (a *App) aliases(args []string) error {
	opts, err := parseAliasesArgs(args)
	if err != nil {
		return err
	}

	switch opts.action {
	case "import":
		return a.importAliases(opts)
	default:
		return aliasesUsage()
	}
}

func parseAliasesArgs(args []string) (aliasesOptions, error) {
	if len(args) == 0 || args[0] != "import" {
		return aliasesOptions{}, aliasesUsage()
	}
	opts := aliasesOptions{action: args[0]}
	positionalsOnly := false
	for _, arg := range args[1:] {
		if !positionalsOnly && arg == "--" {
			positionalsOnly = true
			continue
		}
		if !positionalsOnly {
			switch arg {
			case "--global":
				if opts.scope != "" {
					return aliasesOptions{}, fmt.Errorf("graphene aliases import accepts one scope")
				}
				opts.scope = "global"
				continue
			case "--local":
				if opts.scope != "" {
					return aliasesOptions{}, fmt.Errorf("graphene aliases import accepts one scope")
				}
				opts.scope = "local"
				continue
			case "--force":
				opts.force = true
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return aliasesOptions{}, fmt.Errorf("unsupported argument %q; usage: graphene aliases import [--global|--local] [--force] <path-or-url>", arg)
			}
		}
		if opts.source != "" {
			return aliasesOptions{}, aliasesUsage()
		}
		opts.source = arg
	}
	if opts.source == "" {
		return aliasesOptions{}, aliasesUsage()
	}
	return opts, nil
}

func aliasesUsage() error {
	return fmt.Errorf("usage: graphene aliases import [--global|--local] [--force] <path-or-url>")
}

func (a *App) importAliases(opts aliasesOptions) error {
	aliases, err := a.aliasFileEntries(opts.source)
	if err != nil {
		return err
	}
	if len(aliases) == 0 {
		return fmt.Errorf("no aliases found in %s", opts.source)
	}

	gitArgs := []string{"config"}
	if opts.scope != "" {
		gitArgs = append(gitArgs, "--"+opts.scope)
	}
	if !opts.force {
		conflicts, err := a.aliasConflicts(gitArgs, aliases)
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return fmt.Errorf("refusing to overwrite existing aliases: %s\nrerun with --force to overwrite them", strings.Join(conflicts, ", "))
		}
	}
	for _, alias := range aliases {
		if err := a.git.OutputErr(append(gitArgs, "graphene.alias."+alias.name, alias.value)...); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.stdout, "imported %d aliases\n", len(aliases))
	return nil
}

func (a *App) aliasConflicts(gitArgs []string, aliases []aliasImportEntry) ([]string, error) {
	var conflicts []string
	for _, alias := range aliases {
		_, err := a.git.Output(append(gitArgs, "--get", "graphene.alias."+alias.name)...)
		if err == nil {
			conflicts = append(conflicts, alias.name)
			continue
		}
		if !isGitExit(err, 1) {
			return nil, err
		}
	}
	return conflicts, nil
}

func (a *App) aliasFileEntries(source string) ([]aliasImportEntry, error) {
	path, cleanup, err := a.aliasFilePath(source)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	out, err := a.git.Output("config", "--file", path, "--get-regexp", "^graphene\\.alias\\.")
	if err != nil {
		if isGitExit(err, 1) {
			return nil, nil
		}
		return nil, err
	}

	var entries []aliasImportEntry
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			value = ""
		}
		name := strings.TrimPrefix(key, "graphene.alias.")
		if name == key || !validAliasName(name) {
			return nil, fmt.Errorf("invalid alias key %q", key)
		}
		entries = append(entries, aliasImportEntry{name: name, value: value})
	}
	return entries, nil
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
		if command == "agent-skill" {
			return "skill", nil
		}
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
	case "abort", "aliases", "amend", "agent-skill", "config", "continue", "delete", "forget", "go", "graph", "help", "import", "new", "restack", "send", "sendf", "skill", "split", "squash", "sync", "track", "version", "-h", "--help", "-v", "--version":
		return true
	default:
		return false
	}
}
