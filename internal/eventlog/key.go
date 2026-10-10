package eventlog

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/golusoris/golusoris/core/clock"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

const maxGitRootWalk = 128

func NewSignerFromKeyFile(ctx context.Context, path string, eventLogDir string, clk clock.Clock) (*corereceipt.Signer, error) {
	if err := readyContext(ctx, "load signing key"); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("eventlog: signing key path is required")
	}
	if clk == nil {
		return nil, errors.New("eventlog: signing key clock is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("eventlog: key abs %s: %w", path, err)
	}
	if err = refuseKeyInRepo(abs, eventLogDir); err != nil {
		return nil, err
	}
	seed, err := readSeedFile(abs)
	if err != nil {
		return nil, err
	}
	return corereceipt.NewSignerFromSeed(seed, clk)
}

func readSeedFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("eventlog: stat signing key %s: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("eventlog: signing key %s mode %03o allows group or other access", path, info.Mode().Perm())
	}
	// #nosec G304 -- the signing key path comes from operator config and has
	// passed the repository-tree and file-mode checks above.
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("eventlog: read signing key %s: %w", path, err)
	}
	text := strings.TrimSpace(string(body))
	if len(text) != 64 {
		return nil, fmt.Errorf("eventlog: signing key %s must be 64 hex characters", path)
	}
	seed, err := hex.DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("eventlog: decode signing key %s: %w", path, err)
	}
	return seed, nil
}

func refuseKeyInRepo(absKey string, eventLogDir string) error {
	root, ok, err := findGitRoot(eventLogDir)
	if err != nil || !ok {
		return err
	}
	rel, err := filepath.Rel(root, absKey)
	if err != nil {
		return fmt.Errorf("eventlog: compare key with git root: %w", err)
	}
	if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
		return fmt.Errorf("eventlog: signing key %s is inside git working tree %s", absKey, root)
	}
	return nil
}

func findGitRoot(start string) (string, bool, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", false, fmt.Errorf("eventlog: event log abs %s: %w", start, err)
	}
	for steps := 0; steps < maxGitRootWalk; steps++ {
		if _, err = os.Stat(filepath.Join(abs, ".git")); err == nil {
			return abs, true, nil
		} else if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("eventlog: stat %s: %w", filepath.Join(abs, ".git"), err)
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", false, nil
		}
		abs = parent
	}
	return "", false, fmt.Errorf("eventlog: git root walk exceeded %d levels", maxGitRootWalk)
}
