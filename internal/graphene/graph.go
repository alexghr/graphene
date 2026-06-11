package graphene

import (
	"fmt"
	"io"
	"strings"

	"github.com/alexghr/graphene/internal/flagparse"
)

func (a *App) graph(args []string) error {
	opts, err := parseGraphArgs(args)
	if err != nil {
		return err
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	current, err := a.git.Output("branch", "--show-current")
	if err != nil {
		return err
	}
	if opts.stack {
		return WriteCurrentStackGraph(a.stdout, state, current)
	}
	return WriteGraph(a.stdout, state, current)
}

type graphOptions struct {
	stack bool
}

func parseGraphArgs(args []string) (graphOptions, error) {
	var opts graphOptions
	cursor := flagparse.New(args)
	for arg, ok := cursor.Next(); ok; arg, ok = cursor.Next() {
		if arg.Positional() {
			return opts, fmt.Errorf("unsupported argument %q; usage: graphene graph [--stack]", arg.Raw())
		}
		if flag, ok := arg.Long(); ok {
			if value, matched, err := flag.Bool("stack"); matched {
				if err != nil {
					return opts, err
				}
				opts.stack = value
				continue
			}
		}
		if arg.ShortBoolCluster("s", func(flag byte) { opts.stack = true }) {
			continue
		}
		return opts, fmt.Errorf("unsupported argument %q; usage: graphene graph [--stack]", arg.Raw())
	}
	return opts, nil
}

func WriteGraph(w io.Writer, state State, current string) error {
	out := RenderGraph(state, current)
	if out == "" {
		out = "no graphene stacks\n"
	}
	_, err := io.WriteString(w, out)
	return err
}

func WriteCurrentStackGraph(w io.Writer, state State, current string) error {
	out, err := RenderCurrentStackGraph(state, current)
	if err != nil {
		return err
	}
	if out == "" {
		out = "no graphene stacks\n"
	}
	_, err = io.WriteString(w, out)
	return err
}

func RenderGraph(state State, current string) string {
	graph := newStackGraph(state)
	roots := graph.roots()
	if len(roots) == 0 && state.Pending == nil {
		return ""
	}

	var b strings.Builder
	for i, root := range roots {
		if i > 0 {
			b.WriteByte('\n')
		}
		writeGraphNode(&b, graph, root, current, "")
	}
	if state.Pending != nil {
		writePending(&b, state.Pending)
	}
	return b.String()
}

func RenderCurrentStackGraph(state State, current string) (string, error) {
	if len(state.Stacks) == 0 && state.Pending == nil {
		return "", nil
	}

	path, ok := VisibleStackPath(state, current)
	if !ok {
		return "", fmt.Errorf("branch %q is not in a graphene stack", current)
	}
	if len(path) == 0 {
		return "", nil
	}

	base, ok := BaseBranch(state, path[0])
	if !ok {
		return "", fmt.Errorf("branch %q is not in a graphene stack", current)
	}

	stackState := State{
		Stacks:  []Stack{{Base: base, Branches: path}},
		Pending: state.Pending,
	}
	return RenderGraph(stackState, current), nil
}

func writeGraphNode(b *strings.Builder, graph stackGraph, name, current, prefix string) {
	b.WriteString(prefix)
	b.WriteString(name)
	if name == current {
		b.WriteString(" *")
	}
	b.WriteByte('\n')

	children := graph.children[name]
	childPrefix := prefix + "  "
	for i, child := range children {
		connector := "|- "
		nextPrefix := childPrefix + "|  "
		if i == len(children)-1 {
			connector = "`- "
			nextPrefix = childPrefix + "   "
		}
		writeGraphChild(b, graph, child, current, childPrefix+connector, nextPrefix)
	}
}

func writeGraphChild(b *strings.Builder, graph stackGraph, name, current, linePrefix, childPrefix string) {
	b.WriteString(linePrefix)
	b.WriteString(name)
	if name == current {
		b.WriteString(" *")
	}
	b.WriteByte('\n')
	children := graph.children[name]
	for i, child := range children {
		connector := "|- "
		nextPrefix := childPrefix + "|  "
		if i == len(children)-1 {
			connector = "`- "
			nextPrefix = childPrefix + "   "
		}
		writeGraphChild(b, graph, child, current, childPrefix+connector, nextPrefix)
	}
}

func writePending(b *strings.Builder, pending *Pending) {
	b.WriteString("pending ")
	b.WriteString(pending.Operation)
	if pending.Branch != "" {
		b.WriteString(": ")
		b.WriteString(pending.Branch)
	}
	b.WriteByte('\n')

	if len(pending.Queue) == 0 {
		return
	}
	b.WriteString("  next: rebase ")
	b.WriteString(pending.Queue[0].Top)
	b.WriteString(" onto ")
	b.WriteString(pending.Queue[0].Onto)
	b.WriteByte('\n')
	if len(pending.Queue) > 1 {
		b.WriteString("  remaining: ")
		b.WriteString(fmt.Sprint(len(pending.Queue)))
		b.WriteByte('\n')
	}
}
