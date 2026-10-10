package eventlog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/golusoris/golusoris/core/codec/jcs"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

type unsignedRecord struct {
	Seq     uint64          `json:"seq"`
	Prev    string          `json:"prev"`
	Time    string          `json:"time"`
	Type    string          `json:"type"`
	TaskID  string          `json:"task_id"`
	Payload json.RawMessage `json:"payload"`
}

type head struct {
	Seq     uint64               `json:"seq"`
	Hash    string               `json:"hash"`
	Receipt *corereceipt.Receipt `json:"receipt"`
}

type unsignedHead struct {
	Seq  uint64 `json:"seq"`
	Hash string `json:"hash"`
}

func recordUnsigned(rec Record) unsignedRecord {
	return unsignedRecord{
		Seq:     rec.Seq,
		Prev:    rec.Prev,
		Time:    rec.Time,
		Type:    rec.Type,
		TaskID:  rec.TaskID,
		Payload: rec.Payload,
	}
}

func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("eventlog: marshal canonical JSON: %w", err)
	}
	out, err := jcs.Canonicalize(raw)
	if err != nil {
		return nil, fmt.Errorf("eventlog: canonicalize JSON: %w", err)
	}
	return out, nil
}

func canonicalPayload(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	out, err := jcs.Canonicalize(raw)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func verifyCanonical(path string, data []byte) error {
	canon, err := jcs.Canonicalize(data)
	if err != nil {
		return fmt.Errorf("eventlog: %s: canonicalize: %w", path, err)
	}
	if !bytes.Equal(canon, data) {
		return fmt.Errorf("eventlog: %s: non-canonical JSON", path)
	}
	return nil
}

func readyContext(ctx context.Context, op string) error {
	if ctx == nil {
		return fmt.Errorf("eventlog: %s: nil context", op)
	}
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("eventlog: %s requires a context deadline", op)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("eventlog: %s context: %w", op, err)
	}
	return nil
}

func eventFileName(seq uint64) string {
	return fmt.Sprintf("%020d.json", seq)
}

func eventPath(eventsDir string, seq uint64) string {
	return filepath.Join(eventsDir, eventFileName(seq))
}

func eventsDir(dir string) string {
	return filepath.Join(dir, "events")
}

func isEventRecord(name string) bool {
	if len(name) != len("00000000000000000000.json") {
		return false
	}
	if !strings.HasSuffix(name, ".json") {
		return false
	}
	for i := 0; i < 20; i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}

func readFileBounded(path string, limit int) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("eventlog: stat %s: %w", path, err)
	}
	if info.Size() > int64(limit) {
		return nil, fmt.Errorf("%w: %s is %d bytes", ErrRecordTooLarge, path, info.Size())
	}
	// #nosec G304 -- path is a record or HEAD file inside the configured events
	// directory, size-checked above and verified before it is trusted.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("eventlog: read %s: %w", path, err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%w: %s is %d bytes", ErrRecordTooLarge, path, len(data))
	}
	return data, nil
}

func ensureExpectedPublicKey(got string, want string, where string) error {
	if got != strings.ToLower(want) {
		return fmt.Errorf("eventlog: %s: receipt public key %q does not match configured public key", where, got)
	}
	return nil
}

func checkPublicKeyHex(pub string) error {
	decoded, err := hex.DecodeString(pub)
	if err != nil {
		return fmt.Errorf("eventlog: public key: %w", err)
	}
	if len(decoded) != 32 {
		return errors.New("eventlog: public key must be 32 bytes")
	}
	return nil
}
