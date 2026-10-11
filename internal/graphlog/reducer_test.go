package graphlog

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golusoris/golusoris/core/clock"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/eventlog"
	"github.com/tekmora/epitrop/internal/graph"
	"github.com/tekmora/epitrop/internal/supervisor"
	"github.com/tekmora/epitrop/internal/units"
)

func node(id string) graph.Node {
	return graph.Node{ID: id, Kind: graph.KindUnit, Title: "Task " + id, Status: graph.Status{Name: graph.StatusProposed}}
}

func addNode(id string) graph.Event {
	return graph.Event{Type: graph.EventNodeAdded, Node: node(id)}
}

func addEdge(id string, dependsOn string) graph.Event {
	return graph.Event{Type: graph.EventEdgeAdded, Edge: graph.Edge{NodeID: id, DependsOn: dependsOn}}
}

func stagePayload(t *testing.T, ops ...graph.Event) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(StagePayload{Ops: ops})
	if err != nil {
		t.Fatalf("Marshal(stage) = %v, want nil", err)
	}
	return body
}

func stageRecord(t *testing.T, seq uint64, ops ...graph.Event) eventlog.Record {
	t.Helper()
	return eventlog.Record{Seq: seq, Type: StageCommitted, Payload: stagePayload(t, ops...)}
}

// reduce applies records in order and fails the test on the first error.
func reduce(t *testing.T, recs ...eventlog.Record) eventlog.State {
	t.Helper()
	st := eventlog.State{}
	for i := 0; i < len(recs); i++ {
		next, err := Reducer(st, recs[i])
		if err != nil {
			t.Fatalf("Reducer(seq %d) = %v, want nil", recs[i].Seq, err)
		}
		st = next
	}
	return st
}

func dependsOn(t *testing.T, st eventlog.State, id string) []string {
	t.Helper()
	g, err := Graph(st)
	if err != nil {
		t.Fatalf("Graph() = %v, want nil", err)
	}
	snap, err := g.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() = %v, want nil", err)
	}
	for i := 0; i < len(snap.Nodes); i++ {
		if snap.Nodes[i].ID == id {
			return snap.Nodes[i].DependsOn
		}
	}
	t.Fatalf("node %q is not in the graph %s", id, st.Graph)
	return nil
}

func TestStagesRebuildTheGraph(t *testing.T) {
	st := reduce(t,
		stageRecord(t, 1, addNode("a"), addNode("b"), addEdge("b", "a")),
		stageRecord(t, 2, addNode("c"), addEdge("c", "b"),
			graph.Event{Type: graph.EventStatusChanged, NodeID: "a", Status: graph.Status{Name: graph.StatusReady}}),
	)
	if got := dependsOn(t, st, "c"); len(got) != 1 || got[0] != "b" {
		t.Fatalf("c depends on %v, want [b]", got)
	}
	if st.GraphStagesIgnored != 0 {
		t.Fatalf("ignored stages = %d, want 0", st.GraphStagesIgnored)
	}
	want := `{"max_depth":64,"nodes":[` +
		`{"id":"a","kind":"unit","status":{"name":"ready"},"title":"Task a"},` +
		`{"depends_on":["a"],"id":"b","kind":"unit","status":{"name":"proposed"},"title":"Task b"},` +
		`{"depends_on":["b"],"id":"c","kind":"unit","status":{"name":"proposed"},"title":"Task c"}]}`
	if string(st.Graph) != want {
		t.Fatalf("graph = %s\nwant    %s", st.Graph, want)
	}
}

// The same records give the same bytes on every replay, and the graph sits in the state's
// own canonical form beside the other domains.
func TestReplayTwiceGivesTheSameBytes(t *testing.T) {
	recs := []eventlog.Record{
		stageRecord(t, 1, addNode("a"), addNode("b")),
		stageRecord(t, 2, addEdge("b", "a")),
	}
	first, err := reduce(t, recs...).CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() = %v, want nil", err)
	}
	second, err := reduce(t, recs...).CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() = %v, want nil", err)
	}
	if !bytes.Equal(first, second) || !bytes.Contains(first, []byte(`"graph":{"max_depth":64`)) {
		t.Fatalf("replays differ or hold no graph:\n%s\n%s", first, second)
	}
}

