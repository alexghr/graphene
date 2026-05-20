package graphene

import "sort"

type stackGraph struct {
	nodes    map[string]bool
	parent   map[string]string
	children map[string][]string
	incoming map[string]bool
	order    []string
}

func newStackGraph(state State) stackGraph {
	graph := stackGraph{
		nodes:    map[string]bool{},
		parent:   map[string]string{},
		children: map[string][]string{},
		incoming: map[string]bool{},
	}
	edgeSeen := map[string]bool{}
	addNode := func(name string) {
		if name == "" || graph.nodes[name] {
			return
		}
		graph.nodes[name] = true
		graph.order = append(graph.order, name)
	}
	addEdge := func(parent, child string) {
		if parent == "" || child == "" {
			return
		}
		addNode(parent)
		addNode(child)
		key := parent + "\x00" + child
		if edgeSeen[key] {
			return
		}
		edgeSeen[key] = true
		graph.children[parent] = append(graph.children[parent], child)
		graph.incoming[child] = true
		if graph.parent[child] == "" {
			graph.parent[child] = parent
		}
	}

	for _, stack := range state.Stacks {
		addNode(stack.Base)
		parent := stack.Base
		for _, branch := range stack.Branches {
			addEdge(parent, branch)
			parent = branch
		}
	}
	return graph
}

func (g stackGraph) roots() []string {
	var roots []string
	for _, name := range g.order {
		if !g.incoming[name] {
			roots = append(roots, name)
		}
	}
	sort.Strings(roots)
	return roots
}
