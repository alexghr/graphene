package graphene

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrStateLocked = errors.New("another graphene operation is already running in this repository")

type StateLock struct {
	closeMu       sync.Mutex
	file          *os.File
	inProcessPath string
}

// The file lock coordinates processes. This set rejects a second goroutine
// before it can mistake another goroutine's lock capability for its own.
var inProcessStateLocks = struct {
	sync.Mutex
	held map[string]struct{}
}{held: make(map[string]struct{})}

// WithStateLock holds the repository-wide lock while fn reads, plans, mutates,
// and writes state. The Git value passed to fn reuses the held lock in nested
// calls and by WriteState. The capability is scoped to the callback and must
// not be retained or shared with another goroutine.
func (g Git) WithStateLock(fn func(Git) error) (err error) {
	if g.stateLock != nil && g.stateLock.held() {
		return fn(g)
	}
	lock, err := g.AcquireStateLock()
	if err != nil {
		return err
	}
	g.stateLock = lock
	defer func() {
		if closeErr := lock.Close(); err == nil {
			err = closeErr
		}
	}()
	return fn(g)
}

func (g Git) AcquireStateLock() (*StateLock, error) {
	dir, err := g.GrapheneDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, grapheneStateLockName)
	if !tryLockStatePath(path) {
		return nil, ErrStateLocked
	}
	locked := true
	defer func() {
		if locked {
			unlockStatePath(path)
		}
	}()

	if err := ensureDurableDir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create graphene state directory: %w", err)
	}

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
	locked = false
	return &StateLock{file: file, inProcessPath: path}, nil
}

func (l *StateLock) Close() error {
	if l == nil {
		return nil
	}
	l.closeMu.Lock()
	defer l.closeMu.Unlock()
	if l.file == nil {
		return nil
	}
	file := l.file
	inProcessPath := l.inProcessPath
	l.file = nil
	l.inProcessPath = ""
	defer unlockStatePath(inProcessPath)
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
	if l == nil {
		return false
	}
	l.closeMu.Lock()
	defer l.closeMu.Unlock()
	return l.file != nil
}

func tryLockStatePath(path string) bool {
	path = filepath.Clean(path)
	inProcessStateLocks.Lock()
	defer inProcessStateLocks.Unlock()
	if _, held := inProcessStateLocks.held[path]; held {
		return false
	}
	inProcessStateLocks.held[path] = struct{}{}
	return true
}

func unlockStatePath(path string) {
	path = filepath.Clean(path)
	inProcessStateLocks.Lock()
	delete(inProcessStateLocks.held, path)
	inProcessStateLocks.Unlock()
}
