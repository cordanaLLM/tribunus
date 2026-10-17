package eventlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

const (
	// BaseCommand signs the base of a compacted log. It differs from AppendCommand and
	// CheckpointCommand so that no other receipt passes as one over a base.
	BaseCommand = "epitrop event-log base"
	// BaseName names the directory of the base under checkpoints/, where each base is
	// <seq>.json. It is reserved: no checkpoint or fold takes it, and RecordInGit commits
	// the base with the checkpoints.
	BaseName = "base"
	// maxBaseEntries bounds the directory entries a read of the base looks at.
	maxBaseEntries = 16
	// maxFolds bounds the folds of one base, and with it the bytes a base may hold.
	maxFolds = 8
)

// Fold is one reducer whose state a base holds, under the name its replays give.
type Fold struct {
	// Name is the key of the fold's state in the base, as ReplayFold and ReplayCheckpointed
	// name it: lowercase letters only.
	Name string
	// Reduce is the reducer the fold's state is built with.
	Reduce Reducer
	// Floor returns the highest seq the fold can lose to compaction, given its state at the
	// head of the log; nil means every seq. A reader that keeps, beside the state, something
	// it learns from records past a seq (the supervisor's event triggers) bounds the base so.
	Floor func(State) uint64
}

// Base is the state of every fold after record Seq, whose hash is Hash. A compacted log
// holds no record at or below Seq; its replays start here.
type Base struct {
	Seq     uint64                     `json:"seq"`
	Hash    string                     `json:"hash"`
	States  map[string]json.RawMessage `json:"states"`
	Receipt *corereceipt.Receipt       `json:"receipt"`
}

type unsignedBase struct {
	Seq    uint64                     `json:"seq"`
	Hash   string                     `json:"hash"`
	States map[string]json.RawMessage `json:"states"`
}

// maxBaseBytes bounds a base file: one record's limit per fold.
func maxBaseBytes(limits Limits) int {
	return maxFolds * limits.MaxRecordBytes
}

// baseDir is the directory of the base of the log whose records lie in eventsDir.
func baseDir(eventsDir string) string {
	return filepath.Join(filepath.Dir(eventsDir), "checkpoints", BaseName)
}

// newestBase returns the highest seq of the base files in dir, 0 without one. Older files
// are left by a compaction that stopped before it removed them. Any other file but a dot
// file fails with its name, since a base that cannot be read must not be passed over.
func newestBase(dir string) (uint64, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("eventlog: read %s: %w", dir, err)
	}
	if len(entries) > maxBaseEntries {
		return 0, fmt.Errorf("eventlog: %s holds %d entries, more than %d", dir, len(entries), maxBaseEntries)
	}
	var newest uint64
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		seq, seqErr := seqFromName(name)
		if !isEventRecord(name) || seqErr != nil || seq == 0 || !entry.Type().IsRegular() {
			return 0, fmt.Errorf("eventlog: %s: unexpected entry %s", dir, name)
		}
		newest = max(newest, seq)
	}
	return newest, nil
}

// readBase returns the newest base of the log in eventsDir and whether it has one. A base
// that is not canonical, not signed by pub under BaseCommand, or whose seq is not the one
// its file name gives fails with the file; an older base is never used in its place.
func readBase(eventsDir string, pub string, limits Limits) (Base, bool, error) {
	dir := baseDir(eventsDir)
	seq, err := newestBase(dir)
	if err != nil || seq == 0 {
		return Base{}, false, err
	}
	path := filepath.Join(dir, checkpointFileName(seq))
	data, err := readFileBounded(path, maxBaseBytes(limits))
	if err != nil {
		return Base{}, false, err
	}
	if err = verifyCanonical(path, data); err != nil {
		return Base{}, false, err
	}
	var b Base
	if err = json.Unmarshal(data, &b); err != nil {
		return Base{}, false, fmt.Errorf("eventlog: decode %s: %w", path, err)
	}
	if err = verifyBase(b, pub, path); err != nil {
		return Base{}, false, err
	}
	if b.Seq != seq {
		return Base{}, false, fmt.Errorf("eventlog: base %s holds seq %d", path, b.Seq)
	}
	return b, true, nil
}

