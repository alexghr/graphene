package graphene

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func (a *App) newBranch(args []string) error {
	opts, err := parseNewArgs(args)
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

	recordBase := current
	if opts.base != "" {
		if err := a.validateNewBase(opts.base); err != nil {
			return err
		}
		recordBase = opts.base
	}

	branch := opts.branch
	temp := false
	var cfg Config
	if branch == "" {
		cfg, err = LoadConfig(a.getenv)
		if err != nil {
			return err
		}
		if err := a.ensureBranchPrefixAvailable(cfg.BranchPrefix); err != nil {
			return err
		}
		branch = tempBranchName(os.Getpid(), time.Now().UnixNano())
		temp = true
	}

	if err := a.git.Run("switch", "-c", branch); err != nil {
		return err
	}

	commitGitArgs := append([]string{"commit"}, opts.commitArgs...)
	if err := a.git.Run(commitGitArgs...); err != nil {
		a.cleanupFailedCommit(current, branch)
		return err
	}

	if temp {
		subject, err := a.git.Output("log", "-1", "--format=%s")
		if err != nil {
			return err
		}
		branch, err = a.derivedBranchName(cfg, subject)
		if err != nil {
			return err
		}
		if err := a.git.Run("branch", "-m", branch); err != nil {
			return err
		}
	}

	if err := state.AddCommit(recordBase, branch); err != nil {
		return err
	}
	return a.git.WriteState(state)
}

func (a *App) amend(args []string) error {
	commitArgs, err := parseAmendArgs(args)
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

	oldRefs := a.stateRefs(state)
	oldHead, err := a.git.Head()
	if err != nil {
		return err
	}
	oldRefs[current] = oldHead

	ops, err := RestackOpsAfterRewrite(state, current, oldRefs)
	if err != nil {
		return err
	}
	if len(ops) > 0 {
		dirty, err := a.git.HasUnstagedChanges()
		if err != nil {
			return err
		}
		if dirty {
			return fmt.Errorf("unstaged changes would prevent restacking; stash or commit them before graphene amend")
		}
	}

	commitGitArgs := append([]string{"commit", "--amend"}, commitArgs...)
	if err := a.git.Run(commitGitArgs...); err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}

	state.Pending = &Pending{
		Operation:    "amend",
		Branch:       current,
		ReturnBranch: current,
		Queue:        ops,
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runPendingRebases(state)
}

func (a *App) restack(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return fmt.Errorf("usage: graphene restack <base>")
	}
	base := args[0]

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

	oldBase, ok := BaseBranch(state, current)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
	}
	nextState, _, ok := ReparentBranch(state, current, base)
	if !ok {
		return fmt.Errorf("cannot restack %q onto %q", current, base)
	}

	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("tracked changes would prevent restack; stash or commit them before graphene restack")
	}

	oldRefs := a.stateRefs(state)
	oldHead, err := a.git.Head()
	if err != nil {
		return err
	}
	oldRefs[current] = oldHead
	oldBaseRef := oldRefs[oldBase]
	if oldBaseRef == "" {
		oldBaseRef, err = a.git.Output("rev-parse", "--verify", oldBase+"^{commit}")
		if err != nil {
			return err
		}
	}
	baseRef, err := a.git.Output("rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return err
	}

	if oldBaseRef == baseRef {
		return a.git.WriteState(nextState)
	}

	ops := []RebaseOp{{
		Onto:     base,
		Upstream: oldBaseRef,
		Top:      current,
	}}
	restackOps, err := RestackOpsAfterRewrite(nextState, current, oldRefs)
	if err != nil {
		return err
	}
	ops = append(ops, restackOps...)

	state.Pending = &Pending{
		Operation:    "restack",
		Branch:       current,
		ReturnBranch: current,
		Queue:        ops,
		NextStacks:   nextState.Stacks,
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runPendingRebases(state)
}

