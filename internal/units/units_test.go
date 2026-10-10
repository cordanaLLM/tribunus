package units

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cordanaLLM/tribunus/internal/eventlog"
	"github.com/golusoris/golusoris/core/clock"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

const (
	unitA        = "cordanaLLM/tribunus#22"
	unitB        = "cordanaLLM/tribunus:docs-sweep"
	goodIdentity = `{"physical_model":"model-x","harness":"harness-a","harness_version":"1.2.3","tool_count":3,"future_key":"kept"}`
)

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func testService(t *testing.T) *Service {
	t.Helper()
	clk := clock.NewFake()
	seed := make([]byte, 32)
	for i := 0; i < len(seed); i++ {
		seed[i] = byte(i + 1)
	}
	signer, err := corereceipt.NewSignerFromSeed(seed, clk)
	if err != nil {
		t.Fatalf("NewSignerFromSeed: %v", err)
	}
	svc, err := NewService(t.TempDir(), signer, clk, eventlog.Limits{Clock: clk, LockTimeout: time.Second, MaxReplayRecords: 20000})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func strPtr(s string) *string { return &s }

func intPtr(i int) *int { return &i }

func mustRecord(t *testing.T, svc *Service, id string, payload RecordPayload) eventlog.Unit {
	t.Helper()
	unit, err := svc.Record(testContext(t), id, payload)
	if err != nil {
		t.Fatalf("Record(%s, %s) = %v, want nil", id, payload.Stage, err)
	}
	return unit
}

// rawAppend writes a record as another writer would, past the API's checks.
func rawAppend(t *testing.T, svc *Service, typ string, id string, payload string) eventlog.Record {
	t.Helper()
	writer, err := eventlog.Open(svc.dir, svc.signer, svc.limits)
	if err != nil {
		t.Fatalf("eventlog.Open: %v", err)
	}
	rec, err := writer.Append(testContext(t), eventlog.Event{Type: typ, TaskID: id, Payload: json.RawMessage(payload)})
	if err != nil {
		t.Fatalf("Append(%s) = %v, want nil", typ, err)
	}
	return rec
}

func TestResumeListsOnlyOpenUnitsSorted(t *testing.T) {
	svc := testService(t)
	mustRecord(t, svc, unitB, RecordPayload{Stage: string(StagePlanned)})
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageImplementing)})
	mustRecord(t, svc, "cordanaLLM/tribunus#7", RecordPayload{Stage: string(StageReview)})
	mustRecord(t, svc, "cordanaLLM/tribunus#7", RecordPayload{Stage: string(StageLanded)})
	mustRecord(t, svc, "cordanaLLM/tribunus#8", RecordPayload{Stage: string(StageAbandoned)})
	rows, ignored, err := svc.Resume(testContext(t))
	if err != nil || ignored != 0 {
		t.Fatalf("Resume() = %v, ignored %d, want nil and 0", err, ignored)
	}
	if len(rows) != 2 || rows[0].ID != unitA || rows[1].ID != unitB {
		t.Fatalf("Resume rows = %+v, want exactly the two open units sorted by id", rows)
	}
	if rows[0].Relaunch != "safe" {
		t.Fatalf("relaunch of an implementing unit without a PR = %q, want safe", rows[0].Relaunch)
	}
}

func TestRelaunchRule(t *testing.T) {
	cases := []struct {
		stage Stage
		pr    int
		safe  bool
		why   string
	}{
		{StagePlanned, 0, true, ""},
		{StageImplementing, 0, true, ""},
		{StageVerifying, 0, true, ""},
		{StageImplementing, 5, false, "has PR #5"},
		{StageVerifying, 5, false, "has PR #5"},
		{StageReview, 0, false, "in review/landing"},
		{StageLanding, 0, false, "in review/landing"},
		{StageBlocked, 0, false, "blocked"},
		{StageBlocked, 5, false, "blocked"},
		{StageLanded, 0, false, "landed"},
	}
	for _, tc := range cases {
		safe, why := RelaunchRule(eventlog.Unit{Stage: string(tc.stage), PR: tc.pr})
		if safe != tc.safe || !strings.HasPrefix(why, tc.why) {
			t.Fatalf("RelaunchRule(%s, pr %d) = %v, %q, want %v, %q", tc.stage, tc.pr, safe, why, tc.safe, tc.why)
		}
	}
}

