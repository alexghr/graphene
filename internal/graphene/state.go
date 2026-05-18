package graphene

import (
	"encoding/json"
	"fmt"
)

const stateConfigKey = "graphene.state"

type State struct {
	Stacks  []Stack  `json:"stacks,omitempty"`
	Pending *Pending `json:"pending,omitempty"`
}

type Stack struct {
	Base     string   `json:"base"`
	Branches []string `json:"branches"`
}

type Pending struct {
	Operation    string     `json:"operation"`
	Branch       string     `json:"branch,omitempty"`
	ReturnBranch string     `json:"returnBranch,omitempty"`
	Queue        []RebaseOp `json:"queue,omitempty"`
	Top          string     `json:"top,omitempty"`
	Branches     []string   `json:"branches,omitempty"`
	NextStacks   []Stack    `json:"nextStacks,omitempty"`
}

type RebaseOp struct {
	Onto     string `json:"onto"`
	Upstream string `json:"upstream"`
	Top      string `json:"top"`
}

type BranchLocation struct {
	StackIndex  int
	BranchIndex int
}

func (g Git) ReadState() (State, error) {
	raw, err := g.Output("config", "--local", "--get", stateConfigKey)
	if err != nil {
		if isGitExit(err, 1) {
			return State{}, nil
		}
		return State{}, err
	}
	if raw == "" {
		return State{}, nil
	}

	var state State
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return State{}, fmt.Errorf("parse %s: %w", stateConfigKey, err)
	}
	return state, nil
}

func (g Git) WriteState(state State) error {
	if state.empty() {
		_, err := g.Output("config", "--local", "--unset", stateConfigKey)
		if err == nil || isGitExit(err, 5) {
			return nil
		}
		return err
	}

	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return g.OutputErr("config", "--local", stateConfigKey, string(data))
}

func (s State) empty() bool {
	return len(s.Stacks) == 0 && s.Pending == nil
}

func (s State) BranchLocation(branch string) (BranchLocation, bool) {
	for stackIndex, stack := range s.Stacks {
		for branchIndex, candidate := range stack.Branches {
			if candidate == branch {
				return BranchLocation{StackIndex: stackIndex, BranchIndex: branchIndex}, true
			}
		}
	}
	return BranchLocation{}, false
}

func (s State) StackAt(index int) (Stack, bool) {
	if index < 0 || index >= len(s.Stacks) {
		return Stack{}, false
	}
	return s.Stacks[index], true
}

func (s State) ContainsBranch(branch string) bool {
	_, ok := s.BranchLocation(branch)
	return ok
}

func (s *State) AddCommit(current, next string) error {
	if current == "" || next == "" {
		return fmt.Errorf("current and new branches are required")
	}
	if s.ContainsBranch(next) {
		return fmt.Errorf("branch %q is already recorded in graphene state", next)
	}

	loc, ok := s.BranchLocation(current)
	if !ok {
		s.Stacks = append(s.Stacks, Stack{Base: current, Branches: []string{next}})
		return nil
	}

	stack := &s.Stacks[loc.StackIndex]
	if loc.BranchIndex == len(stack.Branches)-1 {
		stack.Branches = append(stack.Branches, next)
		return nil
	}

	s.Stacks = append(s.Stacks, Stack{Base: current, Branches: []string{next}})
	return nil
}

func BranchesThroughCurrent(s State, current string) []string {
	loc, ok := s.BranchLocation(current)
	if !ok {
		if current == "" {
			return nil
		}
		return []string{current}
	}

	var branches []string
	seen := map[string]bool{}
	add := func(branch string) {
		if branch != "" && !seen[branch] {
			branches = append(branches, branch)
			seen[branch] = true
		}
	}

	var addDependencyPath func(branch string, visiting map[string]bool)
	addDependencyPath = func(branch string, visiting map[string]bool) {
		if branch == "" || visiting[branch] {
			return
		}
		visiting[branch] = true
		defer delete(visiting, branch)

		depLoc, ok := s.BranchLocation(branch)
		if !ok {
			return
		}
		depStack := s.Stacks[depLoc.StackIndex]
		addDependencyPath(depStack.Base, visiting)
		for _, depBranch := range depStack.Branches[:depLoc.BranchIndex+1] {
			add(depBranch)
		}
	}

	stack := s.Stacks[loc.StackIndex]
	addDependencyPath(stack.Base, map[string]bool{})
	for _, branch := range stack.Branches[:loc.BranchIndex+1] {
		add(branch)
	}
	return branches
}

func BranchesInConnectedStack(s State, current string) []string {
	if current == "" {
		return nil
	}

	adjacent := map[string][]string{}
	addEdge := func(a, b string) {
		if a == "" || b == "" {
			return
		}
		adjacent[a] = append(adjacent[a], b)
		adjacent[b] = append(adjacent[b], a)
	}
	for _, stack := range s.Stacks {
		parent := stack.Base
		for _, branch := range stack.Branches {
			addEdge(parent, branch)
			parent = branch
		}
	}
	if len(adjacent[current]) == 0 && !s.ContainsBranch(current) {
		return []string{current}
	}

	seen := map[string]bool{current: true}
	queue := []string{current}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, next := range adjacent[name] {
			if seen[next] {
				continue
			}
			seen[next] = true
			queue = append(queue, next)
		}
	}

	var branches []string
	for _, name := range StateRefNames(s) {
		if seen[name] && s.ContainsBranch(name) {
			branches = append(branches, name)
		}
	}
	if len(branches) == 0 && current != "" {
		return []string{current}
	}
	return branches
}

