package graphlog

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/tekmora/epitrop/internal/eventlog"
	"github.com/tekmora/epitrop/internal/graph"
	"github.com/tekmora/epitrop/internal/units"
)

func setStatus(id string, to graph.StatusName) graph.Event {
	return graph.Event{Type: graph.EventStatusChanged, NodeID: id, Status: graph.Status{Name: to}}
}

// toLanded walks a ready node to landed through the transition table.
func toLanded(id string) []graph.Event {
	path := []graph.StatusName{graph.StatusClaimed, graph.StatusImplementing, graph.StatusReview, graph.StatusLanding, graph.StatusLanded}
	ops := make([]graph.Event, 0, len(path))
	for i := 0; i < len(path); i++ {
		ops = append(ops, setStatus(id, path[i]))
	}
	return ops
}

// frontierWorld holds: done (landed), gone (dropped), open (implementing), x (ready, after
// done), stuck (ready, after open), orphan (ready, after gone), idle (proposed) and epic
// (ready, of kind epic).
func frontierWorld(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	svc := serviceAt(t, dir, 8)
	epic := graph.Node{ID: "epic", Kind: graph.KindEpic, Title: "Epic", Status: graph.Status{Name: graph.StatusProposed}}
	ops := []graph.Event{
		addNode("done"), addNode("gone"), addNode("open"), addNode("x"), addNode("stuck"), addNode("orphan"),
		addNode("idle"), {Type: graph.EventNodeAdded, Node: epic},
		addEdge("x", "done"), addEdge("stuck", "open"), addEdge("orphan", "gone"),
	}
	for _, id := range []string{"done", "gone", "open", "x", "stuck", "orphan", "epic"} {
		ops = append(ops, setStatus(id, graph.StatusReady))
	}
	ops = append(ops, toLanded("done")...)
	ops = append(ops, setStatus("gone", graph.StatusDropped), setStatus("open", graph.StatusClaimed), setStatus("open", graph.StatusImplementing))
	mustCommit(t, svc, ops...)
	return svc, dir
}

func unitsOf(t *testing.T, svc *Service) eventlog.State {
	t.Helper()
	st, err := svc.replayWith(testCtx(t), units.FoldName, units.UnitReducer)
	if err != nil {
		t.Fatalf("replay units = %v, want nil", err)
	}
	return st
}

func nodeStatus(t *testing.T, svc *Service, id string) graph.StatusName {
	t.Helper()
	snap, _, err := svc.Show(testCtx(t))
	if err != nil {
		t.Fatalf("Show() = %v, want nil", err)
	}
	for i := 0; i < len(snap.Nodes); i++ {
		if snap.Nodes[i].ID == id {
			return snap.Nodes[i].Status.Name
		}
	}
	t.Fatalf("node %s is not in the graph", id)
	return ""
}

func frontierIDs(t *testing.T, svc *Service) string {
	t.Helper()
	nodes, err := svc.Frontier(testCtx(t))
	if err != nil {
		t.Fatalf("Frontier() = %v, want nil", err)
	}
	ids := make([]string, 0, len(nodes))
	for i := 0; i < len(nodes); i++ {
		ids = append(ids, nodes[i].ID)
	}
	return strings.Join(ids, ",")
}

// The service lists the ready units whose dependencies landed, and writes nothing.
func TestFrontierListsOnlyAssignableNodes(t *testing.T) {
	svc, dir := frontierWorld(t)
	before := records(t, dir)
	if got := frontierIDs(t, svc); got != "x" {
		t.Fatalf("Frontier() = %s, want x alone", got)
	}
	if after := records(t, dir); after != before {
		t.Fatalf("Frontier() wrote %d records, want none", after-before)
	}
}

