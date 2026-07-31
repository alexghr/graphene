package graphene

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	stateConfigKey         = "graphene.state"
	stateFileVersion       = 1
	stateMigrationPending  = "finalize-config"
	stateMigrationPrefix   = "graphene-file-migration-v1:"
	stateMigrationSentinel = "graphene-file-v1"
	grapheneStateDirName   = "graphene"
	grapheneStateFileName  = "state.json"
	grapheneStateLockName  = "lock"
)

type stateFile struct {
	Version   int      `json:"version"`
	Stacks    []Stack  `json:"stacks"`
	Pending   *Pending `json:"pending,omitempty"`
	Migration string   `json:"migration,omitempty"`
}

func (g Git) GrapheneDir() (string, error) {
	commonDir, err := g.Output("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(commonDir) {
		return "", fmt.Errorf("git returned non-absolute common directory %q", commonDir)
	}
	return filepath.Join(filepath.Clean(commonDir), grapheneStateDirName), nil
}

func (g Git) ReadState() (State, error) {
	path, err := g.stateFilePath()
	if err != nil {
		return State{}, err
	}

	data, err := os.ReadFile(path)
	if err == nil {
		file, err := decodeStateFile(data)
		if err != nil {
			return State{}, fmt.Errorf("parse %s: %w", path, err)
		}
		return file.state(), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return State{}, fmt.Errorf("read %s: %w", path, err)
	}

	raw, found, err := g.readLegacyStateValue()
	if err != nil {
		return State{}, err
	}
	if !found {
		return State{}, nil
	}
	return decodeLegacyStateValue(raw)
}

func (g Git) WriteState(state State) error {
	return g.WithStateLock(func(locked Git) error {
		return locked.writeStateLocked(state)
	})
}

// writeStateLocked writes state while the caller holds the repository state
// lock. It exists for operations that need one lock around a read/modify/write
// sequence; ordinary callers should use WriteState.
func (g Git) writeStateLocked(state State) error {
	path, err := g.stateFilePath()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err == nil {
		current, err := decodeStateFile(data)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if current.Migration != "" {
			if err := g.writeLegacyStateValue(stateMigrationSentinel); err != nil {
				return fmt.Errorf("finalize graphene state migration: %w", err)
			}
		}
		return writeStateFileAtomic(path, newStateFile(state, ""))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	raw, found, err := g.readLegacyStateValue()
	if err != nil {
		return err
	}
	original, alreadyMarked, err := legacyMigrationSource(raw, found)
	if err != nil {
		return err
	}
	if _, err := decodeLegacyState(original); err != nil {
		return err
	}

	if !alreadyMarked {
		marker := stateMigrationPrefix + base64.RawURLEncoding.EncodeToString([]byte(original))
		if err := g.writeLegacyStateValue(marker); err != nil {
			return fmt.Errorf("begin graphene state migration: %w", err)
		}
	}
	if err := writeStateFileAtomic(path, newStateFile(state, stateMigrationPending)); err != nil {
		return err
	}
	if err := g.writeLegacyStateValue(stateMigrationSentinel); err != nil {
		return fmt.Errorf("finalize graphene state migration: %w", err)
	}
	return writeStateFileAtomic(path, newStateFile(state, ""))
}

func (g Git) stateFilePath() (string, error) {
	dir, err := g.GrapheneDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, grapheneStateFileName), nil
}

func newStateFile(state State, migration string) stateFile {
	stacks := cloneStacks(state.Stacks)
	if stacks == nil {
		stacks = []Stack{}
	}
	return stateFile{
		Version:   stateFileVersion,
		Stacks:    stacks,
		Pending:   state.Pending,
		Migration: migration,
	}
}

func (f stateFile) state() State {
	return State{
		Stacks:  f.Stacks,
		Pending: f.Pending,
	}
}

