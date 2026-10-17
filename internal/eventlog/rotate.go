package eventlog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// maxRotateSegments bounds the segments one Rotate writes, and with it how long Rotate holds
// the lock that appends wait for.
const maxRotateSegments = 4

// Rotate moves closed ranges of records into segment files under events/segments, at most
// maxRotateSegments of segmentRecords records each. A range is closed when the record HEAD
// names lies after it; that record stays a file of its own. Each segment line holds the
// exact bytes of the record file it replaces, so hashes, prev links and receipts stay as
// they were, and replay reads the segments before the files.
//
// Rotate runs under the log lock and never on the append path. It writes and syncs a
// segment before it removes the files the segment holds. A rotation stopped in between
// leaves records in both places; replay accepts them when they are identical and fails
// with the seq when they differ, and the next Rotate removes the identical files first.
// It returns the number of records moved.
func (w *Writer) Rotate(ctx context.Context) (moved uint64, err error) {
	if err = readyContext(ctx, "rotate"); err != nil {
		return 0, err
	}
	if _, statErr := os.Stat(w.eventsDir); errors.Is(statErr, os.ErrNotExist) {
		return 0, nil
	}
	lock, err := acquireLock(ctx, w.eventsDir, w.limits.LockTimeout)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return w.rotateLocked()
}

func (w *Writer) rotateLocked() (uint64, error) {
	current, err := w.currentHead()
	if err != nil {
		return 0, err
	}
	base, err := w.baseSeq()
	if err != nil {
		return 0, err
	}
	segs, err := listSegments(w.eventsDir, w.limits.MaxReplayRecords, base)
	if err != nil {
		return 0, err
	}
	if err = w.dropRotated(segs); err != nil {
		return 0, err
	}
	next := base + 1
	if len(segs) > 0 {
		next = segs[len(segs)-1].last + 1
	}
	moved := uint64(0)
	for i := 0; i < maxRotateSegments; i++ {
		last := next + segmentRecords - 1
		if last >= current.Seq {
			break
		}
		if err = w.writeSegment(next, last); err != nil {
			return moved, err
		}
		moved += segmentRecords
		next = last + 1
	}
	return moved, nil
}

// dropRotated removes the record files that an interrupted rotation left beside the
// segment holding them, after checking that each has the segment's bytes. A file that
// differs stays, and the rotation fails with its seq.
func (w *Writer) dropRotated(segs []segment) error {
	if len(segs) == 0 {
		return nil
	}
	entries, err := os.ReadDir(w.eventsDir)
	if err != nil {
		return fmt.Errorf("eventlog: read %s: %w", w.eventsDir, err)
	}
	loose := looseRecords(entries)
	touched := segmentsWithLoose(segs, loose)
	for _, seg := range touched {
		err = readSegment(w.eventsDir, seg, w.limits.MaxRecordBytes, func(seq uint64, data []byte) error {
			if sameErr := sameAsLoose(w.eventsDir, loose, seq, data, seg, w.limits.MaxRecordBytes); sameErr != nil {
				return sameErr
			}
			return removeRecord(w.eventsDir, loose, seq)
		})
		if err != nil {
			return err
		}
	}
	if len(touched) == 0 {
		return nil
	}
	return syncDirPath(w.eventsDir)
}

// segmentsWithLoose returns the segments of segs that hold the seq of a file in loose.
func segmentsWithLoose(segs []segment, loose map[uint64]string) []segment {
	marked := make(map[int]bool, len(segs))
	for seq := range loose {
		i := sort.Search(len(segs), func(i int) bool { return segs[i].last >= seq })
		if i < len(segs) && segs[i].first <= seq {
			marked[i] = true
		}
	}
	out := make([]segment, 0, len(marked))
	for i, seg := range segs {
		if marked[i] {
			out = append(out, seg)
		}
	}
	return out
}

func removeRecord(eventsDir string, loose map[uint64]string, seq uint64) error {
	name, ok := loose[seq]
	if !ok {
		return nil
	}
	path := filepath.Join(eventsDir, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("eventlog: remove %s: %w", path, err)
	}
	return nil
}

// writeSegment writes the records first..last into one segment file, syncs it, then
// removes their files.
func (w *Writer) writeSegment(first uint64, last uint64) error {
	var body bytes.Buffer
	for seq := first; seq <= last; seq++ {
		data, err := readFileBounded(eventPath(w.eventsDir, seq), w.limits.MaxRecordBytes)
		if err != nil {
			return fmt.Errorf("eventlog: rotate seq %d: %w", seq, err)
		}
		if bytes.IndexByte(data, '\n') >= 0 {
			return fmt.Errorf("eventlog: rotate seq %d: record holds a line break", seq)
		}
		body.Write(data)
		body.WriteByte('\n')
	}
	dir := segmentsDir(w.eventsDir)
	if err := w.ensureSegmentsDir(dir); err != nil {
		return err
	}
	if err := atomicReplace(dir, segmentFileName(first, last), body.Bytes(), 0o600); err != nil {
		return err
	}
	for seq := first; seq <= last; seq++ {
		path := eventPath(w.eventsDir, seq)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("eventlog: remove %s: %w", path, err)
		}
	}
	return syncDirPath(w.eventsDir)
}

// ensureSegmentsDir creates dir and syncs the events directory that names it, so a segment
// synced inside it cannot be lost with its directory.
func (w *Writer) ensureSegmentsDir(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("eventlog: mkdir %s: %w", dir, err)
	}
	return syncDirPath(w.eventsDir)
}