func (a *App) continueRebase(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("graphene continue does not accept arguments")
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending == nil || len(state.Pending.Queue) == 0 {
		inProgress, err := a.git.RebaseInProgress()
		if err != nil {
			return err
		}
		if !inProgress {
			return fmt.Errorf("no rebase in progress")
		}
		if err := a.git.Run("rebase", "--continue"); err != nil {
			return err
		}
		return a.clearPendingIfRebaseDone()
	}

	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if inProgress {
		if err := a.git.Run("rebase", "--continue"); err != nil {
			return err
		}
		inProgress, err = a.git.RebaseInProgress()
		if err != nil {
			return err
		}
		if inProgress {
			return nil
		}
		state.Pending.Queue = state.Pending.Queue[1:]
		if len(state.Pending.Queue) == 0 {
			return a.finishPendingRebases(state)
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	return a.runPendingRebases(state)
}

func (a *App) abortRebase(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("graphene abort does not accept arguments")
	}
	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if inProgress {
		if err := a.git.Run("rebase", "--abort"); err != nil {
			return err
		}
	} else if state.Pending == nil {
		return fmt.Errorf("no rebase in progress")
	}
	state.Pending = nil
	return a.git.WriteState(state)
}

func (a *App) forget(args []string) error {
	force, err := parseForgetArgs(args)
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
	if state.Pending != nil && !force {
		return fmt.Errorf("pending rebase exists; use graphene continue or graphene abort")
	}

	state, ok := RemoveStackThroughCurrent(state, current)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
	}
	if force {
		state.Pending = nil
	}
	return a.git.WriteState(state)
}

func (a *App) sync(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("graphene sync does not accept arguments")
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

	loc, ok := state.BranchLocation(current)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
	}
	stack, ok := state.StackAt(loc.StackIndex)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
	}

	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("tracked changes would prevent sync; stash or commit them before graphene sync")
	}

	oldRefs := a.stateRefs(state)
	baseRef, err := a.fetchBase(stack.Base)
	if err != nil {
		return err
	}

	branches, err := a.appliedPrefixBranches(baseRef, stack.Branches[:loc.BranchIndex+1], oldRefs)
	if err != nil {
		return err
	}

	firstRemaining := len(branches)
	nextState := RemoveBranchesWithBase(state, branches, stack.Base)
	deleted := map[string]bool{}
	for _, branch := range branches {
		deleted[branch] = true
	}

	returnBranch := current
	if firstRemaining > loc.BranchIndex {
		returnBranch = ""
		if firstRemaining < len(stack.Branches) {
			returnBranch = stack.Branches[firstRemaining]
		}
	}

	var ops []RebaseOp
	var rewritten []string
	skipTops := map[string]bool{}
	if firstRemaining < len(stack.Branches) {
		predecessor := stack.Base
		if firstRemaining > 0 {
			predecessor = stack.Branches[firstRemaining-1]
		}
		upstream := oldRefs[predecessor]
		if upstream == "" {
			return fmt.Errorf("missing old ref for %q", predecessor)
		}

		topIndex := loc.BranchIndex
		if firstRemaining > loc.BranchIndex {
			topIndex = len(stack.Branches) - 1
		}
		top := stack.Branches[topIndex]
		ops = append(ops, RebaseOp{
			Onto:     baseRef,
			Upstream: upstream,
			Top:      top,
		})
		rewritten = append(rewritten, stack.Branches[firstRemaining:topIndex+1]...)
		if topIndex == len(stack.Branches)-1 {
			skipTops[top] = true
		}
	}

	for i, dependent := range state.Stacks {
		if i == loc.StackIndex || !deleted[dependent.Base] || len(dependent.Branches) == 0 {
			continue
		}
		upstream := oldRefs[dependent.Base]
		if upstream == "" {
			return fmt.Errorf("missing old ref for %q", dependent.Base)
		}
		top := dependent.Branches[len(dependent.Branches)-1]
		ops = append(ops, RebaseOp{
			Onto:     baseRef,
			Upstream: upstream,
			Top:      top,
		})
		rewritten = append(rewritten, dependent.Branches...)
		skipTops[top] = true
		if returnBranch == "" {
			returnBranch = dependent.Branches[0]
		}
	}

	restackOps, err := RestackOpsAfterRewrites(nextState, rewritten, oldRefs, skipTops)
	if err != nil {
		return err
	}
	ops = append(ops, restackOps...)

	if len(ops) == 0 {
		if returnBranch != "" {
			if err := a.git.Run("switch", returnBranch); err != nil {
				return err
			}
		} else if err := a.switchToBaseOrDetach(stack.Base, baseRef); err != nil {
			return err
		}
		if err := a.deleteBranches(branches); err != nil {
			return err
		}
		return a.git.WriteState(nextState)
	}

	state.Pending = &Pending{
		Operation:    "sync",
		Branch:       returnBranch,
		ReturnBranch: returnBranch,
		Queue:        ops,
		Branches:     branches,
		NextStacks:   nextState.Stacks,
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runPendingRebases(state)
}

func (a *App) send(args []string) error {
	return a.sendBranches(args, false)
}

func (a *App) sendf(args []string) error {
	return a.sendBranches(args, true)
}

