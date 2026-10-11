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

// FromSnapshot rebuilds a graph from a snapshot, bounded by maxDepth. It checks what holds for
// every stored graph: node shape, known kinds and statuses, edges between nodes that exist,
// no cycle, the depth and count bounds. It does not apply the rules for a change, such as a
// new node starting as proposed: the snapshot is a graph that changes already led to. A
// node named twice is refused.
func FromSnapshot(snap Snapshot, maxDepth int) (*Graph, error) {
	g, err := New(maxDepth)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(snap.Nodes); i++ {
		node := snap.Nodes[i]
		if _, twice := g.state.nodes[node.ID]; twice {
			return nil, fmt.Errorf("graph: snapshot names node %q twice", node.ID)
		}
		g.state.nodes[node.ID] = Node{ID: node.ID, Kind: node.Kind, Title: node.Title, Status: node.Status}
		for j := 0; j < len(node.DependsOn); j++ {
			g.state.edges[Edge{NodeID: node.ID, DependsOn: node.DependsOn[j]}] = struct{}{}
		}
	}
	if err = validate(g.state, maxDepth); err != nil {
		return nil, err
	}
	return g, nil
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
