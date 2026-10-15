package units

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golusoris/golusoris/core/clock"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
	"github.com/tekmora/epitrop/internal/eventlog"
)

const (
	unitA        = "tekmora/epitrop#22"
	unitB        = "tekmora/epitrop:docs-sweep"
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
		t.Fatalf("Record(%s, %s) = %v, want nil", id, payload.Status, err)
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
	mustRecord(t, svc, unitB, RecordPayload{Status: "ready"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
	mustRecord(t, svc, "tekmora/epitrop#7", RecordPayload{Status: "review"})
	mustRecord(t, svc, "tekmora/epitrop#7", RecordPayload{Status: "landing"})
	mustRecord(t, svc, "tekmora/epitrop#7", RecordPayload{Status: "landed"})
	mustRecord(t, svc, "tekmora/epitrop#8", RecordPayload{Status: "dropped"})
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
		status string
		pr     int
		safe   bool
		why    string
	}{
		{"ready", 0, true, ""},
		{"claimed", 0, true, ""},
		{"implementing", 0, true, ""},
		{"implementing", 5, false, "has PR #5"},
		{"proposed", 0, false, "proposed; not ready to start"},
		{"review", 0, false, "in review"},
		{"fix-round-2", 0, false, "in fix-round-2"},
		{"queued", 0, false, "in queued"},
		{"landing", 0, false, "in landing"},
		{"blocked", 0, false, "blocked"},
		{"blocked", 5, false, "blocked"},
	}
	for _, tc := range cases {
		safe, why := RelaunchRule(eventlog.Unit{Status: tc.status, PR: tc.pr})
		if safe != tc.safe || !strings.HasPrefix(why, tc.why) {
			t.Fatalf("RelaunchRule(%s, pr %d) = %v, %q, want %v, %q", tc.status, tc.pr, safe, why, tc.safe, tc.why)
		}
	}
}

func TestTerminalUnitNeedsReopen(t *testing.T) {
	svc := testService(t)
	mustRecord(t, svc, unitA, RecordPayload{Status: "landed"})
	for _, next := range []string{"implementing", "dropped"} {
		if _, err := svc.Record(testContext(t), unitA, RecordPayload{Status: next}); err == nil || !strings.Contains(err.Error(), "--reopen") {
			t.Fatalf("Record(landed -> %s) = %v, want a refusal naming --reopen", next, err)
		}
	}
	if unit := mustRecord(t, svc, unitA, RecordPayload{Status: "implementing", Reopen: true}); unit.Status != "implementing" {
		t.Fatalf("status after --reopen = %q, want implementing", unit.Status)
	}
	// reopen is an override for a landed or dropped unit only: it does not bypass the table.
	if _, err := svc.Record(testContext(t), unitA, RecordPayload{Status: "landed", Reopen: true}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("Record(implementing -> landed, reopen) = %v, want the table's refusal", err)
	}
}

// TestStatusMovesFollowTheTaskGraphTable: the run record has no status rules of its own. A
// move is allowed exactly when internal/graph allows it; a record that keeps the status only
// updates fields.
func TestStatusMovesFollowTheTaskGraphTable(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)
	for _, status := range []string{"proposed", "ready", "claimed", "implementing", "implementing", "review", "fix-round-1", "review", "queued", "landing", "landed"} {
		mustRecord(t, svc, unitA, RecordPayload{Status: status})
	}
	mustRecord(t, svc, unitB, RecordPayload{Status: "implementing"})
	checkRefusedStatusMoves(t, svc, ctx)
	checkIllegalMoveReplay(t, svc, ctx)
}

func checkRefusedStatusMoves(t *testing.T, svc *Service, ctx context.Context) {
	t.Helper()
	for _, next := range []string{"landed", "landing", "queued", "claimed", "proposed"} {
		if _, err := svc.Record(ctx, unitB, RecordPayload{Status: next}); err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("Record(implementing -> %s) = %v, want the table's refusal", next, err)
		}
	}
	for _, unknown := range []string{"verifying", "abandoned", "planned", "fix-round-N", "fix-round-0"} {
		if _, err := svc.Record(ctx, unitB, RecordPayload{Status: unknown}); err == nil || !strings.Contains(err.Error(), "unknown status") {
			t.Fatalf("Record(%s) = %v, want an unknown-status refusal", unknown, err)
		}
	}
}

