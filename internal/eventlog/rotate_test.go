package eventlog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withSegmentRecords sets the records per segment for one test.
func withSegmentRecords(t *testing.T, n uint64) {
	t.Helper()
	old := segmentRecords
	segmentRecords = n
	t.Cleanup(func() { segmentRecords = old })
}

func appendTasks(t *testing.T, dir string, w *Writer, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ev := Event{Type: "task.created", TaskID: fmt.Sprintf("task-%d", i), Payload: raw(`{"state":"open"}`)}
		if _, err := w.Append(testContext(t), ev); err != nil {
			t.Fatalf("Append(%d) = %v, want nil", i, err)
		}
	}
}

func rotate(t *testing.T, w *Writer) uint64 {
	t.Helper()
	moved, err := w.Rotate(testContext(t))
	if err != nil {
		t.Fatalf("Rotate() = %v, want nil", err)
	}
	return moved
}

// copyLog copies the events directory of dir into a new log directory.
func copyLog(t *testing.T, dir string) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(eventsDir(dst), os.DirFS(eventsDir(dir))); err != nil {
		t.Fatalf("CopyFS() = %v, want nil", err)
	}
	return dst
}

func segmentNames(t *testing.T, dir string) []string {
	t.Helper()
	segs, err := listSegments(eventsDir(dir), 1000)
	if err != nil {
		t.Fatalf("listSegments() = %v, want nil", err)
	}
	names := make([]string, 0, len(segs))
	for _, seg := range segs {
		names = append(names, seg.name)
	}
	return names
}

// rotatedLog writes 11 records in segments of 3 and rotates them: segments 1-3, 4-6 and
// 7-9, with records 10 and 11 left as files. It also returns an unrotated copy.
func rotatedLog(t *testing.T) (string, string, *Writer, Limits) {
	t.Helper()
	withSegmentRecords(t, 3)
	dir, signer, limits := testLog(t)
	w := openWriter(t, dir, signer, limits)
	appendTasks(t, dir, w, 11)
	plain := copyLog(t, dir)
	if moved := rotate(t, w); moved != 9 {
		t.Fatalf("Rotate() moved %d records, want 9", moved)
	}
	return dir, plain, w, limits
}

func TestRotationMovesClosedRangesAndKeepsTheHeadRecord(t *testing.T) {
	dir, _, _, _ := rotatedLog(t)
	got := strings.Join(segmentNames(t, dir), ",")
	want := strings.Join([]string{segmentFileName(1, 3), segmentFileName(4, 6), segmentFileName(7, 9)}, ",")
	if got != want {
		t.Fatalf("segments = %s, want %s", got, want)
	}
	if seqs := eventSeqs(t, eventsDir(dir)); len(seqs) != 2 || seqs[0] != 10 || seqs[1] != 11 {
		t.Fatalf("record files = %v, want [10 11]", seqs)
	}
}

func TestRotatedLogReplaysByteIdentical(t *testing.T) {
	dir, plain, w, limits := rotatedLog(t)
	pub := w.signer.PublicKey()
	if got, want := replayStateBytes(t, dir, pub, limits), replayStateBytes(t, plain, pub, limits); !bytes.Equal(got, want) {
		t.Fatalf("rotated replay = %s, want %s", got, want)
	}
	appendTasks(t, dir, w, 2)
	if moved := rotate(t, w); moved != 3 {
		t.Fatalf("second Rotate() moved %d records, want 3", moved)
	}
	plainWriter := openWriter(t, plain, w.signer, limits)
	appendTasks(t, plain, plainWriter, 2)
	if got, want := replayStateBytes(t, dir, pub, limits), replayStateBytes(t, plain, pub, limits); !bytes.Equal(got, want) {
		t.Fatalf("replay after append and rotation = %s, want %s", got, want)
	}
}

func TestRotateIsBoundedPerCall(t *testing.T) {
	withSegmentRecords(t, 2)
	dir, signer, limits := testLog(t)
	w := openWriter(t, dir, signer, limits)
	appendTasks(t, dir, w, 20)
	if moved := rotate(t, w); moved != 2*maxRotateSegments {
		t.Fatalf("Rotate() moved %d records, want %d", moved, 2*maxRotateSegments)
	}
	if moved := rotate(t, w); moved != 2*maxRotateSegments {
		t.Fatalf("second Rotate() moved %d records, want %d", moved, 2*maxRotateSegments)
	}
	if moved := rotate(t, w); moved != 2 {
		t.Fatalf("third Rotate() moved %d records, want 2 (record 19 then 20 stays)", moved)
	}
}

func TestRotateWithoutAnEventsDirectoryDoesNothing(t *testing.T) {
	dir, signer, limits := testLog(t)
	if moved := rotate(t, openWriter(t, dir, signer, limits)); moved != 0 {
		t.Fatalf("Rotate() moved %d, want 0", moved)
	}
}