func (a *App) sendBranches(args []string, forceWithLease bool) error {
	opts, err := parseSendArgs(args)
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
	branches := BranchesThroughCurrent(state, current)
	if opts.wholeStack {
		branches = BranchesInConnectedStack(state, current)
	}
	if len(branches) == 0 {
		return fmt.Errorf("no branch to send")
	}
	remote := opts.remote
	if remote == "" {
		remote, err = a.pushRemote(current, branches)
		if err != nil {
			return err
		}
	}

	pushArgs := []string{"push"}
	if forceWithLease {
		pushArgs = append(pushArgs, "--force-with-lease")
	}
	if opts.dryRun {
		pushArgs = append(pushArgs, "--dry-run")
	}
	pushArgs = append(pushArgs, remote)
	pushArgs = append(pushArgs, branches...)
	if err := a.git.Run(pushArgs...); err != nil {
		return err
	}
	if opts.dryRun {
		return nil
	}

	for _, branch := range branches {
		hasUpstream, err := a.git.HasUpstream(branch)
		if err != nil {
			return err
		}
		if !hasUpstream {
			if err := a.git.SetUpstream(branch, remote); err != nil {
				return err
			}
		}
	}
	return a.printPullRequestURLs(remote, state, branches)
}

func (a *App) cleanupFailedCommit(original, branch string) {
	if original != "" {
		_ = a.git.OutputErr("switch", original)
	}
	if branch != "" {
		_ = a.git.OutputErr("branch", "-D", branch)
	}
}

func (a *App) derivedBranchName(cfg Config, subject string) (string, error) {
	state, err := a.git.ReadState()
	if err != nil {
		return "", err
	}
	base := BranchName(cfg.BranchPrefix, SlugSubject(subject))
	for n := 1; ; n++ {
		candidate := CandidateName(base, n)
		status, err := a.git.BranchCreateStatus(candidate)
		if err != nil {
			return "", err
		}
		if status == "available" && !StateContainsName(state, candidate) {
			return candidate, nil
		}
	}
}

func (a *App) ensureBranchPrefixAvailable(prefix string) error {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return nil
	}
	if _, err := a.git.Output("check-ref-format", "--branch", prefix+"/graphene-check"); err != nil {
		return fmt.Errorf("invalid branch prefix %q", prefix)
	}
	branches, err := a.git.LocalBranches()
	if err != nil {
		return err
	}
	for _, existing := range branches {
		if existing == prefix || strings.HasPrefix(prefix, existing+"/") {
			return fmt.Errorf("branch prefix %q conflicts with existing branch %q", prefix, existing)
		}
	}
	return nil
}

func (a *App) clearPendingIfRebaseDone() error {
	inProgress, err := a.git.RebaseInProgress()
	if err != nil {
		return err
	}
	if inProgress {
		return nil
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		return a.finishPendingRebases(state)
	}
	state.Pending = nil
	return a.git.WriteState(state)
}

