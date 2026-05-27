package graphene

import (
	"fmt"
	"io"
	"strings"
)

func (a *App) graph(args []string) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "short" && args[0] != "long") {
		return fmt.Errorf("graphene graph accepts only optional alias format short or long")
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	current, err := a.git.Output("branch", "--show-current")
	if err != nil {
		return err
	}
	return WriteGraph(a.stdout, state, current)
}

func WriteGraph(w io.Writer, state State, current string) error {
	out := RenderGraph(state, current)
	if out == "" {
		out = "no graphene stacks\n"
	}
	_, err := io.WriteString(w, out)
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
