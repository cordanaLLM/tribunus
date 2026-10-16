package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tekmora/epitrop/internal/slot"
)

const systemSlotStandIn = `set -eu
echo third > third.txt
git add third.txt
git -c user.name=slot -c user.email=slot@example.invalid -c commit.gpgsign=false commit --quiet --message "the unit's work"
tip=$(git rev-parse HEAD)
printf 'ok 1\nok 2\n' > .slot/out/evidence/unit.log
sum=$(sha256sum .slot/out/evidence/unit.log | cut -d' ' -f1)
brief=$(head -c 6 .slot/in/brief.md)
printf '{"unit":"%[1]s","outcome":"done","head":"%%s","summary":"brief starts with %%s; wrote outside the workspace: no","evidence":[{"path":".slot/out/evidence/unit.log","sha256":"%%s","lines":2}]}\n' "$tip" "$brief" "$sum" > .slot/out/result.json
`

func newSystemSlotRun(t *testing.T) slotRun {
	t.Helper()
	return newSlotRun(t, func(run slotRun) string {
		cmdJSON, err := json.Marshal([]string{"/usr/bin/sh", "-c", fmt.Sprintf(systemSlotStandIn, slotTestUnit)})
		if err != nil {
			t.Fatalf("Marshal command: %v", err)
		}
		return fmt.Sprintf(`{"name": "slot-1", "command": %s, "max_runtime_seconds": 30, "stop": {"signal": "TERM", "grace_seconds": 1}, "sandbox": {"network": "none", "memory_max": "512M", "tasks_max": 128}}`, cmdJSON)
	})
}

func checkSlotSandboxAvailable(t *testing.T, run slotRun, stderr string) {
	t.Helper()
	var report slot.Report
	if body, err := os.ReadFile(filepath.Join(run.out, slot.ReportFile)); err == nil {
		if jsonErr := json.Unmarshal(body, &report); jsonErr != nil {
			t.Fatalf("Unmarshal report: %v", jsonErr)
		}
	}
	unavailable := ""
	if report.Outcome == slot.OutcomeRefused {
		unavailable = report.Reason
	}
	if strings.Contains(stderr, "cannot start:") {
		unavailable = stderr
	}
	if unavailable != "" {
		if os.Getenv("CI") == "true" {
			t.Fatalf("slot run sandbox failed under CI: %s", unavailable)
		}
		t.Skipf("slot sandbox unavailable: %s", unavailable)
	}
}

func slotBusEnv() []string {
	var env []string
	for _, key := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if val, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+val)
		}
	}
	return env
}

func TestSystemSlot_Positive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the slot sandbox runs on Linux only")
	}
	bin := buildCLI(t)
	run := newSystemSlotRun(t)

	args := append([]string{"slot"}, run.args...)
	stdout, stderr, code, err := runSystemCLIWithEnv(t, bin, slotBusEnv(), args...)
	if err != nil || code != 0 {
		checkSlotSandboxAvailable(t, run, stderr)
		t.Fatalf("slot run code = %d, err = %v: %s\n%s", code, err, stdout, stderr)
	}

	for _, want := range []string{"unit\tepitrop:unit-1\treview\tdone", "\tpublished\n", filepath.Join(run.out, slot.ReportFile)} {
		if !strings.Contains(stdout, want) {
			t.Errorf("slot run output missing %q: %s", want, stdout)
		}
	}
	checkSlotArtifacts(t, run.out)

	// Second process reads the state back via jobs status
	cfgFile := filepath.Join(run.out, slot.ConfigFile)
	stdoutJob, stderrJob, codeJob, errJob := runSystemCLI(t, bin, "jobs", "status", "--config="+cfgFile, "slot-1")
	if errJob != nil || codeJob != 0 {
		t.Fatalf("jobs status code = %d, err = %v: %s\n%s", codeJob, errJob, stdoutJob, stderrJob)
	}
	if !strings.HasPrefix(stdoutJob, "slot-1\tdead\t") {
		t.Fatalf("jobs status on slot config = %q, want slot-1 dead", stdoutJob)
	}
}

func checkSlotArtifacts(t *testing.T, outDir string) {
	t.Helper()
	for _, file := range []string{slot.ReportFile, slot.ResultCopy, slot.ConfigFile} {
		if _, err := os.Stat(filepath.Join(outDir, file)); err != nil {
			t.Fatalf("missing artifact %s: %v", file, err)
		}
	}
	evidenceFile := filepath.Join(outDir, slot.EvidenceCopies, "unit.log")
	if _, err := os.Stat(evidenceFile); err != nil {
		t.Fatalf("missing evidence %s: %v", evidenceFile, err)
	}
}

func TestSystemSlot_Negative(t *testing.T) {
	bin := buildCLI(t)

	t.Run("missing command", func(t *testing.T) {
		_, stderr, code, err := runSystemCLI(t, bin, "slot")
		if err == nil || code != 1 {
			t.Fatalf("slot missing command code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "slot command required") {
			t.Fatalf("stderr = %q, want 'slot command required'", stderr)
		}
	})

	t.Run("unknown command", func(t *testing.T) {
		_, stderr, code, err := runSystemCLI(t, bin, "slot", "walk")
		if err == nil || code != 1 {
			t.Fatalf("slot walk code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "unknown slot command: walk") {
			t.Fatalf("stderr = %q, want 'unknown slot command: walk'", stderr)
		}
	})

	t.Run("run missing config flag", func(t *testing.T) {
		_, stderr, code, err := runSystemCLI(t, bin, "slot", "run", "epitrop:unit-1")
		if err == nil || code != 1 {
			t.Fatalf("slot run missing config code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "slot run: --config is required") {
			t.Fatalf("stderr = %q, want '--config is required'", stderr)
		}
	})
}
