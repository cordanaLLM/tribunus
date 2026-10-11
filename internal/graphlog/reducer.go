// Package graphlog keeps the task graph in the event log: one record per committed stage,
// and the reducer that rebuilds the graph from those records.
package graphlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/eventlog"
	"github.com/tekmora/epitrop/internal/graph"
)

const (
	// StageCommitted is the type of the one record a committed stage writes. A stage is all
	// or nothing in memory, so it is one record on disk: a crash between two records of the
	// same stage would leave half of it.
	StageCommitted = "graph.stage_committed"
	// MaxStageOps bounds the operations of one stage. The record limit of the log bounds
	// their bytes.
	MaxStageOps = 256
)

// StagePayload is the payload of a StageCommitted record: the stage's operations in order.
type StagePayload struct {
	Ops []graph.Event `json:"ops"`
}

// Reducer rebuilds the task graph from the shared log. It skips the other known domains and
// fails on a record no writer of this version could have produced: an unknown type, a
// payload that does not parse, a stage with no operation, with too many, or with an unknown
// one.
//
// It never fails on a stage the graph refuses. Two honest writers can produce one by racing:
// each checks its stage against the graph it read, outside the log's lock, and the second
// record may then name a node the first removed or close a cycle with it. The log is
// append-only, so a failure there would stop every later replay for good. Such a stage is
// not applied and is counted in State.GraphStagesIgnored, the same way on every replay.
//
// The graph is checked against the hard depth cap, not the configured depth: the configured
// one binds new writes, and lowering it must not drop what the log already holds.
func Reducer(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	if !strings.HasPrefix(rec.Type, "graph.") {
		if eventlog.KnownDomain(rec.Type) {
			return st, nil
		}
		return eventlog.State{}, fmt.Errorf("eventlog: seq %d: unknown event type %q", rec.Seq, rec.Type)
	}
	if rec.Type != StageCommitted {
		return eventlog.State{}, fmt.Errorf("eventlog: seq %d: unknown event type %q", rec.Seq, rec.Type)
	}
	ops, err := stageOps(rec)
	if err != nil {
		return eventlog.State{}, err
	}
	current, err := Graph(st)
	if err != nil {
		return eventlog.State{}, fmt.Errorf("eventlog: seq %d: %w", rec.Seq, err)
	}
	next, applied := applyStage(current, ops)
	if !applied {
		st.GraphStagesIgnored++
		return st, nil
	}
	if st.Graph, err = next.JCS(); err != nil {
		return eventlog.State{}, fmt.Errorf("eventlog: seq %d: %w", rec.Seq, err)
	}
	return st, nil
}

// applyStage returns the graph after the stage, and false when the graph refuses the stage.
// The refusal is a result here, not an error: the reducer counts it and goes on.
func applyStage(current *graph.Graph, ops []graph.Event) (*graph.Graph, bool) {
	next, err := current.ApplyStage(ops)
	return next, err == nil
}

// Graph returns the task graph a replayed state holds, an empty one when the log has no
// stage yet. It is bounded by the hard depth cap, as the replay is.
func Graph(st eventlog.State) (*graph.Graph, error) {
	if len(st.Graph) == 0 {
		return graph.New(config.MaxGraphDepth)
	}
	var snap graph.Snapshot
	if err := json.Unmarshal(st.Graph, &snap); err != nil {
		return nil, fmt.Errorf("graphlog: stored graph: %w", err)
	}
	g, err := graph.FromSnapshot(snap, config.MaxGraphDepth)
	if err != nil {
		return nil, fmt.Errorf("graphlog: stored graph: %w", err)
	}
	return g, nil
}

// stageOps reads the operations of a stage record and refuses a shape no writer produces.
func stageOps(rec eventlog.Record) ([]graph.Event, error) {
	var payload StagePayload
	dec := json.NewDecoder(bytes.NewReader(rec.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("eventlog: seq %d: payload: %w", rec.Seq, err)
	}
	if err := checkOps(payload.Ops); err != nil {
		return nil, fmt.Errorf("eventlog: seq %d: %w", rec.Seq, err)
	}
	return payload.Ops, nil
}

// checkOps refuses a stage no record may hold: no operation, too many, or an unknown one.
// The writer checks it before it appends and the reducer when it reads, so the two cannot
// disagree about what a stage is.
func checkOps(ops []graph.Event) error {
	if len(ops) == 0 || len(ops) > MaxStageOps {
		return fmt.Errorf("a stage holds 1 to %d operations, this one %d", MaxStageOps, len(ops))
	}
	for i := 0; i < len(ops); i++ {
		if !graph.KnownEvent(ops[i].Type) {
			return fmt.Errorf("operation %d has unknown type %q", i, ops[i].Type)
		}
	}
	return nil
}
