package graphene

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
)

// Rewrite sources are reporting evidence, never rollback or ref ownership data.
type rewriteSource struct {
	Head  string `json:"head"`
	Patch string `json:"patch,omitempty"`
}

func (a *App) captureRewriteSources(state State, refs map[string]string) map[string]rewriteSource {
	before := make(map[string]rewriteSource, len(refs))
	for branch, head := range refs {
		source := rewriteSource{Head: head}
		if boundary, err := a.resolveBranchBoundary(state, branch, refs); err == nil {
			source.Patch = a.branchPatch(boundary, head)
		}
		before[branch] = source
	}
	return before
}

func (a *App) branchPatch(boundary, head string) string {
	patch, err := a.git.Output("diff",
		"--no-ext-diff", "--no-textconv", "--no-renames", "--no-color", "--no-relative",
		"--src-prefix=a/", "--dst-prefix=b/", "--binary", "--submodule=short", "--ignore-submodules=none",
		"--diff-algorithm=myers", "--no-indent-heuristic", "--unified=0", "--inter-hunk-context=0",
		"--output-indicator-new=+", "--output-indicator-old=-", "--output-indicator-context= ", boundary, head, "--")
	if err != nil {
		return ""
	}
	return ownPatchDigest(patch)
}

func ownPatchDigest(patch string) string {
	var normalized strings.Builder
	for line := range strings.SplitSeq(patch, "\n") {
		switch {
		case line == "GIT binary patch":
			// Binary deltas depend on the source blob, which may change upstream.
			return ""
		case strings.HasPrefix(line, "index "):
			continue
		case strings.HasPrefix(line, "@@ "):
			// Line positions can move when the branch inherits an ancestor's edit.
			line = "@@"
		}
		normalized.WriteString(line)
		normalized.WriteByte('\n')
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(normalized.String())))
}

func (a *App) printRewriteSummary(before map[string]rewriteSource, state State) {
	if len(before) == 0 {
		return
	}
	refs, err := a.git.snapshotBranchRefs()
	if err != nil {
		fmt.Fprintln(a.stdout, "Rewrite summary unavailable: could not read branch tips.")
		return
	}
	names := make([]string, 0, len(before))
	for branch := range before {
		names = append(names, branch)
	}
	for branch := range refs {
		if _, existed := before[branch]; !existed && state.ContainsBranch(branch) {
			names = append(names, branch)
		}
	}
	slices.Sort(names)
	printed := false
	for _, branch := range names {
		old := before[branch]
		head := refs[branch]
		if old.Head == head {
			continue
		}
		if !printed {
			fmt.Fprintln(a.stdout, "Rewrite summary:")
			printed = true
		}
		switch {
		case head == "":
			fmt.Fprintf(a.stdout, "  %s: %s -> deleted\n", branch, shortSyncRef(old.Head))
		case old.Head == "":
			fmt.Fprintf(a.stdout, "  %s: created -> %s\n", branch, shortSyncRef(head))
		default:
			classification := "patch comparison unavailable"
			if boundary, err := a.resolveBranchBoundary(state, branch, refs); err == nil && old.Patch != "" {
				if patch := a.branchPatch(boundary, head); patch != "" {
					classification = "patch changed"
					if patch == old.Patch {
						classification = "patch unchanged"
					}
				}
			}
			fmt.Fprintf(a.stdout, "  %s: %s -> %s (%s)\n", branch, shortSyncRef(old.Head), shortSyncRef(head), classification)
		}
	}
}