func decodeStateFile(data []byte) (stateFile, error) {
	var file stateFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return stateFile{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return stateFile{}, err
	}
	if file.Version != stateFileVersion {
		return stateFile{}, fmt.Errorf("unsupported graphene state version %d", file.Version)
	}
	if file.Stacks == nil {
		return stateFile{}, fmt.Errorf("graphene state is missing stacks")
	}
	if file.Migration != "" && file.Migration != stateMigrationPending {
		return stateFile{}, fmt.Errorf("unsupported graphene state migration %q", file.Migration)
	}
	return file, nil
}

func decodeLegacyStateValue(raw string) (State, error) {
	switch {
	case raw == stateMigrationSentinel:
		return State{}, fmt.Errorf("%s indicates file-backed state, but %s is missing", stateConfigKey, grapheneStateFileName)
	case strings.HasPrefix(raw, stateMigrationPrefix):
		encoded := strings.TrimPrefix(raw, stateMigrationPrefix)
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return State{}, fmt.Errorf("parse %s migration marker: %w", stateConfigKey, err)
		}
		return decodeLegacyState(string(decoded))
	default:
		return decodeLegacyState(raw)
	}
}

func decodeLegacyState(raw string) (State, error) {
	if raw == "" {
		return State{}, nil
	}
	var state State
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return State{}, fmt.Errorf("parse %s: %w", stateConfigKey, err)
	}
	return state, nil
}

func legacyMigrationSource(raw string, found bool) (original string, alreadyMarked bool, err error) {
	if !found {
		return "{}", false, nil
	}
	if raw == stateMigrationSentinel {
		return "", false, fmt.Errorf("%s indicates file-backed state, but %s is missing", stateConfigKey, grapheneStateFileName)
	}
	if !strings.HasPrefix(raw, stateMigrationPrefix) {
		return raw, false, nil
	}

	decoded, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, stateMigrationPrefix))
	if decodeErr != nil {
		return "", false, fmt.Errorf("parse %s migration marker: %w", stateConfigKey, decodeErr)
	}
	return string(decoded), true, nil
}

func (g Git) readLegacyStateValue() (string, bool, error) {
	out, err := g.Output("config", "--local", "--null", "--get-all", stateConfigKey)
	if err != nil {
		if isGitExit(err, 1) {
			return "", false, nil
		}
		return "", false, err
	}
	values := strings.Split(out, "\x00")
	if len(values) > 0 && values[len(values)-1] == "" {
		values = values[:len(values)-1]
	}
	if len(values) != 1 {
		return "", false, fmt.Errorf("expected one %s value, found %d", stateConfigKey, len(values))
	}
	return values[0], true, nil
}

func (g Git) writeLegacyStateValue(value string) error {
	return g.OutputErr("config", "--local", "--replace-all", stateConfigKey, value)
}

func writeStateFileAtomic(path string, file stateFile) error {
	dir := filepath.Dir(path)
	if err := ensureDurableDir(dir, 0o700); err != nil {
		return fmt.Errorf("create graphene state directory: %w", err)
	}

	data, err := json.Marshal(file)
	if err != nil {
		return fmt.Errorf("encode graphene state: %w", err)
	}
	data = append(data, '\n')

	temp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary graphene state: %w", err)
	}
	tempName := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempName)
		}
	}()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure temporary graphene state: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary graphene state: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary graphene state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary graphene state: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("install graphene state: %w", err)
	}
	removeTemp = false

	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync graphene state directory: %w", err)
	}
	return nil
}

func ensureDurableDir(dir string, mode os.FileMode) error {
	dir = filepath.Clean(dir)
	info, err := os.Lstat(dir)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		return os.Chmod(dir, mode)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect directory %s: %w", dir, err)
	}

	missing := []string{dir}
	for current := filepath.Dir(dir); ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect directory %s: %w", current, err)
		}
		missing = append(missing, current)
		if parent := filepath.Dir(current); parent == current {
			return fmt.Errorf("cannot find existing parent for %s", dir)
		}
	}

	if err := os.MkdirAll(dir, mode); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	for _, path := range slices.Backward(missing) {
		if err := os.Chmod(path, mode); err != nil {
			return fmt.Errorf("secure directory %s: %w", path, err)
		}
		if err := syncDirectory(path); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(dir string) error {
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}