// One assignment is one record: the node is claimed in the graph and its unit is claimed in
// the run record with the node, the lane and the actor.
func TestAssignClaimsTheNodeAndOpensItsUnitInOneRecord(t *testing.T) {
	svc, dir := frontierWorld(t)
	before := records(t, dir)
	unit, err := svc.Assign(testCtx(t), "tekmora/epitrop", "lane-a", "x", "slot-1")
	if err != nil {
		t.Fatalf("Assign(x) = %v, want nil", err)
	}
	if after := records(t, dir); after != before+1 {
		t.Fatalf("Assign(x) wrote %d records, want 1", after-before)
	}
	want := eventlog.Unit{ID: "tekmora/epitrop:x", Status: "claimed", Lane: "lane-a", By: "slot-1", Node: "x"}
	checkAssignedUnit(t, unit, want)
	checkAssignedServiceState(t, svc, want.ID)
}

func checkAssignedUnit(t *testing.T, unit, want eventlog.Unit) {
	t.Helper()
	if unit.ID != want.ID || unit.Status != want.Status || unit.Lane != want.Lane || unit.By != want.By || unit.Node != want.Node {
		t.Fatalf("Assign(x) = %+v, want %+v", unit, want)
	}
}

func checkAssignedServiceState(t *testing.T, svc *Service, wantID string) {
	t.Helper()
	if got := unitsOf(t, svc).Units[wantID]; got.Status != "claimed" || got.Node != "x" {
		t.Fatalf("replayed unit = %+v, want claimed from x", got)
	}
	if got := nodeStatus(t, svc, "x"); got != graph.StatusClaimed {
		t.Fatalf("node x = %s, want claimed", got)
	}
	if got := frontierIDs(t, svc); got != "" {
		t.Fatalf("Frontier() after the assignment = %s, want empty", got)
	}
}

// A node off the frontier is refused and nothing is written.
func TestAssignRefusesNodesOffTheFrontier(t *testing.T) {
	svc, dir := frontierWorld(t)
	before := records(t, dir)
	cases := map[string]string{
		"stuck":   `depends on "open", which is implementing`,
		"orphan":  `depends on "gone", which is dropped`,
		"idle":    "is proposed, not ready",
		"open":    "is implementing, not ready",
		"epic":    "is of kind epic, not unit",
		"missing": "does not exist",
		"Bad_ID":  "gives no valid unit id",
	}
	for node, want := range cases {
		if _, err := svc.Assign(testCtx(t), "tekmora/epitrop", "lane-a", node, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Assign(%s) = %v, want an error holding %q", node, err, want)
		}
	}
	if _, err := svc.Assign(testCtx(t), "tekmora:epitrop", "lane-a", "x", ""); err == nil || !strings.Contains(err.Error(), "gives no valid unit id") {
		t.Errorf("Assign(x, repository with a colon) = %v, want it refused", err)
	}
	if _, err := svc.Assign(testCtx(t), "tekmora/epitrop", " ", "x", ""); err == nil || !strings.Contains(err.Error(), "lane is required") {
		t.Errorf("Assign(x, blank lane) = %v, want the lane required", err)
	}
	if after := records(t, dir); after != before {
		t.Fatalf("refused assignments wrote %d records, want none", after-before)
	}
}

// A node is assigned once: a second assignment, from the same repository or another, is
// refused, and so is one from two writers racing for it.
func TestSecondAssignOfANodeIsRefused(t *testing.T) {
	svc, dir := frontierWorld(t)
	other := serviceAt(t, dir, 8)
	before := records(t, dir)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, s := range []*Service{svc, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Assign(testCtx(t), "tekmora/epitrop", "lane-a", "x", "")
		}()
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("two racing Assign(x) = %v, %v; want exactly one accepted", errs[0], errs[1])
	}
	for _, repo := range []string{"tekmora/epitrop", "tekmora/other"} {
		if _, err := svc.Assign(testCtx(t), repo, "lane-b", "x", ""); err == nil || !strings.Contains(err.Error(), "is claimed, not ready") {
			t.Errorf("Assign(x) again from %s = %v, want it refused", repo, err)
		}
	}
	if after := records(t, dir); after != before+1 {
		t.Fatalf("assignments wrote %d records, want 1", after-before)
	}
	// A node moved back to ready by hand leaves its unit open: the unit is not opened twice.
	mustCommit(t, svc, setStatus("x", graph.StatusReady))
	if _, err := svc.Assign(testCtx(t), "tekmora/epitrop", "lane-b", "x", ""); err == nil || !strings.Contains(err.Error(), "already open") {
		t.Errorf("Assign(x) with its unit open = %v, want it refused", err)
	}
}

