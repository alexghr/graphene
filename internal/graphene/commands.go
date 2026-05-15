package graphene

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func (a *App) commit(args []string) error {
	requestedBranch, commitArgs, err := parseCommitArgs(args)
	if err != nil {
		return err
	}
	if err := rejectCommitModeArgs(commitArgs); err != nil {
		return err
	}

	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}

	branch := requestedBranch
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

	commitGitArgs := append([]string{"commit"}, commitArgs...)
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

	state, err := a.git.ReadState()
	if err != nil {
		return err
	}
	if err := state.AddCommit(current, branch); err != nil {
		return err
	}
	return a.git.WriteState(state)
}

func (a *App) amend(args []string) error {
	current, err := a.git.CurrentBranch()
	if err != nil {
		return err
	}

	state, err := a.git.ReadState()
	if err != nil {
		return err
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

	commitGitArgs := append([]string{"commit", "--amend"}, args...)
	if err := a.git.Run(commitGitArgs...); err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}

	state.Pending = &Pending{
		Operation: "amend",
		Branch:    current,
		Queue:     ops,
	}
	if err := a.git.WriteState(state); err != nil {
		return err
	}
	return a.runPendingRebases(state)
}

func (a *App) rebase(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("graphene rebase does not accept arguments")
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
	base, ok := RebaseBaseBranch(state, current)
	if !ok {
		return fmt.Errorf("branch %q is not in a graphene stack", current)
	}

	dirty, err := a.git.HasTrackedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("tracked changes would prevent rebase; stash or commit them before graphene rebase")
	}

	oldRefs := a.stateRefs(state)
	if err := a.git.Run("switch", base); err != nil {
		return err
	}
	if err := a.git.Run("pull", "--ff-only"); err != nil {
		return err
	}

	ops, err := RestackOpsAfterRewrite(state, base, oldRefs)
	if err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}

	state.Pending = &Pending{
		Operation: "rebase",
		Branch:    base,
		Queue:     ops,
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
			state.Pending = nil
			return a.git.WriteState(state)
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
		if err := a.git.Run("rebase", "--abort"); err != nil {
			return err
		}
	}
	state.Pending = nil
	return a.git.WriteState(state)
}

func (a *App) push(args []string) error {
	remote, _, flags, err := parsePushArgs(args)
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
	branches := PushBranches(state, current)
	if len(branches) == 0 {
		return fmt.Errorf("no branch to push")
	}
	if remote == "" {
		remote, err = a.pushRemote(current, branches)
		if err != nil {
			return err
		}
	}

	pushArgs := []string{"push"}
	pushArgs = append(pushArgs, flags...)
	pushArgs = append(pushArgs, remote)
	pushArgs = append(pushArgs, branches...)
	if err := a.git.Run(pushArgs...); err != nil {
		return err
	}
	if pushDryRun(flags) {
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

func (a *App) pr(args []string) error {
	remote, err := parsePRArgs(args)
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
	branches := PushBranches(state, current)
	if len(branches) == 0 {
		return fmt.Errorf("no branch for pull request")
	}
	if remote == "" {
		remote, err = a.pushRemote(current, branches)
		if err != nil {
			return err
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
			state.Pending = nil
		}
		if err := a.git.WriteState(state); err != nil {
			return err
		}
	}
	return nil
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

func parseCommitArgs(args []string) (string, []string, error) {
	var branch string
	var commitArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			commitArgs = append(commitArgs, args[i:]...)
			break
		}
		if arg != "-b" {
			commitArgs = append(commitArgs, arg)
			continue
		}
		if branch != "" {
			return "", nil, fmt.Errorf("commit branch specified more than once")
		}
		if i+1 >= len(args) || args[i+1] == "" {
			return "", nil, fmt.Errorf("missing branch after -b")
		}
		branch = args[i+1]
		i++
	}
	return branch, commitArgs, nil
}

func rejectCommitModeArgs(args []string) error {
	for _, arg := range args {
		if arg == "--" {
			return nil
		}
		if arg == "--amend" {
			return fmt.Errorf("graphene commit cannot use --amend; use graphene amend")
		}
	}
	return nil
}

func parsePushArgs(args []string) (string, bool, []string, error) {
	var remote string
	var remoteProvided bool
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		remote = args[0]
		remoteProvided = true
		args = args[1:]
	}

	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return "", false, nil, fmt.Errorf("graphene push accepts the remote only as the first argument")
		}
		if destructivePushFlag(arg) {
			return "", false, nil, fmt.Errorf("graphene push does not support %s", arg)
		}
	}
	return remote, remoteProvided, append([]string(nil), args...), nil
}

func parsePRArgs(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if len(args) > 1 {
		return "", fmt.Errorf("graphene pr accepts at most one remote")
	}
	if strings.HasPrefix(args[0], "-") {
		return "", fmt.Errorf("graphene pr does not accept flags")
	}
	return args[0], nil
}

func pushDryRun(flags []string) bool {
	for _, flag := range flags {
		if flag == "-n" || flag == "--dry-run" || strings.HasPrefix(flag, "--dry-run=") {
			return true
		}
	}
	return false
}

func destructivePushFlag(flag string) bool {
	return flag == "-d" || flag == "--delete" || flag == "--all" || flag == "--mirror"
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
			return nil
		}
	}

	urls := PullRequestURLs(template, remoteURL, state, branches)
	if len(urls) == 0 {
		return nil
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
