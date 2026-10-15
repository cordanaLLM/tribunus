package graph

import "fmt"

// Frontier returns the nodes a slot may take next: units that are ready and whose every
// dependency has landed. A dropped dependency blocks its dependants; it never satisfies
// them. The nodes keep the snapshot's order.
func Frontier(snap Snapshot) []SnapshotNode {
	status := statusIndex(snap)
	var out []SnapshotNode
	for i := 0; i < len(snap.Nodes); i++ {
		if frontierRefusal(snap.Nodes[i], status) == nil {
			out = append(out, snap.Nodes[i])
		}
	}
	return out
}

// FrontierRefusal says why the node id is not on the frontier of snap, or nil when it is.
func FrontierRefusal(snap Snapshot, id string) error {
	for i := 0; i < len(snap.Nodes); i++ {
		if snap.Nodes[i].ID == id {
			return frontierRefusal(snap.Nodes[i], statusIndex(snap))
		}
	}
	return fmt.Errorf("graph: node %q does not exist", id)
}

func statusIndex(snap Snapshot) map[string]StatusName {
	status := make(map[string]StatusName, len(snap.Nodes))
	for i := 0; i < len(snap.Nodes); i++ {
		status[snap.Nodes[i].ID] = snap.Nodes[i].Status.Name
	}
	return status
}

func frontierRefusal(node SnapshotNode, status map[string]StatusName) error {
	if node.Kind != KindUnit {
		return fmt.Errorf("graph: node %q is of kind %s, not unit", node.ID, node.Kind)
	}
	if node.Status.Name != StatusReady {
		return fmt.Errorf("graph: node %q is %s, not ready", node.ID, node.Status.Name)
	}
	for i := 0; i < len(node.DependsOn); i++ {
		if dep := status[node.DependsOn[i]]; dep != StatusLanded {
			return fmt.Errorf("graph: node %q depends on %q, which is %s, not landed", node.ID, node.DependsOn[i], dep)
		}
	}
	return nil
}
