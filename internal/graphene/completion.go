package graphene

import (
	_ "embed"
	"fmt"
	"io"
	"sort"
	"strings"
)

//go:embed graphene.bash
var bashCompletion string

//go:embed _graphene
var zshCompletion string

var publicCompletionCommands = []string{
	"abort",
	"aliases",
	"amend",
	"completion",
	"config",
	"continue",
	"delete",
	"forget",
	"go",
	"graph",
	"help",
	"import",
	"new",
	"restack",
	"send",
	"sendf",
	"skill",
	"split",
	"squash",
	"sync",
	"track",
	"version",
}

var commandCompletionFlags = map[string][]string{
	"abort":      {"-h", "--help"},
	"amend":      {"-a", "--all", "-u", "--update", "-m", "--message", "--no-edit", "--no-verify", "--gpg-sign", "--no-gpg-sign", "-h", "--help"},
	"completion": {"-h", "--help"},
	"continue":   {"-h", "--help"},
	"delete":     {"-s", "--stack", "-h", "--help"},
	"forget":     {"-f", "--force", "-h", "--help"},
	"graph":      {"-s", "--stack", "-h", "--help"},
	"import":     {"-h", "--help"},
	"new":        {"-a", "--all", "-u", "--update", "-b", "--branch", "--base", "--parent", "--reuse-current", "-m", "--message", "--no-edit", "--no-verify", "--gpg-sign", "--no-gpg-sign", "-h", "--help"},
	"restack":    {"-f", "--force", "-h", "--help"},
	"send":       {"--remote", "-s", "--stack", "-n", "--dry-run", "-h", "--help"},
	"sendf":      {"--remote", "-s", "--stack", "-n", "--dry-run", "-h", "--help"},
	"skill":      {"--codex", "--claude", "--out", "-h", "--help"},
	"split":      {"-h", "--help"},
	"squash":     {"-c", "--count", "-m", "--message", "--no-edit", "--no-verify", "--gpg-sign", "--no-gpg-sign", "-h", "--help"},
	"sync":       {"-a", "--all", "-n", "--dry-run", "-f", "--force", "-h", "--help"},
	"track":      {"-p", "--parent", "--base", "-h", "--help"},
	"version":    {"-h", "--help"},
}

type completionResult struct {
	candidates   []string
	files        bool
	filePrefix   string
	fileFragment string
}

type completionValueKind int

const (
	completionNoValues completionValueKind = iota
	completionLocalBranches
	completionHeadBranches
	completionTrackedBranches
	completionExistingTrackedBranches
	completionRemotes
	completionFiles
)

type completionValue struct {
	kind  completionValueKind
	known bool
}

func (a *App) completion(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: graphene completion <bash|zsh>")
	}
	var content string
	switch args[0] {
	case "bash":
		content = bashCompletion
	case "zsh":
		content = zshCompletion
	default:
		return fmt.Errorf("usage: graphene completion <bash|zsh>")
	}
	_, err := io.WriteString(a.stdout, content)
	return err
}

func (a *App) complete(args []string) error {
	if len(args) != 1 {
		return nil
	}
	result := a.completionCandidates(args[0])
	if result.files {
		fmt.Fprintf(a.stdout, "__graphene_files__\t%s\t%s\n", result.filePrefix, result.fileFragment)
	}
	for _, candidate := range result.candidates {
		fmt.Fprintln(a.stdout, candidate)
	}
	return nil
}

