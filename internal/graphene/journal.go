package graphene

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const operationJournalVersion = 1

type operationPhase string

const (
	operationPreparing   operationPhase = "preparing"
	operationInteractive operationPhase = "interactive"
	operationApplying    operationPhase = "applying"
	operationCommitting  operationPhase = "committing"
	operationCleanup     operationPhase = "cleanup"
	operationRollingBack operationPhase = "rolling-back"
)

type OperationJournal struct {
	Version                 int                   `json:"version"`
	ID                      string                `json:"id"`
	Kind                    string                `json:"kind"`
	Phase                   operationPhase        `json:"phase"`
	Worktree                string                `json:"worktree"`
	OriginalBranch          string                `json:"originalBranch,omitempty"`
	OriginalHead            string                `json:"originalHead,omitempty"`
	WorktreePolicy          string                `json:"worktreePolicy,omitempty"`
	WorktreeFingerprint     string                `json:"worktreeFingerprint,omitempty"`
	WorktreeBoundaryCrossed bool                  `json:"worktreeBoundaryCrossed,omitempty"`
	OriginalStacks          []Stack               `json:"originalStacks,omitempty"`
	DesiredStacks           []Stack               `json:"desiredStacks,omitempty"`
	Refs                    map[string]JournalRef `json:"refs,omitempty"`
	ValidationRefs          map[string]RefValue   `json:"validationRefs,omitempty"`
	ValidationRefsComplete  bool                  `json:"validationRefsComplete,omitempty"`
	Configs                 []JournalConfig       `json:"configs,omitempty"`
	IndexArtifact           string                `json:"indexArtifact,omitempty"`
	IndexMode               uint32                `json:"indexMode,omitempty"`
	StagedArtifact          string                `json:"stagedArtifact,omitempty"`
	WorktreeArtifact        string                `json:"worktreeArtifact,omitempty"`
	RawWorktreeArtifact     string                `json:"rawWorktreeArtifact,omitempty"`
	SharedIndexArtifact     string                `json:"sharedIndexArtifact,omitempty"`
	SharedIndexPath         string                `json:"sharedIndexPath,omitempty"`
	SharedIndexMode         uint32                `json:"sharedIndexMode,omitempty"`
	UntrackedArtifact       string                `json:"untrackedArtifact,omitempty"`
	ArtifactDigests         map[string]string     `json:"artifactDigests,omitempty"`
	Active                  *JournalAction        `json:"active,omitempty"`
	Progress                json.RawMessage       `json:"progress,omitempty"`
	RecoveryRefs            map[string]string     `json:"recoveryRefs,omitempty"`
	RecoveryArtifact        string                `json:"recoveryArtifact,omitempty"`
	WorktreeRestoreStarted  bool                  `json:"worktreeRestoreStarted,omitempty"`
	WorktreeRestored        bool                  `json:"worktreeRestored,omitempty"`
}

type JournalRef struct {
	Original RefValue `json:"original"`
	Expected RefValue `json:"expected"`
	Backup   string   `json:"backup,omitempty"`
}

type RefValue struct {
	Exists bool   `json:"exists"`
	OID    string `json:"oid,omitempty"`
}

type JournalConfig struct {
	Section  string        `json:"section"`
	Original []ConfigEntry `json:"original,omitempty"`
	Expected []ConfigEntry `json:"expected,omitempty"`
}

type ConfigEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type JournalAction struct {
	ID           string              `json:"id"`
	Kind         string              `json:"kind"`
	RefsBefore   map[string]RefValue `json:"refsBefore,omitempty"`
	RefsAfter    map[string]RefValue `json:"refsAfter,omitempty"`
	RefInventory map[string]RefValue `json:"refInventory,omitempty"`
}

type RefDrift struct {
	Ref      string
	Expected RefValue
	Actual   RefValue
}

func newOperationJournal(kind, worktree, branch, head string, stacks []Stack) (*OperationJournal, error) {
	id, err := newOperationID()
	if err != nil {
		return nil, err
	}
	return &OperationJournal{
		Version:        operationJournalVersion,
		ID:             id,
		Kind:           kind,
		Phase:          operationPreparing,
		Worktree:       worktree,
		OriginalBranch: branch,
		OriginalHead:   head,
		OriginalStacks: cloneStacks(stacks),
		Refs:           map[string]JournalRef{},
	}, nil
}

