package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Compact moves the log's base forward and removes the segments the new base covers. The base
// holds the state of every fold after the last record of the newest segment that the commit
// on g.Ref holds byte for byte and that no fold's Floor keeps; a replay of a fold starts there
// (ReplayFold). It returns the seq of the base, 0 when the log has none.
//
// Compact runs under the log lock and never removes a record a commit on g.Ref does not hold,
// so it follows RecordInGit; it does not commit itself. The new base is written and synced
// before any segment is removed: a crash in between leaves segments the base covers, which
// replays and the next Compact pass over and remove. The base lies in checkpoints/base/; the
// bases it replaces are removed last.
func (w *Writer) Compact(ctx context.Context, folds []Fold, g GitRecord) (base uint64, err error) {
	if err = readyContext(ctx, "compact"); err != nil {
		return 0, err
	}
	if err = checkFolds(folds); err != nil {
		return 0, err
	}
	if err = g.validate(); err != nil {
		return 0, err
	}
	if _, statErr := os.Stat(w.eventsDir); errors.Is(statErr, os.ErrNotExist) {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()
	repo, err := openGitRepo(ctx, w.dir, g, w.limits.Clock.Now())
	if err != nil {
		return 0, err
	}
	defer func() {
		if rmErr := os.RemoveAll(repo.indexDir); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("eventlog: compact: remove index: %w", rmErr))
		}
	}()
	lock, err := acquireLock(ctx, w.eventsDir, w.limits.LockTimeout)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return w.compactLocked(ctx, folds, repo, g.Ref)
}

func (w *Writer) compactLocked(ctx context.Context, folds []Fold, repo gitRepo, ref string) (uint64, error) {
	old, err := w.baseSeq()
	if err != nil {
		return 0, err
	}
	segs, next, err := w.nextBase(ctx, folds, repo, ref, old)
	if err != nil {
		return 0, err
	}
	if next > old {
		if err = w.moveBase(folds, segs, next); err != nil {
			return old, err
		}
	}
	if err = w.removeCovered(next); err != nil {
		return next, err
	}
	return next, w.removeOldBases(next)
}

// nextBase returns the segments after the base at old and the seq the next base may cover:
// the last record of the longest run of them the commit on ref holds, up to the folds' floor.
func (w *Writer) nextBase(ctx context.Context, folds []Fold, repo gitRepo, ref string, old uint64) ([]segment, uint64, error) {
	if _, err := w.currentHead(); err != nil {
		return nil, 0, err
	}
	segs, err := listSegments(w.eventsDir, w.limits.MaxReplayRecords, old)
	if err != nil {
		return nil, 0, err
	}
	floor, err := w.foldFloor(folds)
	if err != nil {
		return nil, 0, err
	}
	held, err := heldSegments(ctx, repo, ref, segs)
	if err != nil {
		return nil, 0, err
	}
	next := old
	for i := 0; i < len(segs) && i < len(held) && held[i] && segs[i].last <= floor; i++ {
		next = segs[i].last
	}
	return segs, next, nil
}

// foldFloor returns the lowest Floor of folds, each given its state at the head of the log.
func (w *Writer) foldFloor(folds []Fold) (uint64, error) {
	floor := uint64(math.MaxUint64)
	for i := 0; i < len(folds); i++ {
		if folds[i].Floor == nil {
			continue
		}
		st, err := w.replayFold(folds[i], 0)
		if err != nil {
			return 0, err
		}
		floor = min(floor, folds[i].Floor(st))
	}
	return floor, nil
}

// replayFold replays fold under the log lock, to the head, or to seq until when it is set.
func (w *Writer) replayFold(f Fold, until uint64) (State, error) {
	r := replayRun{
		eventsDir: w.eventsDir,
		pubKeyHex: strings.ToLower(w.signer.PublicKey()),
		limits:    w.limits,
		reducer:   f.Reduce,
		fold:      f.Name,
		until:     until,
	}
	st, _, err := r.run()
	if err != nil {
		return State{}, fmt.Errorf("eventlog: compact: fold %q: %w", f.Name, err)
	}
	return st, nil
}

// moveBase writes the base after record seq, the last record of one of segs.
func (w *Writer) moveBase(folds []Fold, segs []segment, seq uint64) error {
	states := make(map[string]json.RawMessage, len(folds))
	for i := 0; i < len(folds); i++ {
		st, err := w.replayFold(folds[i], seq)
		if err != nil {
			return err
		}
		raw, err := canonicalJSON(st)
		if err != nil {
			return err
		}
		states[folds[i].Name] = raw
	}
	data, err := segmentRecord(w.eventsDir, segs, seq, w.limits.MaxRecordBytes)
	if err != nil {
		return err
	}
	return w.writeBase(seq, hashHex(data), states)
}

// removeCovered removes the segments and record files at or below the base at seq.
func (w *Writer) removeCovered(seq uint64) error {
	if seq == 0 {
		return nil
	}
	if err := removeCoveredSegments(segmentsDir(w.eventsDir), seq); err != nil {
		return err
	}
	records, err := os.ReadDir(w.eventsDir)
	if err != nil {
		return fmt.Errorf("eventlog: read %s: %w", w.eventsDir, err)
	}
	for _, entry := range records {
		n, nErr := seqFromName(entry.Name())
		if !isEventRecord(entry.Name()) || nErr != nil || n > seq {
			continue
		}
		if err = removeFile(filepath.Join(w.eventsDir, entry.Name())); err != nil {
			return err
		}
	}
	return syncDirPath(w.eventsDir)
}

// removeCoveredSegments removes the segments in dir that end at or below seq.
func removeCoveredSegments(dir string, seq uint64) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("eventlog: read %s: %w", dir, err)
	}
	for _, entry := range entries {
		seg, ok := parseSegmentName(entry.Name())
		if !ok || seg.last > seq {
			continue
		}
		if err = removeFile(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return syncDirPath(dir)
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("eventlog: remove %s: %w", path, err)
	}
	return nil
}

// heldSegments reports, for each of segs, whether the commit ref names holds the segment
// with the bytes it has on disk.
func heldSegments(ctx context.Context, repo gitRepo, ref string, segs []segment) ([]bool, error) {
	held := make([]bool, len(segs))
	if len(segs) == 0 {
		return held, nil
	}
	commit, err := repo.refCommit(ctx, ref)
	if err != nil || commit == "" {
		return held, err
	}
	out, err := repo.git(ctx, nil, "ls-tree", commit, "--", "events/segments/")
	if err != nil {
		return nil, err
	}
	inRef := treeBlobs(out)
	var paths strings.Builder
	for i := 0; i < len(segs); i++ {
		paths.WriteString(filepath.Join(segmentsDir(eventsDir(repo.dir)), segs[i].name) + "\n")
	}
	out, err = repo.git(ctx, strings.NewReader(paths.String()), "hash-object", "--no-filters", "--stdin-paths")
	if err != nil {
		return nil, err
	}
	onDisk := strings.Fields(out)
	if len(onDisk) != len(segs) {
		return nil, fmt.Errorf("eventlog: compact: hash-object returned %d ids for %d segments", len(onDisk), len(segs))
	}
	for i := 0; i < len(segs); i++ {
		held[i] = inRef["events/segments/"+segs[i].name] == onDisk[i]
	}
	return held, nil
}

// treeBlobs maps the path of every blob in ls-tree output to its object id.
func treeBlobs(out string) map[string]string {
	blobs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		meta, path, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" {
			continue
		}
		blobs[path] = fields[2]
	}
	return blobs
}
