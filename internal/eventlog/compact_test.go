package eventlog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

const testFold = "tasks"

func taskFolds(floor func(State) uint64) []Fold {
	return []Fold{{Name: testFold, Reduce: TaskReducer, Floor: floor}}
}

func compact(t *testing.T, w *Writer, folds []Fold) uint64 {
	t.Helper()
	base, err := w.Compact(testContext(t), folds, testGitRecord())
	if err != nil {
		t.Fatalf("Compact() = %v, want nil", err)
	}
	return base
}

func replayFold(t *testing.T, dir string, w *Writer) State {
	t.Helper()
	st, err := ReplayFold(testContext(t), dir, w.signer.PublicKey(), w.limits, testFold, TaskReducer)
	if err != nil {
		t.Fatalf("ReplayFold() = %v, want nil", err)
	}
	return st
}

func assertSameState(t *testing.T, got State, want State) {
	t.Helper()
	gotBytes, err := got.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := want.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("state = %s, want %s", gotBytes, wantBytes)
	}
}

// compactedLog is gitLog recorded in Git and compacted: base at 9, records 10 and 11 left
// as files. It also returns the state a full replay gave before compaction.
func compactedLog(t *testing.T) (string, string, *Writer, State) {
	t.Helper()
	repo, dir, w := gitLog(t)
	full, err := Replay(testContext(t), dir, w.signer.PublicKey(), w.limits, TaskReducer)
	if err != nil {
		t.Fatalf("Replay() = %v, want nil", err)
	}
	recordInGit(t, w)
	if base := compact(t, w, taskFolds(nil)); base != 9 {
		t.Fatalf("Compact() base = %d, want 9", base)
	}
	return repo, dir, w, full
}

func TestCompactRemovesHeldSegmentsAndReplaysTheSameState(t *testing.T) {
	_, dir, w, full := compactedLog(t)
	if names := segmentNames(t, dir); len(names) != 0 {
		t.Fatalf("segments after Compact() = %v, want none", names)
	}
	for _, seq := range []uint64{10, 11} {
		if _, err := os.Stat(eventPath(eventsDir(dir), seq)); err != nil {
			t.Fatalf("record %d after Compact() = %v, want kept", seq, err)
		}
	}
	assertSameState(t, replayFold(t, dir, w), full)
}

func TestCompactKeepsSegmentsNoCommitHolds(t *testing.T) {
	_, dir, w := gitLog(t)
	if base := compact(t, w, taskFolds(nil)); base != 0 {
		t.Fatalf("Compact() without a commit base = %d, want 0", base)
	}
	if got := len(segmentNames(t, dir)); got != 3 {
		t.Fatalf("segments = %d, want 3", got)
	}
	recordInGit(t, w)
	appendTasks(t, dir, w, 3)
	rotate(t, w)
	if base := compact(t, w, taskFolds(nil)); base != 9 {
		t.Fatalf("Compact() with segment 10-12 uncommitted base = %d, want 9", base)
	}
	if got := segmentNames(t, dir); len(got) != 1 || got[0] != segmentFileName(10, 12) {
		t.Fatalf("segments = %v, want only 10-12", got)
	}
}

func TestCompactKeepsASegmentThatDiffersFromItsCommit(t *testing.T) {
	_, dir, w := gitLog(t)
	recordInGit(t, w)
	seg := filepath.Join(segmentsDir(eventsDir(dir)), segmentFileName(4, 6))
	body := readText(t, seg)
	plant(t, seg, body+"\n")
	base, err := w.Compact(testContext(t), taskFolds(nil), testGitRecord())
	if err != nil {
		t.Fatalf("Compact() = %v, want nil", err)
	}
	if base != 3 {
		t.Fatalf("Compact() base = %d, want 3: segment 4-6 differs from its commit", base)
	}
}

func TestCompactStopsAtTheFloorOfEveryFold(t *testing.T) {
	_, dir, w := gitLog(t)
	recordInGit(t, w)
	folds := append(taskFolds(func(State) uint64 { return 8 }),
		Fold{Name: "other", Reduce: TaskReducer, Floor: func(State) uint64 { return 5 }})
	if base := compact(t, w, folds); base != 3 {
		t.Fatalf("Compact() base = %d, want 3 (floor 5)", base)
	}
	if got := len(segmentNames(t, dir)); got != 2 {
		t.Fatalf("segments = %d, want 2", got)
	}
}

