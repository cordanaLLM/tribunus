//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows

package eventlog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// lockRetryDelay is how long acquireLock waits between two tries of a held lock.
const lockRetryDelay = 10 * time.Millisecond

// fileLock is the exclusive lock on the events directory: flock(2) on Unix, LockFileEx on
// Windows, both through gofrs/flock.
type fileLock struct {
	lock *flock.Flock
}

func acquireLock(ctx context.Context, eventsDir string, timeout time.Duration) (*fileLock, error) {
	path := filepath.Join(eventsDir, ".lock")
	lock := flock.New(path, flock.SetFlag(os.O_CREATE|os.O_RDWR))
	locked, err := tryLock(ctx, lock, timeout)
	if err != nil {
		return nil, fmt.Errorf("eventlog: lock %s: %w", path, err)
	}
	if !locked {
		return nil, fmt.Errorf("%w: %s", ErrLockTimeout, path)
	}
	return &fileLock{lock: lock}, nil
}

// tryLock tries once when timeout is not positive and otherwise until timeout or ctx ends,
// which both report as not locked.
func tryLock(ctx context.Context, lock *flock.Flock, timeout time.Duration) (bool, error) {
	if timeout <= 0 {
		return lock.TryLock()
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	locked, err := lock.TryLockContext(waitCtx, lockRetryDelay)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false, nil
	}
	return locked, err
}

func (l *fileLock) Close() error {
	if err := l.lock.Unlock(); err != nil {
		return fmt.Errorf("eventlog: unlock %s: %w", l.lock.Path(), err)
	}
	return nil
}
