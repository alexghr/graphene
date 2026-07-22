package graphene

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

type refEdit struct {
	Ref string
	Old RefValue
	New RefValue
}

func (g Git) RunWithInput(input string, args ...string) error {
	return g.runWithInput(input, g.Stdout, args...)
}

func (g Git) runWithInput(input string, stdout io.Writer, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Dir
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = stdout
	cmd.Stderr = g.Stderr
	if err := cmd.Run(); err != nil {
		return gitCommandError(args, err, "", true)
	}
	return nil
}

func (g Git) RunWithEnv(env map[string]string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Dir
	cmd.Stdin = g.Stdin
	cmd.Stdout = g.Stdout
	cmd.Stderr = g.Stderr
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := env[key]; !replaced {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if err := cmd.Run(); err != nil {
		return gitCommandError(args, err, "", true)
	}
	return nil
}

func (g Git) RefValue(ref string) (RefValue, error) {
	exists, err := g.Output("show-ref", "--verify", "--quiet", ref)
	if err != nil {
		if isGitExit(err, 1) {
			return RefValue{}, nil
		}
		return RefValue{}, err
	}
	if exists != "" {
		return RefValue{}, fmt.Errorf("unexpected output checking ref %q: %q", ref, exists)
	}
	oid, err := g.Output("rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return RefValue{}, err
	}
	if oid == "" {
		return RefValue{}, nil
	}
	return RefValue{Exists: true, OID: oid}, nil
}

func (g Git) RefValues(refs []string) (map[string]RefValue, error) {
	values := make(map[string]RefValue, len(refs))
	for _, ref := range refs {
		value, err := g.RefValue(ref)
		if err != nil {
			return nil, err
		}
		values[ref] = value
	}
	return values, nil
}

func (a *App) addOperationObservedBranches(operation *OperationJournal, branches []string, required bool) error {
	seen := map[string]bool{}
	for _, branch := range branches {
		if branch == "" || seen[branch] {
			continue
		}
		seen[branch] = true
		if !validBranchArgument(branch) {
			if required {
				return fmt.Errorf("cannot journal invalid dependency branch %q", branch)
			}
			continue
		}
		ref := "refs/heads/" + branch
		value, err := a.git.RefValue(ref)
		if err != nil {
			return err
		}
		if !value.Exists {
			if required {
				return fmt.Errorf("dependency branch %q no longer exists", branch)
			}
			continue
		}
		if snapshot, ok := operation.Refs[ref]; ok {
			if snapshot.Expected != value {
				return fmt.Errorf("dependency branch %q moved from %s to %s", branch, formatRefValue(snapshot.Expected), formatRefValue(value))
			}
			continue
		}
		operation.Refs[ref] = JournalRef{Original: value, Expected: value}
	}
	return nil
}

func (a *App) snapshotOperationValidationRefs(operation *OperationJournal, stackSets ...[]Stack) error {
	refs := map[string]bool{}
	for _, stacks := range stackSets {
		for _, stack := range stacks {
			refs["refs/heads/"+stack.Base] = true
			for _, branch := range stack.Branches {
				refs["refs/heads/"+branch] = true
			}
		}
	}
	operation.ValidationRefs = map[string]RefValue{}
	names := make([]string, 0, len(refs))
	for ref := range refs {
		if _, owned := operation.Refs[ref]; !owned {
			names = append(names, ref)
		}
	}
	sort.Strings(names)
	for _, ref := range names {
		value, err := a.git.RefValue(ref)
		if err != nil {
			return err
		}
		operation.ValidationRefs[ref] = value
	}
	operation.ValidationRefsComplete = true
	return nil
}

func (g Git) LocalHeadRefValues() (map[string]RefValue, error) {
	out, err := g.Output("for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads")
	if err != nil {
		return nil, err
	}
	values := map[string]RefValue{}
	if out == "" {
		return values, nil
	}
	for line := range strings.SplitSeq(out, "\n") {
		ref, oid, ok := strings.Cut(line, "\x00")
		if !ok || ref == "" || oid == "" {
			return nil, fmt.Errorf("parse local branch ref %q", line)
		}
		values[ref] = RefValue{Exists: true, OID: oid}
	}
	return values, nil
}

func (g Git) UpdateRefs(edits []refEdit) error {
	if len(edits) == 0 {
		return nil
	}
	var input bytes.Buffer
	input.WriteString("start\n")
	for _, edit := range edits {
		if !validJournalRef(edit.Ref) {
			return fmt.Errorf("invalid ref name %q", edit.Ref)
		}
		if err := edit.Old.validate(edit.Ref); err != nil {
			return err
		}
		if err := edit.New.validate(edit.Ref); err != nil {
			return err
		}
		switch {
		case !edit.Old.Exists && edit.New.Exists:
			fmt.Fprintf(&input, "create %s %s\n", edit.Ref, edit.New.OID)
		case edit.Old.Exists && !edit.New.Exists:
			fmt.Fprintf(&input, "delete %s %s\n", edit.Ref, edit.Old.OID)
		case edit.Old.Exists && edit.New.Exists:
			fmt.Fprintf(&input, "update %s %s %s\n", edit.Ref, edit.New.OID, edit.Old.OID)
		}
	}
	input.WriteString("prepare\ncommit\n")
	return g.runWithInput(input.String(), io.Discard, "update-ref", "--stdin")
}

func expectedOperationRefs(operation *OperationJournal) map[string]RefValue {
	expected := make(map[string]RefValue, len(operation.Refs))
	for ref, snapshot := range operation.Refs {
		expected[ref] = snapshot.Expected
	}
	return expected
}

func (g Git) OperationRefDrift(operation *OperationJournal) ([]RefDrift, map[string]RefValue, error) {
	refs := make([]string, 0, len(operation.Refs))
	for ref := range operation.Refs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	actual, err := g.RefValues(refs)
	if err != nil {
		return nil, nil, err
	}
	return refDrift(expectedOperationRefs(operation), actual), actual, nil
}

func (g Git) OperationValidationRefDrift(operation *OperationJournal) ([]RefDrift, error) {
	refs := make([]string, 0, len(operation.ValidationRefs))
	for ref := range operation.ValidationRefs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	actual, err := g.RefValues(refs)
	if err != nil {
		return nil, err
	}
	return refDrift(operation.ValidationRefs, actual), nil
}

func (g Git) OperationAbortRefDrift(operation *OperationJournal) ([]RefDrift, map[string]RefValue, error) {
	refs := make([]string, 0, len(operation.Refs))
	for ref := range operation.Refs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	actual, err := g.RefValues(refs)
	if err != nil {
		return nil, nil, err
	}
	var drift []RefDrift
	for _, ref := range refs {
		got := actual[ref]
		snapshot := operation.Refs[ref]
		if got == snapshot.Expected || got == snapshot.Original {
			continue
		}
		if operation.Active != nil {
			if before, ok := operation.Active.RefsBefore[ref]; ok && got == before {
				continue
			}
			if after, ok := operation.Active.RefsAfter[ref]; ok && got == after {
				continue
			}
		}
		drift = append(drift, RefDrift{Ref: ref, Expected: snapshot.Expected, Actual: got})
	}
	return drift, actual, nil
}

func (g Git) InstallOperationBackups(operation *OperationJournal) error {
	if err := prepareOperationBackups(operation); err != nil {
		return err
	}
	refs := make([]string, 0, len(operation.Refs))
	for ref := range operation.Refs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	format, err := g.Output("rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	zeroLength := 40
	if format == "sha256" {
		zeroLength = 64
	}
	zero := strings.Repeat("0", zeroLength)
	var input bytes.Buffer
	input.WriteString("start\n")
	for _, ref := range refs {
		snapshot := operation.Refs[ref]
		want := zero
		if snapshot.Original.Exists {
			want = snapshot.Original.OID
		}
		fmt.Fprintf(&input, "verify %s %s\n", ref, want)
		if !snapshot.Original.Exists {
			continue
		}
		backup := snapshot.Backup
		actual, err := g.RefValue(backup)
		if err != nil {
			return err
		}
		if actual.Exists {
			if actual.OID != snapshot.Original.OID {
				return fmt.Errorf("operation backup %s moved from %s to %s", backup, snapshot.Original.OID, actual.OID)
			}
			fmt.Fprintf(&input, "verify %s %s\n", backup, actual.OID)
		} else {
			fmt.Fprintf(&input, "create %s %s\n", backup, snapshot.Original.OID)
		}
	}
	validationRefs := make([]string, 0, len(operation.ValidationRefs))
	for ref := range operation.ValidationRefs {
		validationRefs = append(validationRefs, ref)
	}
	sort.Strings(validationRefs)
	for _, ref := range validationRefs {
		value := operation.ValidationRefs[ref]
		want := zero
		if value.Exists {
			want = value.OID
		}
		fmt.Fprintf(&input, "verify %s %s\n", ref, want)
	}
	input.WriteString("prepare\ncommit\n")
	return g.runWithInput(input.String(), io.Discard, "update-ref", "--stdin")
}

func prepareOperationBackups(operation *OperationJournal) error {
	refs := make([]string, 0, len(operation.Refs))
	for ref := range operation.Refs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for index, ref := range refs {
		snapshot := operation.Refs[ref]
		if !snapshot.Original.Exists {
			if snapshot.Backup != "" {
				return fmt.Errorf("operation ref %q has a backup despite being originally absent", ref)
			}
			continue
		}
		backup := fmt.Sprintf("refs/graphene/journal/%s/original/%04d", operation.ID, index)
		if snapshot.Backup != "" && snapshot.Backup != backup {
			return fmt.Errorf("operation ref %q has backup %q, want %q", ref, snapshot.Backup, backup)
		}
		snapshot.Backup = backup
		operation.Refs[ref] = snapshot
	}
	return nil
}

func (g Git) PreserveUnexpectedRefs(operation *OperationJournal, actual map[string]RefValue) error {
	refs := make([]string, 0, len(operation.Refs))
	for ref := range operation.Refs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	if operation.RecoveryRefs == nil {
		operation.RecoveryRefs = map[string]string{}
	}

	var edits []refEdit
	for index, ref := range refs {
		value := actual[ref]
		if !value.Exists || value == operation.Refs[ref].Expected {
			continue
		}
		recovery := fmt.Sprintf("refs/graphene/recovery/%s/%04d", operation.ID, index)
		existing, err := g.RefValue(recovery)
		if err != nil {
			return err
		}
		if existing.Exists && existing.OID != value.OID {
			return fmt.Errorf("recovery ref %s already points to %s, want %s", recovery, existing.OID, value.OID)
		}
		if !existing.Exists {
			edits = append(edits, refEdit{Ref: recovery, New: value})
		}
		operation.RecoveryRefs[ref] = recovery
	}
	return g.UpdateRefs(edits)
}

func (g Git) RestoreOperationRefs(operation *OperationJournal, actual map[string]RefValue) error {
	refs := make([]string, 0, len(operation.Refs))
	for ref := range operation.Refs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	var edits []refEdit
	for _, ref := range refs {
		old := actual[ref]
		newValue := operation.Refs[ref].Original
		if old == newValue {
			continue
		}
		edits = append(edits, refEdit{Ref: ref, Old: old, New: newValue})
	}
	return g.UpdateRefs(edits)
}

func (g Git) RemoveOperationBackups(operation *OperationJournal) error {
	var edits []refEdit
	for _, snapshot := range operation.Refs {
		if snapshot.Backup == "" {
			continue
		}
		actual, err := g.RefValue(snapshot.Backup)
		if err != nil {
			return err
		}
		if actual.Exists {
			if !snapshot.Original.Exists || actual.OID != snapshot.Original.OID {
				return fmt.Errorf("operation backup %s moved from %s to %s", snapshot.Backup, formatRefValue(snapshot.Original), formatRefValue(actual))
			}
			edits = append(edits, refEdit{Ref: snapshot.Backup, Old: actual})
		}
	}
	return g.UpdateRefs(edits)
}
