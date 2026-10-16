package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

// retryStable runs op until it succeeds, fails with an error no retry can fix, or budget
// has passed. budget is the lock timeout: an append in flight holds the lock, so a writer
// that waits longer would itself give up on the lock.
func retryStable[T any](ctx context.Context, budget time.Duration, op func() (T, error)) (T, error) {
	deadline := time.Now().Add(budget)
	var last error
	var zero T
	for i := 0; i < stableAttempts(budget); i++ {
		res, err := op()
		if err == nil || !transientReplayError(err) {
			return res, err
		}
		last = err
		if !sleepUntilNextTry(ctx, deadline) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, fmt.Errorf("eventlog: replay: %w", err)
	}
	return zero, last
}

// stableBudget is how long a stable replay waits out an append in flight: the lock
// timeout of limits, or its default when limits leaves it unset.
func stableBudget(limits Limits) time.Duration {
	if limits.LockTimeout > 0 {
		return limits.LockTimeout
	}
	return defaultLockTimeout
}

// ReplayStable is Replay for a log that other processes append to. An append writes its
// record and then HEAD; a replay that lands between the two reports a HEAD mismatch that
// the next try no longer sees, and on Windows a replay that opens a file while an append
// renames it is refused with a sharing violation. It retries only those cases, for at most
// the lock timeout of limits; every other error is returned at once.
func ReplayStable(ctx context.Context, dir string, pubKeyHex string, limits Limits, reducer Reducer) (State, error) {
	return retryStable(ctx, stableBudget(limits), func() (State, error) {
		return Replay(ctx, dir, pubKeyHex, limits, reducer)
	})
}

type replayCheckpointedResult struct {
	state State
	use   CheckpointUse
}

// ReplayCheckpointedStable is ReplayCheckpointed with the same retry helper as ReplayStable.
func ReplayCheckpointedStable(ctx context.Context, dir string, pubKeyHex string, limits Limits, name string, reducer Reducer) (State, CheckpointUse, error) {
	res, err := retryStable(ctx, stableBudget(limits), func() (replayCheckpointedResult, error) {
		st, use, rErr := ReplayCheckpointed(ctx, dir, pubKeyHex, limits, name, reducer)
		return replayCheckpointedResult{state: st, use: use}, rErr
	})
	return res.state, res.use, err
}

func transientReplayError(err error) bool {
	if fileBusy(err) {
		return true
	}
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
	st, _, err := r.run()
	return st, err
}

// ReplayCheckpointed replays the event log from the newest valid checkpoint of name, or full replay if none.
func ReplayCheckpointed(ctx context.Context, dir string, pubKeyHex string, limits Limits, name string, reducer Reducer) (State, CheckpointUse, error) {
	if err := readyContext(ctx, "replay"); err != nil {
		return State{}, CheckpointUse{}, err
	}
	if !validCheckpointName(name) {
		return State{}, CheckpointUse{}, fmt.Errorf("eventlog: invalid checkpoint name %q", name)
	}
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return State{}, CheckpointUse{}, err
	}
	if err = checkReplayInputs(dir, pubKeyHex, reducer); err != nil {
		return State{}, CheckpointUse{}, err
	}
	r := replayRun{
		eventsDir:      eventsDir(dir),
		checkpointsDir: filepath.Join(dir, "checkpoints"),
		pubKeyHex:      strings.ToLower(pubKeyHex),
		limits:         normalized,
		reducer:        reducer,
		name:           name,
	}
	return r.run()
}

type replayRun struct {
	eventsDir      string
	checkpointsDir string
	pubKeyHex      string
	limits         Limits
	reducer        Reducer
	name           string
}

type checkpointResult struct {
	seq   uint64
	hash  string
	state json.RawMessage
	use   CheckpointUse
}

const maxCheckpointsScan = 16

func (r replayRun) run() (State, CheckpointUse, error) {
	entries, err := os.ReadDir(r.eventsDir)
	if err != nil {
		return State{}, CheckpointUse{}, fmt.Errorf("eventlog: read %s: %w", r.eventsDir, err)
	}
	st := State{Tasks: map[string]Task{}}
	cp := r.findCheckpoint()
	if cp.seq > 0 {
		if err = json.Unmarshal(cp.state, &st); err != nil {
			return State{}, CheckpointUse{}, fmt.Errorf("eventlog: unmarshal checkpoint state: %w", err)
		}
	}
	last, lastHash, err := r.applyRecords(entries, &st, cp.seq, cp.hash)
	if err != nil {
		return State{}, CheckpointUse{}, err
	}
	headRecord, hasHead, err := r.readHead()
	if err != nil {
		return State{}, CheckpointUse{}, err
	}
	if err = checkHeadMatchesTail(headRecord, hasHead, last, lastHash); err != nil {
		return State{}, CheckpointUse{}, err
	}
	return st, cp.use, nil
}

