package eventlog

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golusoris/golusoris/core/clock"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

func TestAppendReplayRoundTrip(t *testing.T) {
	dir, signer, limits := testLog(t)
	rec1 := appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open","title":"Write log"}`)})
	rec2 := appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"done"}`)})
	if rec1.Seq != 1 || rec2.Seq != 2 {
		t.Fatalf("seqs = %d,%d, want 1,2", rec1.Seq, rec2.Seq)
	}
	st := replayTasks(t, dir, signer.PublicKey(), limits)
	if st.Tasks["task-1"].State != "done" {
		t.Fatalf("state = %+v, want task-1 done", st)
	}
}

func TestReplayTwiceIdenticalJCS(t *testing.T) {
	dir, signer, limits := testLog(t)
	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-2", Payload: raw(`{"state":"ready"}`)})
	first := replayStateBytes(t, dir, signer.PublicKey(), limits)
	second := replayStateBytes(t, dir, signer.PublicKey(), limits)
	if !bytes.Equal(first, second) {
		t.Fatalf("replay bytes differ\nfirst=%s\nsecond=%s", first, second)
	}
	want := `{"tasks":{"task-1":{"id":"task-1","payload":{"state":"open"},"state":"open"},"task-2":{"id":"task-2","payload":{"state":"ready"},"state":"ready"}}}`
	if string(first) != want {
		t.Fatalf("replay JCS = %s, want %s", first, want)
	}
}

func TestGoldenFixtureReplay(t *testing.T) {
	dir := filepath.Join("testdata", "golden")
	limits := testLimits()
	pub := strings.TrimSpace(readText(t, filepath.Join(dir, "public_key.txt")))
	st := replayTasks(t, dir, pub, limits)
	got, err := st.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() = %v, want nil", err)
	}
	want := strings.TrimSpace(readText(t, filepath.Join(dir, "state.jcs")))
	if string(got) != want {
		t.Fatalf("golden state = %s, want %s", got, want)
	}
}

func TestTamperedPayloadByteNamesSeq(t *testing.T) {
	dir, signer, limits := testLog(t)
	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	replaceInFile(t, eventPath(eventsDir(dir), 1), "open", "done")
	_, err := Replay(testContext(t), dir, signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "seq 1", "verify receipt")
}

func TestTornFileNamesSeqAndFile(t *testing.T) {
	dir, signer, limits := testLog(t)
	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	path := eventPath(eventsDir(dir), 1)
	data := []byte(readText(t, path))
	writeFile(t, path, data[:len(data)/2], 0o600)
	_, err := Replay(testContext(t), dir, signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "seq 1", "00000000000000000001.json", "canonicalize")
}

func TestGapMissingMiddleFile(t *testing.T) {
	dir, signer, limits := testLog(t)
	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"ready"}`)})
	appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"done"}`)})
	if err := os.Remove(eventPath(eventsDir(dir), 2)); err != nil {
		t.Fatalf("Remove(seq2) = %v, want nil", err)
	}
	_, err := Replay(testContext(t), dir, signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "gap at seq 2")
}

func TestDeletedTailHeadMismatch(t *testing.T) {
	dir, signer, limits := testLog(t)
	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"done"}`)})
	if err := os.Remove(eventPath(eventsDir(dir), 2)); err != nil {
		t.Fatalf("Remove(seq2) = %v, want nil", err)
	}
	_, err := Replay(testContext(t), dir, signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "HEAD mismatch")
}

func TestNonCanonicalRecordRefused(t *testing.T) {
	dir, signer, limits := testLog(t)
	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	path := eventPath(eventsDir(dir), 1)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(readText(t, path)), "", "  "); err != nil {
		t.Fatalf("Indent(record) = %v, want nil", err)
	}
	writeFile(t, path, pretty.Bytes(), 0o600)
	_, err := Replay(testContext(t), dir, signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "non-canonical")
}

func TestWrongPublicKeyFailsAtSeqOne(t *testing.T) {
	dir, signer, limits := testLog(t)
	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	wrong := testSigner(t, bytes.Repeat([]byte{9}, 32), limits.Clock).PublicKey()
	_, err := Replay(testContext(t), dir, wrong, limits, TaskReducer)
	assertErrContains(t, err, "seq 1", "public key")
}

func TestRecordOver64KiBRefusedAtAppend(t *testing.T) {
	dir, signer, limits := testLog(t)
	large := `{"state":"open","blob":"` + strings.Repeat("x", MaxRecordBytes) + `"}`
	writer := openWriter(t, dir, signer, limits)
	_, err := writer.Append(testContext(t), Event{Type: "task.created", TaskID: "task-1", Payload: raw(large)})
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("Append(large) = %v, want ErrRecordTooLarge", err)
	}
}

