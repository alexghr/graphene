package graphene

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alexghr/graphene/internal/flagparse"
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
	cursor := flagparse.New(args)
	for arg, ok := cursor.Next(); ok; arg, ok = cursor.Next() {
		var (
			direction      goDirection
			selector       string
			inlineSelector bool
			ok             bool
		)

		switch {
		case arg.Positional() && arg.Raw() == "top":
			direction, ok = goTop, true
		case arg.Positional() && arg.Raw() == "bottom":
			direction, ok = goBottom, true
		case arg.Positional() && arg.Raw() == "up":
			direction, ok = goUp, true
		case arg.Positional() && arg.Raw() == "down":
			direction, ok = goDown, true
		default:
			if flag, matched := goFlag(arg); matched {
				direction, selector, inlineSelector, ok = flag.direction, flag.selector, flag.inlineSelector, true
			} else {
				return opts, fmt.Errorf("unsupported argument %q; supported go directions are up, down, top, and bottom", arg.Raw())
			}
		}

		if ok {
			if opts.direction != "" {
				return opts, fmt.Errorf("graphene go accepts exactly one direction")
			}
			opts.direction = direction
			if inlineSelector && selector == "" {
				return opts, fmt.Errorf("invalid selector %q; use 1, 2, ...", selector)
			}
			if selector == "" {
				selector, _ = cursor.OptionalPositionalValue()
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

type parsedGoFlag struct {
	direction      goDirection
	selector       string
	inlineSelector bool
}

func goFlag(arg flagparse.Arg) (parsedGoFlag, bool) {
	if flag, ok := arg.Long(); ok {
		switch flag.Name() {
		case "top":
			return parsedGoFlag{direction: goTop, selector: flag.Value(), inlineSelector: flag.HasValue()}, true
		case "bottom":
			return parsedGoFlag{direction: goBottom, selector: flag.Value(), inlineSelector: flag.HasValue()}, true
		case "up":
			return parsedGoFlag{direction: goUp, selector: flag.Value(), inlineSelector: flag.HasValue()}, true
		case "down":
			return parsedGoFlag{direction: goDown, selector: flag.Value(), inlineSelector: flag.HasValue()}, true
		}
		return parsedGoFlag{}, false
	}
	switch arg.Raw() {
	case "-t":
		return parsedGoFlag{direction: goTop}, true
	case "-b":
		return parsedGoFlag{direction: goBottom}, true
	case "-u":
		return parsedGoFlag{direction: goUp}, true
	case "-d":
		return parsedGoFlag{direction: goDown}, true
	}
	if selector, ok := arg.AttachedShortValue('t', flagparse.AcceptDigits); ok {
		return parsedGoFlag{direction: goTop, selector: selector}, true
	}
	if selector, ok := arg.AttachedShortValue('b', flagparse.AcceptDigits); ok {
		return parsedGoFlag{direction: goBottom, selector: selector}, true
	}
	if selector, ok := arg.AttachedShortValue('u', flagparse.AcceptDigits); ok {
		return parsedGoFlag{direction: goUp, selector: selector}, true
	}
	if selector, ok := arg.AttachedShortValue('d', flagparse.AcceptDigits); ok {
		return parsedGoFlag{direction: goDown, selector: selector}, true
	}
	return parsedGoFlag{}, false
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
