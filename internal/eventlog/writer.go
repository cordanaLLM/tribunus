package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

func (w *Writer) Append(ctx context.Context, ev Event) (Record, error) {
	if err := readyContext(ctx, "append"); err != nil {
		return Record{}, err
	}
	if err := os.MkdirAll(w.eventsDir, 0o750); err != nil {
		return Record{}, fmt.Errorf("eventlog: mkdir %s: %w", w.eventsDir, err)
	}
	lock, err := acquireLock(ctx, w.eventsDir, w.limits.LockTimeout)
	if err != nil {
		return Record{}, err
	}
	current, err := w.currentHead()
	if err != nil {
		if closeErr := lock.Close(); closeErr != nil {
			return Record{}, fmt.Errorf("%w; close lock: %w", err, closeErr)
		}
		return Record{}, err
	}
	rec, err := w.writeAppend(current, ev)
	if closeErr := lock.Close(); closeErr != nil {
		return Record{}, errors.Join(err, fmt.Errorf("eventlog: close lock after append: %w", closeErr))
	}
	return rec, err
}

func (w *Writer) writeAppend(current unsignedHead, ev Event) (Record, error) {
	payload, err := canonicalPayload(ev.Payload)
	if err != nil {
		return Record{}, fmt.Errorf("eventlog: payload: %w", err)
	}
	rec := Record{
		Seq:     current.Seq + 1,
		Prev:    current.Hash,
		Time:    w.limits.Clock.Now().UTC().Format(time.RFC3339Nano),
		Type:    ev.Type,
		TaskID:  ev.TaskID,
		Payload: payload,
	}
	if rec.Seq == 1 {
		rec.Prev = zeroHash
	}
	unsigned, err := canonicalJSON(recordUnsigned(rec))
	if err != nil {
		return Record{}, err
	}
	signed, err := w.signer.Sign(corereceipt.Run{Command: AppendCommand, Output: unsigned})
	if err != nil {
		return Record{}, fmt.Errorf("eventlog: sign record %d: %w", rec.Seq, err)
	}
	rec.Receipt = signed
	full, err := canonicalJSON(rec)
	if err != nil {
		return Record{}, err
	}
	if len(full) > w.limits.MaxRecordBytes {
		return Record{}, fmt.Errorf("%w: seq %d is %d bytes", ErrRecordTooLarge, rec.Seq, len(full))
	}
	if err = writeNewRecord(w.eventsDir, rec.Seq, full); err != nil {
		return Record{}, err
	}
	if err = w.writeHead(rec.Seq, hashHex(full)); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func (w *Writer) currentHead() (unsignedHead, error) {
	path := filepath.Join(w.eventsDir, "HEAD.json")
	// #nosec G304 -- the events directory comes from operator config and the
	// file name is fixed; HEAD.json is verified before it is trusted.
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return w.firstHead()
	}
	if err != nil {
		return unsignedHead{}, fmt.Errorf("eventlog: read %s: %w", path, err)
	}
	var h head
	if err = decodeHead(path, data, w.signer.PublicKey(), &h); err != nil {
		return unsignedHead{}, err
	}
	if err = verifyHeadTail(w.eventsDir, h); err != nil {
		return unsignedHead{}, err
	}
	return unsignedHead{Seq: h.Seq, Hash: h.Hash}, nil
}

func (w *Writer) firstHead() (unsignedHead, error) {
	entries, err := os.ReadDir(w.eventsDir)
	if err != nil {
		return unsignedHead{}, fmt.Errorf("eventlog: read %s: %w", w.eventsDir, err)
	}
	for _, entry := range entries {
		if isEventRecord(entry.Name()) {
			return unsignedHead{}, fmt.Errorf("eventlog: missing HEAD with existing record %s", entry.Name())
		}
	}
	return unsignedHead{}, nil
}

func (w *Writer) writeHead(seq uint64, hash string) error {
	unsigned := unsignedHead{Seq: seq, Hash: hash}
	out, err := canonicalJSON(unsigned)
	if err != nil {
		return err
	}
	r, err := w.signer.Sign(corereceipt.Run{Command: AppendCommand, Output: out})
	if err != nil {
		return fmt.Errorf("eventlog: sign HEAD: %w", err)
	}
	body, err := canonicalJSON(head{Seq: seq, Hash: hash, Receipt: r})
	if err != nil {
		return err
	}
	return atomicReplace(w.eventsDir, "HEAD.json", body, 0o600)
}

func writeNewRecord(eventsDir string, seq uint64, body []byte) error {
	name := eventFileName(seq)
	path := filepath.Join(eventsDir, name)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("eventlog: %s already exists", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("eventlog: stat %s: %w", path, err)
	}
	return atomicReplace(eventsDir, name, body, 0o600)
}

func decodeHead(path string, data []byte, pub string, h *head) error {
	if err := verifyCanonical(path, data); err != nil {
		return err
	}
	if err := json.Unmarshal(data, h); err != nil {
		return fmt.Errorf("eventlog: decode %s: %w", path, err)
	}
	unsigned, err := canonicalJSON(unsignedHead{Seq: h.Seq, Hash: h.Hash})
	if err != nil {
		return err
	}
	if h.Receipt == nil {
		return fmt.Errorf("eventlog: %s: missing receipt", path)
	}
	if err = ensureExpectedPublicKey(h.Receipt.PublicKey, pub, path); err != nil {
		return err
	}
	if err = corereceipt.VerifyOutput(h.Receipt, unsigned); err != nil {
		return fmt.Errorf("eventlog: verify %s: %w", path, err)
	}
	return nil
}

func verifyHeadTail(eventsDir string, h head) error {
	data, err := os.ReadFile(eventPath(eventsDir, h.Seq))
	if err != nil {
		return fmt.Errorf("eventlog: HEAD tail seq %d: %w", h.Seq, err)
	}
	if got := hashHex(data); got != h.Hash {
		return fmt.Errorf("eventlog: HEAD tail seq %d hash mismatch", h.Seq)
	}
	return nil
}
