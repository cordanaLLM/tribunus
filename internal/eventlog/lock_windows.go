//go:build windows

package eventlog

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

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
