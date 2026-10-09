package graph

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestCycleRefusedAndGraphUnchanged(t *testing.T) {
	g := graphWithNodes(t, 12, "a", "b")
	commitStage(t, g, func(stage *Stage) {
		stage.AddEdge(Edge{NodeID: "b", DependsOn: "a"})
	})
	before := graphJCS(t, g)
	err := commitErr(g, func(stage *Stage) {
		stage.AddEdge(Edge{NodeID: "a", DependsOn: "b"})
	})
	requireErrContains(t, err, "cycle includes node")
	requireEqualBytes(t, graphJCS(t, g), before)
}

func TestSelfLoopRefusedAndGraphUnchanged(t *testing.T) {
	g := graphWithNodes(t, 12, "a")
	before := graphJCS(t, g)
	err := commitErr(g, func(stage *Stage) {
		stage.AddEdge(Edge{NodeID: "a", DependsOn: "a"})
	})
	requireErrContains(t, err, `"a" depends_on "a"`)
	requireEqualBytes(t, graphJCS(t, g), before)
}

func TestDepthBoundary(t *testing.T) {
	g := graphWithNodes(t, 3, "n1", "n2", "n3")
	commitStage(t, g, func(stage *Stage) {
		stage.AddEdge(Edge{NodeID: "n2", DependsOn: "n1"})
		stage.AddEdge(Edge{NodeID: "n3", DependsOn: "n2"})
	})
	before := graphJCS(t, g)
	err := commitErr(g, func(stage *Stage) {
		stage.AddNode(validNode("n4"))
		stage.AddEdge(Edge{NodeID: "n4", DependsOn: "n3"})
	})
	requireErrContains(t, err, "dependency depth 4 exceeds 3")
	requireEqualBytes(t, graphJCS(t, g), before)
}

func TestRollbackLeavesJCSBytesIdentical(t *testing.T) {
	g := graphWithNodes(t, 12, "a")
	before := graphJCS(t, g)
	err := commitErr(g, func(stage *Stage) {
		stage.AddNode(validNode("b"))
		stage.AddEdge(Edge{NodeID: "b", DependsOn: "missing"})
	})
	requireErrContains(t, err, "missing dependency")
	requireEqualBytes(t, graphJCS(t, g), before)
}

func TestUnknownKindAndStatusRefused(t *testing.T) {
	cases := map[string]Node{
		"kind":           {ID: "bad-kind", Kind: "story", Title: "Bad", Status: Status{Name: StatusProposed}},
		"status":         {ID: "bad-status", Kind: KindUnit, Title: "Bad", Status: Status{Name: "todo"}},
		"initial status": {ID: "claimed", Kind: KindUnit, Title: "Bad", Status: Status{Name: StatusClaimed}},
	}
	for name, node := range cases {
		t.Run(name, func(t *testing.T) {
			g := mustGraph(t, 12)
			before := graphJCS(t, g)
			err := commitErr(g, func(stage *Stage) { stage.AddNode(node) })
			requireErrContains(t, err, "node")
			requireEqualBytes(t, graphJCS(t, g), before)
		})
	}
}

func TestNodeShapeRefused(t *testing.T) {
	cases := map[string]Node{
		"empty id":    {ID: "", Kind: KindUnit, Title: "Bad", Status: Status{Name: StatusProposed}},
		"empty title": {ID: "bad-title", Kind: KindUnit, Status: Status{Name: StatusProposed}},
	}
	for name, node := range cases {
		t.Run(name, func(t *testing.T) {
			g := mustGraph(t, 12)
			err := commitErr(g, func(stage *Stage) { stage.AddNode(node) })
			requireErrContains(t, err, "node")
		})
	}
}

func TestEdgeToMissingNodeRefused(t *testing.T) {
	g := graphWithNodes(t, 12, "a")
	before := graphJCS(t, g)
	err := commitErr(g, func(stage *Stage) {
		stage.AddEdge(Edge{NodeID: "missing", DependsOn: "a"})
	})
	requireErrContains(t, err, "missing node")
	requireEqualBytes(t, graphJCS(t, g), before)
}

func TestEdgeEmptyEndpointRefused(t *testing.T) {
	g := graphWithNodes(t, 12, "a")
	err := commitErr(g, func(stage *Stage) {
		stage.AddEdge(Edge{NodeID: "a"})
	})
	requireErrContains(t, err, "empty endpoint")
}

