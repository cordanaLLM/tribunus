//go:build linux

// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

package supervisor

import (
	"testing"

	"github.com/tekmora/epitrop/internal/telemetry"
	"github.com/tekmora/epitrop/internal/telemetry/telemetrytest"
)

func TestSupervisorTelemetryCallSite(t *testing.T) {
	rec := telemetrytest.Install(t)

	sup := testSupervisor(t, testJob("daemon", []string{"/bin/true"}))
	if err := sup.Start(testContext(t), "daemon"); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	_ = sup.Stop(testContext(t), "daemon")

	spans := rec.Spans()
	found := false
	for _, s := range spans {
		if s.Name == "supervisor.job.start" {
			if val, ok := rec.SpanAttr(s, "epitrop.job"); ok && val == "daemon" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("supervisor.job.start span with epitrop.job=daemon not found in %v", spans)
	}

	if val := rec.Value(t, telemetry.MetricJobRunsTotal, "outcome", "started"); val != 1 {
		t.Errorf("job runs started outcome = %d, want 1", val)
	}
}
