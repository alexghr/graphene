package graphene

import (
	"fmt"
	"strconv"
	"strings"
)

type goDirection string

const (
	goTop    goDirection = "top"
	goBottom goDirection = "bottom"
	goUp     goDirection = "up"
	goDown   goDirection = "down"
)

type goOptions struct {
	direction goDirection
	selector  int
}

func (a *App) goBranch(args []string) error {
	opts, err := parseGoArgs(args)
	if err != nil {
		return err
	}

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}
	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}

	target, err := goTarget(state, current, opts.direction, opts.selector)
	if err != nil {
		return err
	}
	return a.git.Run("switch", target)
}

func parseGoArgs(args []string) (goOptions, error) {
	var opts goOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var (
			direction      goDirection
			selector       string
			inlineSelector bool
			ok             bool
		)

		switch {
		case arg == "top":
			direction, ok = goTop, true
		case arg == "bottom":
			direction, ok = goBottom, true
		case arg == "up":
			direction, ok = goUp, true
		case arg == "down":
			direction, ok = goDown, true
		case arg == "--top" || arg == "-t":
			direction, ok = goTop, true
		case strings.HasPrefix(arg, "--top="):
			direction, selector, inlineSelector, ok = goTop, strings.TrimPrefix(arg, "--top="), true, true
		case shortGoSelector(arg, "t"):
			direction, selector, ok = goTop, strings.TrimPrefix(arg, "-t"), true
		case arg == "--bottom" || arg == "-b":
			direction, ok = goBottom, true
		case strings.HasPrefix(arg, "--bottom="):
			direction, selector, inlineSelector, ok = goBottom, strings.TrimPrefix(arg, "--bottom="), true, true
		case shortGoSelector(arg, "b"):
			direction, selector, ok = goBottom, strings.TrimPrefix(arg, "-b"), true
		case arg == "--up" || arg == "-u":
			direction, ok = goUp, true
		case strings.HasPrefix(arg, "--up="):
			direction, selector, inlineSelector, ok = goUp, strings.TrimPrefix(arg, "--up="), true, true
		case shortGoSelector(arg, "u"):
			direction, selector, ok = goUp, strings.TrimPrefix(arg, "-u"), true
		case arg == "--down" || arg == "-d":
			direction, ok = goDown, true
		case strings.HasPrefix(arg, "--down="):
			direction, selector, inlineSelector, ok = goDown, strings.TrimPrefix(arg, "--down="), true, true
		case shortGoSelector(arg, "d"):
			direction, selector, ok = goDown, strings.TrimPrefix(arg, "-d"), true
		default:
			return opts, fmt.Errorf("unsupported argument %q; supported go directions are up, down, top, and bottom", arg)
		}

		if ok {
			if opts.direction != "" {
				return opts, fmt.Errorf("graphene go accepts exactly one direction")
			}
			opts.direction = direction
			if inlineSelector && selector == "" {
				return opts, fmt.Errorf("invalid selector %q; use 1, 2, ...", selector)
			}
			if selector == "" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				selector = args[i+1]
				i++
			}
			if selector != "" {
				n, err := parseGoSelector(selector)
				if err != nil {
					return opts, err
				}
				opts.selector = n
			}
		}
	}
	if opts.direction == "" {
		return opts, fmt.Errorf("usage: graphene go <up|down|top|bottom> [number]")
	}
	return opts, nil
}

func shortGoSelector(arg, flag string) bool {
	prefix := "-" + flag
	if !strings.HasPrefix(arg, prefix) || len(arg) == len(prefix) {
		return false
	}
	for _, r := range strings.TrimPrefix(arg, prefix) {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseGoSelector(raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid selector %q; use 1, 2, ...", raw)
	}
	return n, nil
}

func goTarget(state State, current string, direction goDirection, selector int) (string, error) {
	graph := newStackGraph(state)
	if !graph.nodes[current] {
		return "", fmt.Errorf("branch %q is not in a graphene stack", current)
	}

	candidates := graph.candidates(current, direction)
	if len(candidates) == 0 {
		return "", noGoCandidateError(current, direction)
	}
	if selector == 0 && len(candidates) > 1 {
		return "", goSelectionError(fmt.Sprintf("multiple branches match --%s; rerun with --%s <number>", direction, direction), candidates)
	}
	if selector > len(candidates) {
		return "", goSelectionError(fmt.Sprintf("--%s selector %d is out of range", direction, selector), candidates)
	}
	if selector == 0 {
		selector = 1
	}
	return candidates[selector-1], nil
}

func (g stackGraph) candidates(current string, direction goDirection) []string {
	switch direction {
	case goUp:
		return append([]string(nil), g.children[current]...)
	case goDown:
		if parent := g.parent[current]; parent != "" {
			return []string{parent}
		}
		return nil
	case goTop:
		var leaves []string
		g.addLeaves(current, &leaves)
		return leaves
	case goBottom:
		return g.bottomCandidates(current)
	default:
		return nil
	}
}

func (g stackGraph) addLeaves(current string, leaves *[]string) {
	children := g.children[current]
	if len(children) == 0 {
		return
	}
	for _, child := range children {
		if len(g.children[child]) == 0 {
			*leaves = append(*leaves, child)
			continue
		}
		g.addLeaves(child, leaves)
	}
}

func (g stackGraph) bottomCandidates(current string) []string {
	var path []string
	for branch := current; branch != ""; branch = g.parent[branch] {
		path = append(path, branch)
	}
	if len(path) == 1 {
		return append([]string(nil), g.children[current]...)
	}
	bottom := path[len(path)-2]
	if bottom == current {
		return nil
	}
	return []string{bottom}
}

func noGoCandidateError(current string, direction goDirection) error {
	switch direction {
	case goTop:
		return fmt.Errorf("branch %q is already at the top of its stack", current)
	case goBottom:
		return fmt.Errorf("branch %q is already at the bottom of its stack", current)
	case goUp:
		return fmt.Errorf("branch %q has no branch upstack", current)
	case goDown:
		return fmt.Errorf("branch %q has no branch downstack", current)
	default:
		return fmt.Errorf("branch %q has no branch for --%s", current, direction)
	}
}

func goSelectionError(message string, candidates []string) error {
	var b strings.Builder
	b.WriteString(message)
	b.WriteString(":\npossible branches:\n")
	for i, candidate := range candidates {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, candidate)
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}