// A release puts the node back on the frontier and drops its unit with the reason, in one
// record; the node can then be assigned again, and its unit starts fresh.
func TestReleaseReturnsTheNodeAndDropsTheUnit(t *testing.T) {
	svc, dir := frontierWorld(t)
	if _, err := svc.Assign(testCtx(t), "tekmora/epitrop", "lane-a", "x", ""); err != nil {
		t.Fatalf("Assign(x) = %v, want nil", err)
	}
	before := records(t, dir)
	unit, err := svc.Release(testCtx(t), "x", "slot died", "operator")
	if err != nil {
		t.Fatalf("Release(x) = %v, want nil", err)
	}
	if after := records(t, dir); after != before+1 {
		t.Fatalf("Release(x) wrote %d records, want 1", after-before)
	}
	checkReleasedUnit(t, unit)
	checkPostReleaseState(t, svc)
	checkReassignAfterRelease(t, svc)
}

func checkReleasedUnit(t *testing.T, unit eventlog.Unit) {
	t.Helper()
	if unit.Status != "dropped" || unit.Released == nil || unit.Released.Reason != "slot died" || unit.By != "operator" {
		t.Fatalf("Release(x) = %+v, want dropped with the reason", unit)
	}
}

func checkPostReleaseState(t *testing.T, svc *Service) {
	t.Helper()
	if got := nodeStatus(t, svc, "x"); got != graph.StatusReady {
		t.Fatalf("node x = %s, want ready", got)
	}
	if got := frontierIDs(t, svc); got != "x" {
		t.Fatalf("Frontier() after the release = %s, want x", got)
	}
}

func checkReassignAfterRelease(t *testing.T, svc *Service) {
	t.Helper()
	again, err := svc.Assign(testCtx(t), "tekmora/epitrop", "lane-b", "x", "")
	if err != nil {
		t.Fatalf("Assign(x) after the release = %v, want nil", err)
	}
	if again.Status != "claimed" || again.Released != nil || again.Lane != "lane-b" {
		t.Fatalf("Assign(x) after the release = %+v, want a fresh claimed unit", again)
	}
}

// A node that is not claimed, or claimed without an assigned unit, is not released, and a
// release needs a reason.
func TestReleaseOfAnUnclaimedNodeIsRefused(t *testing.T) {
	svc, dir := frontierWorld(t)
	mustCommit(t, svc, setStatus("epic", graph.StatusClaimed))
	before := records(t, dir)
	cases := map[string]string{
		"x":       "is ready, not claimed",
		"open":    "is implementing, not claimed",
		"missing": "does not exist",
		"epic":    "has 0 open assigned units",
	}
	for node, want := range cases {
		if _, err := svc.Release(testCtx(t), node, "test", ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Release(%s) = %v, want an error holding %q", node, err, want)
		}
	}
	if _, err := svc.Release(testCtx(t), "x", " ", ""); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Errorf("Release(x, blank reason) = %v, want the reason required", err)
	}
	if after := records(t, dir); after != before {
		t.Fatalf("refused releases wrote %d records, want none", after-before)
	}
}

func assignRecord(t *testing.T, seq uint64, id string, payload any, typ string) eventlog.Record {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() = %v, want nil", err)
	}
	return eventlog.Record{Seq: seq, Type: typ, TaskID: id, Payload: body}
}

// A record no writer of this version produces is settled the same way on every replay: a
// move the graph refuses is counted, a malformed one fails the replay, and the other
// domains skip both record types.
func TestAssignmentRecordsTheWriterNeverWrites(t *testing.T) {
	checkIgnoredAssignmentGraphStages(t)
	assign := assignRecord(t, 2, "r:a", units.AssignPayload{Node: "a", Lane: "l"}, units.AssignedType)
	checkIgnoredAssignmentUnitRecords(t, assign)
	checkBadAssignmentRecords(t)
	if next, err := eventlog.TaskReducer(eventlog.State{}, assign); err != nil || len(next.Tasks) != 0 {
		t.Errorf("TaskReducer(unit.assigned) = %v, %+v; want it skipped", err, next)
	}
}