func (a *App) runPendingRebases(state State) error {
	for state.Pending != nil && len(state.Pending.Queue) > 0 {
		op := state.Pending.Queue[0]
		if err := a.git.Run("rebase", "--update-refs", "--onto", op.Onto, op.Upstream, op.Top); err != nil {
			return err
		}
		state.Pending.Queue = state.Pending.Queue[1:]
		if len(state.Pending.Queue) == 0 {
			return a.finishPendingRebases(state)
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) finishPendingRebases(state State) error {
	returnBranch := ""
	var appliedBranches []string
	var nextStacks []Stack
	if state.Pending != nil {
		returnBranch = state.Pending.ReturnBranch
		nextStacks = state.Pending.NextStacks
		if state.Pending.Operation == "sync" {
			appliedBranches = append([]string(nil), state.Pending.Branches...)
		}
	}
	if returnBranch != "" {
		if err := a.git.Run("switch", returnBranch); err != nil {
			return err
		}
	}
	if len(appliedBranches) > 0 {
		if err := a.deleteBranches(appliedBranches); err != nil {
			return err
		}
		if nextStacks == nil {
			state = RemoveBranches(state, appliedBranches)
		}
	}
	if nextStacks != nil {
		state.Stacks = nextStacks
	}
	state.Pending = nil
	return a.git.WriteState(state)
}

func (a *App) fetchBase(base string) (string, error) {
	remote, merge, err := a.git.Upstream(base)
	if err != nil {
		return "", err
	}
	if remote == "" || merge == "" {
		return "", fmt.Errorf("branch %q has no upstream; set one before updating the stack", base)
	}

	oldBase, err := a.git.Output("rev-parse", "--verify", "refs/heads/"+base+"^{commit}")
	if err != nil {
		return "", err
	}
	if err := a.git.Run("fetch", remote); err != nil {
		return "", err
	}

	upstream := base + "@{upstream}"
	updatedBase, err := a.git.Output("rev-parse", "--verify", upstream+"^{commit}")
	if err != nil {
		return "", err
	}
	ancestor, err := a.isAncestor(oldBase, updatedBase)
	if err != nil {
		return "", err
	}
	if !ancestor {
		return "", fmt.Errorf("cannot fast-forward %q to %q; resolve the base branch before updating the stack", base, upstream)
	}
	if oldBase == updatedBase {
		return base, nil
	}

	checkedOut, err := a.git.BranchCheckedOut(base)
	if err != nil {
		return "", err
	}
	if checkedOut {
		return updatedBase, nil
	}
	if err := a.git.OutputErr("update-ref", "refs/heads/"+base, updatedBase, oldBase); err != nil {
		return "", err
	}
	return base, nil
}

func (a *App) switchToBaseOrDetach(base, baseRef string) error {
	if err := a.git.OutputErr("switch", base); err == nil {
		return nil
	}
	return a.git.Run("switch", "--detach", baseRef)
}

func (a *App) deleteBranches(branches []string) error {
	for _, branch := range branches {
		exists, err := a.git.BranchExists(branch)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := a.git.Run("branch", "-D", branch); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) appliedPrefixBranches(base string, branches []string, oldRefs map[string]string) ([]string, error) {
	if len(branches) == 0 {
		return nil, nil
	}

	applied, err := a.appliedCommitRefs(base, branches[len(branches)-1])
	if err != nil {
		return nil, err
	}

	var prefix []string
	for _, branch := range branches {
		ref := oldRefs[branch]
		if ref == "" {
			break
		}
		if applied[ref] {
			prefix = append(prefix, branch)
			continue
		}
		ancestor, err := a.isAncestor(ref, base)
		if err != nil {
			return nil, err
		}
		if !ancestor {
			break
		}
		prefix = append(prefix, branch)
	}
	return prefix, nil
}

func (a *App) appliedCommitRefs(base, top string) (map[string]bool, error) {
	out, err := a.git.Output("cherry", base, top)
	if err != nil {
		return nil, err
	}

	applied := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "-" {
			continue
		}
		applied[fields[1]] = true
	}
	return applied, nil
}

func (a *App) isAncestor(ancestor, descendant string) (bool, error) {
	_, err := a.git.Output("merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if isGitExit(err, 1) {
		return false, nil
	}
	return false, err
}

func (a *App) stateRefs(state State) map[string]string {
	refs := map[string]string{}
	for _, name := range StateRefNames(state) {
		if ref, err := a.git.Output("rev-parse", "--verify", name); err == nil {
			refs[name] = ref
		}
	}
	return refs
}

func (a *App) validateNewBase(base string) error {
	exists, err := a.git.BranchExists(base)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("base branch %q does not exist", base)
	}

	head, err := a.git.Head()
	if err != nil {
		return err
	}
	baseRef, err := a.git.Output("rev-parse", "--verify", "refs/heads/"+base+"^{commit}")
	if err != nil {
		return err
	}
	if baseRef != head {
		return fmt.Errorf("base branch %q does not point to current HEAD", base)
	}
	return nil
}

type commitOptions struct {
	branch     string
	base       string
	commitArgs []string
}

func parseNewArgs(args []string) (commitOptions, error) {
	return parseCommitOptions(args, true)
}

func parseAmendArgs(args []string) ([]string, error) {
	opts, err := parseCommitOptions(args, false)
	return opts.commitArgs, err
}

func parseCommitOptions(args []string, allowBranch bool) (commitOptions, error) {
	var opts commitOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "-b" || arg == "--branch":
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support %s", arg)
			}
			if opts.branch != "" {
				return opts, fmt.Errorf("new branch specified more than once")
			}
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("missing branch after %s", arg)
			}
			opts.branch = args[i+1]
			i++
		case strings.HasPrefix(arg, "--branch="):
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support --branch")
			}
			if opts.branch != "" {
				return opts, fmt.Errorf("new branch specified more than once")
			}
			opts.branch = strings.TrimPrefix(arg, "--branch=")
			if opts.branch == "" {
				return opts, fmt.Errorf("missing branch after --branch")
			}
		case arg == "--base":
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support --base")
			}
			if opts.base != "" {
				return opts, fmt.Errorf("base branch specified more than once")
			}
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("missing base after --base")
			}
			opts.base = args[i+1]
			i++
		case strings.HasPrefix(arg, "--base="):
			if !allowBranch {
				return opts, fmt.Errorf("graphene amend does not support --base")
			}
			if opts.base != "" {
				return opts, fmt.Errorf("base branch specified more than once")
			}
			opts.base = strings.TrimPrefix(arg, "--base=")
			if opts.base == "" {
				return opts, fmt.Errorf("missing base after --base")
			}
		case arg == "-m" || arg == "--message":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("missing message after %s", arg)
			}
			opts.commitArgs = append(opts.commitArgs, "-m", args[i+1])
			i++
		case strings.HasPrefix(arg, "--message="):
			opts.commitArgs = append(opts.commitArgs, arg)
		case arg == "--no-verify":
			opts.commitArgs = append(opts.commitArgs, arg)
		case arg == "--gpg-sign" || strings.HasPrefix(arg, "--gpg-sign=") || arg == "--no-gpg-sign":
			opts.commitArgs = append(opts.commitArgs, arg)
		case arg == "--amend":
			if allowBranch {
				return opts, fmt.Errorf("graphene new cannot use --amend; use graphene amend")
			}
			return opts, unsupportedCommitArg(arg, allowBranch)
		default:
			return opts, unsupportedCommitArg(arg, allowBranch)
		}
	}
	return opts, nil
}

