//go:build !linux && !darwin

package graphene

import (
	"errors"
	"os"
)

var errStateLockUnsupported = errors.New("graphene state locking is supported only on Linux and macOS")

func lockStateFile(*os.File) error {
	return errStateLockUnsupported
}

func unlockStateFile(*os.File) error {
	return nil
}

func isStateLockContention(error) bool {
	return false
}