func TestInvalidTransitionClassesRefused(t *testing.T) {
	cases := map[string]struct {
		status Status
		want   string
	}{
		"unknown status":    {status: Status{Name: "todo"}, want: "unknown"},
		"invalid route":     {status: Status{Name: StatusReview}, want: "refused"},
		"bad fix round 0":   {status: Status{Name: "fix-round-0"}, want: "unknown"},
		"bad fix round x":   {status: Status{Name: "fix-round-x"}, want: "unknown"},
		"bad fix round 1x":  {status: Status{Name: "fix-round-1x"}, want: "unknown"},
		"bad fix round 12x": {status: Status{Name: "fix-round-12x"}, want: "unknown"},
		"fix round token":   {status: Status{Name: StatusFixRound}, want: "unknown"},
		"terminal landed":   {status: Status{Name: StatusLanded}, want: "refused"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := graphWithNodes(t, 12, "a")
			before := graphJCS(t, g)
			err := commitErr(g, func(stage *Stage) { stage.SetStatus("a", tc.status) })
			requireErrContains(t, err, tc.want)
			requireEqualBytes(t, graphJCS(t, g), before)
		})
	}
}

func TestTerminalStatusTransitionRefused(t *testing.T) {
	cases := map[string]StatusName{
		"landed":  StatusLanded,
		"dropped": StatusDropped,
	}
	for name, terminal := range cases {
		t.Run(name, func(t *testing.T) {
			g := graphWithNodes(t, 12, "a")
			commitPath(t, g, pathToStatus(terminal))
			before := graphJCS(t, g)
			err := commitErr(g, func(stage *Stage) {
				stage.SetStatus("a", Status{Name: StatusReview})
			})
			requireErrContains(t, err, "status transition")
			requireEqualBytes(t, graphJCS(t, g), before)
		})
	}
}

func TestEachStatusReachableViaAllowedTransitions(t *testing.T) {
	paths := map[StatusName][]StatusName{
		StatusReady:        {StatusReady},
		StatusClaimed:      {StatusClaimed},
		StatusImplementing: {StatusClaimed, StatusImplementing},
		StatusReview:       {StatusClaimed, StatusImplementing, StatusReview},
		"fix-round-1":      {StatusClaimed, StatusImplementing, StatusReview, "fix-round-1"},
		StatusBlocked:      {StatusBlocked},
		StatusQueued:       {StatusClaimed, StatusQueued},
		StatusLanding:      {StatusClaimed, StatusQueued, StatusLanding},
		StatusLanded:       {StatusClaimed, StatusQueued, StatusLanding, StatusLanded},
		StatusDropped:      {StatusDropped},
	}
	for want, path := range paths {
		t.Run(string(want), func(t *testing.T) {
			g := graphWithNodes(t, 12, "a")
			commitPath(t, g, path)
			got := graphStatus(t, g, "a")
			if got.Name != want {
				t.Fatalf("status = %q, want %q", got.Name, want)
			}
		})
	}
}

func TestClaimStageMapping(t *testing.T) {
	cases := map[string]struct {
		stage   ClaimStage
		outcome ClaimOutcome
		want    StatusName
	}{
		"claimed":      {stage: ClaimStageClaimed, want: StatusClaimed},
		"implementing": {stage: ClaimStageImplementing, want: StatusImplementing},
		"review":       {stage: ClaimStageReview, want: StatusReview},
		"fix round":    {stage: "fix-round-999", want: "fix-round-999"},
		"blocked":      {stage: ClaimStageBlocked, want: StatusBlocked},
		"queued":       {stage: ClaimStageQueued, want: StatusQueued},
		"landing":      {stage: ClaimStageLanding, want: StatusLanding},
		"landed":       {stage: ClaimStageReleased, outcome: ClaimOutcomeLanded, want: StatusLanded},
		"abandoned":    {stage: ClaimStageReleased, outcome: ClaimOutcomeAbandoned, want: StatusDropped},
		"handed over":  {stage: ClaimStageReleased, outcome: ClaimOutcomeHandedOver, want: StatusReady},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := StatusFromClaim(tc.stage, tc.outcome)
			if err != nil {
				t.Fatalf("StatusFromClaim() = %v, want nil", err)
			}
			if got.Name != tc.want {
				t.Fatalf("status = %q, want %q", got.Name, tc.want)
			}
		})
	}
}

