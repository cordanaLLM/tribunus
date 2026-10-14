// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

package effects

import (
	"context"
	"testing"

	"github.com/tekmora/epitrop/internal/telemetry"
	"github.com/tekmora/epitrop/internal/telemetry/telemetrytest"
)

func TestEffectsTelemetryCallSite(t *testing.T) {
	rec := telemetrytest.Install(t)
	ledger, _ := testLedger(t)
	ctx := testCtx(t)
	key := Key{TaskID: "unit-1", ActionType: "dispatch"}

	res, err := ledger.Do(ctx, key, func(c context.Context) error {
		return nil
	})
	if err != nil || !res.Executed {
		t.Fatalf("Do() = %+v, %v, want executed", res, err)
	}

	spans := rec.Spans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if spans[0].Name != "effects.do" {
		t.Errorf("span name = %s, want effects.do", spans[0].Name)
	}
	if val, ok := rec.SpanAttr(spans[0], "epitrop.effect.key"); !ok || val != key.String() {
		t.Errorf("attribute epitrop.effect.key = %q (found=%v), want %s", val, ok, key.String())
	}

	if val := rec.Value(t, telemetry.MetricEffectRunsTotal, "outcome", "executed"); val != 1 {
		t.Errorf("executed outcome = %d, want 1", val)
	}

	res2, err2 := ledger.Do(ctx, key, func(c context.Context) error {
		return nil
	})
	if err2 != nil || !res2.Skipped {
		t.Fatalf("Do() on done key = %+v, %v, want skipped", res2, err2)
	}

	if val := rec.Value(t, telemetry.MetricEffectRunsTotal, "outcome", "skipped"); val != 1 {
		t.Errorf("skipped outcome = %d, want 1", val)
	}
}
