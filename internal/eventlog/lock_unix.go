//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package eventlog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type fileLock struct {
	file *os.File
}

func acquireLock(ctx context.Context, eventsDir string, timeout time.Duration) (*fileLock, error) {
	path := filepath.Join(eventsDir, ".lock")
	// #nosec G304 -- the events directory comes from operator config and the
	// lock file name is fixed; nothing is read from it.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open lock %s: %w", path, err)
	}
	locked, err := waitLock(ctx, file, timeout)
	if err != nil {
		return nil, closeLockFile(file, err)
	}
	if !locked {
		return nil, closeLockFile(file, fmt.Errorf("%w: %s", ErrLockTimeout, path))
	}
	return &fileLock{file: file}, nil
}

func tryLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("eventlog: flock %s: %w", file.Name(), err)
	}
	return true, nil
}

func (l *fileLock) Close() error {
	if err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN); err != nil {
		return closeLockFile(l.file, fmt.Errorf("eventlog: unlock %s: %w", l.file.Name(), err))
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("eventlog: close lock %s: %w", l.file.Name(), err)
	}
	return nil
}

func closeLockFile(file *os.File, cause error) error {
	if err := file.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("eventlog: close lock %s: %w", file.Name(), err))
	}
	return cause
}