func TestClaimStageMappingRejectsUnknowns(t *testing.T) {
	cases := map[string]struct {
		stage   ClaimStage
		outcome ClaimOutcome
		want    string
	}{
		"missing stage":      {want: "claim stage"},
		"unknown stage":      {stage: "todo", want: "claim stage"},
		"unknown outcome":    {stage: ClaimStageReleased, outcome: "lost", want: "claim release outcome"},
		"missing outcome":    {stage: ClaimStageReleased, want: "claim release outcome"},
		"unexpected outcome": {stage: ClaimStageReview, outcome: ClaimOutcomeLanded, want: "invalid for stage"},
		"bad fix round":      {stage: "fix-round-1000", want: "unknown"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := StatusFromClaim(tc.stage, tc.outcome)
			requireErrContains(t, err, tc.want)
		})
	}
}

func TestBareDigitStatusesRefused(t *testing.T) {
	for _, raw := range []StatusName{"7", "123"} {
		t.Run(string(raw), func(t *testing.T) {
			g := graphWithNodes(t, 12, "a")
			commitPath(t, g, []StatusName{StatusClaimed, StatusImplementing, StatusReview})
			before := graphJCS(t, g)
			err := commitErr(g, func(stage *Stage) { stage.SetStatus("a", Status{Name: raw}) })
			requireErrContains(t, err, "unknown")
			requireEqualBytes(t, graphJCS(t, g), before)
			_, err = StatusFromClaim(ClaimStage(raw), "")
			requireErrContains(t, err, "claim stage")
		})
	}
}

func TestFixRoundZeroPaddedZeroRefused(t *testing.T) {
	g := graphWithNodes(t, 12, "a")
	commitPath(t, g, []StatusName{StatusClaimed, StatusImplementing, StatusReview})
	before := graphJCS(t, g)
	err := commitErr(g, func(stage *Stage) {
		stage.SetStatus("a", Status{Name: "fix-round-000"})
	})
	requireErrContains(t, err, "unknown")
	requireEqualBytes(t, graphJCS(t, g), before)
	_, err = StatusFromClaim("fix-round-000", "")
	requireErrContains(t, err, "unknown")
}

func TestEmptyFixRoundSuffixRefused(t *testing.T) {
	g := graphWithNodes(t, 12, "a")
	commitPath(t, g, []StatusName{StatusClaimed, StatusImplementing, StatusReview})
	before := graphJCS(t, g)
	err := commitErr(g, func(stage *Stage) { stage.SetStatus("a", Status{Name: "fix-round-"}) })
	requireErrContains(t, err, "unknown")
	requireEqualBytes(t, graphJCS(t, g), before)
	_, err = StatusFromClaim("fix-round-", "")
	requireErrContains(t, err, "unknown")
}

func TestRemoveNodeWithIncidentEdgeRefused(t *testing.T) {
	for _, id := range []string{"a", "b"} {
		t.Run(id, func(t *testing.T) {
			g := graphWithNodes(t, 12, "a", "b")
			commitStage(t, g, func(stage *Stage) {
				stage.AddEdge(Edge{NodeID: "b", DependsOn: "a"})
			})
			before := graphJCS(t, g)
			err := commitErr(g, func(stage *Stage) { stage.RemoveNode(id) })
			requireErrContains(t, err, "still has edge")
			requireEqualBytes(t, graphJCS(t, g), before)
		})
	}
}

func TestDuplicateAndMissingOperationsRefused(t *testing.T) {
	g := graphWithNodes(t, 12, "a", "b")
	commitStage(t, g, func(stage *Stage) {
		stage.AddEdge(Edge{NodeID: "b", DependsOn: "a"})
	})
	cases := map[string]func(*Stage){
		"duplicate node":      func(stage *Stage) { stage.AddNode(validNode("a")) },
		"duplicate edge":      func(stage *Stage) { stage.AddEdge(Edge{NodeID: "b", DependsOn: "a"}) },
		"missing node remove": func(stage *Stage) { stage.RemoveNode("missing") },
		"missing edge remove": func(stage *Stage) { stage.RemoveEdge(Edge{NodeID: "a", DependsOn: "b"}) },
		"missing status node": func(stage *Stage) { stage.SetStatus("missing", Status{Name: StatusClaimed}) },
	}
	wants := map[string]string{
		"missing status node": "node \"missing\" is missing",
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			before := graphJCS(t, g)
			err := commitErr(g, build)
			want := "graph:"
			if specific := wants[name]; specific != "" {
				want = specific
			}
			requireErrContains(t, err, want)
			requireEqualBytes(t, graphJCS(t, g), before)
		})
	}
}