// A stage the graph refuses is what two racing writers produce. It is not applied, it is
// counted, and the graph keeps the bytes it had. The stage is refused as a whole: its first
// operation alone would have been fine.
func TestRefusedStageIsIgnoredAsAWholeAndCounted(t *testing.T) {
	base := []eventlog.Record{stageRecord(t, 1, addNode("a"), addNode("b"), addEdge("b", "a"))}
	before := reduce(t, base...)
	cases := []struct {
		name string
		ops  []graph.Event
	}{
		{"a cycle with what the log already holds", []graph.Event{addNode("c"), addEdge("a", "b")}},
		{"a node another writer already added", []graph.Event{addNode("c"), addNode("a")}},
		{"an edge to a node that is not there", []graph.Event{addNode("c"), addEdge("c", "gone")}},
		{"a self-loop", []graph.Event{addNode("c"), addEdge("c", "c")}},
		{"a status no node may take from where it is", []graph.Event{addNode("c"),
			{Type: graph.EventStatusChanged, NodeID: "a", Status: graph.Status{Name: graph.StatusLanded}}}},
		{"a node of an unknown kind", []graph.Event{addNode("c"),
			{Type: graph.EventNodeAdded, Node: graph.Node{ID: "d", Kind: "saga", Title: "Task d", Status: graph.Status{Name: graph.StatusProposed}}}}},
	}
	for i := 0; i < len(cases); i++ {
		after := reduce(t, append(base[:1:1], stageRecord(t, 2, cases[i].ops...))...)
		if after.GraphStagesIgnored != 1 {
			t.Errorf("%s: ignored stages = %d, want 1", cases[i].name, after.GraphStagesIgnored)
		}
		if !bytes.Equal(after.Graph, before.Graph) {
			t.Errorf("%s: graph = %s, want it unchanged %s", cases[i].name, after.Graph, before.Graph)
		}
	}
}

// A record no writer of this version produces stops the replay and names its position.
func TestMalformedStageRecordFailsTheReplay(t *testing.T) {
	tooMany := make([]graph.Event, 0, MaxStageOps+1)
	for i := 0; i <= MaxStageOps; i++ {
		tooMany = append(tooMany, addNode("n"+strconv.Itoa(i)))
	}
	cases := []struct {
		name string
		rec  eventlog.Record
		want string
	}{
		{"an unknown graph record type", eventlog.Record{Seq: 7, Type: "graph.node_added", Payload: stagePayload(t, addNode("a"))}, `seq 7: unknown event type "graph.node_added"`},
		{"an unknown domain", eventlog.Record{Seq: 7, Type: "plan.created", Payload: json.RawMessage(`{}`)}, `seq 7: unknown event type "plan.created"`},
		{"a payload that is no JSON object", eventlog.Record{Seq: 7, Type: StageCommitted, Payload: json.RawMessage(`[]`)}, "seq 7: payload"},
		{"a payload with a field no writer sets", eventlog.Record{Seq: 7, Type: StageCommitted, Payload: json.RawMessage(`{"ops":[],"note":"x"}`)}, "seq 7: payload"},
		{"a stage without an operation", eventlog.Record{Seq: 7, Type: StageCommitted, Payload: json.RawMessage(`{"ops":[]}`)}, "seq 7: a stage holds 1 to 256 operations, this one 0"},
		{"a stage with one operation too many", eventlog.Record{Seq: 7, Type: StageCommitted, Payload: stagePayload(t, tooMany...)}, "seq 7: a stage holds 1 to 256 operations, this one 257"},
		{"an operation of an unknown type", eventlog.Record{Seq: 7, Type: StageCommitted, Payload: json.RawMessage(`{"ops":[{"type":"graph.node_renamed"}]}`)}, `seq 7: operation 0 has unknown type "graph.node_renamed"`},
	}
	for i := 0; i < len(cases); i++ {
		_, err := Reducer(eventlog.State{}, cases[i].rec)
		if err == nil || !strings.Contains(err.Error(), cases[i].want) {
			t.Errorf("%s: Reducer() = %v, want an error holding %q", cases[i].name, err, cases[i].want)
		}
	}
}

// The largest stage a record may hold is applied; the bound is on the count, not below it.
func TestStageAtTheOperationBoundIsApplied(t *testing.T) {
	ops := make([]graph.Event, 0, MaxStageOps)
	for i := 0; i < MaxStageOps; i++ {
		ops = append(ops, addNode("n"+strconv.Itoa(i)))
	}
	st := reduce(t, stageRecord(t, 1, ops...))
	g, err := Graph(st)
	if err != nil {
		t.Fatalf("Graph() = %v, want nil", err)
	}
	snap, err := g.Snapshot()
	if err != nil || len(snap.Nodes) != MaxStageOps || st.GraphStagesIgnored != 0 {
		t.Fatalf("nodes = %d, ignored = %d, err = %v; want %d nodes applied", len(snap.Nodes), st.GraphStagesIgnored, err, MaxStageOps)
	}
}

// chain returns the operations of a dependency chain of length nodes.
func chain(length int) []graph.Event {
	ops := make([]graph.Event, 0, 2*length)
	for i := 1; i <= length; i++ {
		ops = append(ops, addNode("n"+strconv.Itoa(i)))
		if i > 1 {
			ops = append(ops, addEdge("n"+strconv.Itoa(i), "n"+strconv.Itoa(i-1)))
		}
	}
	return ops
}

