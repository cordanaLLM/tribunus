//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows

package eventlog

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcquireLock(t *testing.T) {
	t.Run("missing directory fails with open lock", testAcquireLockMissingDir)
	t.Run("timeout while held and reacquire after close", testAcquireLockTimeoutAndReacquire)
}

func testAcquireLockMissingDir(t *testing.T) {
	ctx := context.Background()
	missingDir := filepath.Join(t.TempDir(), "missing-dir")
	missingLock, err := acquireLock(ctx, missingDir, 50*time.Millisecond)
	if err == nil {
		if closeErr := missingLock.Close(); closeErr != nil {
			t.Errorf("missingLock.Close() = %v", closeErr)
		}
		t.Fatal("acquireLock(missing dir) succeeded, want error")
	}
	if !strings.Contains(err.Error(), "open lock") {
		t.Fatalf("acquireLock(missing dir) = %v, want error containing %q", err, "open lock")
	}
}

func testAcquireLockTimeoutAndReacquire(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	lock1, err := acquireLock(ctx, dir, time.Second)
	if err != nil {
		t.Fatalf("acquireLock(1) = %v", err)
	}
	lock2, err := acquireLock(ctx, dir, 20*time.Millisecond)
	if !errors.Is(err, ErrLockTimeout) {
		if err == nil {
			if closeErr := lock2.Close(); closeErr != nil {
				t.Errorf("lock2.Close() = %v", closeErr)
			}
		}
		if closeErr := lock1.Close(); closeErr != nil {
			t.Errorf("lock1.Close() = %v", closeErr)
		}
		t.Fatalf("acquireLock(2) while held = %v, want ErrLockTimeout", err)
	}
	if err := lock1.Close(); err != nil {
		t.Fatalf("lock1.Close() = %v", err)
	}

	lock3, err := acquireLock(ctx, dir, time.Second)
	if err != nil {
		t.Fatalf("acquireLock(3) after close = %v", err)
	}
	if err := lock3.Close(); err != nil {
		t.Fatalf("lock3.Close() = %v", err)
	}
}