func TestNodeAndEdgeCountBoundsRefused(t *testing.T) {
	t.Run("nodes", func(t *testing.T) {
		g := mustGraph(t, 12)
		err := commitErr(g, func(stage *Stage) {
			for i := 0; i <= MaxNodes; i++ {
				stage.AddNode(validNode(fmt.Sprintf("n%04d", i)))
			}
		})
		requireErrContains(t, err, "node count")
	})
	t.Run("edges", func(t *testing.T) {
		g := denseBoundGraph(t)
		err := commitErr(g, func(stage *Stage) {
			addDenseEdges(stage, MaxEdges+1)
		})
		requireErrContains(t, err, "edge count")
	})
}

func TestApplyCommitErrorNamesCauseAndLeavesGraphUnchanged(t *testing.T) {
	g := graphWithNodes(t, 12, "a", "b")
	commitStage(t, g, func(stage *Stage) {
		stage.AddEdge(Edge{NodeID: "b", DependsOn: "a"})
	})
	before := graphJCS(t, g)
	next, err := g.Apply(Event{Type: EventEdgeAdded, Edge: Edge{NodeID: "a", DependsOn: "b"}})
	requireErrContains(t, err, "cycle includes node")
	if next != nil {
		t.Fatalf("Apply(cycle) returned graph, want nil")
	}
	requireEqualBytes(t, graphJCS(t, g), before)
}

func TestApplyReducerPureAndUnknownEventRefused(t *testing.T) {
	g := mustGraph(t, 12)
	before := graphJCS(t, g)
	next, err := g.Apply(Event{Type: EventNodeAdded, Node: validNode("a")})
	if err != nil {
		t.Fatalf("Apply(node_added) = %v, want nil", err)
	}
	requireEqualBytes(t, graphJCS(t, g), before)
	if bytes.Equal(graphJCS(t, next), before) {
		t.Fatalf("Apply(node_added) did not change returned graph")
	}
	_, err = g.Apply(Event{Type: "graph.unknown"})
	requireErrContains(t, err, "event type")
	requireEqualBytes(t, graphJCS(t, g), before)
}

func TestNilGraphAPIsReturnErrors(t *testing.T) {
	var g *Graph
	requireNoPanicErrContains(t, "Clone", "nil graph", func() error {
		_, err := g.Clone()
		return err
	})
	requireNoPanicErrContains(t, "Apply", "nil graph", func() error {
		_, err := g.Apply(Event{Type: "graph.unknown"})
		return err
	})
	requireNoPanicErrContains(t, "Snapshot", "nil graph", func() error {
		_, err := g.Snapshot()
		return err
	})
	requireNoPanicErrContains(t, "JCS", "nil graph", func() error {
		_, err := g.JCS()
		return err
	})
	requireNoPanicErrContains(t, "Commit", "nil graph", func() error {
		return g.Commit(&Stage{})
	})
}

func TestNilStageCommitReturnsError(t *testing.T) {
	g := mustGraph(t, 12)
	requireNoPanicErrContains(t, "Commit", "nil stage", func() error {
		return g.Commit(nil)
	})
}

func TestMalformedStoredNodeRefused(t *testing.T) {
	t.Run("id mismatch", func(t *testing.T) {
		err := validate(malformedState(Node{
			ID: "other", Kind: KindUnit, Title: "Task", Status: Status{Name: StatusProposed},
		}), 12)
		requireErrContains(t, err, `invalid id "other"`)
	})
	t.Run("status", func(t *testing.T) {
		err := validate(malformedState(Node{
			ID: "a", Kind: KindUnit, Title: "Task", Status: Status{Name: "todo"},
		}), 12)
		requireErrContains(t, err, `status: unknown "todo"`)
	})
}

func TestMalformedStoredEdgeEmptyNodeIDRefused(t *testing.T) {
	s := malformedState(validNode("a"))
	s.edges[Edge{DependsOn: "a"}] = struct{}{}
	err := validate(s, 12)
	requireErrContains(t, err, "empty endpoint")
}

func TestUnknownStoredStatusTransitionRefusedAsUnknown(t *testing.T) {
	g := &Graph{maxDepth: 12, state: malformedState(Node{
		ID: "a", Kind: KindUnit, Title: "Task a", Status: Status{Name: "mystery"},
	})}
	err := commitErr(g, func(stage *Stage) { stage.SetStatus("a", Status{Name: StatusClaimed}) })
	requireErrContains(t, err, `unknown "mystery"`)
}

