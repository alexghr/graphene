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
	Operation string     `json:"operation"`
	Branch    string     `json:"branch,omitempty"`
	Queue     []RebaseOp `json:"queue,omitempty"`
	Top       string     `json:"top,omitempty"`
	Branches  []string   `json:"branches,omitempty"`
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

func PushBranches(s State, current string) []string {
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
	for _, branch := range stack.Branches {
		add(branch)
	}
	return branches
}

func StackSuffix(s State, branch string) (Stack, int, bool) {
	loc, ok := s.BranchLocation(branch)
	if !ok {
		return Stack{}, 0, false
	}
	return s.Stacks[loc.StackIndex], loc.BranchIndex, true
}

func RestackOpsAfterRewrite(s State, branch string, oldRefs map[string]string) ([]RebaseOp, error) {
	rewritten := map[string]bool{branch: true}
	scheduled := map[int]bool{}
	var ops []RebaseOp

	for {
		changed := false
		for stackIndex, stack := range s.Stacks {
			if scheduled[stackIndex] || len(stack.Branches) == 0 {
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
				Top:      stack.Branches[len(stack.Branches)-1],
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