func checkIllegalMoveReplay(t *testing.T, svc *Service, ctx context.Context) {
	t.Helper()
	// A racing writer's illegal move is in the log anyway: replay leaves it unapplied.
	rawAppend(t, svc, "unit.recorded", unitB, `{"status":"landed"}`)
	st, err := svc.Replay(ctx)
	if err != nil || st.Units[unitB].Status != "implementing" || st.UnitRecordsIgnored != 1 {
		t.Fatalf("after an illegal move in the log: status %q, ignored %d, %v; want implementing, 1, nil", st.Units[unitB].Status, st.UnitRecordsIgnored, err)
	}
}

func TestStickyFieldsKeepTheirValue(t *testing.T) {
	svc := testService(t)
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing", Worktree: strPtr("/wt/22"), Branch: strPtr("feat/22"),
		PR: intPtr(5), Lane: strPtr("agy"), By: strPtr("epitrop-85"), Target: strPtr("cordana/coding"), ResolvedModel: strPtr("model-x")})
	mustRecord(t, svc, unitA, RecordPayload{Status: "review", Lane: strPtr("orchestrator")})
	unit, err := testService2(t, svc).Show(testContext(t), unitA)
	if err != nil {
		t.Fatalf("Show after replay = %v, want nil", err)
	}
	want := eventlog.Unit{ID: unitA, Status: "review", Worktree: "/wt/22", Branch: "feat/22", PR: 5, Lane: "orchestrator",
		By: "epitrop-85", Target: "cordana/coding", ResolvedModel: "model-x", UpdatedAt: unit.UpdatedAt}
	if !reflect.DeepEqual(unit, want) {
		t.Fatalf("unit after a second record = %+v, want %+v", unit, want)
	}
}

// TestEvidencePointerIsStickyAndReplaced: a unit keeps its evidence pointer across records
// that carry none, and a new pointer replaces it as a whole.
func TestEvidencePointerIsStickyAndReplaced(t *testing.T) {
	svc := testService(t)
	first := &eventlog.UnitEvidence{Path: ".workingdir/evidence/unit a.md", SHA256: "0123456789ab", Lines: 42}
	unit := mustRecord(t, svc, unitA, RecordPayload{Status: "implementing", Evidence: first})
	checkEvidenceFirstRecord(t, unit, first)
	mustRecord(t, svc, unitA, RecordPayload{Status: "review", Lane: strPtr("orchestrator")})
	checkEvidenceKeptAcrossReplay(t, svc)
	checkEvidenceReplacedAndOther(t, svc)
}

func checkEvidenceFirstRecord(t *testing.T, unit eventlog.Unit, first *eventlog.UnitEvidence) {
	t.Helper()
	if unit.Evidence == nil || *unit.Evidence != *first {
		t.Fatalf("evidence after the first record = %+v, want %+v", unit.Evidence, first)
	}
	first.Lines = 7
	if unit.Evidence.Lines != 42 {
		t.Fatalf("the unit shares the caller's pointer: lines = %d after the caller changed its copy, want 42", unit.Evidence.Lines)
	}
}

func checkEvidenceKeptAcrossReplay(t *testing.T, svc *Service) {
	t.Helper()
	kept, err := testService2(t, svc).Show(testContext(t), unitA)
	if err != nil || kept.Evidence == nil || kept.Evidence.Path != ".workingdir/evidence/unit a.md" || kept.Evidence.SHA256 != "0123456789ab" || kept.Evidence.Lines != 42 {
		t.Fatalf("evidence after a record without one = %+v, %v, want the first pointer kept across replay", kept.Evidence, err)
	}
}

func checkEvidenceReplacedAndOther(t *testing.T, svc *Service) {
	t.Helper()
	second := &eventlog.UnitEvidence{Path: "out/result.json", SHA256: strings.Repeat("a", 64), Lines: 0}
	replaced := mustRecord(t, svc, unitA, RecordPayload{Status: "review", Evidence: second})
	if replaced.Evidence == nil || *replaced.Evidence != *second {
		t.Fatalf("evidence after a new pointer = %+v, want %+v", replaced.Evidence, second)
	}
	if other := mustRecord(t, svc, unitB, RecordPayload{Status: "ready"}); other.Evidence != nil {
		t.Fatalf("a unit that never got evidence has %+v, want none", other.Evidence)
	}
}

