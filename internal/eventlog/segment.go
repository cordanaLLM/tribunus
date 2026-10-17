package eventlog

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// segmentRecords is how many records one segment holds. A test lowers it.
var segmentRecords uint64 = 256

const (
	segmentSuffix  = ".jsonl"
	segmentNameLen = 41 + len(segmentSuffix)
)

// errRecordMoved marks a record file that a listing showed and that was gone when it was
// read: a rotation moved it into a segment in between. A stable replay tries again.
var errRecordMoved = errors.New("eventlog: record moved into a segment during replay")

// errLogCompacted marks a replay that failed while a compaction moved the log's base: the
// segments it listed were gone when it read them. A stable replay tries again.
var errLogCompacted = errors.New("eventlog: log compacted during replay")

// errStopSegment ends a segment read early once the wanted record is found.
var errStopSegment = errors.New("eventlog: stop segment read")

// segment is one file of events/segments: the records first..last, one per line, each line
// the exact bytes of the record file it replaced.
type segment struct {
	first uint64
	last  uint64
	name  string
}

func segmentsDir(eventsDir string) string {
	return filepath.Join(eventsDir, "segments")
}

func segmentFileName(first uint64, last uint64) string {
	return fmt.Sprintf("%020d-%020d%s", first, last, segmentSuffix)
}

func parseSegmentName(name string) (segment, bool) {
	if len(name) != segmentNameLen || !strings.HasSuffix(name, segmentSuffix) || name[20] != '-' {
		return segment{}, false
	}
	first, okFirst := parseDigits(name[:20])
	last, okLast := parseDigits(name[21:41])
	if !okFirst || !okLast || first == 0 || last < first {
		return segment{}, false
	}
	return segment{first: first, last: last, name: name}, true
}

func parseDigits(s string) (uint64, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// listSegments returns the segments of eventsDir that lie after seq after, in order; after is
// the seq of the log's base, 0 without one. A name starting with a dot is a temporary file of
// an interrupted write and is passed over. A segment at or below after is one an interrupted
// compaction left behind and is passed over too. Any other file that is not a segment, a
// segment that holds after and the seq past it, and segments that do not follow each other
// from after+1 fail with the file.
func listSegments(eventsDir string, limit int, after uint64) ([]segment, error) {
	dir := segmentsDir(eventsDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("eventlog: read %s: %w", dir, err)
	}
	if len(entries) > limit {
		return nil, fmt.Errorf("eventlog: %s holds %d files, more than %d", dir, len(entries), limit)
	}
	segs := make([]segment, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		seg, ok := parseSegmentName(entry.Name())
		if !ok || entry.IsDir() {
			return nil, fmt.Errorf("eventlog: %s: not a segment file", filepath.Join(dir, entry.Name()))
		}
		if seg.last <= after {
			continue
		}
		segs = append(segs, seg)
	}
	return segs, checkSegmentsFollow(segs, after)
}

func checkSegmentsFollow(segs []segment, after uint64) error {
	want := after + 1
	for _, seg := range segs {
		if seg.first != want {
			return fmt.Errorf("eventlog: gap at seq %d file %s", want, seg.name)
		}
		want = seg.last + 1
	}
	return nil
}

// readSegment calls fn with each record of seg in order. The bytes passed are valid only
// during the call. A segment with fewer or more lines than its name says fails with the seq
// and the file.
func readSegment(eventsDir string, seg segment, maxRecordBytes int, fn func(seq uint64, data []byte) error) (err error) {
	path := filepath.Join(segmentsDir(eventsDir), seg.name)
	// #nosec G304 -- path is a segment file inside the configured events directory, named
	// by listSegments; every line is verified before it is trusted.
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("eventlog: open %s: %w", path, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("eventlog: close %s: %w", path, closeErr))
		}
	}()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), maxRecordBytes+1)
	seq := seg.first
	for ; seq <= seg.last && sc.Scan(); seq++ {
		if err = fn(seq, sc.Bytes()); err != nil {
			return err
		}
	}
	if err = sc.Err(); err != nil {
		return fmt.Errorf("eventlog: seq %d file %s: %w", seq, seg.name, err)
	}
	if seq <= seg.last {
		return fmt.Errorf("eventlog: seq %d file %s: segment ends before seq %d", seq, seg.name, seg.last)
	}
	if sc.Scan() {
		return fmt.Errorf("eventlog: file %s: data after seq %d", seg.name, seg.last)
	}
	return nil
}

// segmentRecord returns the bytes of record seq from the segment of segs that holds it.
func segmentRecord(eventsDir string, segs []segment, seq uint64, maxRecordBytes int) ([]byte, error) {
	for _, seg := range segs {
		if seq < seg.first || seq > seg.last {
			continue
		}
		var found []byte
		err := readSegment(eventsDir, seg, maxRecordBytes, func(s uint64, data []byte) error {
			if s != seq {
				return nil
			}
			found = bytes.Clone(data)
			return errStopSegment
		})
		if errors.Is(err, errStopSegment) {
			return found, nil
		}
		return nil, err
	}
	return nil, fmt.Errorf("eventlog: seq %d: %w", seq, os.ErrNotExist)
}

// looseRecords maps the seq of every record file in entries to its name.
func looseRecords(entries []os.DirEntry) map[uint64]string {
	loose := make(map[uint64]string, len(entries))
	for _, entry := range entries {
		if !isEventRecord(entry.Name()) {
			continue
		}
		if seq, err := seqFromName(entry.Name()); err == nil {
			loose[seq] = entry.Name()
		}
	}
	return loose
}

// sameAsLoose checks that a record file left beside the segment that holds its seq, by a
// rotation that wrote the segment and stopped before it removed the file, has the segment's
// bytes. A file gone since the listing was removed by a rotation and is no conflict.
func sameAsLoose(eventsDir string, loose map[uint64]string, seq uint64, data []byte, seg segment, maxRecordBytes int) error {
	name, ok := loose[seq]
	if !ok {
		return nil
	}
	fileData, err := readFileBounded(filepath.Join(eventsDir, name), maxRecordBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(fileData, data) {
		return fmt.Errorf("eventlog: seq %d: file %s differs from segment %s", seq, name, seg.name)
	}
	return nil
}

func syncDirPath(dir string) error {
	// #nosec G304 -- dir is the configured events directory, opened only to fsync it.
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("eventlog: open dir %s: %w", dir, err)
	}
	if err = syncFile(f); err != nil {
		return errors.Join(fmt.Errorf("eventlog: fsync dir %s: %w", dir, err), f.Close())
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("eventlog: close dir %s: %w", dir, err)
	}
	return nil
}
