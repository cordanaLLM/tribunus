// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

package units

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tekmora/epitrop/internal/eventlog"
)

func TestPreemptSuccessAndRelaunchRule(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)

	mustRecord(t, svc, unitA, RecordPayload{Status: "ready"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "claimed"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})

	u, err := svc.Preempt(ctx, unitA, "priority shift", "alice")
	if err != nil {
		t.Fatalf("Preempt() = %v, want nil", err)
	}
	if u.Status != "blocked" {
		t.Fatalf("u.Status = %s, want blocked", u.Status)
	}
	if u.Preempted == nil {
		t.Fatal("u.Preempted = nil, want set")
	}
	if u.Preempted.Reason != "priority shift" {
		t.Fatalf("Preempted.Reason = %q, want %q", u.Preempted.Reason, "priority shift")
	}
	if u.Preempted.At == "" {
		t.Fatal("Preempted.At is empty")
	}
	if u.By != "alice" {
		t.Fatalf("u.By = %s, want alice", u.By)
	}

	safe, relaunch := RelaunchRule(u)
	if safe {
		t.Fatal("RelaunchRule(preempted) = safe, want false")
	}
	if want := "preempted: priority shift"; relaunch != want {
		t.Fatalf("RelaunchRule reason = %q, want %q", relaunch, want)
	}

	// Move out of blocked clears Preempted.
	uCleared, err := svc.Record(ctx, unitA, RecordPayload{Status: "ready"})
	if err != nil {
		t.Fatalf("Record(ready) = %v", err)
	}
	if uCleared.Status != "ready" {
		t.Fatalf("uCleared.Status = %s, want ready", uCleared.Status)
	}
	if uCleared.Preempted != nil {
		t.Fatalf("uCleared.Preempted = %+v, want nil", uCleared.Preempted)
	}
	safe2, relaunch2 := RelaunchRule(uCleared)
	if !safe2 || relaunch2 != "" {
		t.Fatalf("RelaunchRule(ready) = (%v, %q), want (true, \"\")", safe2, relaunch2)
	}
}

func driveToLanded(t *testing.T, svc *Service) {
	t.Helper()
	mustRecord(t, svc, unitA, RecordPayload{Status: "ready"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "claimed"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "review"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "landing"})
	mustRecord(t, svc, unitA, RecordPayload{Status: "landed"})
}

func TestPreemptTerminalRefusedAndIgnored(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)
	driveToLanded(t, svc)

	if _, err := svc.Preempt(ctx, unitA, "refuse me", "bob"); err == nil {
		t.Fatal("Preempt(landed) = nil, want error")
	}

	st, err := svc.Replay(ctx)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	payload, err := json.Marshal(PreemptPayload{Reason: "refused move"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := eventlog.Record{Seq: 99, Type: "unit.preempted", TaskID: unitA, Payload: payload}
	nextSt, err := UnitReducer(st, rec)
	if err != nil {
		t.Fatalf("UnitReducer: %v", err)
	}
	if nextSt.UnitRecordsIgnored != st.UnitRecordsIgnored+1 {
		t.Fatalf("UnitRecordsIgnored = %d, want %d", nextSt.UnitRecordsIgnored, st.UnitRecordsIgnored+1)
	}
	if nextSt.Units[unitA].Status != "landed" || nextSt.Units[unitA].Preempted != nil {
		t.Fatalf("landed unit modified by reducer: %+v", nextSt.Units[unitA])
	}
}

func TestPreemptValidationBoundaries(t *testing.T) {
	svc := testService(t)
	ctx := testContext(t)
	mustRecord(t, svc, unitA, RecordPayload{Status: "implementing"})

	cases := []struct {
		name, id, reason string
	}{
		{"empty reason", unitA, ""},
		{"whitespace reason", unitA, "   "},
		{"oversized reason", unitA, strings.Repeat("x", 4097)},
		{"invalid id", "invalid unit id", "some reason"},
		{"unknown unit", "tekmora/epitrop#999", "some reason"},
	}
	for i := 0; i < len(cases); i++ {
		c := cases[i]
		t.Run(c.name, func(t *testing.T) {
			if _, err := svc.Preempt(ctx, c.id, c.reason, ""); err == nil {
				t.Fatalf("Preempt(%s) = nil, want error", c.name)
			}
		})
	}
}
