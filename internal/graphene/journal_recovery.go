package graphene

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type configRecoveryFile struct {
	Version   int                    `json:"version"`
	Operation string                 `json:"operation"`
	Configs   []configRecoveryRecord `json:"configs"`
}

type configRecoveryRecord struct {
	Name    string        `json:"section"`
	Entries []ConfigEntry `json:"entries"`
}

func (a *App) preserveUnexpectedConfigs(operation *OperationJournal, drift []ConfigDrift) error {
	recordsByName := map[string]configRecoveryRecord{}
	dir, err := a.git.GrapheneDir()
	if err != nil {
		return err
	}
	relative := filepath.Join("recovery", operation.ID, "config.json")
	path := filepath.Join(dir, relative)
	if existing, readErr := os.ReadFile(path); readErr == nil {
		file, err := decodeConfigRecoveryFile(existing, operation.ID)
		if err != nil {
			return fmt.Errorf("read config recovery artifact: %w", err)
		}
		for _, record := range file.Configs {
			recordsByName[record.Name] = record
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read config recovery artifact: %w", readErr)
	}
	for _, item := range drift {
		record := configRecoveryRecord{
			Name:    item.Section,
			Entries: append([]ConfigEntry(nil), item.Actual...),
		}
		if existing, ok := recordsByName[item.Section]; ok && !equalConfigEntries(existing.Entries, record.Entries) {
			if operation.Phase != operationRollingBack || !configEntriesSubsequence(record.Entries, existing.Entries) {
				return fmt.Errorf("config %s changed again after its value was preserved in %s", item.Section, path)
			}
			continue
		}
		recordsByName[item.Section] = record
	}
	records := make([]configRecoveryRecord, 0, len(recordsByName))
	for _, record := range recordsByName {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	file := configRecoveryFile{Version: 1, Operation: operation.ID, Configs: records}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config recovery artifact: %w", err)
	}
	data = append(data, '\n')

	if existing, readErr := os.ReadFile(path); readErr == nil {
		if bytes.Equal(existing, data) {
			operation.RecoveryArtifact = relative
			return nil
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read config recovery artifact: %w", readErr)
	}
	if err := writeAtomicFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config recovery artifact: %w", err)
	}
	operation.RecoveryArtifact = relative
	return nil
}

func configEntriesSubsequence(actual, original []ConfigEntry) bool {
	next := 0
	for _, entry := range original {
		if next < len(actual) && actual[next] == entry {
			next++
		}
	}
	return next == len(actual)
}

func decodeConfigRecoveryFile(data []byte, operationID string) (configRecoveryFile, error) {
	var file configRecoveryFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return file, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return file, err
	}
	if file.Version != 1 || file.Operation != operationID {
		return file, fmt.Errorf("recovery artifact belongs to an unsupported operation")
	}
	seen := map[string]bool{}
	for _, record := range file.Configs {
		if seen[record.Name] {
			return file, fmt.Errorf("recovery artifact contains invalid section %q", record.Name)
		}
		seen[record.Name] = true
		if _, err := validateBranchConfigSnapshot(record.Name, record.Entries); err != nil {
			return file, fmt.Errorf("recovery artifact: %w", err)
		}
	}
	return file, nil
}

func writeAtomicFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := ensureDurableDir(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".graphene-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}