func TestLockTimeoutFailsClosed(t *testing.T) {
	dir, signer, limits := testLog(t)
	if err := os.MkdirAll(eventsDir(dir), 0o750); err != nil {
		t.Fatalf("MkdirAll(events) = %v, want nil", err)
	}
	lock, err := acquireLock(testContext(t), eventsDir(dir), time.Second)
	if err != nil {
		t.Fatalf("acquireLock() = %v, want nil", err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			t.Fatalf("lock.Close() = %v, want nil", err)
		}
	}()
	limits.LockTimeout = 20 * time.Millisecond
	_, err = openWriter(t, dir, signer, limits).Append(testContext(t), Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("Append(locked) = %v, want ErrLockTimeout", err)
	}
}

func TestConcurrentAppendersContiguous(t *testing.T) {
	dir, signer, limits := testLog(t)
	const total = 16
	var wg sync.WaitGroup
	errs := make(chan error, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ev := Event{Type: "task.created", TaskID: "task-" + jsonInt(id), Payload: raw(`{"state":"open"}`)}
			_, err := openWriter(t, dir, signer, limits).Append(testContext(t), ev)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Append() = %v, want nil", err)
		}
	}
	seqs := eventSeqs(t, eventsDir(dir))
	if len(seqs) != total {
		t.Fatalf("records = %v, want %d", seqs, total)
	}
	for i, seq := range seqs {
		if seq != uint64(i+1) {
			t.Fatalf("seqs = %v, want contiguous", seqs)
		}
	}
}

func TestKeyInRepoTreeRefused(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("Mkdir(.git) = %v, want nil", err)
	}
	keyPath := filepath.Join(repo, "seed.hex")
	writeKey(t, keyPath, 0o600)
	_, err := NewSignerFromKeyFile(testContext(t), keyPath, filepath.Join(repo, ".tribunus"), testLimits().Clock)
	assertErrContains(t, err, "inside git working tree")
}

func TestKeyMode0644Refused(t *testing.T) {
	base := t.TempDir()
	keyPath := filepath.Join(base, "seed.hex")
	writeKey(t, keyPath, 0o644)
	_, err := NewSignerFromKeyFile(testContext(t), keyPath, filepath.Join("/var/tmp", "tribunus-eventlog-test"), testLimits().Clock)
	assertErrContains(t, err, "group or other")
}

