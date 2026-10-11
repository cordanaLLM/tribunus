package effects

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// rt02Fixture is one case of RT-02 in the fixture format of the architecture document: a
// violation is an attempt that must be refused, a compliant case one that must succeed.
// Here the attempt is one delivery of a key, after the steps of setup.
type rt02Fixture struct {
	ID        string     `json:"id"`
	Rule      string     `json:"rule"`
	Direction string     `json:"direction"`
	Setup     []rt02Step `json:"setup"`
	Attempt   rt02Step   `json:"attempt"`
	Expect    struct {
		// Result is refused when the attempt must not run the effect, allowed when it must.
		Result string `json:"result"`
		// Executions is how often the effect ran over setup and attempt together.
		Executions int `json:"executions"`
		// Record is the type of the record the attempt appends last, "" when it appends none.
		Record string `json:"record"`
	} `json:"expect"`
}

type rt02Step struct {
	Act string `json:"act"`
}

var fixtureKey = Key{TaskID: "task-1", ActionType: "file-issue"}

func loadRT02Fixtures(t *testing.T) []rt02Fixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "rt02", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("Glob(testdata/rt02) = %d files, %v; want the fixtures", len(paths), err)
	}
	sort.Strings(paths)
	fixtures := make([]rt02Fixture, 0, len(paths))
	for i := 0; i < len(paths); i++ {
		body, err := os.ReadFile(paths[i])
		if err != nil {
			t.Fatalf("ReadFile(%s) = %v, want nil", paths[i], err)
		}
		var fixture rt02Fixture
		if err = json.Unmarshal(body, &fixture); err != nil {
			t.Fatalf("%s: %v", paths[i], err)
		}
		if fixture.ID+".json" != filepath.Base(paths[i]) || fixture.Rule != "rt02" {
			t.Fatalf("%s: id %q, rule %q; want the file's name and rt02", paths[i], fixture.ID, fixture.Rule)
		}
		fixtures = append(fixtures, fixture)
	}
	return fixtures
}

// rt02Run is one fixture's ledger and the count of how often its effect ran.
type rt02Run struct {
	ledger     *Ledger
	dir        string
	executions int
}

// step performs one act and reports whether it ran the effect.
func (r *rt02Run) step(t *testing.T, ctx context.Context, act string) bool {
	t.Helper()
	before := r.executions
	switch act {
	case "deliver":
		_, err := r.ledger.Do(ctx, fixtureKey, func(context.Context) error { r.executions++; return nil })
		if err != nil && !errors.Is(err, ErrUnresolved) {
			t.Fatalf("deliver = %v, want nil or an open claim", err)
		}
	case "deliver_failing":
		failure := errors.New("the forge answered 502")
		_, err := r.ledger.Do(ctx, fixtureKey, func(context.Context) error { r.executions++; return failure })
		if !errors.Is(err, failure) {
			t.Fatalf("deliver_failing = %v, want the effect's own error", err)
		}
	case "crash_after_claim":
		// The claim is in the log and nothing follows it: what a crash between the claim
		// and the end leaves behind.
		if _, err := r.ledger.append(ctx, Claimed, fixtureKey, payload{}); err != nil {
			t.Fatalf("append(claim) = %v, want nil", err)
		}
	case "resolve_retry", "resolve_done":
		outcome := OutcomeRetry
		if act == "resolve_done" {
			outcome = OutcomeDone
		}
		if err := r.ledger.Resolve(ctx, fixtureKey, outcome, "operator", "checked on the forge by hand"); err != nil {
			t.Fatalf("Resolve(%s) = %v, want nil", outcome, err)
		}
	default:
		t.Fatalf("unknown act %q", act)
	}
	return r.executions > before
}

