package graph

import "fmt"

// EventType names the operations of a staged graph change. The event log writes one record
// per committed stage, and that record holds the stage's operations in order.
type EventType string

const (
	EventNodeAdded     EventType = "graph.node_added"
	EventEdgeAdded     EventType = "graph.edge_added"
	EventNodeRemoved   EventType = "graph.node_removed"
	EventEdgeRemoved   EventType = "graph.edge_removed"
	EventStatusChanged EventType = "graph.status_changed"
)

// Event is one operation of a staged change, the pure reducer input for graph replay.
type Event struct {
	Type   EventType `json:"type"`
	Node   Node      `json:"node,omitempty"`
	Edge   Edge      `json:"edge,omitempty"`
	NodeID string    `json:"node_id,omitempty"`
	Status Status    `json:"status,omitempty"`
}

// ApplyStage returns a new graph with all events applied as one staged change: either every
// one of them holds on the result, or the stage is refused as a whole. The receiver is
// unchanged.
func (g *Graph) ApplyStage(events []Event) (*Graph, error) {
	next, err := g.Clone()
	if err != nil {
		return nil, err
	}
	stage := next.Stage()
	for i := 0; i < len(events); i++ {
		if err = stageEvent(stage, events[i]); err != nil {
			return nil, err
		}
	}
	if err = next.Commit(stage); err != nil {
		return nil, err
	}
	return next, nil
}

// KnownEvent reports whether t is one of the operations a stage may hold.
func KnownEvent(t EventType) bool {
	switch t {
	case EventNodeAdded, EventEdgeAdded, EventNodeRemoved, EventEdgeRemoved, EventStatusChanged:
		return true
	}
	return false
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
