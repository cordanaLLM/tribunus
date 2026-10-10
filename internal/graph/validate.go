package graph

import (
	"fmt"
	"sort"
)

func validate(s graphState, maxDepth int) error {
	if len(s.nodes) > MaxNodes {
		return fmt.Errorf("graph: node count %d exceeds %d", len(s.nodes), MaxNodes)
	}
	if len(s.edges) > MaxEdges {
		return fmt.Errorf("graph: edge count %d exceeds %d", len(s.edges), MaxEdges)
	}
	if err := validateNodes(s); err != nil {
		return err
	}
	if err := validateEdges(s); err != nil {
		return err
	}
	return validateAcyclicDepth(s, maxDepth)
}

func validateNodes(s graphState) error {
	for id, node := range s.nodes {
		if node.ID == "" || node.ID != id {
			return fmt.Errorf("graph: node %q has invalid id %q", id, node.ID)
		}
		if node.Title == "" {
			return fmt.Errorf("graph: node %q title is empty", id)
		}
		if !validKind(node.Kind) {
			return fmt.Errorf("graph: node %q kind %q is unknown", id, node.Kind)
		}
		if err := validateStatus(node.Status); err != nil {
			return fmt.Errorf("graph: node %q status: %w", id, err)
		}
	}
	return nil
}

func validateEdges(s graphState) error {
	for edge := range s.edges {
		if edge.NodeID == "" || edge.DependsOn == "" {
			return fmt.Errorf("graph: edge %s has empty endpoint", edgeKey(edge))
		}
		if edge.NodeID == edge.DependsOn {
			return fmt.Errorf("graph: edge %s is a self-loop", edgeKey(edge))
		}
		if _, ok := s.nodes[edge.NodeID]; !ok {
			return fmt.Errorf("graph: edge %s has missing node %q", edgeKey(edge), edge.NodeID)
		}
		if _, ok := s.nodes[edge.DependsOn]; !ok {
			return fmt.Errorf("graph: edge %s has missing dependency %q", edgeKey(edge), edge.DependsOn)
		}
	}
	return nil
}

func validateAcyclicDepth(s graphState, maxDepth int) error {
	order, edges, err := kahn(s)
	if err != nil {
		return err
	}
	depths := map[string]int{}
	for i := 0; i < len(order); i++ {
		node := order[i]
		depth := depths[node]
		if depth == 0 {
			depth = 1
		}
		if depth > maxDepth {
			return fmt.Errorf("graph: node %q dependency depth %d exceeds %d", node, depth, maxDepth)
		}
		for j := 0; j < len(edges[node]); j++ {
			child := edges[node][j]
			if depths[child] < depth+1 {
				depths[child] = depth + 1
			}
		}
	}
	return nil
}

func kahn(s graphState) ([]string, map[string][]string, error) {
	nodes := sortedNodeIDs(s.nodes)
	degrees := make(map[string]int, len(nodes))
	edges := make(map[string][]string, len(nodes))
	for i := 0; i < len(nodes); i++ {
		degrees[nodes[i]] = 0
	}
	for edge := range s.edges {
		degrees[edge.NodeID]++
		edges[edge.DependsOn] = append(edges[edge.DependsOn], edge.NodeID)
	}
	for id := range edges {
		sort.Strings(edges[id])
	}
	return drainKahn(nodes, degrees, edges)
}

func drainKahn(nodes []string, degrees map[string]int, edges map[string][]string) ([]string, map[string][]string, error) {
	ready := make([]string, 0, len(nodes))
	for i := 0; i < len(nodes); i++ {
		if degrees[nodes[i]] == 0 {
			ready = append(ready, nodes[i])
		}
	}
	order := make([]string, 0, len(nodes))
	for head := 0; head < len(ready); head++ {
		node := ready[head]
		order = append(order, node)
		for i := 0; i < len(edges[node]); i++ {
			child := edges[node][i]
			degrees[child]--
			if degrees[child] == 0 {
				ready = append(ready, child)
			}
		}
	}
	if len(order) != len(nodes) {
		return nil, nil, fmt.Errorf("graph: cycle includes node %q", firstCycleNode(nodes, degrees))
	}
	return order, edges, nil
}

func firstCycleNode(nodes []string, degrees map[string]int) string {
	for i := 0; i < len(nodes); i++ {
		if degrees[nodes[i]] > 0 {
			return nodes[i]
		}
	}
	return ""
}

func sortedNodeIDs(nodes map[string]Node) []string {
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
