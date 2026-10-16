package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Replace atomically replaces path with body, setting permissions to mode.
// It writes to a temporary file beside path, flushes it with fsync, closes it,
// renames it to path, and flushes the parent directory.
// On any failure, the temporary file is removed and errors are joined.
func Replace(path string, body []byte, mode os.FileMode) error {
	return ReplaceWithSync(path, body, mode, (*os.File).Sync)
}

// ReplaceWithSync atomically replaces path with body and mode, using syncFn to flush
// the temporary file and parent directory to disk.
func ReplaceWithSync(path string, body []byte, mode os.FileMode, syncFn func(*os.File) error) error {
	if syncFn == nil {
		syncFn = (*os.File).Sync
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if err = writeTempFile(tmp, body, mode, syncFn); err != nil {
		return removeTemp(tmpName, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		cause := fmt.Errorf("atomicfile: rename %s to %s: %w", tmpName, path, err)
		return removeTemp(tmpName, cause)
	}
	return syncDir(dir, syncFn)
}

func writeTempFile(tmp *os.File, body []byte, mode os.FileMode, syncFn func(*os.File) error) error {
	if _, err := tmp.Write(body); err != nil {
		return closeWithCause(tmp, fmt.Errorf("atomicfile: write %s: %w", tmp.Name(), err))
	}
	if err := tmp.Chmod(mode); err != nil {
		return closeWithCause(tmp, fmt.Errorf("atomicfile: chmod %s: %w", tmp.Name(), err))
	}
	if err := syncFn(tmp); err != nil {
		return closeWithCause(tmp, fmt.Errorf("atomicfile: fsync %s: %w", tmp.Name(), err))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicfile: close %s: %w", tmp.Name(), err)
	}
	return nil
}

func syncDir(dir string, syncFn func(*os.File) error) error {
	// #nosec G304 -- dir is the parent directory of target, opened only to
	// fsync it after rename.
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("atomicfile: open dir %s: %w", dir, err)
	}
	if err = syncFn(f); err != nil {
		return closeWithCause(f, fmt.Errorf("atomicfile: fsync dir %s: %w", dir, err))
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("atomicfile: close dir %s: %w", dir, err)
	}
	return nil
}

func removeTemp(path string, cause error) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return errors.Join(cause, fmt.Errorf("atomicfile: remove temp %s: %w", path, err))
	}
	return cause
}

func closeWithCause(f *os.File, cause error) error {
	if err := f.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("atomicfile: close %s: %w", f.Name(), err))
	}
	return cause
}
