package eventlog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func atomicReplace(dir string, name string, body []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("eventlog: create temp %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if err = writeTempFile(tmp, body, mode); err != nil {
		return removeTemp(tmpName, err)
	}
	dst := filepath.Join(dir, name)
	if err = os.Rename(tmpName, dst); err != nil {
		cause := fmt.Errorf("eventlog: rename %s to %s: %w", tmpName, dst, err)
		return removeTemp(tmpName, cause)
	}
	return syncDir(dir)
}

func writeTempFile(tmp *os.File, body []byte, mode os.FileMode) error {
	if _, err := tmp.Write(body); err != nil {
		return closeWithCause(tmp, fmt.Errorf("eventlog: write %s: %w", tmp.Name(), err))
	}
	if err := tmp.Chmod(mode); err != nil {
		return closeWithCause(tmp, fmt.Errorf("eventlog: chmod %s: %w", tmp.Name(), err))
	}
	if err := tmp.Sync(); err != nil {
		return closeWithCause(tmp, fmt.Errorf("eventlog: fsync %s: %w", tmp.Name(), err))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("eventlog: close %s: %w", tmp.Name(), err)
	}
	return nil
}

func syncDir(dir string) error {
	// #nosec G304 -- dir is the configured events directory, opened only to
	// fsync it after a rename.
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("eventlog: open dir %s: %w", dir, err)
	}
	if err = f.Sync(); err != nil {
		return closeWithCause(f, fmt.Errorf("eventlog: fsync dir %s: %w", dir, err))
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("eventlog: close dir %s: %w", dir, err)
	}
	return nil
}

func removeTemp(path string, cause error) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return errors.Join(cause, fmt.Errorf("eventlog: remove temp %s: %w", path, err))
	}
	return cause
}

func closeWithCause(f *os.File, cause error) error {
	if err := f.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("eventlog: close %s: %w", f.Name(), err))
	}
	return cause
}