func (a *App) completionCandidates(line string) completionResult {
	words := splitCompletionLine(line)
	if len(words) == 0 {
		return completionResult{}
	}

	args := words[1:]
	if len(args) == 0 {
		return a.completeRoot("")
	}
	if len(args) == 1 {
		return a.completeRoot(args[0])
	}

	expanded, shellAlias := a.expandCompletionAlias(args)
	if shellAlias || len(expanded) < 2 {
		return completionResult{}
	}
	command := expanded[0]
	if command == "agent-skill" {
		command = "skill"
	}
	rest := expanded[1:]
	current := rest[len(rest)-1]
	before := rest[:len(rest)-1]

	switch command {
	case "completion":
		if len(completionPositionals(before, nil)) == 0 {
			return completeStatic(current, append([]string{"bash", "zsh"}, commandCompletionFlags[command]...))
		}
		return completionResult{}
	case "help":
		if len(completionPositionals(before, nil)) == 0 {
			commands := append([]string(nil), publicCompletionCommands...)
			commands = append(commands, a.configuredAliasNames()...)
			return completeStatic(current, commands)
		}
		return completionResult{}
	case "config":
		return a.completeConfig(before, current)
	case "aliases":
		return a.completeAliases(before, current)
	case "go":
		return a.completeGo(before, current)
	case "checkout", "switch":
		return a.completeGitBranchSwitch(command, before, current)
	}

	flags, known := commandCompletionFlags[command]
	if !known {
		return completionResult{}
	}
	valueFlags := completionValueFlags(command)
	if value, prefix, fragment, ok := attachedCompletionValue(current, valueFlags); ok {
		return a.completeValue(value, prefix, fragment)
	}
	if value, ok := previousCompletionValue(before, valueFlags); ok {
		return a.completeValue(value, "", current)
	}
	var flagResult completionResult
	if !completionAfterDoubleDash(before) && (current == "" || strings.HasPrefix(current, "-")) {
		flagResult = completeStatic(current, flags)
		if current != "" {
			return flagResult
		}
	}

	positionals := completionPositionals(before, valueFlags)
	switch command {
	case "split", "delete":
		if len(positionals) == 0 {
			return mergeCompletionResults(flagResult, a.completeValue(completionValue{kind: completionExistingTrackedBranches, known: true}, "", current))
		}
	case "forget":
		if len(positionals) == 0 {
			return mergeCompletionResults(flagResult, a.completeValue(completionValue{kind: completionTrackedBranches, known: true}, "", current))
		}
	case "track":
		if len(positionals) == 0 && completionHasValue(before, valueFlags) {
			return mergeCompletionResults(flagResult, a.completeValue(completionValue{kind: completionLocalBranches, known: true}, "", current))
		}
	case "import", "restack":
		if len(positionals) == 0 {
			return mergeCompletionResults(flagResult, a.completeValue(completionValue{kind: completionLocalBranches, known: true}, "", current))
		}
	case "send", "sendf":
		if len(positionals) == 0 {
			return mergeCompletionResults(flagResult, a.completeValue(completionValue{kind: completionRemotes, known: true}, "", current))
		}
	}
	return flagResult
}

func (a *App) completeRoot(current string) completionResult {
	if strings.HasPrefix(current, "-") {
		return completeStatic(current, []string{"-h", "--help", "-v", "--version"})
	}
	commands := append([]string(nil), publicCompletionCommands...)
	commands = append(commands, "checkout", "switch")
	commands = append(commands, a.configuredAliasNames()...)
	return completeStatic(current, commands)
}

func (a *App) completeConfig(before []string, current string) completionResult {
	positionals := completionPositionals(before, nil)
	if len(positionals) == 0 {
		return completeStatic(current, []string{"get", "set", "unset", "-h", "--help"})
	}
	if len(positionals) == 1 && !completionAfterDoubleDash(before) && (current == "" || strings.HasPrefix(current, "-")) {
		return completeStatic(current, []string{"--global", "--local", "-h", "--help"})
	}
	return completionResult{}
}

func (a *App) completeAliases(before []string, current string) completionResult {
	positionals := completionPositionals(before, nil)
	if len(positionals) == 0 {
		return completeStatic(current, []string{"import", "-h", "--help"})
	}
	if positionals[0] != "import" {
		return completionResult{}
	}
	if len(positionals) == 1 && !completionAfterDoubleDash(before) && strings.HasPrefix(current, "-") {
		return completeStatic(current, []string{"--global", "--local", "--force", "-h", "--help"})
	}
	if len(positionals) == 1 {
		result := completionResult{files: true, fileFragment: current}
		if current == "" && !completionAfterDoubleDash(before) {
			result = mergeCompletionResults(result, completeStatic(current, []string{"--global", "--local", "--force", "-h", "--help"}))
		}
		return result
	}
	return completionResult{}
}

func (a *App) completeGo(before []string, current string) completionResult {
	positionals := completionPositionals(before, nil)
	for _, arg := range before {
		if isGoCompletionDirection(arg) {
			return completionResult{}
		}
	}
	if len(positionals) > 0 {
		return completionResult{}
	}
	return completeStatic(current, []string{"top", "bottom", "up", "down", "-t", "--top", "-b", "--bottom", "-u", "--up", "-d", "--down", "-h", "--help"})
}

func isGoCompletionDirection(arg string) bool {
	if arg == "top" || arg == "bottom" || arg == "up" || arg == "down" {
		return true
	}
	if arg == "-t" || arg == "--top" || arg == "-b" || arg == "--bottom" || arg == "-u" || arg == "--up" || arg == "-d" || arg == "--down" {
		return true
	}
	for _, prefix := range []string{"-t", "-b", "-u", "-d", "--top=", "--bottom=", "--up=", "--down="} {
		if strings.HasPrefix(arg, prefix) && len(arg) > len(prefix) {
			return true
		}
	}
	return false
}

