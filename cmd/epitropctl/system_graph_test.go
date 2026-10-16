package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemGraph_Positive(t *testing.T) {
	bin := buildCLI(t)
	cfgPath, stateDir, _ := graphConfig(t, 12)
	stage := stageFile(t, `{"ops":[`+nodeOp("task-sys-1")+`]}`)

	stdoutApply, stderrApply, codeApply, errApply := runSystemCLI(t, bin, "graph", "apply", "--config="+cfgPath, "--file="+stage)
	if errApply != nil || codeApply != 0 {
		t.Fatalf("graph apply code = %d, err = %v: %s\n%s", codeApply, errApply, stdoutApply, stderrApply)
	}
	for _, want := range []string{`"id": "task-sys-1"`, `"Task task-sys-1"`, `"proposed"`} {
		if !strings.Contains(stdoutApply, want) {
			t.Errorf("graph apply output missing %q: %s", want, stdoutApply)
		}
	}
	checkEventLogRecords(t, stateDir)

	stdoutShow, stderrShow, codeShow, errShow := runSystemCLI(t, bin, "graph", "show", "--config="+cfgPath)
	if errShow != nil || codeShow != 0 {
		t.Fatalf("graph show code = %d, err = %v: %s\n%s", codeShow, errShow, stdoutShow, stderrShow)
	}
	for _, want := range []string{`"id": "task-sys-1"`, `"Task task-sys-1"`, `"proposed"`} {
		if !strings.Contains(stdoutShow, want) {
			t.Errorf("graph show output missing %q: %s", want, stdoutShow)
		}
	}
}

func checkEventLogRecords(t *testing.T, stateDir string) {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("event log directory has no files left behind")
	}
}

func TestSystemGraph_Negative(t *testing.T) {
	bin := buildCLI(t)
	cfgPath, _, _ := graphConfig(t, 12)

	t.Run("graph apply missing file flag", func(t *testing.T) {
		_, stderr, code, err := runSystemCLI(t, bin, "graph", "apply", "--config="+cfgPath)
		if err == nil || code != 1 {
			t.Fatalf("graph apply missing --file code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "graph apply: --file is required") {
			t.Fatalf("stderr = %q, want --file is required error", stderr)
		}
	})

	t.Run("graph show missing config", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.json")
		_, stderr, code, err := runSystemCLI(t, bin, "graph", "show", "--config="+missing)
		if err == nil || code != 1 {
			t.Fatalf("graph show missing config code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "missing.json") {
			t.Fatalf("stderr = %q, want missing config error", stderr)
		}
	})
}