func TestTerminalUnitNeedsReopen(t *testing.T) {
	svc := testService(t)
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageLanded)})
	if _, err := svc.Record(testContext(t), unitA, RecordPayload{Stage: string(StageImplementing)}); err == nil || !strings.Contains(err.Error(), "--reopen") {
		t.Fatalf("Record(landed -> implementing) = %v, want a refusal naming --reopen", err)
	}
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageAbandoned)})
	if unit := mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageImplementing), Reopen: true}); unit.Stage != string(StageImplementing) {
		t.Fatalf("stage after --reopen = %q, want implementing", unit.Stage)
	}
}

func TestStickyFieldsKeepTheirValue(t *testing.T) {
	svc := testService(t)
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageImplementing), Worktree: strPtr("/wt/22"), Branch: strPtr("feat/22"),
		PR: intPtr(5), Lane: strPtr("agy"), By: strPtr("tribunus-85"), Target: strPtr("cordana/coding"), ResolvedModel: strPtr("model-x")})
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageLanding), Lane: strPtr("orchestrator")})
	unit, err := testService2(t, svc).Show(testContext(t), unitA)
	if err != nil {
		t.Fatalf("Show after replay = %v, want nil", err)
	}
	want := eventlog.Unit{ID: unitA, Stage: "landing", Worktree: "/wt/22", Branch: "feat/22", PR: 5, Lane: "orchestrator",
		By: "tribunus-85", Target: "cordana/coding", ResolvedModel: "model-x", UpdatedAt: unit.UpdatedAt}
	if !reflect.DeepEqual(unit, want) {
		t.Fatalf("unit after a second record = %+v, want %+v", unit, want)
	}
}

// testService2 opens a second service on the same log: a fresh process after a restart.
func testService2(t *testing.T, first *Service) *Service {
	t.Helper()
	svc, err := NewService(first.dir, first.signer, first.limits.Clock, first.limits)
	if err != nil {
		t.Fatalf("NewService(second) = %v, want nil", err)
	}
	return svc
}

func TestIdentityIsKeptAsGivenWithItsKey(t *testing.T) {
	svc := testService(t)
	key := "sha256:" + strings.Repeat("0f", 32)
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageImplementing), Identity: json.RawMessage(goodIdentity), IdentityKey: strPtr(key)})
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageVerifying)})
	unit, err := testService2(t, svc).Show(testContext(t), unitA)
	if err != nil {
		t.Fatalf("Show = %v, want nil", err)
	}
	var got, want map[string]any
	if err = json.Unmarshal(unit.Identity, &got); err != nil {
		t.Fatalf("stored identity does not parse: %v", err)
	}
	if err = json.Unmarshal([]byte(goodIdentity), &want); err != nil {
		t.Fatalf("test identity does not parse: %v", err)
	}
	if !reflect.DeepEqual(got, want) || unit.IdentityKey != key {
		t.Fatalf("identity = %s key %q, want every given field (unknown ones too) and the key as given", unit.Identity, unit.IdentityKey)
	}
	other := `{"physical_model":"model-y","harness":"harness-a","harness_version":"1.2.3"}`
	if unit = mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageVerifying), Identity: json.RawMessage(other)}); unit.IdentityKey != "" {
		t.Fatalf("identity_key after a new identity without a key = %q, want it cleared", unit.IdentityKey)
	}
}

