// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

package eventlog

import (
	"os"
	"path/filepath"
	"testing"

	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

func TestCheckpointPrefixCorruptionImmunity(t *testing.T) {
	dir, signer, limits := testLog(t)
	writer := openWriter(t, dir, signer, limits)
	ctx := testContext(t)

	rec1 := appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	rec2 := appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"ready"}`)})
	appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"done"}`)})

	stAfterSeq2 := State{Tasks: map[string]Task{"task-1": {ID: "task-1", State: "ready", Payload: raw(`{"state":"open"}`)}}}
	path2 := eventPath(eventsDir(dir), rec2.Seq)
	hash2 := hashHex([]byte(readText(t, path2)))

	if err := writer.WriteCheckpoint(ctx, "tasks", rec2.Seq, hash2, stAfterSeq2); err != nil {
		t.Fatalf("WriteCheckpoint() = %v, want nil", err)
	}

	// Corrupt payload of record 1 on disk.
	replaceInFile(t, eventPath(eventsDir(dir), rec1.Seq), "open", "corr")

	// Full Replay must fail because record 1 is corrupted.
	if _, err := Replay(ctx, dir, signer.PublicKey(), limits, TaskReducer); err == nil {
		t.Fatal("Replay() succeeded on corrupted prefix, want error")
	}

	// ReplayCheckpointed must succeed from seq 2 because prefix is skipped.
	st, use, err := ReplayCheckpointed(ctx, dir, signer.PublicKey(), limits, "tasks", TaskReducer)
	if err != nil {
		t.Fatalf("ReplayCheckpointed() = %v, want nil", err)
	}
	if use.Seq != rec2.Seq {
		t.Fatalf("use.Seq = %d, want %d", use.Seq, rec2.Seq)
	}
	if st.Tasks["task-1"].State != "done" {
		t.Fatalf("task-1 state = %q, want done", st.Tasks["task-1"].State)
	}
}

func TestCheckpointTailTamperRefusal(t *testing.T) {
	dir, signer, limits := testLog(t)
	writer := openWriter(t, dir, signer, limits)
	ctx := testContext(t)

	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	rec2 := appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"ready"}`)})
	rec3 := appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"done"}`)})
	appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"closed"}`)})

	stAfterSeq2 := State{Tasks: map[string]Task{"task-1": {ID: "task-1", State: "ready", Payload: raw(`{"state":"open"}`)}}}
	hash2 := hashHex([]byte(readText(t, eventPath(eventsDir(dir), rec2.Seq))))
	if err := writer.WriteCheckpoint(ctx, "tasks", rec2.Seq, hash2, stAfterSeq2); err != nil {
		t.Fatalf("WriteCheckpoint() = %v, want nil", err)
	}

	// Tamper with record 3, after the checkpoint and before the last record: HEAD names the
	// last record only, so this is found by the record checks of the tail alone.
	replaceInFile(t, eventPath(eventsDir(dir), rec3.Seq), "done", "fail")

	// ReplayCheckpointed must fail on receipt/hash mismatch.
	if _, _, err := ReplayCheckpointed(ctx, dir, signer.PublicKey(), limits, "tasks", TaskReducer); err == nil {
		t.Fatal("ReplayCheckpointed() succeeded on tampered tail record, want error")
	}
}

func TestCheckpointTailHeadMismatchRefusal(t *testing.T) {
	dir, signer, limits := testLog(t)
	writer := openWriter(t, dir, signer, limits)
	ctx := testContext(t)

	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	rec2 := appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"ready"}`)})
	rec3 := appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"done"}`)})

	stAfterSeq2 := State{Tasks: map[string]Task{"task-1": {ID: "task-1", State: "ready", Payload: raw(`{"state":"open"}`)}}}
	hash2 := hashHex([]byte(readText(t, eventPath(eventsDir(dir), rec2.Seq))))
	if err := writer.WriteCheckpoint(ctx, "tasks", rec2.Seq, hash2, stAfterSeq2); err != nil {
		t.Fatalf("WriteCheckpoint() = %v, want nil", err)
	}

	// Remove record 3 so tail does not reach HEAD.
	if err := os.Remove(eventPath(eventsDir(dir), rec3.Seq)); err != nil {
		t.Fatalf("Remove(seq3) = %v", err)
	}

	if _, _, err := ReplayCheckpointed(ctx, dir, signer.PublicKey(), limits, "tasks", TaskReducer); err == nil {
		t.Fatal("ReplayCheckpointed() succeeded with missing tail, want error")
	}
}

func TestCheckpointForgedSkipped(t *testing.T) {
	dir, signer, limits := testLog(t)
	writer := openWriter(t, dir, signer, limits)
	ctx := testContext(t)

	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	rec2 := appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"done"}`)})

	stAfterSeq2 := State{Tasks: map[string]Task{"task-1": {ID: "task-1", State: "done", Payload: raw(`{"state":"open"}`)}}}
	hash2 := hashHex([]byte(readText(t, eventPath(eventsDir(dir), rec2.Seq))))
	if err := writer.WriteCheckpoint(ctx, "tasks", rec2.Seq, hash2, stAfterSeq2); err != nil {
		t.Fatalf("WriteCheckpoint() = %v, want nil", err)
	}

	ckptPath := filepath.Join(dir, "checkpoints", "tasks", checkpointFileName(rec2.Seq))
	// Tamper with state in checkpoint file without valid signature.
	replaceInFile(t, ckptPath, "done", "tampered")

	st, use, err := ReplayCheckpointed(ctx, dir, signer.PublicKey(), limits, "tasks", TaskReducer)
	if err != nil {
		t.Fatalf("ReplayCheckpointed() = %v, want nil", err)
	}
	if use.Seq != 0 {
		t.Fatalf("use.Seq = %d, want 0 (full replay)", use.Seq)
	}
	if len(use.Skipped) == 0 {
		t.Fatal("use.Skipped is empty, want forged checkpoint listed")
	}
	if st.Tasks["task-1"].State != "done" {
		t.Fatalf("task-1 state = %q, want done", st.Tasks["task-1"].State)
	}
}