func TestEvidenceRefusalsAppendNothing(t *testing.T) {
	svc := testService(t)
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
	before := logLength(t, svc)
	cases := []struct {
		name     string
		evidence eventlog.UnitEvidence
		want     string
	}{
		{"no path", eventlog.UnitEvidence{SHA256: "0123456789ab", Lines: 1}, "evidence needs a path"},
		{"path with a line break", eventlog.UnitEvidence{Path: "a\nb", SHA256: "0123456789ab", Lines: 1}, "evidence path holds a control character"},
		{"path too long", eventlog.UnitEvidence{Path: strings.Repeat("p", MaxFieldBytes+1), SHA256: "0123456789ab", Lines: 1}, "evidence path exceeds"},
		{"digest too short", eventlog.UnitEvidence{Path: "e.md", SHA256: "0123456789a", Lines: 1}, "evidence sha256 must be"},
		{"digest too long", eventlog.UnitEvidence{Path: "e.md", SHA256: strings.Repeat("a", 65), Lines: 1}, "evidence sha256 must be"},
		{"digest in upper case", eventlog.UnitEvidence{Path: "e.md", SHA256: "0123456789AB", Lines: 1}, "evidence sha256 must be"},
		{"digest with its prefix", eventlog.UnitEvidence{Path: "e.md", SHA256: "sha256:0123456789ab", Lines: 1}, "evidence sha256 must be"},
		{"negative lines", eventlog.UnitEvidence{Path: "e.md", SHA256: "0123456789ab", Lines: -1}, "evidence lines must be"},
		{"too many lines", eventlog.UnitEvidence{Path: "e.md", SHA256: "0123456789ab", Lines: MaxEvidenceLines + 1}, "evidence lines must be"},
	}
	for i := 0; i < len(cases); i++ {
		evidence := cases[i].evidence
		_, err := svc.Record(testContext(t), unitA, RecordPayload{Status: "implementing", Evidence: &evidence})
		if err == nil || !strings.Contains(err.Error(), cases[i].want) {
			t.Errorf("Record(evidence: %s) = %v, want %q", cases[i].name, err, cases[i].want)
		}
	}
	if after := logLength(t, svc); after != before {
		t.Fatalf("log grew from %d to %d records across refused evidence, want nothing appended", before, after)
	}
	edge := eventlog.UnitEvidence{Path: strings.Repeat("p", MaxFieldBytes), SHA256: "0123456789ab", Lines: MaxEvidenceLines}
	if _, err := svc.Record(testContext(t), unitA, RecordPayload{Status: "implementing", Evidence: &edge}); err != nil {
		t.Fatalf("Record(evidence at every bound) = %v, want nil", err)
	}
}

func TestParseEvidence(t *testing.T) {
	got, err := ParseEvidence("  .workingdir/evidence/unit a.md   sha256:0123456789ab lines:42 ")
	if err != nil || got.Path != ".workingdir/evidence/unit a.md" || got.SHA256 != "0123456789ab" || got.Lines != 42 {
		t.Fatalf("ParseEvidence(path with a space) = %+v, %v, want path, digest and 42 lines", got, err)
	}
	refused := []struct{ text, want string }{
		{"", "evidence must be"},
		{"e.md sha256:0123456789ab", "evidence must be"},
		{"e.md lines:3 sha256:0123456789ab", "evidence must end in"},
		{"e.md sha256:0123456789ab count:3", "evidence must end in"},
		{"e.md md5:0123456789ab lines:3", "evidence must end in"},
		{"e.md sha256:0123456789ab lines:many", "evidence lines"},
		{"e.md sha256:xyz lines:3", "evidence sha256 must be"},
		{"sha256:0123456789ab lines:3", "evidence must be"},
	}
	for i := 0; i < len(refused); i++ {
		if got, err = ParseEvidence(refused[i].text); err == nil || got != nil || !strings.Contains(err.Error(), refused[i].want) {
			t.Errorf("ParseEvidence(%q) = %+v, %v, want %q", refused[i].text, got, err, refused[i].want)
		}
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
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing", Identity: json.RawMessage(goodIdentity), IdentityKey: strPtr(key)})
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
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
	if unit = mustRecord(t, svc, unitA, RecordPayload{Status: "implementing", Identity: json.RawMessage(other)}); unit.IdentityKey != "" {
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
		_, err := svc.Record(testContext(t), unitA, RecordPayload{Status: "ready", Identity: json.RawMessage(tc.identity), IdentityKey: tc.key})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: Record() = %v, want error containing %q", tc.name, err, tc.want)
		}
	}
}

