package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tekmora/epitrop/internal/effects"
	"github.com/tekmora/epitrop/internal/eventlog"
)

func TestSystemEffects_Positive(t *testing.T) {
	bin := buildCLI(t)
	cfgPath, _, writer, ctx := effectsLog(t)
	taskID := "task-sys-fx"
	actionType := "file-issue"

	_, err := writer.Append(ctx, eventlog.Event{
		Type:    effects.Claimed,
		TaskID:  taskID,
		Payload: []byte(`{"action_type":"file-issue"}`),
	})
	if err != nil {
		t.Fatalf("seed open claim: %v", err)
	}

	resolveArgs := []string{
		"effects", "resolve",
		"--config=" + cfgPath,
		"--outcome=done",
		"--by=operator",
		"--note=manually verified",
		taskID, actionType,
	}
	stdoutRes, stderrRes, codeRes, errRes := runSystemCLI(t, bin, resolveArgs...)
	if errRes != nil || codeRes != 0 {
		t.Fatalf("effects resolve code = %d, err = %v: %s\n%s", codeRes, errRes, stdoutRes, stderrRes)
	}

	stdoutList, stderrList, codeList, errList := runSystemCLI(t, bin, "effects", "list", "--config="+cfgPath)
	if errList != nil || codeList != 0 {
		t.Fatalf("effects list code = %d, err = %v: %s\n%s", codeList, errList, stdoutList, stderrList)
	}
	wantLine := "task-sys-fx\tfile-issue\tdone\t1\t0\tresolved by operator: \"manually verified\"\n"
	if stdoutList != wantLine {
		t.Fatalf("effects list output = %q, want exact line %q", stdoutList, wantLine)
	}
}

func TestSystemEffects_Negative(t *testing.T) {
	bin := buildCLI(t)
	cfgPath, _, _, _ := effectsLog(t)

	t.Run("resolve without open claim", func(t *testing.T) {
		args := []string{
			"effects", "resolve",
			"--config=" + cfgPath,
			"--outcome=done",
			"--by=operator",
			"--note=chk",
			"no-such-task", "file-issue",
		}
		_, stderr, code, err := runSystemCLI(t, bin, args...)
		if err == nil || code != 1 {
			t.Fatalf("effects resolve missing code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "has no open claim to resolve") {
			t.Fatalf("stderr = %q, want 'has no open claim to resolve'", stderr)
		}
	})

	t.Run("resolve missing outcome flag", func(t *testing.T) {
		args := []string{
			"effects", "resolve",
			"--config=" + cfgPath,
			"task-1", "file-issue",
		}
		_, stderr, code, err := runSystemCLI(t, bin, args...)
		if err == nil || code != 1 {
			t.Fatalf("effects resolve missing --outcome code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "effects resolve: --outcome is required") {
			t.Fatalf("stderr = %q, want --outcome is required", stderr)
		}
	})

	t.Run("list missing config", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.json")
		_, stderr, code, err := runSystemCLI(t, bin, "effects", "list", "--config="+missing)
		if err == nil || code != 1 {
			t.Fatalf("effects list missing config code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "missing.json") {
			t.Fatalf("stderr = %q, want missing config error", stderr)
		}
	})
}