func checkIgnoredAssignmentGraphStages(t *testing.T) {
	t.Helper()
	ready := []graph.Event{addNode("a"), setStatus("a", graph.StatusReady)}
	assign := assignRecord(t, 2, "r:a", units.AssignPayload{Node: "a", Lane: "l"}, units.AssignedType)
	st := reduce(t, stageRecord(t, 1, ready...), assign, assignRecord(t, 3, "r:a", units.AssignPayload{Node: "a", Lane: "l"}, units.AssignedType))
	if st.GraphStagesIgnored != 1 {
		t.Fatalf("second assignment of a: ignored = %d, want 1", st.GraphStagesIgnored)
	}
	st = reduce(t, stageRecord(t, 1, ready...), assignRecord(t, 2, "r:a", units.ReleasePayload{Node: "a", Reason: "x"}, units.ReleasedType))
	if st.GraphStagesIgnored != 1 {
		t.Fatalf("release of a ready node: ignored = %d, want 1", st.GraphStagesIgnored)
	}
	// The transition table allows these moves; the frontier and the release rule do not.
	blocked := []graph.Event{addNode("a"), addNode("b"), addEdge("b", "a"), setStatus("b", graph.StatusReady),
		setStatus("a", graph.StatusReady), setStatus("a", graph.StatusClaimed), setStatus("a", graph.StatusImplementing)}
	st = reduce(t, stageRecord(t, 1, blocked...), assignRecord(t, 2, "r:b", units.AssignPayload{Node: "b", Lane: "l"}, units.AssignedType),
		assignRecord(t, 3, "r:a", units.ReleasePayload{Node: "a", Reason: "x"}, units.ReleasedType))
	if st.GraphStagesIgnored != 2 {
		t.Fatalf("assignment of a blocked node and release of an implementing one: ignored = %d, want 2", st.GraphStagesIgnored)
	}
}

func replayUnitRecords(t *testing.T, recs ...eventlog.Record) eventlog.State {
	t.Helper()
	st := eventlog.State{}
	for i := 0; i < len(recs); i++ {
		var err error
		if st, err = units.UnitReducer(st, recs[i]); err != nil {
			t.Fatalf("UnitReducer(seq %d) = %v, want nil", recs[i].Seq, err)
		}
	}
	return st
}

func checkIgnoredAssignmentUnitRecords(t *testing.T, assign eventlog.Record) {
	t.Helper()
	if st := replayUnitRecords(t, assign, assignRecord(t, 3, "r:a", units.AssignPayload{Node: "a", Lane: "m"}, units.AssignedType)); st.UnitRecordsIgnored != 1 || st.Units["r:a"].Lane != "l" {
		t.Fatalf("second assignment of an open unit: %+v, want it ignored", st)
	}
	if st := replayUnitRecords(t, assign, assignRecord(t, 3, "r:a", units.ReleasePayload{Node: "b", Reason: "x"}, units.ReleasedType)); st.UnitRecordsIgnored != 1 || st.Units["r:a"].Status != "claimed" {
		t.Fatalf("release naming another node: %+v, want it ignored", st)
	}
}

func checkBadAssignmentRecords(t *testing.T) {
	t.Helper()
	bad := []eventlog.Record{
		assignRecord(t, 1, "r:b", units.AssignPayload{Node: "a", Lane: "l"}, units.AssignedType),
		assignRecord(t, 1, "r:a", units.AssignPayload{Node: "a"}, units.AssignedType),
		assignRecord(t, 1, "r:a", units.ReleasePayload{Node: "a"}, units.ReleasedType),
		{Seq: 1, Type: units.AssignedType, TaskID: "r:a", Payload: json.RawMessage(`[`)},
	}
	for i := 0; i < len(bad); i++ {
		if _, err := Reducer(eventlog.State{}, bad[i]); err == nil {
			t.Errorf("Reducer(bad record %d) = nil, want it refused", i)
		}
		if _, err := units.UnitReducer(eventlog.State{}, bad[i]); err == nil {
			t.Errorf("UnitReducer(bad record %d) = nil, want it refused", i)
		}
	}
}