func TestCheckpointPruningKeepsAtMostThree(t *testing.T) {
	dir, signer, limits := testLog(t)
	writer := openWriter(t, dir, signer, limits)
	ctx := testContext(t)

	st := State{Tasks: map[string]Task{}}
	for i := 1; i <= 5; i++ {
		rec := appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
		hash := hashHex([]byte(readText(t, eventPath(eventsDir(dir), rec.Seq))))
		if err := writer.WriteCheckpoint(ctx, "tasks", rec.Seq, hash, st); err != nil {
			t.Fatalf("WriteCheckpoint(%d) = %v, want nil", rec.Seq, err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(dir, "checkpoints", "tasks"))
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("checkpoint count = %d, want 3", len(entries))
	}
	wantNames := []string{checkpointFileName(3), checkpointFileName(4), checkpointFileName(5)}
	for i := 0; i < 3; i++ {
		if entries[i].Name() != wantNames[i] {
			t.Errorf("entries[%d] = %s, want %s", i, entries[i].Name(), wantNames[i])
		}
	}
}

func TestCheckpointInvalidNameAndSeqRefused(t *testing.T) {
	dir, signer, limits := testLog(t)
	writer := openWriter(t, dir, signer, limits)
	ctx := testContext(t)
	st := State{}

	if err := writer.WriteCheckpoint(ctx, "Tasks", 1, "abc", st); err == nil {
		t.Fatal("WriteCheckpoint(Tasks) = nil, want error")
	}
	if err := writer.WriteCheckpoint(ctx, "", 1, "abc", st); err == nil {
		t.Fatal("WriteCheckpoint('') = nil, want error")
	}
	if err := writer.WriteCheckpoint(ctx, "tasks", 0, "abc", st); err == nil {
		t.Fatal("WriteCheckpoint(seq 0) = nil, want error")
	}
	if err := writer.WriteCheckpoint(ctx, "tasks", 1, "", st); err == nil {
		t.Fatal("WriteCheckpoint(hash '') = nil, want error")
	}
}

func TestReplayCheckpointedStable(t *testing.T) {
	dir, signer, limits := testLog(t)
	writer := openWriter(t, dir, signer, limits)
	ctx := testContext(t)

	rec := appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	hash := hashHex([]byte(readText(t, eventPath(eventsDir(dir), rec.Seq))))
	st := State{Tasks: map[string]Task{"task-1": {ID: "task-1", State: "open"}}}
	if err := writer.WriteCheckpoint(ctx, "tasks", rec.Seq, hash, st); err != nil {
		t.Fatalf("WriteCheckpoint() = %v", err)
	}

	got, use, err := ReplayCheckpointedStable(ctx, dir, signer.PublicKey(), limits, "tasks", TaskReducer)
	if err != nil {
		t.Fatalf("ReplayCheckpointedStable() = %v", err)
	}
	if use.Seq != rec.Seq {
		t.Fatalf("use.Seq = %d, want %d", use.Seq, rec.Seq)
	}
	if got.Tasks["task-1"].State != "open" {
		t.Fatalf("task state = %s, want open", got.Tasks["task-1"].State)
	}
}

func TestCheckpointDivergenceReported(t *testing.T) {
	dir, signer, limits := testLog(t)
	writer := openWriter(t, dir, signer, limits)
	ctx := testContext(t)

	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	rec2 := appendEvent(t, dir, signer, limits, Event{Type: "task.state_changed", TaskID: "task-1", Payload: raw(`{"state":"ready"}`)})

	st := State{Tasks: map[string]Task{"task-1": {ID: "task-1", State: "ready"}}}
	// Write checkpoint with wrong hash (diverged log)
	wrongHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := writer.WriteCheckpoint(ctx, "tasks", rec2.Seq, wrongHash, st); err != nil {
		t.Fatalf("WriteCheckpoint() = %v", err)
	}

	_, use, err := ReplayCheckpointed(ctx, dir, signer.PublicKey(), limits, "tasks", TaskReducer)
	if err != nil {
		t.Fatalf("ReplayCheckpointed() = %v, want nil", err)
	}
	if use.Diverged == nil {
		t.Fatal("use.Diverged = nil, want divergence reported")
	}
	if use.Diverged.Checkpoint.Seq != rec2.Seq || use.Diverged.Checkpoint.Hash != wrongHash {
		t.Fatalf("Diverged checkpoint = %+v, want seq %d hash %s", use.Diverged.Checkpoint, rec2.Seq, wrongHash)
	}
}

// writeSignedCheckpoint writes a checkpoint for st at seq, signed by signer under command,
// so that a test can plant one the writer would never produce.
func writeSignedCheckpoint(t *testing.T, dir string, signer *corereceipt.Signer, command string, seq uint64, hash string, st State) {
	t.Helper()
	stateBytes, err := st.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() = %v, want nil", err)
	}
	unsigned, err := canonicalJSON(unsignedCheckpoint{Name: "tasks", Seq: seq, Hash: hash, State: stateBytes})
	if err != nil {
		t.Fatalf("canonicalJSON(unsigned) = %v, want nil", err)
	}
	receipt, err := signer.Sign(corereceipt.Run{Command: command, Output: unsigned})
	if err != nil {
		t.Fatalf("Sign() = %v, want nil", err)
	}
	full, err := canonicalJSON(Checkpoint{Name: "tasks", Seq: seq, Hash: hash, State: stateBytes, Receipt: receipt})
	if err != nil {
		t.Fatalf("canonicalJSON(checkpoint) = %v, want nil", err)
	}
	cpDir := filepath.Join(dir, "checkpoints", "tasks")
	if err = os.MkdirAll(cpDir, 0o750); err != nil {
		t.Fatalf("MkdirAll() = %v, want nil", err)
	}
	if err = os.WriteFile(filepath.Join(cpDir, checkpointFileName(seq)), full, 0o600); err != nil {
		t.Fatalf("WriteFile() = %v, want nil", err)
	}
}