func TestInboxIsReadAtStageBoundaries(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)
	note := checkInboxNoteCreation(t, svc, ctx)
	checkUnreadNotesPersist(t, svc)
	checkInboxReadsAndState(t, svc, ctx, note)
}

func checkInboxNoteCreation(t *testing.T, svc *Service, ctx context.Context) eventlog.UnitNote {
	t.Helper()
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
	note, err := svc.Note(ctx, unitA, "reviewer", "rebase before landing")
	if err != nil {
		t.Fatalf("Note() = %v, want nil", err)
	}
	if note.Seq != 2 {
		t.Fatalf("note seq = %d, want 2: the sequence number of its own record", note.Seq)
	}
	return note
}

func checkUnreadNotesPersist(t *testing.T, svc *Service) {
	t.Helper()
	for i := 0; i < 2; i++ {
		unit := mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
		if unread := UnreadNotes(unit); len(unread) != 1 || unread[0].Text != "rebase before landing" {
			t.Fatalf("unread after set %d = %+v, want the note until the inbox is read", i, unread)
		}
	}
}

func checkInboxReadsAndState(t *testing.T, svc *Service, ctx context.Context, note eventlog.UnitNote) {
	t.Helper()
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
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
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
	mustRecord(t, svc, unitA, RecordPayload{Status: "review", PR: intPtr(5)})
	mustRecord(t, svc, unitA, RecordPayload{Status: "landing"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "landed"})
	rawAppend(t, svc, "unit.recorded", unitA, `{"status":"implementing","pr":9}`)
	rawAppend(t, svc, "unit.note", "tekmora/epitrop#404", `{"from":"peer","text":"nobody home"}`)
	rawAppend(t, svc, "unit.note_read", "tekmora/epitrop#404", `{"upto_seq":1}`)
	st, err := svc.Replay(ctx)
	if err != nil {
		t.Fatalf("Replay() = %v, want nil: one racing record must not stop every later replay", err)
	}
	if unit := st.Units[unitA]; unit.Status != "landed" || unit.PR != 5 {
		t.Fatalf("landed unit after a racing record = %+v, want it still landed with PR 5", unit)
	}
	if st.UnitRecordsIgnored != 3 {
		t.Fatalf("UnitRecordsIgnored = %d, want 3", st.UnitRecordsIgnored)
	}
	if _, ignored, resumeErr := svc.Resume(ctx); resumeErr != nil || ignored != 3 {
		t.Fatalf("Resume() = ignored %d, %v, want 3 reported", ignored, resumeErr)
	}
	mustRecord(t, svc, unitB, RecordPayload{Status: "ready"})
}

func TestReducerIgnoresUnitsBeyondCapacity(t *testing.T) {
	st := eventlog.State{Units: make(map[string]eventlog.Unit, MaxUnits)}
	for i := 0; i < MaxUnits; i++ {
		id := fmt.Sprintf("r#%d", i)
		st.Units[id] = eventlog.Unit{ID: id, Status: "ready"}
	}
	if err := validateRecordState(st, "r#99999", "ready", false); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("validateRecordState(full record) = %v, want a refusal", err)
	}
	if err := validateRecordState(st, "r#1", "claimed", false); err != nil {
		t.Fatalf("validateRecordState(existing unit in a full record) = %v, want nil", err)
	}
	next, err := UnitReducer(st, eventlog.Record{Seq: 1, Type: "unit.recorded", TaskID: "r#99999", Payload: json.RawMessage(`{"status":"ready"}`)})
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
		{"bad id", "unit.recorded", "no-suffix", `{"status":"ready"}`, "invalid unit id"},
		{"malformed record", "unit.recorded", unitA, `[1]`, "payload"},
		{"unknown status", "unit.recorded", unitA, `{"status":"paused"}`, "unknown status"},
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
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
	st, err := svc.Replay(testContext(t))
	if err != nil || len(st.Units) != 1 {
		t.Fatalf("Replay(task + job + unit) = %d units, %v, want 1, nil", len(st.Units), err)
	}
}

func TestRecordAndNoteRefusals(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)
	checkRecordRefusals(t, svc, ctx)
	checkNoteRefusals(t, svc, ctx)
}