func unsupportedCommitArg(arg string, allowBranch bool) error {
	supported := "-m/--message, --no-verify, --gpg-sign, and --no-gpg-sign"
	if allowBranch {
		supported = "-b/--branch, --base, " + supported
	}
	return fmt.Errorf("unsupported argument %q; supported commit options are %s", arg, supported)
}

func parseForgetArgs(args []string) (bool, error) {
	var force bool
	for _, arg := range args {
		switch arg {
		case "-f", "--force":
			force = true
		default:
			return false, fmt.Errorf("graphene forget does not support %s", arg)
		}
	}
	return force, nil
}

type sendOptions struct {
	remote     string
	wholeStack bool
	dryRun     bool
}

func parseSendArgs(args []string) (sendOptions, error) {
	var opts sendOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "--stack":
			opts.wholeStack = true
		case arg == "--dry-run":
			opts.dryRun = true
		case arg == "--remote":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("missing remote after --remote")
			}
			if opts.remote != "" {
				return opts, fmt.Errorf("graphene send accepts at most one remote")
			}
			opts.remote = args[i+1]
			i++
		case strings.HasPrefix(arg, "--remote="):
			if opts.remote != "" {
				return opts, fmt.Errorf("graphene send accepts at most one remote")
			}
			opts.remote = strings.TrimPrefix(arg, "--remote=")
			if opts.remote == "" {
				return opts, fmt.Errorf("missing remote after --remote")
			}
		case strings.HasPrefix(arg, "-"):
			return opts, fmt.Errorf("unsupported argument %q; supported send options are --remote, --stack, and --dry-run", arg)
		default:
			if opts.remote != "" {
				return opts, fmt.Errorf("graphene send accepts at most one remote")
			}
			opts.remote = arg
		}
	}
	return opts, nil
}

func (a *App) pushRemote(current string, branches []string) (string, error) {
	currentRemote, err := a.git.UpstreamRemote(current)
	if err != nil {
		return "", err
	}

	remote := currentRemote
	for _, branch := range branches {
		branchRemote, err := a.git.UpstreamRemote(branch)
		if err != nil {
			return "", err
		}
		if branchRemote == "" {
			continue
		}
		if remote == "" {
			remote = branchRemote
			continue
		}
		if branchRemote != remote {
			return "", fmt.Errorf("stack branches have mixed upstream remotes; pass a remote explicitly")
		}
	}
	if remote == "" {
		remote = "origin"
	}
	return remote, nil
}

func (a *App) printPullRequestURLs(remote string, state State, branches []string) error {
	template, err := a.git.PRURLTemplate()
	if err != nil {
		return err
	}

	var remoteURL string
	if template == "" {
		remoteURL, err = a.git.RemoteURL(remote)
		if err != nil {
			_, printErr := fmt.Fprintf(a.stdout, "No pull request URLs: remote %q has no push URL.\n", remote)
			return printErr
		}
	}

	urls := PullRequestURLs(template, remoteURL, state, branches)
	if len(urls) == 0 {
		_, err := fmt.Fprintf(a.stdout, "No pull request URLs for remote %q; set graphene.prUrlTemplate for non-GitHub remotes.\n", remote)
		return err
	}

	if _, err := fmt.Fprintln(a.stdout, "Pull request URLs:"); err != nil {
		return err
	}
	for _, pull := range urls {
		if _, err := fmt.Fprintf(a.stdout, "  %s into %s: %s\n", pull.Branch, pull.Base, pull.URL); err != nil {
			return err
		}
	}
	return nil
}
