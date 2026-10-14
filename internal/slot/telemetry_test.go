// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

package slot

import (
	"testing"

	"github.com/tekmora/epitrop/internal/telemetry"
	"github.com/tekmora/epitrop/internal/telemetry/telemetrytest"
)

func TestSlotTelemetryCallSite(t *testing.T) {
	rec := telemetrytest.Install(t)
	run := newTestRun(t, &runWork{outcome: OutcomeDone, commit: true, evidence: true})
	report, err := Run(slotCtx(t), run.req)
	if err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
	if report.Outcome != OutcomeDone {
		t.Fatalf("report.Outcome = %s, want done", report.Outcome)
	}

	spans := rec.Spans()
	if len(spans) != 1 {
		t.Fatalf("spans count = %d, want 1", len(spans))
	}
	if spans[0].Name != "slot.run" {
		t.Errorf("span name = %s, want slot.run", spans[0].Name)
	}
	if val, ok := rec.SpanAttr(spans[0], "epitrop.unit"); !ok || val != testUnit {
		t.Errorf("epitrop.unit attribute %q (ok=%v), want %s", val, ok, testUnit)
	}

	if val := rec.Value(t, telemetry.MetricSlotRunsTotal, "outcome", "done"); val != 1 {
		t.Errorf("slot run done outcome = %d, want 1", val)
	}
}

func TestSlotOutcomesMatchTelemetryCatalog(t *testing.T) {
	cat, err := telemetry.Catalog()
	if err != nil {
		t.Fatalf("telemetry.Catalog: %v", err)
	}
	def, ok := cat.Lookup(telemetry.MetricSlotRunsTotal)
	if !ok {
		t.Fatalf("metric %s not found in catalog", telemetry.MetricSlotRunsTotal)
	}
	catalogAllowed := make(map[string]bool)
	for _, a := range def.Limits["outcome"].Allow {
		catalogAllowed[a] = true
	}
	expected := map[string]bool{
		OutcomeDone:     true,
		OutcomeBlocked:  true,
		OutcomeFailed:   true,
		OutcomeRefused:  true,
		OutcomeNotTaken: true,
	}
	for exp := range expected {
		if !catalogAllowed[exp] {
			t.Errorf("outcome %q missing from catalog Allow", exp)
		}
	}
	for cat := range catalogAllowed {
		if !expected[cat] {
			t.Errorf("catalog Allow has unexpected outcome %q", cat)
		}
	}
}
