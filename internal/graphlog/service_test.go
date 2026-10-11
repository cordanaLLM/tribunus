package graphlog

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golusoris/golusoris/core/clock"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/eventlog"
	"github.com/tekmora/epitrop/internal/graph"
)

func testSigner(t *testing.T) (*corereceipt.Signer, clock.Clock) {
	t.Helper()
	clk := clock.NewFake()
	signer, err := corereceipt.NewSignerFromSeed(bytes.Repeat([]byte{9}, 32), clk)
	if err != nil {
		t.Fatalf("NewSignerFromSeed() = %v, want nil", err)
	}
	return signer, clk
}

// serviceAt opens the log in dir under maxDepth. Two services on one directory are two
// writers of one log.
func serviceAt(t *testing.T, dir string, maxDepth int) *Service {
	t.Helper()
	signer, clk := testSigner(t)
	svc, err := NewService(dir, signer, clk, eventlog.Limits{LockTimeout: time.Minute, MaxReplayRecords: 1000}, maxDepth)
	if err != nil {
		t.Fatalf("NewService() = %v, want nil", err)
	}
	return svc
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// records counts the records in the log's directory, HEAD apart.
func records(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "events"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("ReadDir(events) = %v, want nil", err)
	}
	count := 0
	for i := 0; i < len(entries); i++ {
		name := entries[i].Name()
		if strings.HasSuffix(name, ".json") && name != "HEAD.json" {
			count++
		}
	}
	return count
}

func shown(t *testing.T, svc *Service) ([]byte, int) {
	t.Helper()
	st, err := svc.Replay(testCtx(t))
	if err != nil {
		t.Fatalf("Replay() = %v, want nil", err)
	}
	return st.Graph, st.GraphStagesIgnored
}

func mustCommit(t *testing.T, svc *Service, ops ...graph.Event) graph.Snapshot {
	t.Helper()
	snap, err := svc.Commit(testCtx(t), ops)
	if err != nil {
		t.Fatalf("Commit() = %v, want nil", err)
	}
	return snap
}

// A stage of several operations is one record, and the graph comes back from the log alone.
func TestCommitAppendsOneRecordPerStage(t *testing.T) {
	dir := t.TempDir()
	svc := serviceAt(t, dir, 12)
	if snap, ignored, err := svc.Show(testCtx(t)); err != nil || len(snap.Nodes) != 0 || ignored != 0 {
		t.Fatalf("Show(no log yet) = %+v, %d, %v; want an empty graph", snap, ignored, err)
	}
	after := mustCommit(t, svc, addNode("a"), addNode("b"), addEdge("b", "a"))
	if len(after.Nodes) != 2 || records(t, dir) != 1 {
		t.Fatalf("after one stage: %d nodes, %d records; want 2 nodes in 1 record", len(after.Nodes), records(t, dir))
	}
	mustCommit(t, svc, graph.Event{Type: graph.EventStatusChanged, NodeID: "a", Status: graph.Status{Name: graph.StatusReady}})
	snap, ignored, err := serviceAt(t, dir, 12).Show(testCtx(t))
	if err != nil || ignored != 0 || records(t, dir) != 2 {
		t.Fatalf("Show() = %v, ignored %d, records %d; want nil, 0 and 2", err, ignored, records(t, dir))
	}
	if len(snap.Nodes) != 2 || snap.Nodes[0].Status.Name != graph.StatusReady || len(snap.Nodes[1].DependsOn) != 1 {
		t.Fatalf("graph from the log = %+v, want a ready and b depending on a", snap)
	}
}

// A stage the graph refuses appends nothing: the log holds the records and the graph the
// bytes it had before.
func TestRefusedCommitAppendsNothing(t *testing.T) {
	dir := t.TempDir()
	svc := serviceAt(t, dir, 3)
	mustCommit(t, svc, addNode("n1"), addNode("n2"), addNode("n3"), addEdge("n2", "n1"), addEdge("n3", "n2"))
	before, _ := shown(t, svc)
	tooMany := make([]graph.Event, 0, MaxStageOps+1)
	for i := 0; i <= MaxStageOps; i++ {
		tooMany = append(tooMany, addNode("m"+strconv.Itoa(i)))
	}
	huge := node("big")
	huge.Title = strings.Repeat("t", eventlog.MaxRecordBytes)
	cases := []struct {
		name string
		ops  []graph.Event
		want string
	}{
		{"a cycle", []graph.Event{addEdge("n1", "n3")}, "cycle includes node"},
		{"one node deeper than the configured depth", []graph.Event{addNode("n4"), addEdge("n4", "n3")}, "dependency depth 4 exceeds 3"},
		{"a status no node may take from where it is", []graph.Event{{Type: graph.EventStatusChanged, NodeID: "n1", Status: graph.Status{Name: graph.StatusLanded}}}, "stage refused"},
		{"a valid operation followed by a refused one", []graph.Event{addNode("ok"), addNode("n1")}, "stage refused"},
		{"an unknown operation", []graph.Event{{Type: "graph.node_renamed"}}, `operation 0 has unknown type "graph.node_renamed"`},
		{"no operation", nil, "a stage holds 1 to 256 operations, this one 0"},
		{"one operation too many", tooMany, "a stage holds 1 to 256 operations, this one 257"},
		{"a stage larger than a record", []graph.Event{{Type: graph.EventNodeAdded, Node: huge}}, "record exceeds"},
	}
	for i := 0; i < len(cases); i++ {
		_, err := svc.Commit(testCtx(t), cases[i].ops)
		if err == nil || !strings.Contains(err.Error(), cases[i].want) {
			t.Errorf("%s: Commit() = %v, want an error holding %q", cases[i].name, err, cases[i].want)
		}
		after, ignored := shown(t, svc)
		if records(t, dir) != 1 || !bytes.Equal(after, before) || ignored != 0 {
			t.Errorf("%s: %d records, ignored %d, graph %s; want 1 record and the graph unchanged", cases[i].name, records(t, dir), ignored, after)
		}
	}
}