func RemoveBranches(s State, branches []string) State {
	deleted := map[string]bool{}
	for _, branch := range branches {
		if branch != "" {
			deleted[branch] = true
		}
	}
	if len(deleted) == 0 {
		return s
	}

	var stacks []Stack
	for _, stack := range s.Stacks {
		if deleted[stack.Base] {
			continue
		}

		kept := make([]string, 0, len(stack.Branches))
		for _, branch := range stack.Branches {
			if !deleted[branch] {
				kept = append(kept, branch)
			}
		}
		if len(kept) > 0 {
			stacks = append(stacks, Stack{Base: stack.Base, Branches: kept})
		}
	}
	s.Stacks = stacks
	return s
}

func RemoveBranchesWithBase(s State, branches []string, replacementBase string) State {
	deleted := map[string]bool{}
	for _, branch := range branches {
		if branch != "" {
			deleted[branch] = true
		}
	}
	if len(deleted) == 0 {
		return s
	}

	var stacks []Stack
	for _, stack := range s.Stacks {
		base := stack.Base
		if deleted[base] {
			base = replacementBase
		}

		kept := make([]string, 0, len(stack.Branches))
		for _, branch := range stack.Branches {
			if !deleted[branch] {
				kept = append(kept, branch)
			}
		}
		if len(kept) > 0 {
			stacks = append(stacks, Stack{Base: base, Branches: kept})
		}
	}
	s.Stacks = stacks
	return s
}

func RemoveStackThroughCurrent(s State, current string) (State, bool) {
	loc, ok := s.BranchLocation(current)
	if !ok {
		return s, false
	}

	stack := s.Stacks[loc.StackIndex]
	if loc.BranchIndex == len(stack.Branches)-1 {
		s.Stacks = append(s.Stacks[:loc.StackIndex], s.Stacks[loc.StackIndex+1:]...)
		return s, true
	}

	branches := append([]string(nil), stack.Branches[loc.BranchIndex+1:]...)
	s.Stacks[loc.StackIndex] = Stack{Base: current, Branches: branches}
	return s, true
}

func ReparentBranch(s State, current, base string) (State, []string, bool) {
	loc, ok := s.BranchLocation(current)
	if !ok {
		return s, nil, false
	}

	stack := s.Stacks[loc.StackIndex]
	moved := append([]string(nil), stack.Branches[loc.BranchIndex:]...)
	for _, branch := range moved {
		if branch == base {
			return s, nil, false
		}
	}

	if loc.BranchIndex == 0 {
		s.Stacks = append(s.Stacks[:loc.StackIndex], s.Stacks[loc.StackIndex+1:]...)
	} else {
		kept := append([]string(nil), stack.Branches[:loc.BranchIndex]...)
		s.Stacks[loc.StackIndex] = Stack{Base: stack.Base, Branches: kept}
	}
	s.Stacks = append(s.Stacks, Stack{Base: base, Branches: moved})
	return s, moved, true
}

func BaseBranch(s State, branch string) (string, bool) {
	loc, ok := s.BranchLocation(branch)
	if !ok {
		return "", false
	}
	if loc.BranchIndex == 0 {
		base := s.Stacks[loc.StackIndex].Base
		return base, base != ""
	}
	return s.Stacks[loc.StackIndex].Branches[loc.BranchIndex-1], true
}

func StackSuffix(s State, branch string) (Stack, int, bool) {
	loc, ok := s.BranchLocation(branch)
	if !ok {
		return Stack{}, 0, false
	}
	return s.Stacks[loc.StackIndex], loc.BranchIndex, true
}

func RestackOpsAfterRewrite(s State, branch string, oldRefs map[string]string) ([]RebaseOp, error) {
	return RestackOpsAfterRewrites(s, []string{branch}, oldRefs, nil)
}

func RestackOpsAfterRewrites(s State, branches []string, oldRefs map[string]string, skipTops map[string]bool) ([]RebaseOp, error) {
	rewritten := map[string]bool{}
	for _, branch := range branches {
		if branch != "" {
			rewritten[branch] = true
		}
	}
	if len(rewritten) == 0 {
		return nil, nil
	}

	scheduled := map[int]bool{}
	var ops []RebaseOp

	for {
		changed := false
		for stackIndex, stack := range s.Stacks {
			if scheduled[stackIndex] || len(stack.Branches) == 0 {
				continue
			}
			top := stack.Branches[len(stack.Branches)-1]
			if skipTops[top] {
				continue
			}

			start, predecessor := restackStart(stack, rewritten)
			if start < 0 {
				continue
			}

			upstream := oldRefs[predecessor]
			if upstream == "" {
				return nil, fmt.Errorf("missing old ref for %q", predecessor)
			}

			ops = append(ops, RebaseOp{
				Onto:     predecessor,
				Upstream: upstream,
				Top:      top,
			})
			scheduled[stackIndex] = true
			for _, name := range stack.Branches[start:] {
				if !rewritten[name] {
					rewritten[name] = true
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}

	return ops, nil
}

func restackStart(stack Stack, rewritten map[string]bool) (int, string) {
	if rewritten[stack.Base] {
		return 0, stack.Base
	}
	for i, branch := range stack.Branches {
		if rewritten[branch] && i+1 < len(stack.Branches) {
			return i + 1, branch
		}
	}
	return -1, ""
}

func StateRefNames(s State) []string {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if name != "" && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	for _, stack := range s.Stacks {
		add(stack.Base)
		for _, branch := range stack.Branches {
			add(branch)
		}
	}
	return names
}

func StateContainsName(s State, name string) bool {
	for _, existing := range StateRefNames(s) {
		if existing == name {
			return true
		}
	}
	return false
}
