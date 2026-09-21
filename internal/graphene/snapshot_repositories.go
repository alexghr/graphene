package graphene

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func nestedRepositoryCollision(repository, path string) error {
	return fmt.Errorf("nested repository %q would be overwritten by parent path %q; move it aside and retry", repository, path)
}

// Values identify gitlinks already owned by the parent, which may be restored
// or updated without changing the checkout inside the directory.
func (g Git) nestedRepositories(savedTree string) (map[string]bool, error) {
	root, err := g.Output("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	g.Dir = root
	entries, err := g.OutputBytes("ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	if savedTree != "" {
		saved, err := g.OutputBytes("ls-tree", "-r", "-z", savedTree)
		if err != nil {
			return nil, err
		}
		entries = append(entries, saved...)
	}
	gitlinks := map[string]bool{}
	var candidates []string
	for entry := range strings.SplitSeq(string(entries), "\x00") {
		header, path, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		if strings.HasPrefix(header, "160000 ") {
			gitlinks[path] = true
		}
		candidates = append(candidates, path)
	}
	for _, ignored := range []bool{false, true} {
		args := []string{"ls-files", "--others", "--exclude-standard", "-z"}
		if ignored {
			args = append(args, "--ignored")
		}
		out, err := g.OutputBytes(args...)
		if err != nil {
			return nil, err
		}
		for path := range strings.SplitSeq(string(out), "\x00") {
			if path != "" {
				candidates = append(candidates, strings.TrimSuffix(path, "/"))
			}
		}
	}
	repositories := map[string]bool{}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		for path := candidate; path != "." && path != ""; path = filepath.ToSlash(filepath.Dir(path)) {
			if seen[path] {
				break
			}
			seen[path] = true
			full := filepath.Join(root, filepath.FromSlash(path))
			info, err := os.Lstat(full)
			if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if !info.IsDir() {
				continue
			}
			_, err = os.Lstat(filepath.Join(full, ".git"))
			if err == nil {
				repositories[path] = gitlinks[path]
				continue
			}
			if !os.IsNotExist(err) {
				return nil, err
			}
			if gitlinks[path] {
				children, err := os.ReadDir(full)
				if err != nil {
					return nil, err
				}
				if len(children) > 0 {
					repositories[path] = true
				}
			}
		}
	}
	return repositories, nil
}

func (g Git) checkNestedRepositories(savedTree string, trees ...string) error {
	repositories, err := g.nestedRepositories(savedTree)
	if err != nil || len(repositories) == 0 {
		return err
	}
	risks := map[nestedRepositoryRisk]bool{}
	if err := g.collectRepositoryTreeRisks(repositories, risks, trees...); err != nil {
		return err
	}
	if sorted := sortedRepositoryRisks(risks); len(sorted) > 0 {
		return nestedRepositoryCollision(sorted[0].repository, sorted[0].path)
	}
	return nil
}

type nestedRepositoryRisk struct {
	repository string
	path       string
}

func (g Git) collectRepositoryTreeRisks(repositories map[string]bool, risks map[nestedRepositoryRisk]bool, trees ...string) error {
	// Reset and checkout also remove paths from the current index. Protect
	// repositories created around those paths while an operation was paused.
	index, err := g.OutputBytes("ls-files", "--stage", "--full-name", "-z", "--", ":/")
	if err != nil {
		return err
	}
	collectRepositoryEntryRisks(repositories, risks, index)
	seen := map[string]bool{}
	for _, tree := range trees {
		if tree == "" || seen[tree] {
			continue
		}
		seen[tree] = true
		out, err := g.OutputBytes("ls-tree", "-r", "--full-tree", "-z", tree)
		if err != nil {
			return err
		}
		collectRepositoryEntryRisks(repositories, risks, out)
	}
	return nil
}

func collectRepositoryEntryRisks(repositories map[string]bool, risks map[nestedRepositoryRisk]bool, entries []byte) {
	for entry := range strings.SplitSeq(string(entries), "\x00") {
		header, path, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		mode, _, _ := strings.Cut(header, " ")
		collectRepositoryPathRisks(repositories, risks, mode, path)
	}
}

func collectRepositoryPathRisks(repositories map[string]bool, risks map[nestedRepositoryRisk]bool, mode, path string) {
	if mode == "000000" {
		return
	}
	for repository, gitlink := range repositories {
		if gitlink && path == repository && mode == "160000" {
			continue
		}
		if pathsOverlap(path, repository) {
			risks[nestedRepositoryRisk{repository: repository, path: path}] = true
		}
	}
}