func TestIdentityRefusals(t *testing.T) {
	svc := testService(t)
	key := "sha256:" + strings.Repeat("0f", 32)
	cases := []struct {
		name     string
		identity string
		key      *string
		want     string
	}{
		{"no model", `{"harness":"h","harness_version":"1"}`, nil, "needs physical_model"},
		{"no harness", `{"physical_model":"m","harness_version":"1"}`, nil, "needs physical_model"},
		{"no harness version", `{"physical_model":"m","harness":"h"}`, nil, "needs physical_model"},
		{"not an object", `["m"]`, nil, "not a JSON object"},
		{"key without identity", ``, strPtr(key), "identity_key needs the identity"},
		{"malformed key", goodIdentity, strPtr("sha256:xyz"), "identity_key must be"},
		{"too large", `{"physical_model":"m","harness":"h","harness_version":"1","pad":"` + strings.Repeat("a", MaxIdentityBytes) + `"}`, nil, "identity exceeds"},
	}
	for _, tc := range cases {
		_, err := svc.Record(testContext(t), unitA, RecordPayload{Stage: string(StagePlanned), Identity: json.RawMessage(tc.identity), IdentityKey: tc.key})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: Record() = %v, want error containing %q", tc.name, err, tc.want)
		}
	}
}

func TestInboxIsReadAtStageBoundaries(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageImplementing)})
	note, err := svc.Note(ctx, unitA, "reviewer", "rebase before landing")
	if err != nil {
		t.Fatalf("Note() = %v, want nil", err)
	}
	if note.Seq != 2 {
		t.Fatalf("note seq = %d, want 2: the sequence number of its own record", note.Seq)
	}
	for i := 0; i < 2; i++ {
		unit := mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageVerifying)})
		if unread := UnreadNotes(unit); len(unread) != 1 || unread[0].Text != "rebase before landing" {
			t.Fatalf("unread after set %d = %+v, want the note until the inbox is read", i, unread)
		}
	}
	read, err := testService2(t, svc).Inbox(ctx, unitA)
	if err != nil || len(read) != 1 {
		t.Fatalf("Inbox() after a restart = %+v, %v, want the one note", read, err)
	}
	again, err := svc.Inbox(ctx, unitA)
	if err != nil || len(again) != 0 {
		t.Fatalf("second Inbox() = %+v, %v, want nothing", again, err)
	}
	unit, err := testService2(t, svc).Show(ctx, unitA)
	if err != nil || len(unit.Notes) != 0 || unit.ReadUpto != note.Seq {
		t.Fatalf("unit after reading = %+v, %v, want no notes kept and the marker at %d", unit, err, note.Seq)
	}
}

// TestReadMarkerNeverSwallowsLaterNotes: a marker beyond the log's end covers only notes
// written before it.
func TestReadMarkerNeverSwallowsLaterNotes(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageImplementing)})
	rawAppend(t, svc, "unit.note_read", unitA, `{"upto_seq":999999}`)
	if _, err := svc.Note(ctx, unitA, "reviewer", "still to read"); err != nil {
		t.Fatalf("Note() = %v, want nil", err)
	}
	unit, err := svc.Show(ctx, unitA)
	if err != nil || len(UnreadNotes(unit)) != 1 {
		t.Fatalf("unread after an oversized marker = %+v, %v, want the later note", UnreadNotes(unit), err)
	}
	if _, err = svc.Inbox(ctx, unitA); err != nil {
		t.Fatalf("Inbox() = %v, want nil", err)
	}
	read, err := svc.Show(ctx, unitA)
	if err != nil {
		t.Fatalf("Show() = %v, want nil", err)
	}
	rawAppend(t, svc, "unit.note_read", unitA, `{"upto_seq":1}`)
	if after, showErr := svc.Show(ctx, unitA); showErr != nil || after.ReadUpto != read.ReadUpto {
		t.Fatalf("read marker after an older marker = %d, %v, want it to stay at %d", after.ReadUpto, showErr, read.ReadUpto)
	}
}