func (a *App) completeGitBranchSwitch(command string, before []string, current string) completionResult {
	if command == "checkout" && completionAfterDoubleDash(before) {
		return completionResult{files: true, fileFragment: current}
	}
	valueFlags := completionValueFlags(command)
	if _, _, _, ok := attachedCompletionValue(current, valueFlags); ok {
		return completionResult{}
	}
	if _, ok := previousCompletionValue(before, valueFlags); ok {
		return completionResult{}
	}
	if !completionAfterDoubleDash(before) && strings.HasPrefix(current, "-") {
		return completionResult{}
	}
	if len(completionPositionals(before, valueFlags)) > 0 {
		return completionResult{}
	}
	return a.completeValue(completionValue{kind: completionLocalBranches, known: true}, "", current)
}

func completionValueFlags(command string) map[string]completionValue {
	value := func(kind completionValueKind) completionValue {
		return completionValue{kind: kind, known: true}
	}
	switch command {
	case "new":
		return map[string]completionValue{
			"-b": value(completionNoValues), "--branch": value(completionNoValues),
			"--base": value(completionHeadBranches), "--parent": value(completionHeadBranches),
			"-m": value(completionNoValues), "--message": value(completionNoValues),
		}
	case "amend":
		return map[string]completionValue{"-m": value(completionNoValues), "--message": value(completionNoValues)}
	case "squash":
		return map[string]completionValue{
			"-c": value(completionNoValues), "--count": value(completionNoValues),
			"-m": value(completionNoValues), "--message": value(completionNoValues),
		}
	case "skill":
		return map[string]completionValue{"--out": value(completionFiles)}
	case "track":
		return map[string]completionValue{"-p": value(completionLocalBranches), "--parent": value(completionLocalBranches), "--base": value(completionLocalBranches)}
	case "send", "sendf":
		return map[string]completionValue{"--remote": value(completionRemotes)}
	case "checkout":
		return map[string]completionValue{
			"-b": value(completionNoValues), "-B": value(completionNoValues), "-c": value(completionNoValues), "-C": value(completionNoValues),
			"--orphan": value(completionNoValues),
		}
	case "switch":
		return map[string]completionValue{
			"-c": value(completionNoValues), "-C": value(completionNoValues), "--create": value(completionNoValues),
			"--force-create": value(completionNoValues), "--orphan": value(completionNoValues),
		}
	default:
		return nil
	}
}

func attachedCompletionValue(current string, values map[string]completionValue) (completionValue, string, string, bool) {
	flag, fragment, ok := strings.Cut(current, "=")
	if !ok {
		return completionValue{}, "", "", false
	}
	value, ok := values[flag]
	if !ok {
		return completionValue{}, "", "", false
	}
	return value, flag + "=", fragment, true
}

func previousCompletionValue(before []string, values map[string]completionValue) (completionValue, bool) {
	if len(before) == 0 || completionAfterDoubleDash(before[:len(before)-1]) {
		return completionValue{}, false
	}
	value, ok := values[before[len(before)-1]]
	return value, ok
}

func (a *App) completeValue(value completionValue, prefix, fragment string) completionResult {
	if !value.known || value.kind == completionNoValues {
		return completionResult{}
	}
	if value.kind == completionFiles {
		return completionResult{files: true, filePrefix: prefix, fileFragment: fragment}
	}

	var candidates []string
	switch value.kind {
	case completionLocalBranches:
		candidates, _ = a.git.LocalBranches()
	case completionHeadBranches:
		candidates, _ = a.git.LocalBranchesPointingAt("HEAD")
	case completionTrackedBranches:
		candidates = a.trackedCompletionBranches(false)
	case completionExistingTrackedBranches:
		candidates = a.trackedCompletionBranches(true)
	case completionRemotes:
		out, err := a.git.Output("remote")
		if err == nil && strings.TrimSpace(out) != "" {
			candidates = strings.Split(out, "\n")
		}
	}
	return completeStaticWithPrefix(fragment, prefix, candidates)
}

func (a *App) trackedCompletionBranches(requireLocal bool) []string {
	state, err := a.git.ReadState()
	if err != nil {
		return nil
	}
	local := map[string]bool{}
	if requireLocal {
		branches, err := a.git.LocalBranches()
		if err != nil {
			return nil
		}
		for _, branch := range branches {
			local[branch] = true
		}
	}

	var branches []string
	for _, stack := range state.Stacks {
		for _, branch := range stack.Branches {
			if !requireLocal || local[branch] {
				branches = append(branches, branch)
			}
		}
	}
	return branches
}