func sortedRepositoryRisks(risks map[nestedRepositoryRisk]bool) []nestedRepositoryRisk {
	result := make([]nestedRepositoryRisk, 0, len(risks))
	for risk := range risks {
		result = append(result, risk)
	}
	slices.SortFunc(result, func(a, b nestedRepositoryRisk) int {
		if order := strings.Compare(a.repository, b.repository); order != 0 {
			return order
		}
		return strings.Compare(a.path, b.path)
	})
	return result
}

func (g Git) hasParentTrackedChanges() (bool, error) {
	for _, args := range [][]string{
		{"diff", "--cached", "--quiet", "--no-ext-diff", "--ignore-submodules=none", "--", ":/"},
		{"diff", "--quiet", "--no-ext-diff", "--ignore-submodules=all", "--", ":/"},
	} {
		if err := g.OutputErr(args...); err != nil {
			if isGitExit(err, 1) {
				return true, nil
			}
			return false, err
		}
	}
	return false, nil
}

func (g Git) runWithoutSubmodules(args ...string) error {
	return g.Run(append([]string{"-c", "submodule.recurse=false"}, args...)...)
}

func (a *App) rebaseRepositoryRisks(p *Pending, savedTree string) ([]nestedRepositoryRisk, error) {
	repositories, err := a.git.nestedRepositories(savedTree)
	if err != nil || len(repositories) == 0 {
		return nil, err
	}
	r := p.Recovery
	risks := map[nestedRepositoryRisk]bool{}
	trees := []string{r.BaseHead, r.FastForward, r.Onto}
	if p.ReturnRef != "" {
		trees = append(trees, p.ReturnRef)
	} else if p.ReturnBranch != "" {
		trees = append(trees, "refs/heads/"+p.ReturnBranch)
	}
	for _, op := range p.Queue {
		top, onto := r.Expected[op.Top], r.Expected[op.Onto]
		if p.Operation == "restack" && op.Top == p.Branch && r.FastForward != "" {
			top = r.FastForward
		}
		if op.Onto == r.Base {
			onto = r.BaseHead
		}
		trees = append(trees, top, onto)
		// Git can fast-forward to a target already containing the whole branch.
		// The historical upstream tree is not a checkout destination.
		contained, err := a.isAncestor(top, onto)
		if err != nil {
			return nil, err
		}
		if contained {
			continue
		}
		if err := a.git.collectReplayRepositoryRisks(repositories, risks, op.Upstream, top); err != nil {
			return nil, err
		}
	}
	if err := a.git.collectRepositoryTreeRisks(repositories, risks, trees...); err != nil {
		return nil, err
	}
	return sortedRepositoryRisks(risks), nil
}

func (g Git) collectReplayRepositoryRisks(repositories map[string]bool, risks map[nestedRepositoryRisk]bool, upstream, top string) error {
	// Ask Git for each replayed patch's paths, not the net diff of its endpoints.
	// This is a conservative warning, not a simulation of Git's merge machinery.
	out, err := g.OutputBytes("log", "--format=", "--raw", "--no-abbrev", "--no-renames", "--no-merges", "--no-ext-diff", "--no-relative", "--no-color", "--no-show-signature", "--root", "-z", upstream+".."+top, "--")
	if err != nil {
		return err
	}
	records := strings.Split(string(out), "\x00")
	for i := 0; i < len(records); i++ {
		header := strings.TrimSpace(records[i])
		if header == "" {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") || i+1 >= len(records) {
			return fmt.Errorf("cannot read replay path record %q", header)
		}
		i++
		collectRepositoryPathRisks(repositories, risks, strings.TrimPrefix(fields[0], ":"), records[i])
		collectRepositoryPathRisks(repositories, risks, fields[1], records[i])
	}
	return nil
}

func (a *App) warnNestedRepositoryRisks(risks []nestedRepositoryRisk) {
	if len(risks) == 0 {
		return
	}
	for _, risk := range risks {
		fmt.Fprintf(a.stderr, "warning: parent changes touch %q inside or overlapping nested repository %q\n", risk.path, risk.repository)
	}
	fmt.Fprintln(a.stderr, "Local files or edits there may be overwritten or deleted. Graphene's abort cannot restore nested repository contents.")
}

func (a *App) preflightRebaseRepositories(p *Pending, savedTree string, reportAccepted bool) error {
	risks, err := a.rebaseRepositoryRisks(p, savedTree)
	if err != nil || len(risks) == 0 {
		return err
	}
	if !p.Recovery.AcceptRisk || reportAccepted {
		a.warnNestedRepositoryRisks(risks)
	}
	if !p.Recovery.AcceptRisk {
		return fmt.Errorf("move the nested repository aside, or rerun %s with --accept-risk to allow possible overwrites; for a pending operation, abort before rerunning", p.Operation)
	}
	return nil
}
