package graph

import "fmt"

// EventType names graph events. The append-only event log will drive these.
type EventType string

const (
	EventNodeAdded     EventType = "graph.node_added"
	EventEdgeAdded     EventType = "graph.edge_added"
	EventNodeRemoved   EventType = "graph.node_removed"
	EventEdgeRemoved   EventType = "graph.edge_removed"
	EventStatusChanged EventType = "graph.status_changed"
)

// Event is the pure reducer input shape for graph replay.
type Event struct {
	Type   EventType `json:"type"`
	Node   Node      `json:"node,omitempty"`
	Edge   Edge      `json:"edge,omitempty"`
	NodeID string    `json:"node_id,omitempty"`
	Status Status    `json:"status,omitempty"`
}

// Apply returns a new graph with event applied. The receiver is unchanged.
func (g *Graph) Apply(event Event) (*Graph, error) {
	next, err := g.Clone()
	if err != nil {
		return nil, err
	}
	stage := next.Stage()
	if err = stageEvent(stage, event); err != nil {
		return nil, err
	}
	if err = next.Commit(stage); err != nil {
		return nil, err
	}
	return next, nil
}

func stageEvent(stage *Stage, event Event) error {
	switch event.Type {
	case EventNodeAdded:
		stage.AddNode(event.Node)
	case EventEdgeAdded:
		stage.AddEdge(event.Edge)
	case EventNodeRemoved:
		stage.RemoveNode(event.NodeID)
	case EventEdgeRemoved:
		stage.RemoveEdge(event.Edge)
	case EventStatusChanged:
		stage.SetStatus(event.NodeID, event.Status)
	default:
		return fmt.Errorf("graph: event type %q is unknown", event.Type)
	}
	return nil
}