func (r replayRun) findCheckpoint() checkpointResult {
	if r.name == "" || r.checkpointsDir == "" {
		return checkpointResult{}
	}
	cpDir := filepath.Join(r.checkpointsDir, r.name)
	entries, err := os.ReadDir(cpDir)
	if os.IsNotExist(err) {
		return checkpointResult{}
	}
	if err != nil {
		// An unreadable checkpoint directory costs speed, not correctness: the full replay
		// still runs. It is listed so that the slower replay has a named cause.
		return checkpointResult{use: CheckpointUse{Skipped: []string{fmt.Sprintf("read %s: %v", cpDir, err)}}}
	}
	seqs := filterCheckpointSeqs(entries)
	var use CheckpointUse
	for i := 0; i < len(seqs) && i < maxCheckpointsScan; i++ {
		seq := seqs[i]
		cpPath := filepath.Join(cpDir, checkpointFileName(seq))
		cp, ok := r.checkCheckpointCandidate(cpPath, &use)
		if !ok {
			continue
		}
		if div, diverged := r.checkCheckpointAgainstLog(cp, cpPath); diverged {
			if use.Diverged == nil {
				use.Diverged = div
			}
			continue
		}
		use.Seq = cp.Seq
		return checkpointResult{seq: cp.Seq, hash: cp.Hash, state: cp.State, use: use}
	}
	return checkpointResult{use: use}
}

func filterCheckpointSeqs(entries []os.DirEntry) []uint64 {
	var seqs []uint64
	for i := 0; i < len(entries) && i < maxCheckpointEntries; i++ {
		entry := entries[i]
		if !entry.IsDir() && isEventRecord(entry.Name()) {
			if seq, parseErr := seqFromName(entry.Name()); parseErr == nil {
				seqs = append(seqs, seq)
			}
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] > seqs[j] })
	return seqs
}

func (r replayRun) checkCheckpointCandidate(cpPath string, use *CheckpointUse) (Checkpoint, bool) {
	data, err := readFileBounded(cpPath, r.limits.MaxRecordBytes)
	if err != nil {
		use.Skipped = append(use.Skipped, fmt.Sprintf("%s: %v", filepath.Base(cpPath), err))
		return Checkpoint{}, false
	}
	cp, err := decodeCheckpoint(cpPath, data, r.pubKeyHex)
	if err != nil {
		use.Skipped = append(use.Skipped, fmt.Sprintf("%s: %v", filepath.Base(cpPath), err))
		return Checkpoint{}, false
	}
	if cp.Name != r.name {
		use.Skipped = append(use.Skipped, fmt.Sprintf("%s: name %q does not match %q", filepath.Base(cpPath), cp.Name, r.name))
		return Checkpoint{}, false
	}
	return cp, true
}

func (r replayRun) divergenceFor(cp Checkpoint, cpPath string, gotHash string) *Divergence {
	h, _, err := r.readHead()
	if err != nil {
		h = head{}
	}
	return &Divergence{
		Checkpoint: DivergedCheckpoint{Name: cp.Name, Seq: cp.Seq, Hash: cp.Hash},
		Log:        DivergedLog{Seq: h.Seq, Hash: h.Hash, HashAtCheckpointSeq: gotHash},
		File:       cpPath,
	}
}

func (r replayRun) checkCheckpointAgainstLog(cp Checkpoint, cpPath string) (*Divergence, bool) {
	recPath := eventPath(r.eventsDir, cp.Seq)
	recData, err := readFileBounded(recPath, r.limits.MaxRecordBytes)
	if err != nil {
		return r.divergenceFor(cp, cpPath, ""), true
	}
	gotHash := hashHex(recData)
	if gotHash != cp.Hash {
		return r.divergenceFor(cp, cpPath, gotHash), true
	}
	return nil, false
}

func (r replayRun) applyRecords(entries []os.DirEntry, st *State, startSeq uint64, startHash string) (uint64, string, error) {
	prev := startHash
	last := startSeq
	count := 0
	for _, entry := range entries {
		if !isEventRecord(entry.Name()) {
			continue
		}
		seq, err := seqFromName(entry.Name())
		if err != nil {
			return 0, "", err
		}
		if seq <= startSeq {
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
	if errors.Is(err, os.ErrNotExist) {
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

// stableAttempts bounds retryStable's tries over budget: one every lockRetryDelay, as a
// writer tries the lock, plus the first and the last.
func stableAttempts(budget time.Duration) int {
	if budget <= 0 {
		return 1
	}
	return int(budget/lockRetryDelay) + 2
}

// sleepUntilNextTry waits lockRetryDelay, or what is left until deadline when that is less.
// It reports false when deadline has passed or ctx ended.
func sleepUntilNextTry(ctx context.Context, deadline time.Time) bool {
	wait := lockRetryDelay
	if remaining := time.Until(deadline); remaining <= 0 {
		return false
	} else if remaining < wait {
		wait = remaining
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
