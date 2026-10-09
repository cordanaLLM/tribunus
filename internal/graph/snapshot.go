package graph

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/golusoris/golusoris/core/codec/jcs"
)

// SnapshotNode is the deterministic public shape of a graph node.
type SnapshotNode struct {
	ID        string   `json:"id"`
	Kind      Kind     `json:"kind"`
	Title     string   `json:"title"`
	Status    Status   `json:"status"`
	DependsOn []string `json:"depends_on,omitempty"`
}

// Snapshot is the deterministic public shape of the graph.
type Snapshot struct {
	MaxDepth int            `json:"max_depth"`
	Nodes    []SnapshotNode `json:"nodes"`
}

// Snapshot returns a sorted copy of graph state.
func (g *Graph) Snapshot() (Snapshot, error) {
	if g == nil {
		return Snapshot{}, errNilGraph
	}
	return snapshot(g.maxDepth, g.state), nil
}

// JCS returns the graph snapshot as RFC 8785 canonical JSON.
func (g *Graph) JCS() ([]byte, error) {
	snap, err := g.Snapshot()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("graph: marshal snapshot: %w", err)
	}
	out, err := jcs.Canonicalize(data)
	if err != nil {
		return nil, fmt.Errorf("graph: canonicalize snapshot: %w", err)
	}
	return out, nil
}

func snapshot(maxDepth int, state graphState) Snapshot {
	ids := sortedNodeIDs(state.nodes)
	nodes := make([]SnapshotNode, 0, len(ids))
	deps := dependencyMap(state.edges)
	for i := 0; i < len(ids); i++ {
		node := state.nodes[ids[i]]
		nodes = append(nodes, SnapshotNode{
			ID: node.ID, Kind: node.Kind, Title: node.Title,
			Status: node.Status, DependsOn: deps[node.ID],
		})
	}
	return Snapshot{MaxDepth: maxDepth, Nodes: nodes}
}

func dependencyMap(edges map[Edge]struct{}) map[string][]string {
	deps := map[string][]string{}
	for edge := range edges {
		deps[edge.NodeID] = append(deps[edge.NodeID], edge.DependsOn)
	}
	for id := range deps {
		sort.Strings(deps[id])
	}
	return deps
}

func edgeKey(edge Edge) string {
	return fmt.Sprintf("%q depends_on %q", edge.NodeID, edge.DependsOn)
}

func incidentEdge(s graphState, id string) (Edge, bool) {
	for edge := range s.edges {
		if edge.NodeID == id || edge.DependsOn == id {
			return edge, true
		}
	}
	return Edge{}, false
}
