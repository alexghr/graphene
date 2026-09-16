package graphene

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Snapshots are immutable. The pending operation will record its snapshot ID
// and the refs it changed; taking a backup does not grant ownership of a ref.
type operationSnapshot struct {
	Version      int               `json:"version"`
	Worktree     string            `json:"worktree"`
	Branch       string            `json:"branch"`
	Head         string            `json:"head"`
	Refs         map[string]string `json:"refs"`
	Stacks       []Stack           `json:"stacks"`
	Index        []byte            `json:"index,omitempty"`
	IndexTree    string            `json:"indexTree,omitempty"`
	WorktreeTree string            `json:"worktreeTree,omitempty"`
}

func (g Git) captureSnapshot(includeWorktree bool) (string, error) {
	if !g.stateLock.held() {
		return "", fmt.Errorf("capturing a snapshot requires the repository state lock")
	}
	state, err := g.ReadState()
	if err != nil {
		return "", err
	}
	if state.Pending != nil {
		return "", fmt.Errorf("finish or abort the pending operation before taking a snapshot")
	}
	if err := g.requireNoGitOperation(); err != nil {
		return "", err
	}
	branch, err := g.CurrentBranch()
	if err != nil {
		return "", err
	}
	worktree, err := g.WorktreeID()
	if err != nil {
		return "", err
	}
	refs, err := g.snapshotBranchRefs()
	if err != nil {
		return "", err
	}
	snapshot := operationSnapshot{
		Version: 1, Worktree: worktree, Branch: branch, Head: refs[branch],
		Refs: refs, Stacks: cloneStacks(state.Stacks),
	}
	if snapshot.Head == "" {
		return "", fmt.Errorf("snapshot requires an existing commit on %q", branch)
	}
	if includeWorktree {
		if err := g.captureSnapshotWorktree(&snapshot); err != nil {
			return "", err
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(random[:])
	path, err := g.snapshotPath(id)
	if err != nil {
		return "", err
	}
	if err := writeJSONFileAtomic(path, snapshot); err != nil {
		return "", err
	}
	var edits []snapshotRefEdit
	for branch, oid := range refs {
		edits = append(edits,
			snapshotRefEdit{Ref: "refs/heads/" + branch, Old: oid, New: oid},
			snapshotRefEdit{Ref: snapshotRefPrefix(id) + "heads/" + branch, New: oid},
		)
	}
	if includeWorktree {
		edits = append(edits,
			snapshotRefEdit{Ref: snapshotRefPrefix(id) + "index", New: snapshot.IndexTree},
			snapshotRefEdit{Ref: snapshotRefPrefix(id) + "worktree", New: snapshot.WorktreeTree},
		)
	}
	// Verify branch tips and install their backups in one ref transaction.
	// The caller must not mutate the repository until this succeeds.
	if err := g.updateSnapshotRefs(edits); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return id, nil
}

func (g Git) readSnapshot(id string) (operationSnapshot, error) {
	path, err := g.snapshotPath(id)
	if err != nil {
		return operationSnapshot{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return operationSnapshot{}, err
	}
	var snapshot operationSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshot, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return snapshot, err
	}
	if snapshot.Version != 1 || snapshot.Worktree == "" || snapshot.Branch == "" || snapshot.Head == "" || snapshot.Refs[snapshot.Branch] != snapshot.Head {
		return snapshot, fmt.Errorf("invalid recovery snapshot %s", id)
	}
	if (len(snapshot.Index) == 0) != (snapshot.IndexTree == "") || (snapshot.IndexTree == "") != (snapshot.WorktreeTree == "") {
		return snapshot, fmt.Errorf("snapshot %s has incomplete worktree data", id)
	}
	backups := map[string]string{}
	for branch, oid := range snapshot.Refs {
		backups[snapshotRefPrefix(id)+"heads/"+branch] = oid
	}
	if snapshot.IndexTree != "" {
		backups[snapshotRefPrefix(id)+"index"] = snapshot.IndexTree
		backups[snapshotRefPrefix(id)+"worktree"] = snapshot.WorktreeTree
	}
	for ref, oid := range backups {
		actual, err := g.Output("show-ref", "--verify", "--hash", ref)
		if err != nil {
			return snapshot, fmt.Errorf("read snapshot backup %s: %w", ref, err)
		}
		if actual != oid {
			return snapshot, fmt.Errorf("snapshot backup %s changed", ref)
		}
	}
	return snapshot, nil
}

// expected names only the branches owned by the operation, including created
// branches (absent from snapshot.Refs) and deleted branches (empty expected OID).
// The caller aborts any owned Git rebase first, then restores metadata after this
// succeeds. Keeping the snapshot until then makes rollback safe to retry.
func (g Git) restoreSnapshot(id string, expected map[string]string) ([]Stack, error) {
	if !g.stateLock.held() {
		return nil, fmt.Errorf("restoring a snapshot requires the repository state lock")
	}
	snapshot, err := g.readSnapshot(id)
	if err != nil {
		return nil, err
	}
	worktree, err := g.WorktreeID()
	if err != nil {
		return nil, err
	}
	if worktree != snapshot.Worktree {
		return nil, fmt.Errorf("restore snapshot from its original worktree %s", snapshot.Worktree)
	}
	if err := g.requireNoGitOperation(); err != nil {
		return nil, err
	}
	current, err := g.Output("branch", "--show-current")
	if err != nil {
		return nil, err
	}
	actual, err := g.snapshotBranchRefs()
	if err != nil {
		return nil, err
	}
	var edits []snapshotRefEdit
	for branch, want := range expected {
		original := snapshot.Refs[branch]
		if actual[branch] != want && actual[branch] != original {
			return nil, fmt.Errorf("cannot restore branch %q: it changed outside the operation", branch)
		}
		if actual[branch] != original && (branch != current || snapshot.WorktreeTree == "") {
			if err := g.requireSnapshotBranchAvailable(branch); err != nil {
				return nil, err
			}
		}
		edits = append(edits, snapshotRefEdit{Ref: "refs/heads/" + branch, Old: actual[branch], New: original})
	}
	if snapshot.WorktreeTree != "" {
		if _, owned := expected[current]; current != "" && current != snapshot.Branch && !owned {
			return nil, fmt.Errorf("switch back to the operation's branch %q before restoring the snapshot", snapshot.Branch)
		}
		if current != snapshot.Branch {
			if err := g.requireSnapshotBranchAvailable(snapshot.Branch); err != nil {
				return nil, err
			}
		}
		if _, owned := expected[snapshot.Branch]; !owned {
			if actual[snapshot.Branch] != snapshot.Head {
				return nil, fmt.Errorf("original branch %q moved outside the operation", snapshot.Branch)
			}
			edits = append(edits, snapshotRefEdit{Ref: "refs/heads/" + snapshot.Branch, Old: snapshot.Head, New: snapshot.Head})
		}
		if err := g.preflightSnapshotWorktree(snapshot); err != nil {
			return nil, err
		}
	}
	if err := g.updateSnapshotRefs(edits); err != nil {
		return nil, err
	}
	if snapshot.WorktreeTree != "" {
		if err := g.restoreSnapshotWorktree(snapshot); err != nil {
			return nil, err
		}
	}
	return cloneStacks(snapshot.Stacks), nil
}

func (g Git) requireSnapshotBranchAvailable(branch string) error {
	checkedOut, err := g.BranchCheckedOut(branch)
	if err != nil {
		return err
	}
	if checkedOut {
		return fmt.Errorf("cannot restore branch %q while it is checked out in another worktree; switch that worktree away first", branch)
	}
	return nil
}

// Cleanup is called only after the operation's completion is persisted. It does
// not need the manifest or backup refs to survive an interrupted earlier cleanup.
func (g Git) removeSnapshot(id string) error {
	if !g.stateLock.held() {
		return fmt.Errorf("removing a snapshot requires the repository state lock")
	}
	path, err := g.snapshotPath(id)
	if err != nil {
		return err
	}
	out, err := g.Output("for-each-ref", "--format=%(refname) %(objectname)", snapshotRefPrefix(id))
	if err != nil {
		return err
	}
	var edits []snapshotRefEdit
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			edits = append(edits, snapshotRefEdit{Ref: fields[0], Old: fields[1]})
		}
	}
	if err := g.updateSnapshotRefs(edits); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (g Git) snapshotPath(id string) (string, error) {
	if _, err := hex.DecodeString(id); err != nil || len(id) != 32 {
		return "", fmt.Errorf("invalid snapshot ID %q", id)
	}
	dir, err := g.GrapheneDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "snapshots", id+".json"), nil
}

func snapshotRefPrefix(id string) string {
	return "refs/graphene/snapshots/" + id + "/"
}