func TestMaxReplayRecordsExceeded(t *testing.T) {
	dir, signer, limits := testLog(t)
	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"done"}`)})
	limits.MaxReplayRecords = 1
	_, err := Replay(testContext(t), dir, signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "max replay records")
}

func TestUnknownEventTypeFailsWithPosition(t *testing.T) {
	dir, signer, limits := testLog(t)
	appendEvent(t, dir, signer, limits, Event{Type: "task.unknown", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	_, err := Replay(testContext(t), dir, signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "seq 1", "unknown event type")
}

func testLog(t *testing.T) (string, *corereceipt.Signer, Limits) {
	t.Helper()
	limits := testLimits()
	return t.TempDir(), testSigner(t, testSeed(), limits.Clock), limits
}

func testLimits() Limits {
	return Limits{Clock: clock.NewFake(), LockTimeout: time.Second, MaxReplayRecords: 1000, MaxRecordBytes: MaxRecordBytes}
}

func testSigner(t *testing.T, seed []byte, clk clock.Clock) *corereceipt.Signer {
	t.Helper()
	signer, err := corereceipt.NewSignerFromSeed(seed, clk)
	if err != nil {
		t.Fatalf("NewSignerFromSeed() = %v, want nil", err)
	}
	return signer
}

func testSeed() []byte {
	seed := make([]byte, 32)
	for i := 0; i < len(seed); i++ {
		seed[i] = byte(i + 1)
	}
	return seed
}

func openWriter(t *testing.T, dir string, signer *corereceipt.Signer, limits Limits) *Writer {
	t.Helper()
	writer, err := Open(dir, signer, limits)
	if err != nil {
		t.Fatalf("Open() = %v, want nil", err)
	}
	return writer
}

func appendEvent(t *testing.T, dir string, signer *corereceipt.Signer, limits Limits, ev Event) Record {
	t.Helper()
	rec, err := openWriter(t, dir, signer, limits).Append(testContext(t), ev)
	if err != nil {
		t.Fatalf("Append() = %v, want nil", err)
	}
	return rec
}

func replayTasks(t *testing.T, dir string, pub string, limits Limits) State {
	t.Helper()
	st, err := Replay(testContext(t), dir, pub, limits, TaskReducer)
	if err != nil {
		t.Fatalf("Replay() = %v, want nil", err)
	}
	return st
}

func replayStateBytes(t *testing.T, dir string, pub string, limits Limits) []byte {
	t.Helper()
	out, err := replayTasks(t, dir, pub, limits).CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() = %v, want nil", err)
	}
	return out
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func raw(s string) []byte {
	return []byte(s)
}

func readText(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) = %v, want nil", path, err)
	}
	return string(body)
}

func writeFile(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatalf("WriteFile(%s) = %v, want nil", path, err)
	}
}

func replaceInFile(t *testing.T, path string, old string, repl string) {
	t.Helper()
	data := strings.Replace(readText(t, path), old, repl, 1)
	writeFile(t, path, []byte(data), 0o600)
}

func assertErrContains(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want failure")
	}
	text := err.Error()
	for _, part := range parts {
		if !strings.Contains(text, part) {
			t.Fatalf("error %q does not contain %q", text, part)
		}
	}
}

func eventSeqs(t *testing.T, dir string) []uint64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s) = %v, want nil", dir, err)
	}
	seqs := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		if isEventRecord(entry.Name()) {
			seq, err := seqFromName(entry.Name())
			if err != nil {
				t.Fatalf("seqFromName(%s) = %v, want nil", entry.Name(), err)
			}
			seqs = append(seqs, seq)
		}
	}
	sort.Slice(seqs, func(i int, j int) bool { return seqs[i] < seqs[j] })
	return seqs
}

func writeKey(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	body := []byte(hex.EncodeToString(testSeed()))
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatalf("WriteFile(key) = %v, want nil", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod(key) = %v, want nil", err)
	}
}

func jsonInt(v int) string {
	body, err := json.Marshal(v)
	if err != nil {
		return "0"
	}
	return string(body)
}

// twoLogs writes two three-record logs signed with the same key whose
// records differ only in their task ids. A record or HEAD copied from one
// into the other therefore carries a valid signature, and only the chain,
// sequence and HEAD checks can refuse it.
func twoLogs(t *testing.T) (string, string, *corereceipt.Signer, Limits) {
	t.Helper()
	a, signer, limits := testLog(t)
	b, _, _ := testLog(t)
	for i := 0; i < 3; i++ {
		appendEvent(t, a, signer, limits, Event{Type: "task.created", TaskID: fmt.Sprintf("a-%d", i), Payload: raw(`{"state":"open"}`)})
		appendEvent(t, b, signer, limits, Event{Type: "task.created", TaskID: fmt.Sprintf("b-%d", i), Payload: raw(`{"state":"open"}`)})
	}
	return a, b, signer, limits
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	body, err := os.ReadFile(from)
	if err != nil {
		t.Fatalf("ReadFile(%s) = %v", from, err)
	}
	writeFile(t, to, body, 0o600)
}

func TestSplicedSignedRecordBreaksChain(t *testing.T) {
	a, b, signer, limits := twoLogs(t)
	copyFile(t, eventPath(eventsDir(b), 2), eventPath(eventsDir(a), 2))
	_, err := Replay(testContext(t), a, signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "seq 2", "prev mismatch")
}

func TestRecordSeqMustMatchItsFile(t *testing.T) {
	a, b, signer, limits := twoLogs(t)
	copyFile(t, eventPath(eventsDir(b), 3), eventPath(eventsDir(a), 2))
	_, err := Replay(testContext(t), a, signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "seq 2", "record seq 3")
}

func TestHeadReceiptMustSignThisHead(t *testing.T) {
	a, b, signer, limits := twoLogs(t)
	var headA, headB map[string]any
	for _, h := range []struct {
		dir  string
		into *map[string]any
	}{{a, &headA}, {b, &headB}} {
		dec := json.NewDecoder(strings.NewReader(readText(t, filepath.Join(eventsDir(h.dir), "HEAD.json"))))
		dec.UseNumber()
		if err := dec.Decode(h.into); err != nil {
			t.Fatalf("decode HEAD of %s: %v", h.dir, err)
		}
	}
	headA["receipt"] = headB["receipt"]
	forged, err := canonicalJSON(headA)
	if err != nil {
		t.Fatalf("canonicalJSON(forged HEAD) = %v", err)
	}
	writeFile(t, filepath.Join(eventsDir(a), "HEAD.json"), forged, 0o600)
	_, err = Replay(testContext(t), a, signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "HEAD.json", "verify")
}

func TestHeadHashMustMatchTail(t *testing.T) {
	a, b, signer, limits := twoLogs(t)
	copyFile(t, filepath.Join(eventsDir(b), "HEAD.json"), filepath.Join(eventsDir(a), "HEAD.json"))
	_, err := Replay(testContext(t), a, signer.PublicKey(), limits, TaskReducer)
	assertErrContains(t, err, "HEAD mismatch", "hash mismatch")
}