// restoreFiles puts the record files of plain back into dir, as a rotation that stopped
// after writing its segments leaves them.
func restoreFiles(t *testing.T, dir string, plain string, seqs ...uint64) {
	t.Helper()
	for _, seq := range seqs {
		copyFile(t, eventPath(eventsDir(plain), seq), eventPath(eventsDir(dir), seq))
	}
}

func TestInterruptedRotationReplaysAndTheNextRotateFinishesIt(t *testing.T) {
	dir, plain, w, limits := rotatedLog(t)
	restoreFiles(t, dir, plain, 4, 5, 6)
	pub := w.signer.PublicKey()
	if got, want := replayStateBytes(t, dir, pub, limits), replayStateBytes(t, plain, pub, limits); !bytes.Equal(got, want) {
		t.Fatalf("replay with leftover files = %s, want %s", got, want)
	}
	if moved := rotate(t, w); moved != 0 {
		t.Fatalf("Rotate() moved %d, want 0", moved)
	}
	if seqs := eventSeqs(t, eventsDir(dir)); len(seqs) != 2 || seqs[0] != 10 {
		t.Fatalf("record files after Rotate = %v, want [10 11]", seqs)
	}
}

func TestLeftoverFileThatDiffersFromItsSegmentFailsWithTheSeq(t *testing.T) {
	dir, plain, w, limits := rotatedLog(t)
	restoreFiles(t, dir, plain, 5)
	replaceInFile(t, eventPath(eventsDir(dir), 5), "open", "done")
	_, err := Replay(testContext(t), dir, w.signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "seq 5", "differs from segment", segmentFileName(4, 6))
	_, err = w.Rotate(testContext(t))
	assertErrContains(t, err, "seq 5", "differs from segment")
}

func TestDamagedSegmentFailsWithTheSeqAndTheFile(t *testing.T) {
	cases := []struct {
		name   string
		damage func(body []byte) []byte
		want   []string
	}{
		{"tampered", func(b []byte) []byte { return bytes.Replace(b, []byte("task-4"), []byte("task-X"), 1) }, []string{"seq 5", segmentFileName(4, 6), "verify receipt"}},
		{"truncated", func(b []byte) []byte { return b[:bytes.LastIndexByte(b[:len(b)-1], '\n')+1] }, []string{"seq 6", segmentFileName(4, 6), "segment ends before"}},
		{"extra line", func(b []byte) []byte { return append(b, b[:bytes.IndexByte(b, '\n')+1]...) }, []string{segmentFileName(4, 6), "data after seq 6"}},
		{"torn line", func(b []byte) []byte { return b[:len(b)/2] }, []string{segmentFileName(4, 6), "seq 5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, w, limits := rotatedLog(t)
			path := filepath.Join(segmentsDir(eventsDir(dir)), segmentFileName(4, 6))
			writeFile(t, path, tc.damage([]byte(readText(t, path))), 0o600)
			_, err := Replay(testContext(t), dir, w.signer.PublicKey(), limits, TaskReducer)
			assertErrContains(t, err, tc.want...)
		})
	}
}

func TestMissingOrForeignSegmentFails(t *testing.T) {
	dir, _, w, limits := rotatedLog(t)
	segDir := segmentsDir(eventsDir(dir))
	if err := os.Remove(filepath.Join(segDir, segmentFileName(4, 6))); err != nil {
		t.Fatal(err)
	}
	_, err := Replay(testContext(t), dir, w.signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "gap at seq 4", segmentFileName(7, 9))
	_, err = w.Rotate(testContext(t))
	assertErrContains(t, err, "gap at seq 4", segmentFileName(7, 9))

	dir, _, w, limits = rotatedLog(t)
	writeFile(t, filepath.Join(segmentsDir(eventsDir(dir)), "notes.txt"), raw("x"), 0o600)
	_, err = Replay(testContext(t), dir, w.signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "notes.txt", "not a segment file")
}

func TestTemporaryFileInSegmentsIsPassedOver(t *testing.T) {
	dir, plain, w, limits := rotatedLog(t)
	writeFile(t, filepath.Join(segmentsDir(eventsDir(dir)), "."+segmentFileName(10, 12)+".tmp-1"), raw("torn"), 0o600)
	pub := w.signer.PublicKey()
	if got, want := replayStateBytes(t, dir, pub, limits), replayStateBytes(t, plain, pub, limits); !bytes.Equal(got, want) {
		t.Fatalf("replay = %s, want %s", got, want)
	}
}

