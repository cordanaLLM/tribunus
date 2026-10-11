package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cordanaLLM/tribunus/internal/eventlog"
	"github.com/cordanaLLM/tribunus/internal/graph"
	"github.com/cordanaLLM/tribunus/internal/graphlog"
	"github.com/jonboulle/clockwork"
)

// graphConfig writes a config whose event log is fresh and whose graph depth is depth. It
// returns the config path, the log directory and the signing key path.
func graphConfig(t *testing.T, depth int) (string, string, string) {
	t.Helper()
	cfgPath, stateDir := writeUnitsConfig(t)
	keyPath := filepath.Join(filepath.Dir(cfgPath), "seed.hex")
	body := fmt.Sprintf(`{"event_log":{"dir":"%s","signing_key_path":"%s"},"graph":{"max_depth":%d}}`, stateDir, keyPath, depth)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(config) = %v", err)
	}
	return cfgPath, stateDir, keyPath
}

// graphRun runs one graph command and returns what it wrote to stdout and stderr.
func graphRun(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	oldOut, oldErr, oldIn := graphOut, graphErr, graphStdin
	graphOut, graphErr, graphStdin = &out, &errOut, strings.NewReader(stdin)
	defer func() { graphOut, graphErr, graphStdin = oldOut, oldErr, oldIn }()
	err := runGraph(args)
	return out.String(), errOut.String(), err
}

func stageFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stage.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(stage) = %v", err)
	}
	return path
}

func nodeOp(id string) string {
	return fmt.Sprintf(`{"type":"graph.node_added","node":{"id":"%s","kind":"unit","title":"Task %s","status":{"name":"proposed"}}}`, id, id)
}

func edgeOp(id string, dependsOn string) string {
	return fmt.Sprintf(`{"type":"graph.edge_added","edge":{"node_id":"%s","depends_on":"%s"}}`, id, dependsOn)
}

func shownNodes(t *testing.T, out string) []graph.SnapshotNode {
	t.Helper()
	var snap graph.Snapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("output is no graph: %v\n%s", err, out)
	}
	return snap.Nodes
}

// mustShow returns what graph show prints for the log of cfg.
func mustShow(t *testing.T, cfg string) string {
	t.Helper()
	out, _, err := graphRun(t, "", "show", "--config", cfg)
	if err != nil {
		t.Fatalf("show = %v, want nil", err)
	}
	return out
}

func TestRunGraphApplyThenShowEndToEnd(t *testing.T) {
	cfg, _, _ := graphConfig(t, 12)
	out, errOut, err := graphRun(t, "", "show", "--config", cfg)
	if err != nil || len(shownNodes(t, out)) != 0 || errOut != "" {
		t.Fatalf("show(no log yet) = %q, %q, %v; want an empty graph", out, errOut, err)
	}
	stage := stageFile(t, `{"ops":[`+nodeOp("a")+`,`+nodeOp("b")+`,`+edgeOp("b", "a")+`]}`)
	out, _, err = graphRun(t, "", "apply", "--config", cfg, "--file", stage)
	if err != nil || len(shownNodes(t, out)) != 2 {
		t.Fatalf("apply(file) = %q, %v; want the graph with two nodes", out, err)
	}
	fromStdin := `{"ops":[{"type":"graph.status_changed","node_id":"a","status":{"name":"ready"}}]}`
	if _, _, err = graphRun(t, fromStdin, "apply", "--config", cfg, "--file", "-"); err != nil {
		t.Fatalf("apply(stdin) = %v, want nil", err)
	}
	out, errOut, err = graphRun(t, "", "show", "--config", cfg)
	nodes := shownNodes(t, out)
	if err != nil || errOut != "" || len(nodes) != 2 || nodes[0].Status.Name != graph.StatusReady || len(nodes[1].DependsOn) != 1 {
		t.Fatalf("show = %q, %q, %v; want a ready and b depending on a", out, errOut, err)
	}
}

