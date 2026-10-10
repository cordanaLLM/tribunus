// Package graph stores the in-memory task graph used by the Tribunus control
// plane.
package graph

import (
	"errors"
	"fmt"

	"github.com/cordanaLLM/tribunus/internal/config"
)

// Store bounds keep validation work finite and reject accidental event floods.
const (
	MaxNodes = 4096
	MaxEdges = 16384
)

// Kind is the planning-graph node vocabulary owned by Praetor.
type Kind string

const (
	KindEpic     Kind = "epic"
	KindUnit     Kind = "unit"
	KindDecision Kind = "decision"
	KindResearch Kind = "research"
	KindGate     Kind = "gate"
)

// StatusName is the claim-aligned task status vocabulary.
type StatusName string

const (
	StatusProposed     StatusName = "proposed"
	StatusReady        StatusName = "ready"
	StatusClaimed      StatusName = "claimed"
	StatusImplementing StatusName = "implementing"
	StatusReview       StatusName = "review"
	StatusFixRound     StatusName = "fix-round-N"
	StatusBlocked      StatusName = "blocked"
	StatusQueued       StatusName = "queued"
	StatusLanding      StatusName = "landing"
	StatusLanded       StatusName = "landed"
	StatusDropped      StatusName = "dropped"
)

// ClaimStage is the lifecycle stage emitted by the claim marker.
type ClaimStage string

const (
	ClaimStageClaimed      ClaimStage = "claimed"
	ClaimStageImplementing ClaimStage = "implementing"
	ClaimStageReview       ClaimStage = "review"
	ClaimStageBlocked      ClaimStage = "blocked"
	ClaimStageQueued       ClaimStage = "queued"
	ClaimStageLanding      ClaimStage = "landing"
	ClaimStageReleased     ClaimStage = "released"
)

// ClaimOutcome is the release outcome emitted with ClaimStageReleased.
type ClaimOutcome string

const (
	ClaimOutcomeLanded     ClaimOutcome = "landed"
	ClaimOutcomeAbandoned  ClaimOutcome = "abandoned"
	ClaimOutcomeHandedOver ClaimOutcome = "handed-over"
)

// Status holds the node status name.
type Status struct {
	Name StatusName `json:"name"`
}

// Node is a task graph node. Dependency edges are stored separately.
type Node struct {
	ID     string `json:"id"`
	Kind   Kind   `json:"kind"`
	Title  string `json:"title"`
	Status Status `json:"status"`
}

// Edge means NodeID depends on DependsOn.
type Edge struct {
	NodeID    string `json:"node_id"`
	DependsOn string `json:"depends_on"`
}

// Graph is an all-or-nothing staged task graph.
type Graph struct {
	maxDepth int
	state    graphState
}

type graphState struct {
	nodes map[string]Node
	edges map[Edge]struct{}
}

var (
	errNilGraph  = errors.New("graph: nil graph")
	errNilStage  = errors.New("graph: nil stage")
	errBadConfig = errors.New("graph: max depth outside config bounds")
)

// New returns an empty graph with traversal depth bounded by maxDepth.
func New(maxDepth int) (*Graph, error) {
	if maxDepth < 1 || maxDepth > config.MaxGraphDepth {
		return nil, fmt.Errorf("%w: %d", errBadConfig, maxDepth)
	}
	return &Graph{maxDepth: maxDepth, state: newState()}, nil
}

// Clone returns an independent copy of g.
func (g *Graph) Clone() (*Graph, error) {
	if g == nil {
		return nil, errNilGraph
	}
	return &Graph{maxDepth: g.maxDepth, state: g.state.clone()}, nil
}

func newState() graphState {
	return graphState{
		nodes: map[string]Node{},
		edges: map[Edge]struct{}{},
	}
}

func (s graphState) clone() graphState {
	next := newState()
	for id, node := range s.nodes {
		next.nodes[id] = node
	}
	for edge := range s.edges {
		next.edges[edge] = struct{}{}
	}
	return next
}
