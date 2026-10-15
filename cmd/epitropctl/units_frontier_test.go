package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tekmora/epitrop/internal/eventlog"
)

// unitsGraphRun runs one units command that prints through graphOut.
func unitsGraphRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	old := graphOut
	graphOut = &out
	defer func() { graphOut = old }()
	err := runUnits(args)
	return out.String(), err
}

func statusOp(id string, to string) string {
	return `{"type":"graph.status_changed","node_id":"` + id + `","status":{"name":"` + to + `"}}`
}

func printedUnit(t *testing.T, out string) eventlog.Unit {
	t.Helper()
	var u eventlog.Unit
	if err := json.Unmarshal([]byte(out), &u); err != nil {
		t.Fatalf("output is no unit: %v\n%s", err, out)
	}
	return u
}

// frontier, assign and release run end to end against one log: the frontier names the
// ready node whose dependency landed, an assignment takes it off, a release puts it back.
func TestRunUnitsFrontierAssignReleaseEndToEnd(t *testing.T) {
	cfg, _, _ := graphConfig(t, 12)
	setupUnitsFrontierStage(t, cfg)
	checkUnitsFrontierAssignRefusals(t, cfg)
	checkUnitsFrontierAssignSuccess(t, cfg)
	checkUnitsFrontierReleaseSuccess(t, cfg)
	checkUnitsFrontierReleaseRefusal(t, cfg)
}

func setupUnitsFrontierStage(t *testing.T, cfg string) {
	t.Helper()
	ops := []string{nodeOp("a"), nodeOp("b"), nodeOp("c"), edgeOp("b", "a"), edgeOp("c", "b"),
		statusOp("a", "ready"), statusOp("b", "ready"), statusOp("c", "ready")}
	for _, to := range []string{"claimed", "implementing", "review", "landing", "landed"} {
		ops = append(ops, statusOp("a", to))
	}
	stage := stageFile(t, `{"ops":[`+strings.Join(ops, ",")+`]}`)
	if _, _, err := graphRun(t, "", "apply", "--config", cfg, "--file", stage); err != nil {
		t.Fatalf("apply = %v, want nil", err)
	}
	out, err := unitsGraphRun(t, "frontier", "--config", cfg)
	if err != nil || out != "b\t\"Task b\"\n" {
		t.Fatalf("frontier = %q, %v; want b alone", out, err)
	}
}

func checkUnitsFrontierAssignRefusals(t *testing.T, cfg string) {
	t.Helper()
	if _, err := unitsGraphRun(t, "assign", "--config", cfg, "--repo", "tekmora/epitrop", "c"); err == nil || !strings.Contains(err.Error(), "--repo and --lane") {
		t.Fatalf("assign without a lane = %v, want it refused", err)
	}
	if _, err := unitsGraphRun(t, "assign", "--config", cfg, "--repo", "tekmora/epitrop", "--lane", "l", "c"); err == nil || !strings.Contains(err.Error(), `depends on "b"`) {
		t.Fatalf("assign c = %v, want it refused for its dependency", err)
	}
}

func checkUnitsFrontierAssignSuccess(t *testing.T, cfg string) {
	t.Helper()
	out, err := unitsGraphRun(t, "assign", "--config", cfg, "--repo", "tekmora/epitrop", "--lane", "l", "--by", "slot-1", "b")
	if u := printedUnit(t, out); err != nil || u.ID != "tekmora/epitrop:b" || u.Status != "claimed" || u.Node != "b" {
		t.Fatalf("assign b = %+v, %v; want the claimed unit of b", u, err)
	}
	if out, err = unitsGraphRun(t, "frontier", "--config", cfg); err != nil || out != "" {
		t.Fatalf("frontier after the assignment = %q, %v; want empty", out, err)
	}
}

func checkUnitsFrontierReleaseSuccess(t *testing.T, cfg string) {
	t.Helper()
	if _, err := unitsGraphRun(t, "release", "--config", cfg, "b"); err == nil || !strings.Contains(err.Error(), "--reason is required") {
		t.Fatalf("release without a reason = %v, want it refused", err)
	}
	out, err := unitsGraphRun(t, "release", "--config", cfg, "--reason", "lane down", "b")
	if u := printedUnit(t, out); err != nil || u.Status != "dropped" || u.Released == nil || u.Released.Reason != "lane down" {
		t.Fatalf("release b = %+v, %v; want the unit dropped with the reason", u, err)
	}
	if out, err = unitsGraphRun(t, "frontier", "--config", cfg); err != nil || out != "b\t\"Task b\"\n" {
		t.Fatalf("frontier after the release = %q, %v; want b again", out, err)
	}
}

func checkUnitsFrontierReleaseRefusal(t *testing.T, cfg string) {
	t.Helper()
	if _, err := unitsGraphRun(t, "release", "--config", cfg, "--reason", "again", "b"); err == nil || !strings.Contains(err.Error(), "not claimed") {
		t.Fatalf("release of a ready node = %v, want it refused", err)
	}
}
