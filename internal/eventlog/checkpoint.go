package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

const (
	maxCheckpointsKept   = 3
	maxCheckpointEntries = 128
)

var validCheckpointNameRegex = regexp.MustCompile(`^[a-z]+$`)

// Checkpoint is one snapshot of reducer state after applying a specific record.
type Checkpoint struct {
	Name    string               `json:"name"`
	Seq     uint64               `json:"seq"`
	Hash    string               `json:"hash"`
	State   json.RawMessage      `json:"state"`
	Receipt *corereceipt.Receipt `json:"receipt"`
}

type unsignedCheckpoint struct {
	Name  string          `json:"name"`
	Seq   uint64          `json:"seq"`
	Hash  string          `json:"hash"`
	State json.RawMessage `json:"state"`
}

// CheckpointUse reports which checkpoint, if any, a replay started from.
type CheckpointUse struct {
	Seq      uint64      `json:"seq"`
	Skipped  []string    `json:"skipped,omitempty"`
	Diverged *Divergence `json:"diverged,omitempty"`
}

// Divergence describes a checkpoint that does not match the event log on disk.
type Divergence struct {
	Checkpoint DivergedCheckpoint `json:"checkpoint"`
	Log        DivergedLog        `json:"log"`
	File       string             `json:"-"`
}

// DivergedCheckpoint is the checkpoint that diverged from the log.
type DivergedCheckpoint struct {
	Name string `json:"name"`
	Seq  uint64 `json:"seq"`
	Hash string `json:"hash"`
}

// DivergedLog holds the log's state when divergence was detected.
type DivergedLog struct {
	Seq                 uint64 `json:"seq"`
	Hash                string `json:"hash"`
	HashAtCheckpointSeq string `json:"hash_at_checkpoint_seq"`
}

// WriteCheckpoint writes a signed, canonical checkpoint file and keeps at most 3 checkpoints for name.
func (w *Writer) WriteCheckpoint(ctx context.Context, name string, seq uint64, hash string, st State) error {
	if err := readyContext(ctx, "write checkpoint"); err != nil {
		return err
	}
	if !validCheckpointName(name) {
		return fmt.Errorf("eventlog: invalid checkpoint name %q", name)
	}
	if seq == 0 || hash == "" {
		return errors.New("eventlog: checkpoint seq and hash are required")
	}
	full, err := w.buildCheckpoint(name, seq, hash, st)
	if err != nil {
		return err
	}
	cpDir := filepath.Join(w.dir, "checkpoints", name)
	if err = os.MkdirAll(cpDir, 0o750); err != nil {
		return fmt.Errorf("eventlog: mkdir %s: %w", cpDir, err)
	}
	fileName := checkpointFileName(seq)
	if err = atomicReplace(cpDir, fileName, full, 0o600); err != nil {
		return err
	}
	return pruneCheckpoints(cpDir)
}

func (w *Writer) buildCheckpoint(name string, seq uint64, hash string, st State) ([]byte, error) {
	stateBytes, err := st.CanonicalJSON()
	if err != nil {
		return nil, fmt.Errorf("eventlog: checkpoint state: %w", err)
	}
	unsigned := unsignedCheckpoint{Name: name, Seq: seq, Hash: hash, State: stateBytes}
	unsignedBytes, err := canonicalJSON(unsigned)
	if err != nil {
		return nil, err
	}
	signed, err := w.signer.Sign(corereceipt.Run{Command: CheckpointCommand, Output: unsignedBytes})
	if err != nil {
		return nil, fmt.Errorf("eventlog: sign checkpoint %s seq %d: %w", name, seq, err)
	}
	cp := Checkpoint{Name: name, Seq: seq, Hash: hash, State: stateBytes, Receipt: signed}
	full, err := canonicalJSON(cp)
	if err != nil {
		return nil, err
	}
	return full, nil
}

func validCheckpointName(name string) bool {
	return validCheckpointNameRegex.MatchString(name)
}

func checkpointFileName(seq uint64) string {
	return fmt.Sprintf("%020d.json", seq)
}

func pruneCheckpoints(cpDir string) error {
	entries, err := os.ReadDir(cpDir)
	if err != nil {
		return fmt.Errorf("eventlog: read checkpoint dir %s: %w", cpDir, err)
	}
	var seqs []uint64
	for i := 0; i < len(entries) && i < maxCheckpointEntries; i++ {
		entry := entries[i]
		if !entry.IsDir() && isEventRecord(entry.Name()) {
			seq, seqErr := seqFromName(entry.Name())
			if seqErr == nil {
				seqs = append(seqs, seq)
			}
		}
	}
	sort.Slice(seqs, func(i int, j int) bool { return seqs[i] > seqs[j] })
	for i := maxCheckpointsKept; i < len(seqs); i++ {
		path := filepath.Join(cpDir, checkpointFileName(seqs[i]))
		if remErr := os.Remove(path); remErr != nil && !os.IsNotExist(remErr) {
			return fmt.Errorf("eventlog: prune checkpoint %s: %w", path, remErr)
		}
	}
	return nil
}

func verifyCheckpointReceipt(cp Checkpoint, pubKeyHex string, path string) error {
	if cp.Receipt == nil {
		return fmt.Errorf("checkpoint %s: missing receipt", path)
	}
	if cp.Receipt.Command != CheckpointCommand {
		return fmt.Errorf("checkpoint %s: receipt command %q, want %q", path, cp.Receipt.Command, CheckpointCommand)
	}
	if err := ensureExpectedPublicKey(cp.Receipt.PublicKey, pubKeyHex, path); err != nil {
		return err
	}
	unsigned := unsignedCheckpoint{Name: cp.Name, Seq: cp.Seq, Hash: cp.Hash, State: cp.State}
	unsignedBytes, err := canonicalJSON(unsigned)
	if err != nil {
		return fmt.Errorf("checkpoint %s: unsigned canonical JSON: %w", path, err)
	}
	if err = corereceipt.VerifyOutput(cp.Receipt, unsignedBytes); err != nil {
		return fmt.Errorf("checkpoint %s: verify receipt: %w", path, err)
	}
	return nil
}

func decodeCheckpoint(path string, data []byte, pubKeyHex string) (Checkpoint, error) {
	if err := verifyCanonical(path, data); err != nil {
		return Checkpoint{}, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return Checkpoint{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := verifyCheckpointReceipt(cp, pubKeyHex, path); err != nil {
		return Checkpoint{}, err
	}
	return cp, nil
}