// TestCheckpointWrongSignerOrCommandSkipped plants checkpoints whose signature is valid but
// whose key or signing command is not the log's checkpoint signer, and with a state no
// replay produces. Replay must skip each and rebuild the state from the records.
func TestCheckpointWrongSignerOrCommandSkipped(t *testing.T) {
	cases := []struct {
		name    string
		seed    byte
		command string
	}{
		{name: "another key", seed: 7, command: CheckpointCommand},
		{name: "record command", seed: 0, command: AppendCommand},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, signer, limits := testLog(t)
			ctx := testContext(t)
			rec := appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
			hash := hashHex([]byte(readText(t, eventPath(eventsDir(dir), rec.Seq))))
			planter := signer
			if tc.seed != 0 {
				seed := testSeed()
				seed[0] ^= tc.seed
				planter = testSigner(t, seed, limits.Clock)
			}
			forged := State{Tasks: map[string]Task{"task-1": {ID: "task-1", State: "forged"}}}
			writeSignedCheckpoint(t, dir, planter, tc.command, rec.Seq, hash, forged)

			st, use, err := ReplayCheckpointed(ctx, dir, signer.PublicKey(), limits, "tasks", TaskReducer)
			if err != nil {
				t.Fatalf("ReplayCheckpointed() = %v, want nil", err)
			}
			if use.Seq != 0 || len(use.Skipped) != 1 {
				t.Fatalf("use = %+v, want a full replay with one skipped checkpoint", use)
			}
			if got := st.Tasks["task-1"].State; got != "open" {
				t.Fatalf("task-1 state = %q, want open", got)
			}
		})
	}
}

// TestCheckpointDirectoryUnreadableIsListed shows that a checkpoint directory replay cannot
// read gives a full replay with the cause listed, never a silent one.
func TestCheckpointDirectoryUnreadableIsListed(t *testing.T) {
	dir, signer, limits := testLog(t)
	ctx := testContext(t)
	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	if err := os.MkdirAll(filepath.Join(dir, "checkpoints"), 0o750); err != nil {
		t.Fatalf("MkdirAll() = %v, want nil", err)
	}
	// A file where the directory belongs makes ReadDir fail with an error other than
	// "does not exist".
	if err := os.WriteFile(filepath.Join(dir, "checkpoints", "tasks"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v, want nil", err)
	}
	st, use, err := ReplayCheckpointed(ctx, dir, signer.PublicKey(), limits, "tasks", TaskReducer)
	if err != nil {
		t.Fatalf("ReplayCheckpointed() = %v, want nil", err)
	}
	if use.Seq != 0 || len(use.Skipped) != 1 {
		t.Fatalf("use = %+v, want a full replay with the unreadable directory listed", use)
	}
	if got := st.Tasks["task-1"].State; got != "open" {
		t.Fatalf("task-1 state = %q, want open", got)
	}
}