// The chain exactly at the configured depth is written; the bound is the depth, not below it.
func TestCommitAtTheConfiguredDepthIsWritten(t *testing.T) {
	dir := t.TempDir()
	svc := serviceAt(t, dir, 3)
	mustCommit(t, svc, chain(3)...)
	if records(t, dir) != 1 {
		t.Fatalf("records = %d, want the chain of 3 written under depth 3", records(t, dir))
	}
}

// The configured depth binds new writes only. A log written under depth 12 still replays
// when the config says 3, without a stage ignored, but nothing is written to it until the
// depth is raised again.
func TestLoweredDepthStillReplaysAndRefusesWrites(t *testing.T) {
	dir := t.TempDir()
	mustCommit(t, serviceAt(t, dir, 12), chain(5)...)
	lowered := serviceAt(t, dir, 3)
	snap, ignored, err := lowered.Show(testCtx(t))
	if err != nil || len(snap.Nodes) != 5 || ignored != 0 {
		t.Fatalf("Show() under the lowered depth = %d nodes, ignored %d, %v; want all 5 and nothing ignored", len(snap.Nodes), ignored, err)
	}
	_, err = lowered.Commit(testCtx(t), []graph.Event{addNode("apart")})
	if err == nil || !strings.Contains(err.Error(), "raise graph.max_depth") {
		t.Fatalf("Commit() under the lowered depth = %v, want a refusal that names graph.max_depth", err)
	}
	if records(t, dir) != 1 {
		t.Fatalf("records = %d, want 1: the refused write appended nothing", records(t, dir))
	}
	mustCommit(t, serviceAt(t, dir, 12), addNode("apart"))
}

func TestServiceRefusesWhatItCannotServe(t *testing.T) {
	signer, clk := testSigner(t)
	limits := eventlog.Limits{LockTimeout: time.Minute, MaxReplayRecords: 10}
	cases := []struct {
		name   string
		dir    string
		signer *corereceipt.Signer
		depth  int
		want   string
	}{
		{"no directory", "", signer, 12, "dir is required"},
		{"no signer", t.TempDir(), nil, 12, "signer is required"},
		{"depth zero", t.TempDir(), signer, 0, "max depth 0 is outside 1 to 64"},
		{"depth above the cap", t.TempDir(), signer, config.MaxGraphDepth + 1, "max depth 65 is outside 1 to 64"},
	}
	for i := 0; i < len(cases); i++ {
		_, err := NewService(cases[i].dir, cases[i].signer, clk, limits, cases[i].depth)
		if err == nil || !strings.Contains(err.Error(), cases[i].want) {
			t.Errorf("%s: NewService() = %v, want an error holding %q", cases[i].name, err, cases[i].want)
		}
	}
	if _, err := NewService(t.TempDir(), signer, nil, limits, config.MaxGraphDepth); err != nil {
		t.Errorf("NewService(depth at the cap, no clock) = %v, want nil", err)
	}
	svc := serviceAt(t, t.TempDir(), 12)
	if _, err := svc.Commit(context.Background(), []graph.Event{addNode("a")}); err == nil || !strings.Contains(err.Error(), "graphlog: commit requires a context deadline") {
		t.Errorf("Commit(no deadline) = %v, want the service to refuse it", err)
	}
	// On a log without events a replay touches no file: the service itself has to ask for
	// the deadline, nothing below it would.
	if _, _, err := svc.Show(context.Background()); err == nil || !strings.Contains(err.Error(), "graphlog: replay requires a context deadline") {
		t.Errorf("Show(no deadline, empty log) = %v, want the service to refuse it", err)
	}
	ended, cancel := context.WithTimeout(t.Context(), time.Minute)
	cancel()
	if _, _, err := svc.Show(ended); err == nil || !strings.Contains(err.Error(), "replay context") {
		t.Errorf("Show(ended context) = %v, want it refused", err)
	}
}

// What a second writer appended is seen by the first: both check against the log, not
// against a graph they keep.
func TestTwoWritersSeeEachOthersStages(t *testing.T) {
	dir := t.TempDir()
	first, second := serviceAt(t, dir, 12), serviceAt(t, dir, 12)
	mustCommit(t, first, addNode("a"))
	mustCommit(t, second, addNode("b"), addEdge("b", "a"))
	_, err := first.Commit(testCtx(t), []graph.Event{addEdge("a", "b")})
	if err == nil || !strings.Contains(err.Error(), "cycle includes node") {
		t.Fatalf("Commit(edge closing a cycle with the other writer's stage) = %v, want it refused", err)
	}
	if records(t, dir) != 2 {
		t.Fatalf("records = %d, want 2", records(t, dir))
	}
}