func newOperationID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("create operation id: %w", err)
	}
	return hex.EncodeToString(data[:]), nil
}

func (o *OperationJournal) validate() error {
	if o == nil {
		return nil
	}
	if o.Version != operationJournalVersion {
		return fmt.Errorf("unsupported graphene operation journal version %d", o.Version)
	}
	if len(o.ID) != 32 {
		return fmt.Errorf("invalid graphene operation id %q", o.ID)
	}
	if _, err := hex.DecodeString(o.ID); err != nil {
		return fmt.Errorf("invalid graphene operation id %q", o.ID)
	}
	if o.Kind == "" {
		return fmt.Errorf("graphene operation kind is missing")
	}
	if o.OriginalBranch != "" && !validBranchArgument(o.OriginalBranch) {
		return fmt.Errorf("invalid graphene original branch %q", o.OriginalBranch)
	}
	switch o.Kind {
	case "sync", "restack", "amend", "squash", "new", "split", "delete", "import", "track":
	default:
		return fmt.Errorf("unsupported graphene operation kind %q", o.Kind)
	}
	switch o.Phase {
	case operationPreparing, operationInteractive, operationApplying, operationCommitting, operationCleanup, operationRollingBack:
	default:
		return fmt.Errorf("invalid graphene operation phase %q", o.Phase)
	}
	if o.Phase == operationInteractive && o.Kind != "split" {
		return fmt.Errorf("%s operation cannot be interactive", o.Kind)
	}
	if o.WorktreeRestored && o.Phase != operationRollingBack {
		return fmt.Errorf("graphene operation restored its worktree outside rollback")
	}
	if o.WorktreeRestoreStarted && o.Phase != operationRollingBack {
		return fmt.Errorf("graphene operation started worktree restoration outside rollback")
	}
	if o.Worktree == "" {
		return fmt.Errorf("graphene operation worktree is missing")
	}
	switch o.WorktreePolicy {
	case "", worktreeRestoreNone, worktreeRestoreHard, worktreeRestoreIndex, worktreeRestoreSwitch:
	default:
		return fmt.Errorf("invalid graphene worktree restore policy %q", o.WorktreePolicy)
	}
	if o.OriginalHead != "" && !validObjectID(o.OriginalHead) {
		return fmt.Errorf("invalid graphene original HEAD %q", o.OriginalHead)
	}
	if o.WorktreeFingerprint != "" {
		if len(o.WorktreeFingerprint) != 64 {
			return fmt.Errorf("invalid graphene worktree fingerprint %q", o.WorktreeFingerprint)
		}
		if _, err := hex.DecodeString(o.WorktreeFingerprint); err != nil {
			return fmt.Errorf("invalid graphene worktree fingerprint %q", o.WorktreeFingerprint)
		}
	}
	if o.WorktreeBoundaryCrossed && o.WorktreeFingerprint == "" && o.WorktreePolicy != "" && o.WorktreePolicy != worktreeRestoreNone {
		return fmt.Errorf("graphene operation crossed its worktree boundary without a fingerprint")
	}
	backupPrefix := "refs/graphene/journal/" + o.ID + "/original/"
	backups := map[string]bool{}
	for ref, snapshot := range o.Refs {
		if !validJournalRef(ref) || !strings.HasPrefix(ref, "refs/heads/") {
			return fmt.Errorf("operation owns invalid branch ref %q", ref)
		}
		if err := snapshot.Original.validate(ref); err != nil {
			return err
		}
		if err := snapshot.Expected.validate(ref); err != nil {
			return err
		}
		if snapshot.Backup != "" {
			if !snapshot.Original.Exists {
				return fmt.Errorf("operation ref %q has a backup despite being originally absent", ref)
			}
			if !validJournalRef(snapshot.Backup) || !strings.HasPrefix(snapshot.Backup, backupPrefix) || strings.TrimPrefix(snapshot.Backup, backupPrefix) == "" {
				return fmt.Errorf("operation ref %q has unsafe backup %q", ref, snapshot.Backup)
			}
			if backups[snapshot.Backup] {
				return fmt.Errorf("operation backup %q is used more than once", snapshot.Backup)
			}
			backups[snapshot.Backup] = true
		}
	}
	for ref, value := range o.ValidationRefs {
		if !validJournalRef(ref) || !strings.HasPrefix(ref, "refs/heads/") {
			return fmt.Errorf("operation validates invalid branch ref %q", ref)
		}
		if _, owned := o.Refs[ref]; owned {
			return fmt.Errorf("operation both owns and validates branch ref %q", ref)
		}
		if err := value.validate(ref); err != nil {
			return err
		}
	}
	if o.IndexArtifact != "" && (filepath.Base(o.IndexArtifact) != o.IndexArtifact || o.IndexArtifact == "." || o.IndexArtifact == "..") {
		return fmt.Errorf("unsafe operation index artifact %q", o.IndexArtifact)
	}
	if o.IndexMode > 0o777 {
		return fmt.Errorf("unsafe operation index mode %#o", o.IndexMode)
	}
	if o.WorktreeArtifact != "" && (filepath.Base(o.WorktreeArtifact) != o.WorktreeArtifact || o.WorktreeArtifact == "." || o.WorktreeArtifact == "..") {
		return fmt.Errorf("unsafe operation worktree artifact %q", o.WorktreeArtifact)
	}
	if o.RawWorktreeArtifact != "" && (filepath.Base(o.RawWorktreeArtifact) != o.RawWorktreeArtifact || !strings.HasPrefix(o.RawWorktreeArtifact, "worktree-raw-") || !strings.HasSuffix(o.RawWorktreeArtifact, ".tar")) {
		return fmt.Errorf("unsafe operation raw worktree artifact %q", o.RawWorktreeArtifact)
	}
	if o.StagedArtifact != "" && (filepath.Base(o.StagedArtifact) != o.StagedArtifact || o.StagedArtifact == "." || o.StagedArtifact == "..") {
		return fmt.Errorf("unsafe operation staged artifact %q", o.StagedArtifact)
	}
	if o.SharedIndexArtifact != "" && (filepath.Base(o.SharedIndexArtifact) != o.SharedIndexArtifact || o.SharedIndexArtifact == "." || o.SharedIndexArtifact == "..") {
		return fmt.Errorf("unsafe operation shared-index artifact %q", o.SharedIndexArtifact)
	}
	if o.SharedIndexPath != "" && (filepath.Base(o.SharedIndexPath) != o.SharedIndexPath || !strings.HasPrefix(o.SharedIndexPath, "sharedindex.")) {
		return fmt.Errorf("unsafe operation shared-index path %q", o.SharedIndexPath)
	}
	if (o.SharedIndexArtifact == "") != (o.SharedIndexPath == "") {
		return fmt.Errorf("operation shared-index recovery metadata is incomplete")
	}
	if o.SharedIndexMode > 0o777 {
		return fmt.Errorf("unsafe operation shared-index mode %#o", o.SharedIndexMode)
	}
	if o.UntrackedArtifact != "" && (filepath.Base(o.UntrackedArtifact) != o.UntrackedArtifact || o.UntrackedArtifact == "." || o.UntrackedArtifact == "..") {
		return fmt.Errorf("unsafe operation untracked artifact %q", o.UntrackedArtifact)
	}
	for artifact, digest := range o.ArtifactDigests {
		if filepath.Base(artifact) != artifact || artifact == "." || artifact == ".." {
			return fmt.Errorf("unsafe operation artifact digest path %q", artifact)
		}
		if len(digest) != 64 {
			return fmt.Errorf("operation artifact %q has invalid digest %q", artifact, digest)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return fmt.Errorf("operation artifact %q has invalid digest %q", artifact, digest)
		}
	}
	for _, artifact := range []string{o.IndexArtifact, o.StagedArtifact, o.WorktreeArtifact, o.RawWorktreeArtifact, o.SharedIndexArtifact, o.UntrackedArtifact} {
		if artifact != "" && o.ArtifactDigests[artifact] == "" {
			return fmt.Errorf("operation artifact %q is missing its digest", artifact)
		}
	}
	if o.RecoveryArtifact != "" && !safeJournalRelativePath(o.RecoveryArtifact) {
		return fmt.Errorf("unsafe operation recovery artifact %q", o.RecoveryArtifact)
	}
	if o.Active != nil {
		if o.Active.ID == "" || o.Active.Kind == "" {
			return fmt.Errorf("graphene operation has an incomplete active action")
		}
		for _, refs := range []map[string]RefValue{o.Active.RefsBefore, o.Active.RefsAfter, o.Active.RefInventory} {
			for ref, value := range refs {
				if !validJournalRef(ref) || !strings.HasPrefix(ref, "refs/heads/") {
					return fmt.Errorf("operation action owns invalid branch ref %q", ref)
				}
				if err := value.validate(ref); err != nil {
					return err
				}
			}
		}
	}
	sections := map[string]bool{}
	for _, config := range o.Configs {
		if sections[config.Section] {
			return fmt.Errorf("operation owns config section %q more than once", config.Section)
		}
		sections[config.Section] = true
		if _, err := validateBranchConfigSnapshot(config.Section, config.Original, config.Expected); err != nil {
			return err
		}
	}
	recoveryPrefix := "refs/graphene/recovery/" + o.ID + "/"
	for ref, recovery := range o.RecoveryRefs {
		if _, ok := o.Refs[ref]; !ok {
			return fmt.Errorf("operation recovery ref records unowned ref %q", ref)
		}
		if !validJournalRef(recovery) || !strings.HasPrefix(recovery, recoveryPrefix) || strings.TrimPrefix(recovery, recoveryPrefix) == "" {
			return fmt.Errorf("operation ref %q has unsafe recovery ref %q", ref, recovery)
		}
	}
	return nil
}

