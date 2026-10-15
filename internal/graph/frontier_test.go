package graph

import (
	"strings"
	"testing"
)

func frontierNode(id string, kind Kind, status StatusName, deps ...string) SnapshotNode {
	return SnapshotNode{ID: id, Kind: kind, Title: "Task " + id, Status: Status{Name: status}, DependsOn: deps}
}

// The frontier holds ready units whose every dependency has landed, and nothing else: a
// node in another status, a node of another kind, a dependency still open and a dropped
// dependency each keep a node off it.
func TestFrontierHoldsReadyUnitsWithLandedDependencies(t *testing.T) {
	snap := Snapshot{MaxDepth: 8, Nodes: []SnapshotNode{
		frontierNode("done", KindUnit, StatusLanded),
		frontierNode("gone", KindUnit, StatusDropped),
		frontierNode("open", KindUnit, StatusImplementing),
		frontierNode("free", KindUnit, StatusReady),
		frontierNode("after-done", KindUnit, StatusReady, "done"),
		frontierNode("after-open", KindUnit, StatusReady, "done", "open"),
		frontierNode("after-gone", KindUnit, StatusReady, "gone"),
		frontierNode("proposed", KindUnit, StatusProposed),
		frontierNode("claimed", KindUnit, StatusClaimed),
		frontierNode("epic", KindEpic, StatusReady),
		frontierNode("gate", KindGate, StatusReady, "done"),
	}}
	var got []string
	for _, n := range Frontier(snap) {
		got = append(got, n.ID)
	}
	if want := "free,after-done"; strings.Join(got, ",") != want {
		t.Fatalf("Frontier() = %v, want %s", got, want)
	}
	refusals := map[string]string{
		"after-open": `depends on "open", which is implementing`,
		"after-gone": `depends on "gone", which is dropped`,
		"proposed":   "is proposed, not ready",
		"claimed":    "is claimed, not ready",
		"epic":       "is of kind epic, not unit",
		"missing":    "does not exist",
	}
	for id, want := range refusals {
		if err := FrontierRefusal(snap, id); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("FrontierRefusal(%s) = %v, want it to hold %q", id, err, want)
		}
	}
	if err := FrontierRefusal(snap, "after-done"); err != nil {
		t.Errorf("FrontierRefusal(after-done) = %v, want nil", err)
	}
}

// An empty graph has an empty frontier.
func TestFrontierOfAnEmptyGraphIsEmpty(t *testing.T) {
	if got := Frontier(Snapshot{MaxDepth: 1}); len(got) != 0 {
		t.Fatalf("Frontier(empty) = %v, want none", got)
	}
}
