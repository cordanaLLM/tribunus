package eventlog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

const maxStableReplayTries = 100

// ReplayStable is Replay for a log that other processes append to. An append writes its
// record and then HEAD; a replay that lands between the two reports a HEAD mismatch that
// the next try no longer sees. It retries only that case, for at most a second; every
// other error is returned at once.
func ReplayStable(ctx context.Context, dir string, pubKeyHex string, limits Limits, reducer Reducer) (State, error) {
	deadline := time.Now().Add(time.Second)
	var last error
	for i := 0; i < maxStableReplayTries; i++ {
		state, err := Replay(ctx, dir, pubKeyHex, limits, reducer)
		if err == nil || !transientReplayError(err) {
			return state, err
		}
		last = err
		if !sleepUntilNextTry(ctx, deadline) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return State{}, fmt.Errorf("eventlog: replay: %w", err)
	}
	return State{}, last
}

func transientReplayError(err error) bool {
	text := err.Error()
	if strings.Contains(text, "HEAD mismatch") || strings.Contains(text, "missing HEAD") {
		return true
	}
	return strings.Contains(text, "HEAD.json") && strings.Contains(text, "no such file")
}

func Replay(ctx context.Context, dir string, pubKeyHex string, limits Limits, reducer Reducer) (State, error) {
	if err := readyContext(ctx, "replay"); err != nil {
		return State{}, err
	}
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return State{}, err
	}
	if err = checkReplayInputs(dir, pubKeyHex, reducer); err != nil {
		return State{}, err
	}
	r := replayRun{eventsDir: eventsDir(dir), pubKeyHex: strings.ToLower(pubKeyHex), limits: normalized, reducer: reducer}
	return r.run()
}

type replayRun struct {
	eventsDir string
	pubKeyHex string
	limits    Limits
	reducer   Reducer
}

func (r replayRun) run() (State, error) {
	entries, err := os.ReadDir(r.eventsDir)
	if err != nil {
		return State{}, fmt.Errorf("eventlog: read %s: %w", r.eventsDir, err)
	}
	st := State{Tasks: map[string]Task{}}
	last, lastHash, err := r.applyRecords(entries, &st)
	if err != nil {
		return State{}, err
	}
	headRecord, hasHead, err := r.readHead()
	if err != nil {
		return State{}, err
	}
	if err = checkHeadMatchesTail(headRecord, hasHead, last, lastHash); err != nil {
		return State{}, err
	}
	return st, nil
}

func (r replayRun) applyRecords(entries []os.DirEntry, st *State) (uint64, string, error) {
	var prev string
	var last uint64
	count := 0
	for _, entry := range entries {
		if !isEventRecord(entry.Name()) {
			continue
		}
		count++
		if count > r.limits.MaxReplayRecords {
			return 0, "", fmt.Errorf("eventlog: max replay records %d exceeded", r.limits.MaxReplayRecords)
		}
		rec, hash, err := r.readRecord(entry.Name(), last+1, prev)
		if err != nil {
			return 0, "", err
		}
		next, err := r.reducer(*st, rec)
		if err != nil {
			return 0, "", fmt.Errorf("eventlog: seq %d file %s: %w", rec.Seq, entry.Name(), err)
		}
		*st, last, prev = next, rec.Seq, hash
	}
	return last, prev, nil
}

func (r replayRun) readRecord(name string, expected uint64, prev string) (Record, string, error) {
	seq, err := seqFromName(name)
	if err != nil {
		return Record{}, "", err
	}
	if seq != expected {
		return Record{}, "", fmt.Errorf("eventlog: gap at seq %d file %s", expected, name)
	}
	path := filepath.Join(r.eventsDir, name)
	data, err := readFileBounded(path, r.limits.MaxRecordBytes)
	if err != nil {
		return Record{}, "", err
	}
	rec, err := decodeRecord(path, data, r.pubKeyHex, expected, prev)
	if err != nil {
		return Record{}, "", fmt.Errorf("eventlog: seq %d file %s: %w", expected, name, err)
	}
	return rec, hashHex(data), nil
}

func decodeRecord(path string, data []byte, pub string, expected uint64, prev string) (Record, error) {
	if err := verifyCanonical(path, data); err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("eventlog: decode %s: %w", path, err)
	}
	if err := checkRecordLinks(rec, expected, prev, path); err != nil {
		return Record{}, err
	}
	unsigned, err := canonicalJSON(recordUnsigned(rec))
	if err != nil {
		return Record{}, err
	}
	if err = ensureExpectedPublicKey(rec.Receipt.PublicKey, pub, fmt.Sprintf("seq %d file %s", rec.Seq, path)); err != nil {
		return Record{}, err
	}
	if err = corereceipt.VerifyOutput(rec.Receipt, unsigned); err != nil {
		return Record{}, fmt.Errorf("eventlog: seq %d file %s: verify receipt: %w", rec.Seq, path, err)
	}
	return rec, nil
}

func checkRecordLinks(rec Record, expected uint64, prev string, path string) error {
	if rec.Seq != expected {
		return fmt.Errorf("eventlog: seq %d file %s: record seq %d", expected, path, rec.Seq)
	}
	wantPrev := prev
	if expected == 1 {
		wantPrev = zeroHash
	}
	if rec.Prev != wantPrev {
		return fmt.Errorf("eventlog: seq %d file %s: prev mismatch", expected, path)
	}
	if rec.Receipt == nil {
		return fmt.Errorf("eventlog: seq %d file %s: missing receipt", expected, path)
	}
	return nil
}

func (r replayRun) readHead() (head, bool, error) {
	path := filepath.Join(r.eventsDir, "HEAD.json")
	data, err := readFileBounded(path, r.limits.MaxRecordBytes)
	if os.IsNotExist(err) {
		return head{}, false, nil
	}
	if err != nil {
		return head{}, false, err
	}
	var h head
	if err = decodeHead(path, data, r.pubKeyHex, &h); err != nil {
		return head{}, false, err
	}
	return h, true, nil
}

func checkHeadMatchesTail(h head, hasHead bool, last uint64, lastHash string) error {
	if !hasHead && last == 0 {
		return nil
	}
	if !hasHead {
		return fmt.Errorf("eventlog: missing HEAD with last seq %d", last)
	}
	if h.Seq != last {
		return fmt.Errorf("eventlog: HEAD mismatch: head seq %d, last seq %d", h.Seq, last)
	}
	if h.Hash != lastHash {
		return fmt.Errorf("eventlog: HEAD mismatch: seq %d hash mismatch", last)
	}
	return nil
}

func checkReplayInputs(dir string, pub string, reducer Reducer) error {
	if dir == "" {
		return fmt.Errorf("eventlog: dir is required")
	}
	if err := checkPublicKeyHex(pub); err != nil {
		return err
	}
	if reducer == nil {
		return fmt.Errorf("eventlog: reducer is required")
	}
	return nil
}

func seqFromName(name string) (uint64, error) {
	seq, err := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("eventlog: parse seq from %s: %w", name, err)
	}
	return seq, nil
}