func safeJournalRelativePath(path string) bool {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for part := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (r RefValue) validate(ref string) error {
	if r.Exists && !validObjectID(r.OID) {
		return fmt.Errorf("operation ref %q has invalid object id %q", ref, r.OID)
	}
	if !r.Exists && r.OID != "" {
		return fmt.Errorf("operation ref %q is absent with object id %q", ref, r.OID)
	}
	return nil
}

func validObjectID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, char := range oid {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

func validJournalRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.Contains(ref, "//") {
		return false
	}
	for _, char := range ref {
		if char <= ' ' || char == 0x7f || strings.ContainsRune("~^:?*[\\", char) {
			return false
		}
	}
	for part := range strings.SplitSeq(ref, "/") {
		if part == "" || part == "." || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func safeGitArgument(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") {
		return false
	}
	for _, char := range value {
		if char == 0 || char == '\n' || char == '\r' {
			return false
		}
	}
	return true
}

func validBranchArgument(branch string) bool {
	return safeGitArgument(branch) && validJournalRef("refs/heads/"+branch)
}

func validateJournalCommitArgs(mode string, args []string) error {
	if len(args) == 0 || args[0] != "commit" {
		return fmt.Errorf("journaled %s action is not a git commit", mode)
	}
	index := 1
	if mode == "amend" {
		if index >= len(args) || args[index] != "--amend" {
			return fmt.Errorf("journaled amend is missing --amend")
		}
		index++
	}
	for index < len(args) {
		arg := args[index]
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("journaled %s commit contains a NUL argument", mode)
		}
		switch {
		case arg == "-m":
			index++
			if index >= len(args) || strings.ContainsRune(args[index], 0) {
				return fmt.Errorf("journaled %s commit has an invalid -m argument", mode)
			}
		case strings.HasPrefix(arg, "--message="):
		case arg == "--no-edit", arg == "--no-verify", arg == "--gpg-sign", arg == "--no-gpg-sign":
		case strings.HasPrefix(arg, "--gpg-sign="):
		case arg == "--edit" && mode == "squash":
		default:
			return fmt.Errorf("journaled %s commit contains unsupported argument %q", mode, arg)
		}
		index++
	}
	return nil
}

func refDrift(expected, actual map[string]RefValue) []RefDrift {
	var refs []string
	for ref := range expected {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	var drift []RefDrift
	for _, ref := range refs {
		want := expected[ref]
		got := actual[ref]
		if want != got {
			drift = append(drift, RefDrift{Ref: ref, Expected: want, Actual: got})
		}
	}
	return drift
}

func formatRefValue(value RefValue) string {
	if !value.Exists {
		return "absent"
	}
	return value.OID
}

func formatRefDrift(drift []RefDrift) string {
	var lines []string
	for _, item := range drift {
		lines = append(lines, fmt.Sprintf("  %s: expected %s, found %s", item.Ref, formatRefValue(item.Expected), formatRefValue(item.Actual)))
	}
	return strings.Join(lines, "\n")
}