func TestCompactRefusesBadFolds(t *testing.T) {
	_, _, w := gitLog(t)
	cases := map[string][]Fold{
		"no folds":  nil,
		"no name":   {{Reduce: TaskReducer}},
		"bad name":  {{Name: "Tasks", Reduce: TaskReducer}},
		"no reduce": {{Name: testFold}},
		"twice":     {{Name: testFold, Reduce: TaskReducer}, {Name: testFold, Reduce: TaskReducer}},
	}
	for name, folds := range cases {
		if _, err := w.Compact(testContext(t), folds, testGitRecord()); err == nil {
			t.Fatalf("Compact(%s) = nil, want error", name)
		}
	}
}

func TestReplayWithoutAFoldFailsOnACompactedLog(t *testing.T) {
	_, dir, w, _ := compactedLog(t)
	_, err := Replay(testContext(t), dir, w.signer.PublicKey(), w.limits, TaskReducer)
	assertErrContains(t, err, "compacted through seq 9")
}

func TestReplayOfAFoldTheBaseLacksFailsWithTheSeq(t *testing.T) {
	_, dir, w, _ := compactedLog(t)
	_, err := ReplayFold(testContext(t), dir, w.signer.PublicKey(), w.limits, "units", TaskReducer)
	assertErrContains(t, err, "base at seq 9", `fold "units"`)
}

func TestTamperedBaseFailsClosed(t *testing.T) {
	_, dir, w, _ := compactedLog(t)
	path := basePath(dir, 9)
	good := readText(t, path)
	var b Base
	if err := json.Unmarshal([]byte(good), &b); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(Base) Base{
		"state": func(b Base) Base {
			b.States = map[string]json.RawMessage{testFold: json.RawMessage(`{"tasks":{}}`)}
			return b
		},
		"seq":     func(b Base) Base { b.Seq = 8; return b },
		"hash":    func(b Base) Base { b.Hash = zeroHash; return b },
		"command": func(b Base) Base { r := *b.Receipt; r.Command = CheckpointCommand; b.Receipt = &r; return b },
		"receipt": func(b Base) Base { b.Receipt = nil; return b },
	}
	for name, change := range cases {
		body, err := canonicalJSON(change(b))
		if err != nil {
			t.Fatal(err)
		}
		plant(t, path, string(body))
		if _, err = ReplayFold(testContext(t), dir, w.signer.PublicKey(), w.limits, testFold, TaskReducer); err == nil {
			t.Fatalf("ReplayFold(base with changed %s) = nil, want error", name)
		}
	}
	// A valid signature under another command is still not a base.
	unsigned, err := canonicalJSON(unsignedBase{Seq: b.Seq, Hash: b.Hash, States: b.States})
	if err != nil {
		t.Fatal(err)
	}
	r, err := w.signer.Sign(corereceipt.Run{Command: CheckpointCommand, Output: unsigned})
	if err != nil {
		t.Fatal(err)
	}
	body, err := canonicalJSON(Base{Seq: b.Seq, Hash: b.Hash, States: b.States, Receipt: r})
	if err != nil {
		t.Fatal(err)
	}
	plant(t, path, string(body))
	_, err = ReplayFold(testContext(t), dir, w.signer.PublicKey(), w.limits, testFold, TaskReducer)
	assertErrContains(t, err, path, "receipt command")
	plant(t, path, good+" ")
	_, err = ReplayFold(testContext(t), dir, w.signer.PublicKey(), w.limits, testFold, TaskReducer)
	assertErrContains(t, err, path)
	plant(t, path, good)
	other := testSigner(t, bytes.Repeat([]byte{7}, 32), w.limits.Clock)
	_, err = ReplayFold(testContext(t), dir, other.PublicKey(), w.limits, testFold, TaskReducer)
	assertErrContains(t, err, path)
}

// basePath is the file of the base at seq of the log in dir.
func basePath(dir string, seq uint64) string {
	return filepath.Join(dir, "checkpoints", BaseName, checkpointFileName(seq))
}

func TestBaseUnderAnotherFileNameFails(t *testing.T) {
	_, dir, w, _ := compactedLog(t)
	if err := os.Rename(basePath(dir, 9), basePath(dir, 10)); err != nil {
		t.Fatal(err)
	}
	_, err := ReplayFold(testContext(t), dir, w.signer.PublicKey(), w.limits, testFold, TaskReducer)
	assertErrContains(t, err, basePath(dir, 10), "holds seq 9")
}