func TestUnknownStagedOperationRefused(t *testing.T) {
	g := mustGraph(t, 12)
	err := g.Commit(&Stage{ops: []operation{{kind: operationKind(99)}}})
	requireErrContains(t, err, "unknown staged operation 99")
}

func TestLargeLinearChainWithinBounds(t *testing.T) {
	g := mustGraph(t, 64)
	commitStage(t, g, func(stage *Stage) {
		for i := 1; i <= 64; i++ {
			stage.AddNode(validNode(fmt.Sprintf("n%02d", i)))
			if i > 1 {
				stage.AddEdge(Edge{
					NodeID:    fmt.Sprintf("n%02d", i),
					DependsOn: fmt.Sprintf("n%02d", i-1),
				})
			}
		}
	})
}

func denseBoundGraph(t *testing.T) *Graph {
	t.Helper()
	g := mustGraph(t, 12)
	commitStage(t, g, func(stage *Stage) {
		for i := 0; i < 260; i++ {
			stage.AddNode(validNode(fmt.Sprintf("n%03d", i)))
		}
	})
	return g
}

func addDenseEdges(stage *Stage, limit int) {
	added := 0
	for dep := 0; dep < 130 && added < limit; dep++ {
		for node := 130; node < 260 && added < limit; node++ {
			stage.AddEdge(Edge{
				NodeID:    fmt.Sprintf("n%03d", node),
				DependsOn: fmt.Sprintf("n%03d", dep),
			})
			added++
		}
	}
}

func TestNewDepthBoundsRefused(t *testing.T) {
	for _, depth := range []int{0, 65} {
		if _, err := New(depth); err == nil {
			t.Fatalf("New(%d) = nil, want error", depth)
		}
	}
}

func graphWithNodes(t *testing.T, maxDepth int, ids ...string) *Graph {
	t.Helper()
	g := mustGraph(t, maxDepth)
	commitStage(t, g, func(stage *Stage) {
		for i := 0; i < len(ids); i++ {
			stage.AddNode(validNode(ids[i]))
		}
	})
	return g
}

func mustGraph(t *testing.T, maxDepth int) *Graph {
	t.Helper()
	g, err := New(maxDepth)
	if err != nil {
		t.Fatalf("New(%d) = %v", maxDepth, err)
	}
	return g
}

func validNode(id string) Node {
	return Node{
		ID: id, Kind: KindUnit, Title: "Task " + id,
		Status: Status{Name: StatusProposed},
	}
}

func commitStage(t *testing.T, g *Graph, build func(*Stage)) {
	t.Helper()
	if err := commitErr(g, build); err != nil {
		t.Fatalf("Commit() = %v, want nil", err)
	}
}

func commitErr(g *Graph, build func(*Stage)) error {
	stage := g.Stage()
	build(stage)
	return g.Commit(stage)
}

func graphJCS(t *testing.T, g *Graph) []byte {
	t.Helper()
	out, err := g.JCS()
	if err != nil {
		t.Fatalf("JCS() = %v", err)
	}
	return out
}

func graphStatus(t *testing.T, g *Graph, id string) Status {
	t.Helper()
	snap, err := g.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() = %v", err)
	}
	for i := 0; i < len(snap.Nodes); i++ {
		if snap.Nodes[i].ID == id {
			return snap.Nodes[i].Status
		}
	}
	t.Fatalf("node %q missing", id)
	return Status{}
}

func malformedState(node Node) graphState {
	return graphState{
		nodes: map[string]Node{"a": node},
		edges: map[Edge]struct{}{},
	}
}

func commitPath(t *testing.T, g *Graph, path []StatusName) {
	t.Helper()
	for i := 0; i < len(path); i++ {
		name := path[i]
		commitStage(t, g, func(stage *Stage) {
			stage.SetStatus("a", Status{Name: name})
		})
	}
}

func pathToStatus(status StatusName) []StatusName {
	switch status {
	case StatusLanded:
		return []StatusName{StatusClaimed, StatusQueued, StatusLanding, StatusLanded}
	case StatusDropped:
		return []StatusName{StatusDropped}
	}
	return []StatusName{status}
}

func requireErrContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want substring %q", err, want)
	}
}

func requireNoPanicErrContains(t *testing.T, name string, want string, call func() error) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("%s panic = %v, want error containing %q", name, recovered, want)
		}
	}()
	requireErrContains(t, call(), want)
}

func requireEqualBytes(t *testing.T, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Fatalf("bytes differ:\n got %s\nwant %s", got, want)
	}
}
