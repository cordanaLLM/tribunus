package graph

import "testing"

func mustSnapshot(t *testing.T, g *Graph) Snapshot {
	t.Helper()
	snap, err := g.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() = %v, want nil", err)
	}
	return snap
}

// A graph that changes already led to comes back from its snapshot byte for byte, with the
// statuses its nodes have reached.
func TestFromSnapshotGivesTheSameGraph(t *testing.T) {
	g := graphWithNodes(t, 12, "a", "b", "c")
	commitStage(t, g, func(stage *Stage) {
		stage.AddEdge(Edge{NodeID: "b", DependsOn: "a"})
		stage.AddEdge(Edge{NodeID: "c", DependsOn: "b"})
	})
	commitPath(t, g, []StatusName{StatusReady, StatusClaimed, StatusImplementing})
	back, err := FromSnapshot(mustSnapshot(t, g), 12)
	if err != nil {
		t.Fatalf("FromSnapshot() = %v, want nil", err)
	}
	requireEqualBytes(t, graphJCS(t, back), graphJCS(t, g))
	empty, err := FromSnapshot(Snapshot{}, 12)
	if err != nil {
		t.Fatalf("FromSnapshot(empty) = %v, want an empty graph", err)
	}
	requireEqualBytes(t, graphJCS(t, empty), graphJCS(t, mustGraph(t, 12)))
}

func TestFromSnapshotRefusesAGraphThatDoesNotHold(t *testing.T) {
	stored := func(id string, dependsOn ...string) SnapshotNode {
		return SnapshotNode{ID: id, Kind: KindUnit, Title: "Task " + id, Status: Status{Name: StatusReady}, DependsOn: dependsOn}
	}
	cases := []struct {
		name     string
		nodes    []SnapshotNode
		maxDepth int
		want     string
	}{
		{"a node named twice", []SnapshotNode{stored("a"), stored("a")}, 12, `names node "a" twice`},
		{"a dependency that is not there", []SnapshotNode{stored("a", "gone")}, 12, `missing dependency "gone"`},
		{"a cycle", []SnapshotNode{stored("a", "b"), stored("b", "a")}, 12, "cycle includes node"},
		{"a self-loop", []SnapshotNode{stored("a", "a")}, 12, "self-loop"},
		{"an unknown kind", []SnapshotNode{{ID: "a", Kind: "saga", Title: "Task a", Status: Status{Name: StatusReady}}}, 12, `kind "saga" is unknown`},
		{"an unknown status", []SnapshotNode{{ID: "a", Kind: KindUnit, Title: "Task a", Status: Status{Name: "paused"}}}, 12, `node "a" status`},
		{"no title", []SnapshotNode{{ID: "a", Kind: KindUnit, Status: Status{Name: StatusReady}}}, 12, "title is empty"},
		{"a chain one deeper than the bound", []SnapshotNode{stored("a"), stored("b", "a"), stored("c", "b"), stored("d", "c")}, 3, "dependency depth 4 exceeds 3"},
		{"a depth outside the config bounds", []SnapshotNode{stored("a")}, 0, "max depth outside config bounds"},
	}
	for i := 0; i < len(cases); i++ {
		_, err := FromSnapshot(Snapshot{Nodes: cases[i].nodes}, cases[i].maxDepth)
		if err == nil {
			t.Errorf("%s: FromSnapshot() = nil, want it refused", cases[i].name)
			continue
		}
		requireErrContains(t, err, cases[i].want)
	}
	atBound, err := FromSnapshot(Snapshot{Nodes: []SnapshotNode{stored("a"), stored("b", "a"), stored("c", "b")}}, 3)
	if err != nil || atBound == nil {
		t.Fatalf("FromSnapshot(chain at the bound) = %v, want nil", err)
	}
}

// A stage of several operations is applied as one change or not at all, and the graph it was
// asked of stays as it was either way.
func TestApplyStageIsAllOrNothing(t *testing.T) {
	g := graphWithNodes(t, 12, "a")
	before := graphJCS(t, g)
	add := func(id string) Event { return Event{Type: EventNodeAdded, Node: validNode(id)} }
	edge := func(id string, dependsOn string) Event {
		return Event{Type: EventEdgeAdded, Edge: Edge{NodeID: id, DependsOn: dependsOn}}
	}
	next, err := g.ApplyStage([]Event{add("b"), edge("b", "a")})
	if err != nil {
		t.Fatalf("ApplyStage(valid) = %v, want nil", err)
	}
	if got := mustSnapshot(t, next); len(got.Nodes) != 2 || len(got.Nodes[1].DependsOn) != 1 {
		t.Fatalf("graph after the stage = %+v, want b depending on a", got)
	}
	requireEqualBytes(t, graphJCS(t, g), before)
	_, err = g.ApplyStage([]Event{add("b"), edge("a", "a")})
	requireErrContains(t, err, "self-loop")
	_, err = g.ApplyStage([]Event{add("b"), {Type: "graph.node_renamed"}})
	requireErrContains(t, err, `event type "graph.node_renamed" is unknown`)
	requireEqualBytes(t, graphJCS(t, g), before)
	same, err := g.ApplyStage(nil)
	if err != nil {
		t.Fatalf("ApplyStage(no operation) = %v, want the graph unchanged", err)
	}
	requireEqualBytes(t, graphJCS(t, same), before)
}

func TestKnownEventNamesTheFiveOperations(t *testing.T) {
	known := []EventType{EventNodeAdded, EventEdgeAdded, EventNodeRemoved, EventEdgeRemoved, EventStatusChanged}
	for i := 0; i < len(known); i++ {
		if !KnownEvent(known[i]) {
			t.Errorf("KnownEvent(%q) = false, want true", known[i])
		}
	}
	unknown := []EventType{"", "graph.stage_committed", "graph.node_renamed", "task.created"}
	for i := 0; i < len(unknown); i++ {
		if KnownEvent(unknown[i]) {
			t.Errorf("KnownEvent(%q) = true, want false", unknown[i])
		}
	}
}
