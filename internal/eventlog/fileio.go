package eventlog

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tekmora/epitrop/internal/atomicfile"
)

// syncFile writes a file's or a directory's data to disk. An append calls it four times,
// twice for the record and twice for HEAD, and that is what an append costs on a slow disk.
// The tests replace it to make a disk slow.
var syncFile = atomicfile.Sync

func atomicReplace(dir string, name string, body []byte, mode os.FileMode) error {
	dst := filepath.Join(dir, name)
	if err := atomicfile.ReplaceWithSync(dst, body, mode, syncFile); err != nil {
		return fmt.Errorf("eventlog: %w", err)
	}
	return nil
}
