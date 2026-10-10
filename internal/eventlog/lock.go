//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows

package eventlog

import (
	"context"
	"os"
	"time"
)

// waitLock, lockAttempts and sleepUntilNextTry are the platform-neutral retry loop around
// tryLock, which lock_unix.go (flock) and lock_windows.go (LockFileEx) each provide.

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