// A replay checks the hard depth cap, whatever depth a config names: a chain at the cap is
// applied, one node deeper is refused and counted.
func TestReplayChecksTheHardDepthCap(t *testing.T) {
	atCap := reduce(t, stageRecord(t, 1, chain(config.MaxGraphDepth)...))
	if atCap.GraphStagesIgnored != 0 || len(atCap.Graph) == 0 {
		t.Fatalf("chain of %d: ignored = %d, graph = %d bytes; want it applied", config.MaxGraphDepth, atCap.GraphStagesIgnored, len(atCap.Graph))
	}
	deeper := reduce(t, stageRecord(t, 1, chain(config.MaxGraphDepth+1)...))
	if deeper.GraphStagesIgnored != 1 || len(deeper.Graph) != 0 {
		t.Fatalf("chain of %d: ignored = %d, graph = %s; want it refused and counted", config.MaxGraphDepth+1, deeper.GraphStagesIgnored, deeper.Graph)
	}
}

// A state whose stored graph does not hold together is refused, not read as an empty graph.
func TestStoredGraphThatIsBrokenIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		stored string
	}{
		{"no JSON", `{`},
		{"an edge to a missing node", `{"max_depth":64,"nodes":[{"id":"a","kind":"unit","title":"Task a","status":{"name":"proposed"},"depends_on":["gone"]}]}`},
		{"a cycle", `{"max_depth":64,"nodes":[{"id":"a","kind":"unit","title":"Task a","status":{"name":"proposed"},"depends_on":["b"]},{"id":"b","kind":"unit","title":"Task b","status":{"name":"proposed"},"depends_on":["a"]}]}`},
	}
	for i := 0; i < len(cases); i++ {
		st := eventlog.State{Graph: json.RawMessage(cases[i].stored)}
		if _, err := Graph(st); err == nil || !strings.Contains(err.Error(), "stored graph") {
			t.Errorf("%s: Graph() = %v, want the stored graph refused", cases[i].name, err)
		}
		if _, err := Reducer(st, stageRecord(t, 9, addNode("z"))); err == nil || !strings.Contains(err.Error(), "seq 9") {
			t.Errorf("%s: Reducer() = %v, want the replay stopped at seq 9", cases[i].name, err)
		}
	}
}

// The other domains' reducers pass over a stage record, and this one passes over theirs: one
// log serves all of them.
func TestOtherReducersSkipStageRecordsAndThisOneSkipsTheirs(t *testing.T) {
	stage := stageRecord(t, 1, addNode("a"))
	others := []struct {
		name   string
		reduce eventlog.Reducer
	}{
		{"tasks", eventlog.TaskReducer},
		{"units", units.UnitReducer},
		{"jobs", supervisor.JobReducer},
	}
	for i := 0; i < len(others); i++ {
		st, err := others[i].reduce(eventlog.State{}, stage)
		if err != nil || len(st.Graph) != 0 {
			t.Errorf("%s reducer on a stage record = %v, graph %s; want it skipped", others[i].name, err, st.Graph)
		}
	}
	theirs := []string{"task.created", "job.started", "unit.recorded"}
	for i := 0; i < len(theirs); i++ {
		st, err := Reducer(eventlog.State{}, eventlog.Record{Seq: 1, Type: theirs[i], TaskID: "x", Payload: json.RawMessage(`{}`)})
		if err != nil || len(st.Graph) != 0 || st.GraphStagesIgnored != 0 {
			t.Errorf("Reducer(%s) = %v, state %+v; want the record skipped", theirs[i], err, st)
		}
	}
}

// Stage records written to a real signed log, between a task and a unit record, replay into
// the graph through the log's own replay.
func TestStagesReplayFromASignedLogBesideOtherRecords(t *testing.T) {
	clk := clock.NewFake()
	seed := bytes.Repeat([]byte{7}, 32)
	signer, err := corereceipt.NewSignerFromSeed(seed, clk)
	if err != nil {
		t.Fatalf("NewSignerFromSeed() = %v, want nil", err)
	}
	dir := t.TempDir()
	limits := eventlog.Limits{Clock: clk, LockTimeout: time.Minute, MaxReplayRecords: 100}
	writer, err := eventlog.Open(dir, signer, limits)
	if err != nil {
		t.Fatalf("Open() = %v, want nil", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	events := []eventlog.Event{
		{Type: "task.created", TaskID: "task-1", Payload: json.RawMessage(`{"state":"open"}`)},
		{Type: StageCommitted, Payload: stagePayload(t, addNode("a"), addNode("b"))},
		{Type: "unit.recorded", TaskID: "u1", Payload: json.RawMessage(`{"status":"claimed"}`)},
		{Type: StageCommitted, Payload: stagePayload(t, addEdge("b", "a"))},
	}
	for i := 0; i < len(events); i++ {
		if _, err = writer.Append(ctx, events[i]); err != nil {
			t.Fatalf("Append(%s) = %v, want nil", events[i].Type, err)
		}
	}
	st, err := eventlog.ReplayStable(ctx, dir, signer.PublicKey(), limits, Reducer)
	if err != nil {
		t.Fatalf("ReplayStable() = %v, want nil", err)
	}
	if got := dependsOn(t, st, "b"); len(got) != 1 || got[0] != "a" || st.GraphStagesIgnored != 0 {
		t.Fatalf("b depends on %v, ignored = %d; want [a] and 0", got, st.GraphStagesIgnored)
	}
}
