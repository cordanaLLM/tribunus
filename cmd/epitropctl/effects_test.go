package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/tekmora/epitrop/internal/effects"
	"github.com/tekmora/epitrop/internal/eventlog"
)

// effectsRun runs one effects command and returns what it wrote to stdout and stderr.
func effectsRun(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	oldOut, oldErr := effectsOut, effectsErr
	effectsOut, effectsErr = &out, &errOut
	defer func() { effectsOut, effectsErr = oldOut, oldErr }()
	err := runEffects(args)
	return out.String(), errOut.String(), err
}

// effectsLog returns a config, a ledger on its log and a writer for raw records.
func effectsLog(t *testing.T) (string, *effects.Ledger, *eventlog.Writer, context.Context) {
	t.Helper()
	cfgPath, stateDir := writeUnitsConfig(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	clk := clockwork.NewRealClock()
	signer, err := eventlog.NewSignerFromKeyFile(ctx, filepath.Join(filepath.Dir(cfgPath), "seed.hex"), stateDir, clk)
	if err != nil {
		t.Fatalf("NewSignerFromKeyFile() = %v, want nil", err)
	}
	limits := eventlog.Limits{Clock: clk}
	ledger, err := effects.NewLedger(stateDir, signer, clk, limits)
	if err != nil {
		t.Fatalf("NewLedger() = %v, want nil", err)
	}
	writer, err := eventlog.Open(stateDir, signer, limits)
	if err != nil {
		t.Fatalf("Open() = %v, want nil", err)
	}
	return cfgPath, ledger, writer, ctx
}

func TestRunEffectsListAndResolveEndToEnd(t *testing.T) {
	cfg, ledger, writer, ctx := effectsLog(t)
	out, errOut, err := effectsRun(t, "list", "--config", cfg)
	if err != nil || out != "" || errOut != "" {
		t.Fatalf("list(no effects) = %q, %q, %v; want nothing", out, errOut, err)
	}
	seedEffectsForResolve(t, ctx, ledger, writer)
	checkEffectsListBeforeResolve(t, cfg)
	resolveEffectsTasks(t, cfg)
	checkEffectsListAfterResolve(t, cfg)
}

func seedEffectsForResolve(t *testing.T, ctx context.Context, ledger *effects.Ledger, writer *eventlog.Writer) {
	t.Helper()
	if _, err := ledger.Do(ctx, effects.Key{TaskID: "task-1", ActionType: "file-issue"}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Do() = %v, want nil", err)
	}
	failure := errors.New("the forge answered 502")
	if _, err := ledger.Do(ctx, effects.Key{TaskID: "task-2", ActionType: "file-issue"}, func(context.Context) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("Do(failing) = %v, want the effect's error", err)
	}
	// A claim and nothing after it, as a crash leaves it.
	if _, err := writer.Append(ctx, eventlog.Event{Type: effects.Claimed, TaskID: "task-3", Payload: []byte(`{"action_type":"file-issue"}`)}); err != nil {
		t.Fatalf("Append(claim) = %v, want nil", err)
	}
}

func checkEffectsListBeforeResolve(t *testing.T, cfg string) {
	t.Helper()
	out, errOut, err := effectsRun(t, "list", "--config", cfg)
	want := "task-1\tfile-issue\tdone\t1\t0\t\n" +
		"task-2\tfile-issue\tfailed\t1\t0\t\"the forge answered 502\"\n" +
		"task-3\tfile-issue\tclaimed\t1\t0\t\n"
	if err != nil || out != want {
		t.Fatalf("list = %v\n%q\nwant\n%q", err, out, want)
	}
	if !strings.Contains(errOut, "2 open claim(s)") {
		t.Fatalf("list stderr = %q, want the two open claims counted", errOut)
	}
}

func resolveEffectsTasks(t *testing.T, cfg string) {
	t.Helper()
	if _, _, err := effectsRun(t, "resolve", "--config", cfg, "--outcome", "retry", "--by", "operator", "--note", "no issue on the forge", "task-2", "file-issue"); err != nil {
		t.Fatalf("resolve(retry) = %v, want nil", err)
	}
	if _, _, err := effectsRun(t, "resolve", "--config", cfg, "--outcome=done", "--by=operator", "--note=issue 12 is there", "task-3", "file-issue"); err != nil {
		t.Fatalf("resolve(done) = %v, want nil", err)
	}
}

func checkEffectsListAfterResolve(t *testing.T, cfg string) {
	t.Helper()
	out, errOut, err := effectsRun(t, "list", "--config", cfg)
	want := "task-1\tfile-issue\tdone\t1\t0\t\n" +
		"task-2\tfile-issue\treleased\t1\t0\tresolved by operator: \"no issue on the forge\"\n" +
		"task-3\tfile-issue\tdone\t1\t0\tresolved by operator: \"issue 12 is there\"\n"
	if err != nil || out != want || errOut != "" {
		t.Fatalf("list after the resolutions = %v, stderr %q\n%q\nwant\n%q", err, errOut, out, want)
	}
}

func TestRunEffectsRefusals(t *testing.T) {
	cfg, _, writer, ctx := effectsLog(t)
	if _, err := writer.Append(ctx, eventlog.Event{Type: effects.Claimed, TaskID: "task-3", Payload: []byte(`{"action_type":"file-issue"}`)}); err != nil {
		t.Fatalf("Append(claim) = %v, want nil", err)
	}
	before, _, err := effectsRun(t, "list", "--config", cfg)
	if err != nil {
		t.Fatalf("list = %v, want nil", err)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no command", nil, "effects command required"},
		{"an unknown command", []string{"forget"}, "unknown effects command: forget"},
		{"list without a config", []string{"list"}, "--config is required"},
		{"resolve without a task id", []string{"resolve", "--config", cfg, "--outcome", "done", "--by", "operator"}, "task id is required"},
		{"resolve without an action type", []string{"resolve", "--config", cfg, "--outcome", "done", "--by", "operator", "task-3"}, "action type is required"},
		{"resolve without an outcome", []string{"resolve", "--config", cfg, "--by", "operator", "task-3", "file-issue"}, "--outcome is required"},
		{"resolve with an unknown outcome", []string{"resolve", "--config", cfg, "--outcome", "abandon", "--by", "operator", "task-3", "file-issue"}, `unknown outcome "abandon"`},
		{"resolve by nobody", []string{"resolve", "--config", cfg, "--outcome", "done", "task-3", "file-issue"}, "names who made it"},
		{"resolve of a key without a claim", []string{"resolve", "--config", cfg, "--outcome", "done", "--by", "operator", "task-9", "file-issue"}, "has no open claim to resolve"},
	}
	for i := 0; i < len(cases); i++ {
		out, _, err := effectsRun(t, cases[i].args...)
		if err == nil || !strings.Contains(err.Error(), cases[i].want) || out != "" {
			t.Errorf("%s: output %q, error %v; want no output and an error holding %q", cases[i].name, out, err, cases[i].want)
		}
	}
	after, _, err := effectsRun(t, "list", "--config", cfg)
	if err != nil || after != before {
		t.Fatalf("list after the refusals = %q, %v; want it unchanged %q", after, err, before)
	}
}

func TestRunEffectsListWarnings(t *testing.T) {
	cfg, _, writer, ctx := effectsLog(t)

	_, errOut, err := effectsRun(t, "list", "--config", cfg)
	if err != nil || errOut != "" {
		t.Fatalf("list(empty) = %q, %v, want empty stderr", errOut, err)
	}

	claimPayload := []byte(`{"action_type":"file-issue"}`)
	wantOpenLine := checkEffectsListWarningOpen(t, cfg, writer, ctx, claimPayload)
	checkEffectsListWarningDuplicate(t, cfg, writer, ctx, claimPayload, wantOpenLine)
	checkEffectsListWarningDone(t, cfg, writer, ctx)
}

func checkEffectsListWarningOpen(t *testing.T, cfg string, writer *eventlog.Writer, ctx context.Context, claimPayload []byte) string {
	t.Helper()
	if _, err := writer.Append(ctx, eventlog.Event{Type: effects.Claimed, TaskID: "task-open", Payload: claimPayload}); err != nil {
		t.Fatalf("Append(claim) = %v", err)
	}
	_, errOut, err := effectsRun(t, "list", "--config", cfg)
	if err != nil {
		t.Fatalf("list = %v", err)
	}
	wantOpenLine := "effects: 1 open claim(s): each blocks its key until `effects resolve` records what became of it\n"
	if errOut != wantOpenLine {
		t.Fatalf("stderr = %q, want exact open warning %q", errOut, wantOpenLine)
	}
	return wantOpenLine
}

func checkEffectsListWarningDuplicate(t *testing.T, cfg string, writer *eventlog.Writer, ctx context.Context, claimPayload []byte, wantOpenLine string) {
	t.Helper()
	if _, err := writer.Append(ctx, eventlog.Event{Type: effects.Claimed, TaskID: "task-open", Payload: claimPayload}); err != nil {
		t.Fatalf("Append(duplicate claim) = %v", err)
	}
	wantBoth := wantOpenLine + "effects: replay did not apply 1 effect record(s) that lost a race\n"
	_, errOut, err := effectsRun(t, "list", "--config", cfg)
	if err != nil || errOut != wantBoth {
		t.Fatalf("stderr = %q, want exact both warnings %q", errOut, wantBoth)
	}
}

func checkEffectsListWarningDone(t *testing.T, cfg string, writer *eventlog.Writer, ctx context.Context) {
	t.Helper()
	donePayload := []byte(`{"action_type":"file-issue","claim_seq":1}`)
	if _, err := writer.Append(ctx, eventlog.Event{Type: effects.Done, TaskID: "task-open", Payload: donePayload}); err != nil {
		t.Fatalf("Append(done) = %v", err)
	}
	wantIgnoredOnly := "effects: replay did not apply 1 effect record(s) that lost a race\n"
	_, errOut, err := effectsRun(t, "list", "--config", cfg)
	if err != nil || errOut != wantIgnoredOnly {
		t.Fatalf("stderr = %q, want exact ignored warning %q", errOut, wantIgnoredOnly)
	}
}