func checkRecordRefusals(t *testing.T, svc *Service, ctx context.Context) {
	t.Helper()
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
	refusedFrom := logLength(t, svc)
	records := []struct {
		name    string
		id      string
		payload RecordPayload
		want    string
	}{
		{"bad id", "no-suffix", RecordPayload{Status: "ready"}, "invalid unit id"},
		{"unknown status", unitA, RecordPayload{Status: "paused"}, "unknown status"},
		{"tab in a field", unitA, RecordPayload{Status: "ready", Branch: strPtr("feat\t22")}, "control character"},
		{"newline in a field", unitA, RecordPayload{Status: "ready", Lane: strPtr("a\nb")}, "control character"},
		{"field too long", unitA, RecordPayload{Status: "ready", Worktree: strPtr(strings.Repeat("a", MaxFieldBytes+1))}, "exceeds"},
		{"negative pr", unitA, RecordPayload{Status: "ready", PR: intPtr(-1)}, "pr must be"},
	}
	for _, tc := range records {
		if _, err := svc.Record(ctx, tc.id, tc.payload); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: Record() = %v, want error containing %q", tc.name, err, tc.want)
		}
	}
	if got := logLength(t, svc); got != refusedFrom {
		t.Fatalf("the log grew from %d to %d records during refused records: a refusal must append nothing", refusedFrom, got)
	}
	if unit := mustRecord(t, svc, unitA, RecordPayload{Status: "implementing", Worktree: strPtr(strings.Repeat("a", MaxFieldBytes))}); len(unit.Worktree) != MaxFieldBytes {
		t.Fatalf("a field of exactly %d bytes was not kept", MaxFieldBytes)
	}
}

func checkNoteRefusals(t *testing.T, svc *Service, ctx context.Context) {
	t.Helper()
	before := logLength(t, svc)
	notes := []struct {
		name, id, from, text, want string
	}{
		{"unknown unit", "tekmora/epitrop#404", "peer", "x", "does not exist"},
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
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
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
	if _, err := svc.Record(context.Background(), unitA, RecordPayload{Status: "ready"}); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Record(no deadline) = %v, want a refusal", err)
	}
}

// TestConcurrentRecordTerminalRace verifies that concurrent transitions from a non-terminal status
// to conflicting terminal statuses are serialized under the lock.
// With the check outside the lock both groups can pass on "landing" and one record is ignored.
func TestConcurrentRecordTerminalRace(t *testing.T) {
	svc := testService(t)
	svc.limits.LockTimeout = time.Minute
	ctx := testContext(t)
	mustRecord(t, svc, unitA, RecordPayload{Status: "ready"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "claimed"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "review"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "landing"})

	const total = 8
	results := make([]recordRaceResult, total)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < total; i++ {
		wg.Add(1)
		targetStatus := "landed"
		if i%2 != 0 {
			targetStatus = "dropped"
		}
		go func(idx int, target string) {
			defer wg.Done()
			<-start
			_, err := svc.Record(ctx, unitA, RecordPayload{Status: target})
			results[idx] = recordRaceResult{status: target, err: err}
		}(i, targetStatus)
	}
	close(start)
	wg.Wait()

	winningStatus := evaluateTerminalRaceResults(t, results)
	checkTerminalRaceFinalState(t, svc, ctx, winningStatus)
}

type recordRaceResult struct {
	status string
	err    error
}

func checkRaceResultItem(t *testing.T, res recordRaceResult, winningStatus *string, successes *int) {
	t.Helper()
	if res.err == nil {
		*successes++
		if *winningStatus == "" {
			*winningStatus = res.status
		} else if *winningStatus != res.status {
			t.Fatalf("successful call asked for %q, but winning status is %q", res.status, *winningStatus)
		}
		return
	}
	if !strings.Contains(res.err.Error(), "pass --reopen") {
		t.Fatalf("error for %q = %v, want error containing 'pass --reopen'", res.status, res.err)
	}
}

func evaluateTerminalRaceResults(t *testing.T, results []recordRaceResult) string {
	t.Helper()
	var winningStatus string
	successes := 0
	for _, res := range results {
		checkRaceResultItem(t, res, &winningStatus, &successes)
	}
	if successes < 1 {
		t.Fatalf("successes = %d, want at least 1", successes)
	}
	return winningStatus
}

func checkTerminalRaceFinalState(t *testing.T, svc *Service, ctx context.Context, winningStatus string) {
	t.Helper()
	st, err := svc.Replay(ctx)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got := st.Units[unitA].Status; got != winningStatus {
		t.Fatalf("final status = %q, want %q", got, winningStatus)
	}
	if st.UnitRecordsIgnored != 0 {
		t.Fatalf("UnitRecordsIgnored = %d, want 0", st.UnitRecordsIgnored)
	}
}

