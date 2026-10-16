//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package eventlog

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

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
