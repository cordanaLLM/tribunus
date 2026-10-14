// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

// Package telemetry declares Epitrop's metrics and creates its spans and metric instruments
// on the global OpenTelemetry providers.
//
// Until golusoris registers a metricdef catalog on an OTel meter (golusoris/golusoris#846),
// this package creates the instruments itself and a test holds them in step with the catalog.
package telemetry

import (
	"fmt"
	"sync"

	"github.com/golusoris/golusoris/observability/metricdef"
	"github.com/tekmora/epitrop/internal/graph"
)

const (
	// MetricEffectRunsTotal counts outbound side effect runs by outcome.
	MetricEffectRunsTotal = "epitrop_effect_runs_total"
	// MetricSlotRunsTotal counts unit slot executions by outcome.
	MetricSlotRunsTotal = "epitrop_slot_runs_total"
	// MetricJobRunsTotal counts supervisor job start attempts by outcome.
	MetricJobRunsTotal = "epitrop_job_runs_total"
	// MetricCheckpointWritesTotal counts event log checkpoint write attempts by outcome.
	MetricCheckpointWritesTotal = "epitrop_checkpoint_writes_total"
	// MetricGraphTasks tracks tasks in the task graph per status.
	MetricGraphTasks = "epitrop_graph_tasks"
)

// Catalog returns every metric Epitrop declares, with its labels, label limits and help text.
// It builds the catalog once; an error means a definition breaks a metricdef naming rule.
var Catalog = sync.OnceValues(buildCatalog)

func buildCatalog() (*metricdef.Catalog, error) {
	defs := append(runMetricDefs(), graphMetricDefs()...)
	c, err := metricdef.NewCatalog(defs...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: build catalog: %w", err)
	}
	return c, nil
}

func runMetricDefs() []metricdef.Def {
	return []metricdef.Def{
		{
			Name:   MetricEffectRunsTotal,
			Help:   "Side effect runs through the effects ledger, by outcome.",
			Kind:   metricdef.KindCounter,
			Labels: []string{"outcome"},
			Limits: map[string]metricdef.LabelLimit{
				"outcome": {
					Allow: []string{"executed", "failed", "refused", "skipped"},
				},
			},
		},
		{
			Name:   MetricSlotRunsTotal,
			Help:   "Slot runs that were recorded, by outcome.",
			Kind:   metricdef.KindCounter,
			Labels: []string{"outcome"},
			Limits: map[string]metricdef.LabelLimit{
				"outcome": {
					Allow: []string{
						"blocked",
						"done",
						"failed",
						"not-taken",
						"preempted",
						"refused",
					},
				},
			},
		},
		{
			Name:   MetricJobRunsTotal,
			Help:   "Supervisor job start attempts, by outcome.",
			Kind:   metricdef.KindCounter,
			Labels: []string{"outcome"},
			Limits: map[string]metricdef.LabelLimit{
				"outcome": {
					Allow: []string{"failed", "refused", "started"},
				},
			},
		},
		{
			Name:   MetricCheckpointWritesTotal,
			Help:   "Event log checkpoint write attempts, by outcome.",
			Kind:   metricdef.KindCounter,
			Labels: []string{"outcome"},
			Limits: map[string]metricdef.LabelLimit{
				"outcome": {
					Allow: []string{"failed", "written"},
				},
			},
		},
	}
}

func graphMetricDefs() []metricdef.Def {
	return []metricdef.Def{
		{
			Name:   MetricGraphTasks,
			Help:   "Tasks in the task graph per status after the last committed change (queue depth).",
			Kind:   metricdef.KindGauge,
			Labels: []string{"status"},
			Limits: map[string]metricdef.LabelLimit{
				"status": {
					Allow: []string{
						string(graph.StatusBlocked),
						string(graph.StatusClaimed),
						string(graph.StatusDropped),
						"fix-round",
						string(graph.StatusImplementing),
						string(graph.StatusLanded),
						string(graph.StatusLanding),
						string(graph.StatusProposed),
						string(graph.StatusQueued),
						string(graph.StatusReady),
						string(graph.StatusReview),
					},
				},
			},
		},
	}
}
