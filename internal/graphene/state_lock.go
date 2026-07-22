package graphene

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrStateLocked = errors.New("another graphene operation is already running in this repository")

type StateLock struct {
	file *os.File
}

// WithStateLock holds the repository-wide lock while fn reads, plans, mutates,
// and writes state. WriteState notices the held lock and does not reacquire it.
func (g *Git) WithStateLock(fn func() error) (err error) {
	if g.stateLock != nil && g.stateLock.held() {
		return fn()
	}
	lock, err := g.AcquireStateLock()
	if err != nil {
		return err
	}
	g.stateLock = lock
	defer func() {
		g.stateLock = nil
		if closeErr := lock.Close(); err == nil {
			err = closeErr
		}
	}()
	return fn()
}

func (g Git) AcquireStateLock() (*StateLock, error) {
	dir, err := g.GrapheneDir()
	if err != nil {
		return nil, err
	}
	if err := ensureDurableDir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create graphene state directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure graphene state directory: %w", err)
	}

	path := filepath.Join(dir, grapheneStateLockName)
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, fmt.Errorf("inspect graphene state lock: %w", statErr)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open graphene state lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure graphene state lock: %w", err)
	}
	if created {
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("sync graphene state lock: %w", err)
		}
		if err := syncDirectory(dir); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("sync graphene state directory: %w", err)
		}
	}
	if err := lockStateFile(file); err != nil {
		_ = file.Close()
		if isStateLockContention(err) {
			return nil, ErrStateLocked
		}
		return nil, fmt.Errorf("lock graphene state: %w", err)
	}
	return &StateLock{file: file}, nil
}

func (l *StateLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	if err := unlockStateFile(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("unlock graphene state: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close graphene state lock: %w", err)
	}
	return nil
}

func (l *StateLock) held() bool {
	return l != nil && l.file != nil
}