// TestReducerSettlesRacesWithoutFailing: records two honest writers can produce by racing
// must not stop replay; they are not applied and are counted.
func TestReducerSettlesRacesWithoutFailing(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageReview), PR: intPtr(5)})
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageLanded)})
	rawAppend(t, svc, "unit.recorded", unitA, `{"stage":"implementing","pr":9}`)
	rawAppend(t, svc, "unit.note", "cordanaLLM/tribunus#404", `{"from":"peer","text":"nobody home"}`)
	rawAppend(t, svc, "unit.note_read", "cordanaLLM/tribunus#404", `{"upto_seq":1}`)
	st, err := svc.Replay(ctx)
	if err != nil {
		t.Fatalf("Replay() = %v, want nil: one racing record must not stop every later replay", err)
	}
	if unit := st.Units[unitA]; unit.Stage != string(StageLanded) || unit.PR != 5 {
		t.Fatalf("landed unit after a racing record = %+v, want it still landed with PR 5", unit)
	}
	if st.UnitRecordsIgnored != 3 {
		t.Fatalf("UnitRecordsIgnored = %d, want 3", st.UnitRecordsIgnored)
	}
	if _, ignored, resumeErr := svc.Resume(ctx); resumeErr != nil || ignored != 3 {
		t.Fatalf("Resume() = ignored %d, %v, want 3 reported", ignored, resumeErr)
	}
	mustRecord(t, svc, unitB, RecordPayload{Stage: string(StagePlanned)})
}

func TestReducerIgnoresUnitsBeyondCapacity(t *testing.T) {
	st := eventlog.State{Units: make(map[string]eventlog.Unit, MaxUnits)}
	for i := 0; i < MaxUnits; i++ {
		id := fmt.Sprintf("r#%d", i)
		st.Units[id] = eventlog.Unit{ID: id, Stage: string(StagePlanned)}
	}
	if err := validateRecordState(st, "r#99999", string(StagePlanned), false); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("validateRecordState(full record) = %v, want a refusal", err)
	}
	if err := validateRecordState(st, "r#1", string(StageReview), false); err != nil {
		t.Fatalf("validateRecordState(existing unit in a full record) = %v, want nil", err)
	}
	next, err := UnitReducer(st, eventlog.Record{Seq: 1, Type: "unit.recorded", TaskID: "r#99999", Payload: json.RawMessage(`{"stage":"planned"}`)})
	if err != nil || len(next.Units) != MaxUnits || next.UnitRecordsIgnored != 1 {
		t.Fatalf("UnitReducer(full record) = %d units, ignored %d, %v, want %d, 1, nil", len(next.Units), next.UnitRecordsIgnored, err, MaxUnits)
	}
}

// TestReducerFailsOnRecordsNoWriterProduces: what cannot come from a race is corruption or
// a writer bug, and replay says so.
func TestReducerFailsOnRecordsNoWriterProduces(t *testing.T) {
	cases := []struct {
		name, typ, id, payload, want string
	}{
		{"unknown unit type", "unit.renamed", unitA, `{}`, "unknown event type"},
		{"unknown domain", "foo.bar", unitA, `{}`, "unknown event type"},
		{"bad id", "unit.recorded", "no-suffix", `{"stage":"planned"}`, "invalid unit id"},
		{"malformed record", "unit.recorded", unitA, `[1]`, "payload"},
		{"unknown stage", "unit.recorded", unitA, `{"stage":"paused"}`, "unknown stage"},
		{"malformed note", "unit.note", unitA, `"x"`, "payload"},
		{"malformed marker", "unit.note_read", unitA, `{"upto_seq":"7"}`, "payload"},
	}
	for _, tc := range cases {
		_, err := UnitReducer(eventlog.State{}, eventlog.Record{Seq: 4, Type: tc.typ, TaskID: tc.id, Payload: json.RawMessage(tc.payload)})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: UnitReducer() = %v, want error containing %q", tc.name, err, tc.want)
		}
	}
}

func TestSharedLogReplaysBesideJobsAndTasks(t *testing.T) {
	svc := testService(t)
	rawAppend(t, svc, "task.created", "task-1", `{"state":"open"}`)
	rawAppend(t, svc, "job.started", "daemon", `{"state":"running"}`)
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageImplementing)})
	st, err := svc.Replay(testContext(t))
	if err != nil || len(st.Units) != 1 {
		t.Fatalf("Replay(task + job + unit) = %d units, %v, want 1, nil", len(st.Units), err)
	}
}

