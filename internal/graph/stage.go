package graph

import "fmt"

// Stage records a candidate change set. Operations apply in the order added.
type Stage struct {
	ops []operation
}

type operationKind uint8

const (
	opAddNode operationKind = iota
	opRemoveNode
	opAddEdge
	opRemoveEdge
	opSetStatus
)

type operation struct {
	kind   operationKind
	node   Node
	edge   Edge
	nodeID string
	status Status
}

// Stage returns an empty candidate change set.
func (g *Graph) Stage() *Stage {
	return &Stage{}
}

// AddNode stages a node addition.
func (s *Stage) AddNode(node Node) {
	s.ops = append(s.ops, operation{kind: opAddNode, node: node})
}

// RemoveNode stages a node removal.
func (s *Stage) RemoveNode(id string) {
	s.ops = append(s.ops, operation{kind: opRemoveNode, nodeID: id})
}

// AddEdge stages a dependency edge addition.
func (s *Stage) AddEdge(edge Edge) {
	s.ops = append(s.ops, operation{kind: opAddEdge, edge: edge})
}

// RemoveEdge stages a dependency edge removal.
func (s *Stage) RemoveEdge(edge Edge) {
	s.ops = append(s.ops, operation{kind: opRemoveEdge, edge: edge})
}

// SetStatus stages a status transition.
func (s *Stage) SetStatus(nodeID string, status Status) {
	s.ops = append(s.ops, operation{kind: opSetStatus, nodeID: nodeID, status: status})
}

// Commit validates the staged result, then swaps it into g.
func (g *Graph) Commit(stage *Stage) error {
	if g == nil {
		return errNilGraph
	}
	if stage == nil {
		return errNilStage
	}
	next := g.state.clone()
	if err := stage.apply(next); err != nil {
		return err
	}
	if err := validate(next, g.maxDepth); err != nil {
		return err
	}
	g.state = next
	return nil
}

func (s *Stage) apply(next graphState) error {
	for i := 0; i < len(s.ops); i++ {
		if err := applyOperation(next, s.ops[i]); err != nil {
			return err
		}
	}
	return nil
}

func applyOperation(next graphState, op operation) error {
	switch op.kind {
	case opAddNode:
		return applyAddNode(next, op.node)
	case opRemoveNode:
		return applyRemoveNode(next, op.nodeID)
	case opAddEdge:
		return applyAddEdge(next, op.edge)
	case opRemoveEdge:
		return applyRemoveEdge(next, op.edge)
	case opSetStatus:
		return applySetStatus(next, op.nodeID, op.status)
	}
	return fmt.Errorf("graph: unknown staged operation %d", op.kind)
}

func applyAddNode(next graphState, node Node) error {
	if _, exists := next.nodes[node.ID]; exists {
		return fmt.Errorf("graph: node %q already exists", node.ID)
	}
	if node.Status.Name != StatusProposed {
		return fmt.Errorf("graph: node %q initial status %q is not proposed", node.ID, node.Status.Name)
	}
	next.nodes[node.ID] = node
	return nil
}

func applyRemoveNode(next graphState, id string) error {
	if _, exists := next.nodes[id]; !exists {
		return fmt.Errorf("graph: node %q is missing", id)
	}
	if edge, ok := incidentEdge(next, id); ok {
		return fmt.Errorf("graph: node %q still has edge %s", id, edgeKey(edge))
	}
	delete(next.nodes, id)
	return nil
}

func applyAddEdge(next graphState, edge Edge) error {
	if _, exists := next.edges[edge]; exists {
		return fmt.Errorf("graph: edge %s already exists", edgeKey(edge))
	}
	next.edges[edge] = struct{}{}
	return nil
}

func applyRemoveEdge(next graphState, edge Edge) error {
	if _, exists := next.edges[edge]; !exists {
		return fmt.Errorf("graph: edge %s is missing", edgeKey(edge))
	}
	delete(next.edges, edge)
	return nil
}

func applySetStatus(next graphState, id string, status Status) error {
	node, exists := next.nodes[id]
	if !exists {
		return fmt.Errorf("graph: node %q is missing", id)
	}
	if err := allowTransition(node.Status, status); err != nil {
		return fmt.Errorf("graph: node %q status transition: %w", id, err)
	}
	node.Status = status
	next.nodes[id] = node
	return nil
}
