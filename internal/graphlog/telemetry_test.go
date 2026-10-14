// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

package graphlog

import (
	"testing"

	"github.com/tekmora/epitrop/internal/graph"
	"github.com/tekmora/epitrop/internal/telemetry"
	"github.com/tekmora/epitrop/internal/telemetry/telemetrytest"
)

func TestGraphlogTelemetryCallSite(t *testing.T) {
	rec := telemetrytest.Install(t)
	dir := t.TempDir()
	svc := serviceAt(t, dir, 12)
	ctx := testCtx(t)

	_, err := svc.Commit(ctx, []graph.Event{addNode("task-1")})
	if err != nil {
		t.Fatalf("Commit() = %v, want nil", err)
	}

	if val := rec.Value(t, telemetry.MetricGraphTasks, "status", "proposed"); val != 1 {
		t.Errorf("graph tasks proposed = %d, want 1", val)
	}
}