func TestRecordAndNoteRefusals(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageImplementing)})
	refusedFrom := logLength(t, svc)
	records := []struct {
		name    string
		id      string
		payload RecordPayload
		want    string
	}{
		{"bad id", "no-suffix", RecordPayload{Stage: string(StagePlanned)}, "invalid unit id"},
		{"unknown stage", unitA, RecordPayload{Stage: "paused"}, "unknown stage"},
		{"tab in a field", unitA, RecordPayload{Stage: string(StagePlanned), Branch: strPtr("feat\t22")}, "control character"},
		{"newline in a field", unitA, RecordPayload{Stage: string(StagePlanned), Lane: strPtr("a\nb")}, "control character"},
		{"field too long", unitA, RecordPayload{Stage: string(StagePlanned), Worktree: strPtr(strings.Repeat("a", MaxFieldBytes+1))}, "exceeds"},
		{"negative pr", unitA, RecordPayload{Stage: string(StagePlanned), PR: intPtr(-1)}, "pr must be"},
	}
	for _, tc := range records {
		if _, err := svc.Record(ctx, tc.id, tc.payload); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: Record() = %v, want error containing %q", tc.name, err, tc.want)
		}
	}
	if got := logLength(t, svc); got != refusedFrom {
		t.Fatalf("the log grew from %d to %d records during refused records: a refusal must append nothing", refusedFrom, got)
	}
	if unit := mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageImplementing), Worktree: strPtr(strings.Repeat("a", MaxFieldBytes))}); len(unit.Worktree) != MaxFieldBytes {
		t.Fatalf("a field of exactly %d bytes was not kept", MaxFieldBytes)
	}
	before := logLength(t, svc)
	notes := []struct {
		name, id, from, text, want string
	}{
		{"unknown unit", "cordanaLLM/tribunus#404", "peer", "x", "does not exist"},
		{"bad id", "no-suffix", "peer", "x", "invalid unit id"},
		{"no sender", unitA, "", "x", "invalid sender"},
		{"sender with a space", unitA, "a b", "x", "invalid sender"},
		{"empty text", unitA, "peer", "", "note text must be"},
		{"text too long", unitA, "peer", strings.Repeat("a", MaxNoteBytes+1), "note text must be"},
	}
	for _, tc := range notes {
		if _, err := svc.Note(ctx, tc.id, tc.from, tc.text); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: Note() = %v, want error containing %q", tc.name, err, tc.want)
		}
	}
	if got := logLength(t, svc); got != before {
		t.Fatalf("the log grew from %d to %d records during refused calls: a refusal must append nothing", before, got)
	}
	if _, err := svc.Note(ctx, unitA, "peer", strings.Repeat("a", MaxNoteBytes)); err != nil {
		t.Fatalf("Note(text of exactly %d bytes) = %v, want nil", MaxNoteBytes, err)
	}
}

// logLength counts the records in the log; replay fails the test if the log is unreadable.
func logLength(t *testing.T, svc *Service) int {
	t.Helper()
	count := 0
	counter := func(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
		count++
		return UnitReducer(st, rec)
	}
	if _, err := eventlog.Replay(testContext(t), svc.dir, svc.signer.PublicKey(), svc.limits, counter); err != nil {
		t.Fatalf("Replay() = %v, want a readable log", err)
	}
	return count
}

func TestInboxHoldsAtMostItsCap(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)
	mustRecord(t, svc, unitA, RecordPayload{Stage: string(StageImplementing)})
	for i := 0; i < MaxUnreadNotes; i++ {
		rawAppend(t, svc, "unit.note", unitA, `{"from":"peer","text":"n"}`)
	}
	if _, err := svc.Note(ctx, unitA, "peer", "one too many"); err == nil || !strings.Contains(err.Error(), "unread notes") {
		t.Fatalf("Note() on a full inbox = %v, want a refusal", err)
	}
	if _, err := svc.Inbox(ctx, unitA); err != nil {
		t.Fatalf("Inbox() = %v, want nil", err)
	}
	if _, err := svc.Note(ctx, unitA, "peer", "room again"); err != nil {
		t.Fatalf("Note() after the inbox was read = %v, want nil", err)
	}
}

func TestServiceNeedsADeadline(t *testing.T) {
	svc := testService(t)
	if _, err := svc.Record(context.Background(), unitA, RecordPayload{Stage: string(StagePlanned)}); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Record(no deadline) = %v, want a refusal", err)
	}
}