// A stage that is refused, by the command or by the graph, leaves the log's graph as it was.
func TestRunGraphApplyRefusalsChangeNothing(t *testing.T) {
	cfg, _, _ := graphConfig(t, 12)
	first := stageFile(t, `{"ops":[`+nodeOp("a")+`,`+nodeOp("b")+`,`+edgeOp("b", "a")+`]}`)
	if _, _, err := graphRun(t, "", "apply", "--config", cfg, "--file", first); err != nil {
		t.Fatalf("apply(first stage) = %v, want nil", err)
	}
	before := mustShow(t, cfg)
	tooLarge := `{"ops":[` + nodeOp(strings.Repeat("x", eventlog.MaxRecordBytes)) + `]}`
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no stage file named", []string{"apply", "--config", cfg}, "--file is required"},
		{"a stage file that is not there", []string{"apply", "--config", cfg, "--file", filepath.Join(t.TempDir(), "none.json")}, "read stage"},
		{"a stage that is no JSON", []string{"apply", "--config", cfg, "--file", stageFile(t, `{"ops":`)}, "graph apply: stage:"},
		{"a field the stage does not have", []string{"apply", "--config", cfg, "--file", stageFile(t, `{"ops":[],"note":"x"}`)}, "graph apply: stage:"},
		{"text after the stage", []string{"apply", "--config", cfg, "--file", stageFile(t, `{"ops":[`+nodeOp("c")+`]} {}`)}, "text after the stage document"},
		{"a stage larger than a record", []string{"apply", "--config", cfg, "--file", stageFile(t, tooLarge)}, "larger than 65536 bytes"},
		{"a stage without an operation", []string{"apply", "--config", cfg, "--file", stageFile(t, `{"ops":[]}`)}, "a stage holds 1 to 256 operations, this one 0"},
		{"a cycle", []string{"apply", "--config", cfg, "--file", stageFile(t, `{"ops":[`+edgeOp("a", "b")+`]}`)}, "cycle includes node"},
		{"a second operation the graph refuses", []string{"apply", "--config", cfg, "--file", stageFile(t, `{"ops":[`+nodeOp("c")+`,`+nodeOp("a")+`]}`)}, "stage refused"},
		{"no config", []string{"apply", "--file", first}, "--config is required"},
		{"no command", nil, "graph command required"},
		{"an unknown command", []string{"prune"}, "unknown graph command: prune"},
	}
	for i := 0; i < len(cases); i++ {
		out, _, err := graphRun(t, "", cases[i].args...)
		if err == nil || !strings.Contains(err.Error(), cases[i].want) || out != "" {
			t.Errorf("%s: output %q, error %v; want no output and an error holding %q", cases[i].name, out, err, cases[i].want)
		}
		if after := mustShow(t, cfg); after != before {
			t.Errorf("%s: graph = %s, want it unchanged %s", cases[i].name, after, before)
		}
	}
}

// The commands write under the configured depth: a chain at graph.max_depth is taken, one
// node deeper is refused.
func TestRunGraphApplyUsesTheConfiguredDepth(t *testing.T) {
	cfg, _, _ := graphConfig(t, 2)
	atDepth := stageFile(t, `{"ops":[`+nodeOp("a")+`,`+nodeOp("b")+`,`+edgeOp("b", "a")+`]}`)
	if _, _, err := graphRun(t, "", "apply", "--config", cfg, "--file", atDepth); err != nil {
		t.Fatalf("apply(chain of 2 under depth 2) = %v, want nil", err)
	}
	deeper := stageFile(t, `{"ops":[`+nodeOp("c")+`,`+edgeOp("c", "b")+`]}`)
	_, _, err := graphRun(t, "", "apply", "--config", cfg, "--file", deeper)
	if err == nil || !strings.Contains(err.Error(), "dependency depth 3 exceeds 2") {
		t.Fatalf("apply(chain of 3 under depth 2) = %v, want it refused", err)
	}
}

// A stage that reached the log although the graph refuses it, as a writer that lost a race
// leaves one, is not part of the graph shown; show says how many there are, on stderr.
func TestRunGraphShowReportsStagesTheReplayIgnored(t *testing.T) {
	cfg, stateDir, keyPath := graphConfig(t, 12)
	if _, _, err := graphRun(t, "", "apply", "--config", cfg, "--file", stageFile(t, `{"ops":[`+nodeOp("a")+`]}`)); err != nil {
		t.Fatalf("apply = %v, want nil", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	clk := clockwork.NewRealClock()
	signer, err := eventlog.NewSignerFromKeyFile(ctx, keyPath, stateDir, clk)
	if err != nil {
		t.Fatalf("NewSignerFromKeyFile() = %v, want nil", err)
	}
	writer, err := eventlog.Open(stateDir, signer, eventlog.Limits{Clock: clk})
	if err != nil {
		t.Fatalf("Open() = %v, want nil", err)
	}
	again, err := json.Marshal(graphlog.StagePayload{Ops: []graph.Event{{Type: graph.EventNodeAdded,
		Node: graph.Node{ID: "a", Kind: graph.KindUnit, Title: "Task a", Status: graph.Status{Name: graph.StatusProposed}}}}})
	if err != nil {
		t.Fatalf("Marshal() = %v, want nil", err)
	}
	if _, err = writer.Append(ctx, eventlog.Event{Type: graphlog.StageCommitted, Payload: again}); err != nil {
		t.Fatalf("Append(duplicate node) = %v, want nil", err)
	}
	out, errOut, err := graphRun(t, "", "show", "--config", cfg)
	if err != nil || len(shownNodes(t, out)) != 1 {
		t.Fatalf("show = %q, %v; want the graph with its one node", out, err)
	}
	if !strings.Contains(errOut, "1 stage(s) the graph refused") {
		t.Fatalf("show stderr = %q, want the count of ignored stages", errOut)
	}
}