// checkRT02Fixture replays one fixture and returns what is wrong with the outcome, "" when
// nothing is. It fails a violation that was allowed and a compliant case that was refused.
func checkRT02Fixture(t *testing.T, fixture rt02Fixture) string {
	t.Helper()
	ledger, dir := testLedger(t)
	run := &rt02Run{ledger: ledger, dir: dir}
	ctx := testCtx(t)
	for i := 0; i < len(fixture.Setup); i++ {
		run.step(t, ctx, fixture.Setup[i].Act)
	}
	typesBefore := recordTypes(t, dir)
	executed := run.step(t, ctx, fixture.Attempt.Act)
	appended := recordTypes(t, dir)[len(typesBefore):]
	last := ""
	if len(appended) > 0 {
		last = appended[len(appended)-1]
	}
	switch {
	case fixture.Direction == "violation" && executed:
		return "a violation was allowed: the attempt ran the effect"
	case fixture.Direction == "compliant" && !executed:
		return "a compliant delivery was refused: the attempt did not run the effect"
	case (fixture.Expect.Result == "allowed") != executed:
		return "expect.result does not match the direction"
	case run.executions != fixture.Expect.Executions:
		return "the effect ran " + itoa(run.executions) + " time(s), want " + itoa(fixture.Expect.Executions)
	case last != fixture.Expect.Record:
		return "the attempt's last record is " + quote(last) + ", want " + quote(fixture.Expect.Record)
	}
	return ""
}

func TestRT02Fixtures(t *testing.T) {
	fixtures := loadRT02Fixtures(t)
	directions := map[string]int{}
	for i := 0; i < len(fixtures); i++ {
		fixture := fixtures[i]
		directions[fixture.Direction]++
		t.Run(fixture.ID, func(t *testing.T) {
			if wrong := checkRT02Fixture(t, fixture); wrong != "" {
				t.Fatalf("%s (%s): %s", fixture.ID, fixture.Direction, wrong)
			}
		})
	}
	if directions["violation"] == 0 || directions["compliant"] == 0 || len(directions) != 2 {
		t.Fatalf("fixture directions = %v, want violations and compliant cases and nothing else", directions)
	}
}

// The checker itself is checked: a fixture that claims the wrong outcome is reported, in
// both directions, so a fixture cannot pass by being wrong about what it shows.
func TestRT02CheckerRefusesAFixtureThatClaimsTheWrongOutcome(t *testing.T) {
	second := rt02Fixture{ID: "planted", Rule: "rt02", Direction: "compliant", Setup: []rt02Step{{Act: "deliver"}}, Attempt: rt02Step{Act: "deliver"}}
	second.Expect.Result, second.Expect.Executions, second.Expect.Record = "allowed", 2, Done
	if wrong := checkRT02Fixture(t, second); wrong == "" {
		t.Fatal("a second delivery planted as compliant passed the checker, want it reported")
	}
	first := rt02Fixture{ID: "planted", Rule: "rt02", Direction: "violation", Attempt: rt02Step{Act: "deliver"}}
	first.Expect.Result, first.Expect.Executions = "refused", 0
	if wrong := checkRT02Fixture(t, first); wrong == "" {
		t.Fatal("a first delivery planted as a violation passed the checker, want it reported")
	}
	count := rt02Fixture{ID: "planted", Rule: "rt02", Direction: "compliant", Attempt: rt02Step{Act: "deliver"}}
	count.Expect.Result, count.Expect.Executions, count.Expect.Record = "allowed", 2, Done
	if wrong := checkRT02Fixture(t, count); wrong == "" {
		t.Fatal("a wrong execution count passed the checker, want it reported")
	}
	record := rt02Fixture{ID: "planted", Rule: "rt02", Direction: "compliant", Attempt: rt02Step{Act: "deliver"}}
	record.Expect.Result, record.Expect.Executions, record.Expect.Record = "allowed", 1, Skipped
	if wrong := checkRT02Fixture(t, record); wrong == "" {
		t.Fatal("a wrong record type passed the checker, want it reported")
	}
}