func verifyBase(b Base, pub string, path string) error {
	if b.Seq == 0 || len(b.Hash) != len(zeroHash) {
		return fmt.Errorf("eventlog: base %s: seq and hash are required", path)
	}
	if b.Receipt == nil {
		return fmt.Errorf("eventlog: base %s: missing receipt", path)
	}
	if b.Receipt.Command != BaseCommand {
		return fmt.Errorf("eventlog: base %s: receipt command %q, want %q", path, b.Receipt.Command, BaseCommand)
	}
	if err := ensureExpectedPublicKey(b.Receipt.PublicKey, pub, path); err != nil {
		return err
	}
	unsigned, err := canonicalJSON(unsignedBase{Seq: b.Seq, Hash: b.Hash, States: b.States})
	if err != nil {
		return err
	}
	if err = corereceipt.VerifyOutput(b.Receipt, unsigned); err != nil {
		return fmt.Errorf("eventlog: base %s: verify receipt: %w", path, err)
	}
	return nil
}

// baseSeq returns the seq of the log's base, 0 without one.
func (w *Writer) baseSeq() (uint64, error) {
	b, ok, err := readBase(w.eventsDir, w.signer.PublicKey(), w.limits)
	if err != nil || !ok {
		return 0, err
	}
	return b.Seq, nil
}

// writeBase signs the states of the folds after record seq and writes them as the log's
// newest base. The base it replaces stays until removeOldBases.
func (w *Writer) writeBase(seq uint64, hash string, states map[string]json.RawMessage) error {
	unsigned, err := canonicalJSON(unsignedBase{Seq: seq, Hash: hash, States: states})
	if err != nil {
		return err
	}
	r, err := w.signer.Sign(corereceipt.Run{Command: BaseCommand, Output: unsigned})
	if err != nil {
		return fmt.Errorf("eventlog: sign base seq %d: %w", seq, err)
	}
	body, err := canonicalJSON(Base{Seq: seq, Hash: hash, States: states, Receipt: r})
	if err != nil {
		return err
	}
	if len(body) > maxBaseBytes(w.limits) {
		return fmt.Errorf("eventlog: base seq %d is %d bytes, more than %d", seq, len(body), maxBaseBytes(w.limits))
	}
	dir := baseDir(w.eventsDir)
	if err = os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("eventlog: mkdir %s: %w", dir, err)
	}
	if err = atomicReplace(dir, checkpointFileName(seq), body, 0o600); err != nil {
		return err
	}
	return syncDirPath(dir)
}

// removeOldBases removes every base file below seq, the seq of the newest base.
func (w *Writer) removeOldBases(seq uint64) error {
	dir := baseDir(w.eventsDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("eventlog: read %s: %w", dir, err)
	}
	removed := false
	for _, entry := range entries {
		n, nErr := seqFromName(entry.Name())
		if !isEventRecord(entry.Name()) || nErr != nil || n >= seq {
			continue
		}
		if err = removeFile(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
		removed = true
	}
	if !removed {
		return nil
	}
	return syncDirPath(dir)
}

func checkFolds(folds []Fold) error {
	if len(folds) == 0 || len(folds) > maxFolds {
		return fmt.Errorf("eventlog: compact takes 1 to %d folds, got %d", maxFolds, len(folds))
	}
	seen := make(map[string]bool, len(folds))
	for i := 0; i < len(folds); i++ {
		f := folds[i]
		if !validCheckpointName(f.Name) || f.Reduce == nil {
			return fmt.Errorf("eventlog: fold %q needs a lowercase name and a reducer", f.Name)
		}
		if seen[f.Name] {
			return fmt.Errorf("eventlog: fold %q given twice", f.Name)
		}
		seen[f.Name] = true
	}
	return nil
}
