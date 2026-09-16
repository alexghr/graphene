package graphene

import (
	"fmt"
	"sort"
	"strings"
)

type snapshotRefEdit struct {
	Ref string
	Old string
	New string
}

func (g Git) snapshotBranchRefs() (map[string]string, error) {
	out, err := g.Output("for-each-ref", "--format=%(refname:strip=2) %(objectname) %(symref)", "refs/heads")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("snapshots require ordinary local branch refs: %s", line)
		}
		refs[fields[0]] = fields[1]
	}
	return refs, nil
}

func (g Git) updateSnapshotRefs(edits []snapshotRefEdit) error {
	if len(edits) == 0 {
		return nil
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].Ref < edits[j].Ref })
	var input strings.Builder
	input.WriteString("start\x00")
	for _, edit := range edits {
		// NUL is the protocol delimiter; Git validates ref names and object IDs.
		if strings.ContainsRune(edit.Ref+edit.Old+edit.New, 0) || !strings.HasPrefix(edit.Ref, "refs/") {
			return fmt.Errorf("invalid snapshot ref update for %q", edit.Ref)
		}
		switch {
		case edit.Old == edit.New:
			fmt.Fprintf(&input, "verify %s\x00%s\x00", edit.Ref, edit.Old)
		case edit.New == "":
			fmt.Fprintf(&input, "delete %s\x00%s\x00", edit.Ref, edit.Old)
		case edit.Old == "":
			fmt.Fprintf(&input, "create %s\x00%s\x00", edit.Ref, edit.New)
		default:
			fmt.Fprintf(&input, "update %s\x00%s\x00%s\x00", edit.Ref, edit.New, edit.Old)
		}
	}
	input.WriteString("prepare\x00commit\x00")
	_, err := g.outputWithInput(strings.NewReader(input.String()), "update-ref", "--no-deref", "--stdin", "-z")
	return err
}
