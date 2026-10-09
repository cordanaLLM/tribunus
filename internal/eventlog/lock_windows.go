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

func waitLock(ctx context.Context, file *os.File, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for attempts := 0; attempts < lockAttempts(timeout); attempts++ {
		ok, err := tryLock(file)
		if ok || err != nil {
			return ok, err
		}
		if !sleepUntilNextTry(ctx, deadline) {
			return false, nil
		}
	}
	return false, nil
}

func lockAttempts(timeout time.Duration) int {
	if timeout <= 0 {
		return 1
	}
	attempts := int(timeout/(10*time.Millisecond)) + 2
	if attempts < 2 {
		return 2
	}
	return attempts
}

func sleepUntilNextTry(ctx context.Context, deadline time.Time) bool {
	wait := 10 * time.Millisecond
	if remaining := time.Until(deadline); remaining <= 0 {
		return false
	} else if remaining < wait {
		wait = remaining
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
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
