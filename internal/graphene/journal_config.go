package graphene

import (
	"fmt"
	"regexp"
	"strings"
)

func (g Git) BranchConfig(branch string) ([]ConfigEntry, error) {
	if !validBranchArgument(branch) {
		return nil, fmt.Errorf("invalid branch config name %q", branch)
	}
	pattern := "^" + regexp.QuoteMeta("branch."+branch+".")
	out, err := g.Output("config", "--local", "--null", "--get-regexp", pattern)
	if err != nil {
		if isGitExit(err, 1) {
			return nil, nil
		}
		return nil, err
	}
	var entries []ConfigEntry
	for record := range strings.SplitSeq(out, "\x00") {
		if record == "" {
			continue
		}
		key, value, ok := strings.Cut(record, "\n")
		if !ok || key == "" {
			return nil, fmt.Errorf("parse branch config record %q", record)
		}
		entries = append(entries, ConfigEntry{Key: key, Value: value})
	}
	return entries, nil
}

func equalConfigEntries(left, right []ConfigEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (g Git) RestoreBranchConfig(section string, original, actual []ConfigEntry) error {
	branch, err := validateBranchConfigSnapshot(section, original, actual)
	if err != nil {
		return err
	}
	// Git rewrites the local config through its lockfile, so whole-section
	// removal gives recovery one atomic boundary before ordered replay.
	if len(actual) > 0 {
		if err := g.RemoveBranchConfig(branch); err != nil {
			return err
		}
	}
	for _, entry := range original {
		if err := g.OutputErr("config", "--local", "--add", entry.Key, entry.Value); err != nil {
			return err
		}
	}
	return nil
}

func validateBranchConfigSnapshot(section string, snapshots ...[]ConfigEntry) (string, error) {
	branch, ok := strings.CutPrefix(section, "branch.")
	if !ok || !validBranchArgument(branch) {
		return "", fmt.Errorf("invalid branch config section %q", section)
	}
	prefix := section + "."
	for _, entries := range snapshots {
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Key, prefix) {
				return "", fmt.Errorf("branch config section %q contains key %q outside its section", section, entry.Key)
			}
		}
	}
	return branch, nil
}

func (g Git) RemoveBranchConfig(branch string) error {
	if !validBranchArgument(branch) {
		return fmt.Errorf("invalid branch config name %q", branch)
	}
	err := g.OutputErr("config", "--local", "--remove-section", "branch."+branch)
	if err == nil || isGitExit(err, 1) {
		return nil
	}
	return err
}

type ConfigDrift struct {
	Section  string
	Expected []ConfigEntry
	Actual   []ConfigEntry
}

func (g Git) OperationConfigDrift(operation *OperationJournal, aborting bool) ([]ConfigDrift, error) {
	var drift []ConfigDrift
	for _, snapshot := range operation.Configs {
		branch, ok := strings.CutPrefix(snapshot.Section, "branch.")
		if !ok || branch == "" {
			return nil, fmt.Errorf("invalid branch config journal section %q", snapshot.Section)
		}
		actual, err := g.BranchConfig(branch)
		if err != nil {
			return nil, err
		}
		if equalConfigEntries(snapshot.Expected, actual) {
			continue
		}
		if aborting {
			if equalConfigEntries(snapshot.Original, actual) || configEntriesPrefix(actual, snapshot.Original) {
				continue
			}
		}
		drift = append(drift, ConfigDrift{
			Section:  snapshot.Section,
			Expected: append([]ConfigEntry(nil), snapshot.Expected...),
			Actual:   actual,
		})
	}
	return drift, nil
}

func formatConfigDrift(drift []ConfigDrift) string {
	var lines []string
	for _, item := range drift {
		lines = append(lines, fmt.Sprintf("  %s: expected %d entrie(s), found %d", item.Section, len(item.Expected), len(item.Actual)))
	}
	return strings.Join(lines, "\n")
}
