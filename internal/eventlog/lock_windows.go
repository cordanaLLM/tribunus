//go:build windows

package eventlog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

type fileLock struct {
	file *os.File
}

func acquireLock(ctx context.Context, eventsDir string, timeout time.Duration) (*fileLock, error) {
	path := filepath.Join(eventsDir, ".lock")
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
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("eventlog: lock %s: %w", file.Name(), err)
	}
	return true, nil
}

func (l *fileLock) Close() error {
	var overlapped windows.Overlapped
	err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &overlapped)
	if err != nil {
		return closeLockFile(l.file, fmt.Errorf("eventlog: unlock %s: %w", l.file.Name(), err))
	}
	if err = l.file.Close(); err != nil {
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
