package graphlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/golusoris/golusoris/core/clock"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
	"github.com/jonboulle/clockwork"
	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/eventlog"
	"github.com/tekmora/epitrop/internal/graph"
)

// Service is the write path of the task graph: it checks a stage against the graph the log
// holds and appends it as one record.
type Service struct {
	dir      string
	signer   *corereceipt.Signer
	limits   eventlog.Limits
	maxDepth int
}

// NewService returns the service for the log in dir. maxDepth is the configured dependency
// depth; it binds what this service writes.
func NewService(dir string, signer *corereceipt.Signer, clk clock.Clock, limits eventlog.Limits, maxDepth int) (*Service, error) {
	if dir == "" {
		return nil, errors.New("graphlog: dir is required")
	}
	if signer == nil {
		return nil, errors.New("graphlog: signer is required")
	}
	if maxDepth < 1 || maxDepth > config.MaxGraphDepth {
		return nil, fmt.Errorf("graphlog: max depth %d is outside 1 to %d", maxDepth, config.MaxGraphDepth)
	}
	if clk == nil {
		clk = clockwork.NewRealClock()
	}
	limits.Clock = clk
	return &Service{dir: dir, signer: signer, limits: limits, maxDepth: maxDepth}, nil
}

// Replay rebuilds the state the log holds. A log directory without events yet is an empty
// state, not an error.
func (s *Service) Replay(ctx context.Context) (eventlog.State, error) {
	if err := readyContext(ctx, "replay"); err != nil {
		return eventlog.State{}, err
	}
	events := filepath.Join(s.dir, "events")
	if _, err := os.Stat(events); os.IsNotExist(err) {
		return eventlog.State{}, nil
	} else if err != nil {
		return eventlog.State{}, fmt.Errorf("graphlog: stat %s: %w", events, err)
	}
	return eventlog.ReplayStable(ctx, s.dir, s.signer.PublicKey(), s.limits, Reducer)
}

// Show returns the graph the log holds and the number of stages its replay ignored.
func (s *Service) Show(ctx context.Context) (graph.Snapshot, int, error) {
	st, err := s.Replay(ctx)
	if err != nil {
		return graph.Snapshot{}, 0, err
	}
	stored, err := Graph(st)
	if err != nil {
		return graph.Snapshot{}, 0, err
	}
	snap, err := stored.Snapshot()
	return snap, st.GraphStagesIgnored, err
}

// Commit checks ops as one stage against the graph the log holds and appends the stage as
// one record. A stage the graph refuses appends nothing. It returns the graph after the
// stage.
//
// The check runs under the configured depth. A log may hold a deeper graph, written when the
// depth was higher; it still replays, but nothing more is written to it until the depth is
// raised again: a write checked against a graph that already breaks the bound would say
// nothing about the write.
//
// Like the run record, the check runs outside the log's lock. A writer that appends between
// this one's replay and its append can make the stage one the graph refuses; the replay then
// ignores and counts it, and Show reports the count.
func (s *Service) Commit(ctx context.Context, ops []graph.Event) (graph.Snapshot, error) {
	if err := readyContext(ctx, "commit"); err != nil {
		return graph.Snapshot{}, err
	}
	if err := checkOps(ops); err != nil {
		return graph.Snapshot{}, fmt.Errorf("graphlog: %w", err)
	}
	current, err := s.writable(ctx)
	if err != nil {
		return graph.Snapshot{}, err
	}
	next, err := current.ApplyStage(ops)
	if err != nil {
		return graph.Snapshot{}, fmt.Errorf("graphlog: stage refused: %w", err)
	}
	body, err := json.Marshal(StagePayload{Ops: ops})
	if err != nil {
		return graph.Snapshot{}, fmt.Errorf("graphlog: marshal stage: %w", err)
	}
	writer, err := eventlog.Open(s.dir, s.signer, s.limits)
	if err != nil {
		return graph.Snapshot{}, err
	}
	if _, err = writer.Append(ctx, eventlog.Event{Type: StageCommitted, Payload: body}); err != nil {
		return graph.Snapshot{}, err
	}
	return next.Snapshot()
}

// writable returns the graph the log holds, bounded by the configured depth.
func (s *Service) writable(ctx context.Context) (*graph.Graph, error) {
	st, err := s.Replay(ctx)
	if err != nil {
		return nil, err
	}
	stored, err := Graph(st)
	if err != nil {
		return nil, err
	}
	snap, err := stored.Snapshot()
	if err != nil {
		return nil, err
	}
	current, err := graph.FromSnapshot(snap, s.maxDepth)
	if err != nil {
		return nil, fmt.Errorf("graphlog: the graph in the log does not hold under the configured max depth %d, raise graph.max_depth to write to it: %w", s.maxDepth, err)
	}
	return current, nil
}

func readyContext(ctx context.Context, op string) error {
	if ctx == nil {
		return fmt.Errorf("graphlog: %s: nil context", op)
	}
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("graphlog: %s requires a context deadline", op)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("graphlog: %s context: %w", op, err)
	}
	return nil
}