func TestConcurrentNoteAtMaxUnreadNotesBoundary(t *testing.T) {
	svc := testService(t)
	svc.limits.LockTimeout = time.Minute
	ctx := testContext(t)
	setupNoteRaceBoundary(t, svc, ctx)

	const total = 8
	var wg sync.WaitGroup
	errs := make(chan error, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, noteErr := svc.Note(ctx, unitA, "peer", fmt.Sprintf("race-%d", idx))
			errs <- noteErr
		}(i)
	}
	wg.Wait()
	close(errs)

	evaluateNoteRaceErrors(t, errs)
	checkNoteRaceFinalState(t, svc, ctx)
}

func setupNoteRaceBoundary(t *testing.T, svc *Service, ctx context.Context) {
	t.Helper()
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
	writer, err := eventlog.Open(svc.dir, svc.signer, svc.limits)
	if err != nil {
		t.Fatalf("eventlog.Open: %v", err)
	}
	for i := 0; i < MaxUnreadNotes-1; i++ {
		ev := eventlog.Event{Type: "unit.note", TaskID: unitA, Payload: json.RawMessage(`{"from":"peer","text":"note"}`)}
		if _, err := writer.Append(ctx, ev); err != nil {
			t.Fatalf("setup note %d: %v", i, err)
		}
	}
}

func evaluateNoteRaceErrors(t *testing.T, errs <-chan error) {
	t.Helper()
	successes := 0
	alreadyHas := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if strings.Contains(err.Error(), "already has") {
			alreadyHas++
		} else {
			t.Fatalf("unexpected Note error: %v", err)
		}
	}
	if successes != 1 || alreadyHas != 7 {
		t.Fatalf("successes = %d, alreadyHas = %d, want 1 and 7", successes, alreadyHas)
	}
}

func checkNoteRaceFinalState(t *testing.T, svc *Service, ctx context.Context) {
	t.Helper()
	st, err := svc.Replay(ctx)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if st.UnitRecordsIgnored != 0 {
		t.Fatalf("UnitRecordsIgnored = %d, want 0", st.UnitRecordsIgnored)
	}
	unit, err := svc.Show(ctx, unitA)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(UnreadNotes(unit)) != MaxUnreadNotes {
		t.Fatalf("unread notes = %d, want %d", len(UnreadNotes(unit)), MaxUnreadNotes)
	}
}

func TestConcurrentInboxOnlyOneReturnsNotes(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)
	checkConcurrentInboxNotesSetup(t, svc, ctx)

	var notesA, notesB []eventlog.UnitNote
	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		notesA, errA = svc.Inbox(ctx, unitA)
	}()
	go func() {
		defer wg.Done()
		notesB, errB = svc.Inbox(ctx, unitA)
	}()
	wg.Wait()

	checkConcurrentInboxResults(t, notesA, notesB, errA, errB)
	checkConcurrentInboxReplay(t, svc, ctx)
}

func checkConcurrentInboxNotesSetup(t *testing.T, svc *Service, ctx context.Context) {
	t.Helper()
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
	if _, err := svc.Note(ctx, unitA, "peer", "first"); err != nil {
		t.Fatalf("Note: %v", err)
	}
	if _, err := svc.Note(ctx, unitA, "peer", "second"); err != nil {
		t.Fatalf("Note: %v", err)
	}
}

func checkConcurrentInboxResults(t *testing.T, notesA, notesB []eventlog.UnitNote, errA, errB error) {
	t.Helper()
	if errA != nil || errB != nil {
		t.Fatalf("Inbox errors: A=%v, B=%v", errA, errB)
	}
	lenA, lenB := len(notesA), len(notesB)
	if (lenA != 2 || lenB != 0) && (lenA != 0 || lenB != 2) {
		t.Fatalf("got lenA=%d, lenB=%d; want one 2 and other 0", lenA, lenB)
	}
}

func checkConcurrentInboxReplay(t *testing.T, svc *Service, ctx context.Context) {
	t.Helper()
	st, err := svc.Replay(ctx)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if st.UnitRecordsIgnored != 0 {
		t.Fatalf("UnitRecordsIgnored = %d, want 0", st.UnitRecordsIgnored)
	}
}