func TestMissingHeadWithSegmentsRefusesAppend(t *testing.T) {
	dir, _, w, _ := rotatedLog(t)
	for _, seq := range []uint64{10, 11} {
		if err := os.Remove(eventPath(eventsDir(dir), seq)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(eventsDir(dir), "HEAD.json")); err != nil {
		t.Fatal(err)
	}
	_, err := w.Append(testContext(t), Event{Type: "task.created", TaskID: "late", Payload: raw(`{"state":"open"}`)})
	assertErrContains(t, err, "missing HEAD with existing segment", segmentFileName(1, 3))
}

func TestCheckpointInsideASegmentIsUsed(t *testing.T) {
	withSegmentRecords(t, 3)
	dir, signer, limits := testLog(t)
	w := openWriter(t, dir, signer, limits)
	appendTasks(t, dir, w, 5)
	rec, err := os.ReadFile(eventPath(eventsDir(dir), 5))
	if err != nil {
		t.Fatal(err)
	}
	st := replayTasks(t, dir, signer.PublicKey(), limits)
	if err = w.WriteCheckpoint(testContext(t), "tasks", 5, hashHex(rec), st); err != nil {
		t.Fatalf("WriteCheckpoint() = %v, want nil", err)
	}
	appendTasks(t, dir, w, 4)
	plain := copyLog(t, dir)
	if moved := rotate(t, w); moved != 6 {
		t.Fatalf("Rotate() moved %d, want 6", moved)
	}
	got, use, err := ReplayCheckpointed(testContext(t), dir, signer.PublicKey(), limits, "tasks", TaskReducer)
	if err != nil {
		t.Fatalf("ReplayCheckpointed() = %v, want nil", err)
	}
	if use.Seq != 5 || use.Diverged != nil || len(use.Skipped) != 0 {
		t.Fatalf("checkpoint use = %+v, want seq 5", use)
	}
	gotBytes, err := got.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if want := replayStateBytes(t, plain, signer.PublicKey(), limits); !bytes.Equal(gotBytes, want) {
		t.Fatalf("checkpointed replay = %s, want %s", gotBytes, want)
	}
}

func TestRecordMovedDuringReplayIsTransient(t *testing.T) {
	err := fmt.Errorf("%w: seq 4 file 00000000000000000004.json", errRecordMoved)
	if !transientReplayError(err) {
		t.Fatal("transientReplayError(record moved) = false, want true")
	}
	if transientReplayError(errors.New("eventlog: gap at seq 4")) {
		t.Fatal("transientReplayError(gap) = true, want false")
	}
}

// v010Seed is the fixed seed testdata/v0.1.0/generate.sh signs the fixtures with.
func v010Seed() []byte {
	return bytes.Repeat([]byte{7}, 32)
}

// recordingReducer keeps every record, so two replays agree only when they read the same
// records in the same order.
func recordingReducer(st State, rec Record) (State, error) {
	if st.Tasks == nil {
		st.Tasks = map[string]Task{}
	}
	id := fmt.Sprintf("%020d", rec.Seq)
	st.Tasks[id] = Task{ID: id, State: rec.Type, Payload: rec.Payload}
	return st, nil
}

func TestV010LogReplaysTheSameAfterRotation(t *testing.T) {
	withSegmentRecords(t, 2)
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "testdata", "v0.1.0", "eventlog"))); err != nil {
		t.Fatalf("CopyFS() = %v, want nil", err)
	}
	limits := testLimits()
	signer := testSigner(t, v010Seed(), limits.Clock)
	before, err := Replay(testContext(t), dir, signer.PublicKey(), limits, recordingReducer)
	if err != nil {
		t.Fatalf("Replay(v0.1.0) = %v, want nil", err)
	}
	if len(before.Tasks) != 4 {
		t.Fatalf("v0.1.0 records = %d, want 4", len(before.Tasks))
	}
	if moved := rotate(t, openWriter(t, dir, signer, limits)); moved != 2 {
		t.Fatalf("Rotate() moved %d, want 2", moved)
	}
	after, err := Replay(testContext(t), dir, signer.PublicKey(), limits, recordingReducer)
	if err != nil {
		t.Fatalf("Replay(rotated v0.1.0) = %v, want nil", err)
	}
	beforeBytes, err := before.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := after.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatalf("rotated v0.1.0 replay = %s, want %s", afterBytes, beforeBytes)
	}
}

func TestRotateRefusesARecordWithALineBreak(t *testing.T) {
	withSegmentRecords(t, 3)
	dir, signer, limits := testLog(t)
	w := openWriter(t, dir, signer, limits)
	appendTasks(t, dir, w, 4)
	writeFile(t, eventPath(eventsDir(dir), 2), raw("{\n}"), 0o600)
	_, err := w.Rotate(testContext(t))
	assertErrContains(t, err, "rotate seq 2", "line break")
	if names := segmentNames(t, dir); len(names) != 0 {
		t.Fatalf("segments after refused rotation = %v, want none", names)
	}
}
