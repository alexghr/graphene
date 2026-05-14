package graphene

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

type graphNode struct {
	Name     string
	Current  bool
	Children []*graphNode
}

func (a *App) graph(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("graphene graph does not accept arguments")
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
	roots := graphRoots(state, current)
	if len(roots) == 0 && state.Pending == nil {
		return ""
	}

	var b strings.Builder
	for i, root := range roots {
		if i > 0 {
			b.WriteByte('\n')
		}
		writeGraphNode(&b, root, "")
	}
	if state.Pending != nil {
		writePending(&b, state.Pending)
	}
	return b.String()
}

func graphRoots(state State, current string) []*graphNode {
	nodes := map[string]*graphNode{}
	incoming := map[string]bool{}
	var order []string
	edgeSeen := map[string]bool{}

	node := func(name string) *graphNode {
		if existing := nodes[name]; existing != nil {
			return existing
		}
		n := &graphNode{Name: name, Current: name == current}
		nodes[name] = n
		order = append(order, name)
		return n
	}
	addEdge := func(parent, child string) {
		if parent == "" || child == "" {
			return
		}
		parentNode := node(parent)
		childNode := node(child)
		key := parent + "\x00" + child
		if edgeSeen[key] {
			return
		}
		edgeSeen[key] = true
		parentNode.Children = append(parentNode.Children, childNode)
		incoming[child] = true
	}

	for _, stack := range state.Stacks {
		node(stack.Base)
		parent := stack.Base
		for _, branch := range stack.Branches {
			addEdge(parent, branch)
			parent = branch
		}
	}

	var roots []*graphNode
	for _, name := range order {
		if !incoming[name] {
			roots = append(roots, nodes[name])
		}
	}
	sort.SliceStable(roots, func(i, j int) bool {
		return roots[i].Name < roots[j].Name
	})
	return roots
}

func writeGraphNode(b *strings.Builder, node *graphNode, prefix string) {
	b.WriteString(prefix)
	b.WriteString(node.Name)
	if node.Current {
		b.WriteString(" *")
	}
	b.WriteByte('\n')

	childPrefix := prefix + "  "
	for i, child := range node.Children {
		connector := "|- "
		nextPrefix := childPrefix + "|  "
		if i == len(node.Children)-1 {
			connector = "`- "
			nextPrefix = childPrefix + "   "
		}
		writeGraphChild(b, child, childPrefix+connector, nextPrefix)
	}
}

func writeGraphChild(b *strings.Builder, node *graphNode, linePrefix, childPrefix string) {
	b.WriteString(linePrefix)
	b.WriteString(node.Name)
	if node.Current {
		b.WriteString(" *")
	}
	b.WriteByte('\n')
	for i, child := range node.Children {
		connector := "|- "
		nextPrefix := childPrefix + "|  "
		if i == len(node.Children)-1 {
			connector = "`- "
			nextPrefix = childPrefix + "   "
		}
		writeGraphChild(b, child, childPrefix+connector, nextPrefix)
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