func TestBrokenNewestBaseNeverFallsBackToAnOlderOne(t *testing.T) {
	_, dir, w, _ := compactedLog(t)
	good := readText(t, basePath(dir, 9))
	plant(t, basePath(dir, 3), good)
	plant(t, basePath(dir, 9), good+" ")
	_, err := ReplayFold(testContext(t), dir, w.signer.PublicKey(), w.limits, testFold, TaskReducer)
	assertErrContains(t, err, basePath(dir, 9))
}

func TestUnexpectedEntryBesideTheBaseFails(t *testing.T) {
	_, dir, w, _ := compactedLog(t)
	for _, name := range []string{"base.json", checkpointFileName(0)} {
		path := filepath.Join(dir, "checkpoints", BaseName, name)
		plant(t, path, "{}")
		_, err := ReplayFold(testContext(t), dir, w.signer.PublicKey(), w.limits, testFold, TaskReducer)
		assertErrContains(t, err, "unexpected entry "+name)
		if err = os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	// A dot file is a temporary file of an interrupted write.
	plant(t, filepath.Join(dir, "checkpoints", BaseName, ".tmp-base"), "{}")
	replayFold(t, dir, w)
}

func TestBaseNameIsReserved(t *testing.T) {
	_, dir, w, _ := compactedLog(t)
	if err := w.WriteCheckpoint(testContext(t), BaseName, 9, zeroHash, State{}); err == nil {
		t.Fatal("WriteCheckpoint(base) = nil, want error")
	}
	if _, err := ReplayFold(testContext(t), dir, w.signer.PublicKey(), w.limits, BaseName, TaskReducer); err == nil {
		t.Fatal("ReplayFold(base) = nil, want error")
	}
	if _, _, err := ReplayCheckpointed(testContext(t), dir, w.signer.PublicKey(), w.limits, BaseName, TaskReducer); err == nil {
		t.Fatal("ReplayCheckpointed(base) = nil, want error")
	}
	folds := taskFolds(nil)
	folds[0].Name = BaseName
	if _, err := w.Compact(testContext(t), folds, testGitRecord()); err == nil {
		t.Fatal("Compact(fold named base) = nil, want error")
	}
}

func TestBaseHashBindsTheNextRecord(t *testing.T) {
	_, dir, w, _ := compactedLog(t)
	replayFold(t, dir, w)
	b, _, err := readBase(eventsDir(dir), w.signer.PublicKey(), w.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.writeBase(b.Seq, hashHex([]byte("another record")), b.States); err != nil {
		t.Fatal(err)
	}
	_, err = ReplayFold(testContext(t), dir, w.signer.PublicKey(), w.limits, testFold, TaskReducer)
	assertErrContains(t, err, "seq 10", "prev mismatch")
}

func TestInterruptedCompactionReplaysAndTheNextCompactFinishesIt(t *testing.T) {
	_, dir, w := gitLog(t)
	recordInGit(t, w)
	kept := copyLog(t, dir)
	full := compact(t, w, taskFolds(nil))
	if full != 9 {
		t.Fatalf("Compact() base = %d, want 9", full)
	}
	// Put the covered segments back, as a crash after the base was written leaves them.
	for _, name := range segmentNames(t, kept) {
		plant(t, filepath.Join(segmentsDir(eventsDir(dir)), name), readText(t, filepath.Join(segmentsDir(eventsDir(kept)), name)))
	}
	want := replayFold(t, kept, w)
	assertSameState(t, replayFold(t, dir, w), want)
	if base := compact(t, w, taskFolds(nil)); base != 9 {
		t.Fatalf("Compact() again base = %d, want 9", base)
	}
	if names := segmentNames(t, dir); len(names) != 0 {
		t.Fatalf("segments after the second Compact() = %v, want none", names)
	}
}

func TestSegmentAcrossTheBaseFails(t *testing.T) {
	_, dir, w, _ := compactedLog(t)
	plant(t, filepath.Join(segmentsDir(eventsDir(dir)), segmentFileName(8, 10)), "x\ny\nz\n")
	_, err := ReplayFold(testContext(t), dir, w.signer.PublicKey(), w.limits, testFold, TaskReducer)
	assertErrContains(t, err, segmentFileName(8, 10))
}

func TestCompactedLogAppendsRotatesAndRecordsTheBase(t *testing.T) {
	repo, dir, w, _ := compactedLog(t)
	appendTasks(t, dir, w, 4)
	if moved := rotate(t, w); moved != 3 {
		t.Fatalf("Rotate() after Compact() moved %d, want 3", moved)
	}
	if got := segmentNames(t, dir); len(got) != 1 || got[0] != segmentFileName(10, 12) {
		t.Fatalf("segments = %v, want only 10-12", got)
	}
	commit := recordInGit(t, w)
	files := committedContent(t, repo, commit)
	baseFile := "checkpoints/base/" + checkpointFileName(9)
	if _, ok := files[baseFile]; !ok {
		t.Fatalf("commit %s holds no %s", commit, baseFile)
	}
	if base := compact(t, w, taskFolds(nil)); base != 12 {
		t.Fatalf("Compact() base = %d, want 12", base)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "checkpoints", BaseName)); err != nil || len(entries) != 1 || entries[0].Name() != checkpointFileName(12) {
		t.Fatalf("bases after the second Compact() = %v (%v), want only seq 12", entries, err)
	}
	st := replayFold(t, dir, w)
	if len(st.Tasks) != 11 {
		t.Fatalf("tasks after two compactions = %d, want 11 (names repeat from task-0)", len(st.Tasks))
	}
}

func TestMissingHeadWithABaseRefusesAppend(t *testing.T) {
	_, dir, w, _ := compactedLog(t)
	if err := os.Remove(filepath.Join(eventsDir(dir), "HEAD.json")); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []uint64{10, 11} {
		if err := os.Remove(eventPath(eventsDir(dir), seq)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := w.Append(testContext(t), Event{Type: "task.created", TaskID: "x", Payload: raw(`{}`)})
	assertErrContains(t, err, "missing HEAD", "base at seq 9")
}

func TestCheckpointAtOrBelowTheBaseIsSkipped(t *testing.T) {
	_, dir, w := gitLog(t)
	recordInGit(t, w)
	compact(t, w, taskFolds(nil))
	b, _, err := readBase(eventsDir(dir), w.signer.PublicKey(), w.limits)
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err = json.Unmarshal(b.States[testFold], &st); err != nil {
		t.Fatal(err)
	}
	if err = w.WriteCheckpoint(testContext(t), testFold, 9, b.Hash, st); err != nil {
		t.Fatalf("WriteCheckpoint() = %v, want nil", err)
	}
	if err = os.Remove(filepath.Join(dir, "checkpoints", testFold, checkpointFileName(10))); err != nil {
		t.Fatal(err)
	}
	_, use, err := ReplayCheckpointed(testContext(t), dir, w.signer.PublicKey(), w.limits, testFold, TaskReducer)
	if err != nil {
		t.Fatalf("ReplayCheckpointed() = %v, want nil", err)
	}
	if use.Seq != 0 || use.Diverged != nil || len(use.Skipped) != 1 {
		t.Fatalf("checkpoint use = %+v, want the checkpoint at 9 skipped, none used", use)
	}
	if !strings.Contains(use.Skipped[0], "below the base at seq 9") {
		t.Fatalf("skipped = %q, want the base named", use.Skipped[0])
	}
}

func TestCheckpointPastTheBaseIsUsed(t *testing.T) {
	_, dir, w, _ := compactedLog(t)
	_, use, err := ReplayCheckpointed(testContext(t), dir, w.signer.PublicKey(), w.limits, testFold, TaskReducer)
	if err != nil {
		t.Fatalf("ReplayCheckpointed() = %v, want nil", err)
	}
	if use.Seq != 10 {
		t.Fatalf("checkpoint use = %+v, want seq 10", use)
	}
}

func TestV010LogReplaysTheSameAfterCompaction(t *testing.T) {
	withSegmentRecords(t, 2)
	repo := t.TempDir()
	gitAt(t, repo, "init", "--quiet", "--initial-branch=main")
	dir := filepath.Join(repo, "log")
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "testdata", "v0.1.0", "eventlog"))); err != nil {
		t.Fatalf("CopyFS() = %v, want nil", err)
	}
	limits := testLimits()
	signer := testSigner(t, v010Seed(), limits.Clock)
	before, err := Replay(testContext(t), dir, signer.PublicKey(), limits, recordingReducer)
	if err != nil {
		t.Fatalf("Replay(v0.1.0) = %v, want nil", err)
	}
	w := openWriter(t, dir, signer, limits)
	rotate(t, w)
	recordInGit(t, w)
	folds := []Fold{{Name: "recording", Reduce: recordingReducer}}
	if base := compact(t, w, folds); base != 2 {
		t.Fatalf("Compact(v0.1.0) base = %d, want 2", base)
	}
	after, err := ReplayFold(testContext(t), dir, signer.PublicKey(), limits, "recording", recordingReducer)
	if err != nil {
		t.Fatalf("ReplayFold(compacted v0.1.0) = %v, want nil", err)
	}
	assertSameState(t, after, before)
}