func completeStatic(current string, candidates []string) completionResult {
	return completeStaticWithPrefix(current, "", candidates)
}

func completeStaticWithPrefix(fragment, prefix string, candidates []string) completionResult {
	seen := map[string]bool{}
	var matches []string
	for _, candidate := range candidates {
		if !strings.HasPrefix(candidate, fragment) {
			continue
		}
		candidate = prefix + candidate
		if !seen[candidate] {
			seen[candidate] = true
			matches = append(matches, candidate)
		}
	}
	sort.Strings(matches)
	return completionResult{candidates: matches}
}

func mergeCompletionResults(left, right completionResult) completionResult {
	result := left
	if right.files {
		result.files = true
		result.filePrefix = right.filePrefix
		result.fileFragment = right.fileFragment
	}
	candidates := append(append([]string(nil), left.candidates...), right.candidates...)
	result.candidates = completeStatic("", candidates).candidates
	return result
}

func completionPositionals(args []string, valueFlags map[string]completionValue) []string {
	var positionals []string
	positionalsOnly := false
	skipValue := false
	for _, arg := range args {
		if skipValue {
			skipValue = false
			continue
		}
		if !positionalsOnly && arg == "--" {
			positionalsOnly = true
			continue
		}
		if !positionalsOnly {
			if _, _, ok := strings.Cut(arg, "="); ok && strings.HasPrefix(arg, "--") {
				continue
			}
			if _, ok := valueFlags[arg]; ok {
				skipValue = true
				continue
			}
			if strings.HasPrefix(arg, "-") && arg != "-" {
				continue
			}
		}
		positionals = append(positionals, arg)
	}
	return positionals
}

func completionHasValue(args []string, valueFlags map[string]completionValue) bool {
	for index, arg := range args {
		if flag, _, ok := strings.Cut(arg, "="); ok {
			if _, known := valueFlags[flag]; known {
				return true
			}
		}
		if _, known := valueFlags[arg]; known && index+1 < len(args) {
			return true
		}
	}
	return false
}

func completionAfterDoubleDash(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return true
		}
	}
	return false
}

func splitCompletionLine(line string) []string {
	var words []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	haveWord := false
	endedWithSpace := false

	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			current.WriteByte(c)
			haveWord = true
			escaped = false
			endedWithSpace = false
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
			} else {
				current.WriteByte(c)
			}
			haveWord = true
			endedWithSpace = false
			continue
		}
		if inDouble {
			switch c {
			case '"':
				inDouble = false
			case '\\':
				escaped = true
			default:
				current.WriteByte(c)
			}
			haveWord = true
			endedWithSpace = false
			continue
		}

		switch c {
		case '\\':
			escaped = true
			haveWord = true
			endedWithSpace = false
		case '\'':
			inSingle = true
			haveWord = true
			endedWithSpace = false
		case '"':
			inDouble = true
			haveWord = true
			endedWithSpace = false
		case ' ', '\t', '\n', '\r':
			if haveWord {
				words = append(words, current.String())
				current.Reset()
				haveWord = false
			}
			endedWithSpace = true
		default:
			current.WriteByte(c)
			haveWord = true
			endedWithSpace = false
		}
	}
	if escaped {
		current.WriteByte('\\')
	}
	if haveWord {
		words = append(words, current.String())
	} else if endedWithSpace {
		words = append(words, "")
	}
	return words
}

func (a *App) configuredAliasNames() []string {
	out, err := a.git.Output("config", "--null", "--get-regexp", "^graphene\\.alias\\.")
	if err != nil {
		return nil
	}
	var names []string
	for _, record := range strings.Split(out, "\x00") {
		key, _, _ := strings.Cut(record, "\n")
		name := strings.TrimPrefix(key, "graphene.alias.")
		if name != key && validAliasName(name) {
			names = append(names, name)
		}
	}
	return names
}

func (a *App) expandCompletionAlias(args []string) ([]string, bool) {
	expanded := append([]string(nil), args...)
	seen := map[string]bool{}
	for depth := 0; depth < maxAliasDepth; depth++ {
		if len(expanded) == 0 || isBuiltinCommand(expanded[0]) {
			return expanded, false
		}
		name := expanded[0]
		value, ok, err := a.configAliasFor(name)
		if err != nil || !ok {
			return expanded, false
		}
		if strings.HasPrefix(value, "!") {
			return nil, true
		}
		if seen[name] {
			return nil, false
		}
		seen[name] = true
		words, err := splitAlias(value)
		if err != nil || len(words) == 0 {
			return nil, false
		}
		next := append([]string(nil), words...)
		next = append(next, expanded[1:]...)
		expanded = next
	}
	return nil, false
}
