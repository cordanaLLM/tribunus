//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/eventlog"
	"github.com/tekmora/epitrop/internal/slot"
)

// The stand-in harnesses: shell scripts that do inside the sandbox what a harness would.
// %[1]s is the unit, %[2]s a directory outside the workspace, %[3]s the run's output
// directory.
const (
	// standInDone commits, writes evidence and reports done. On the way it tries to write
	// outside its workspace and to forge the runner's report, and says in its summary
	// whether either worked.
	standInDone = `set -eu
echo third > third.txt
git add third.txt
git -c user.name=slot -c user.email=slot@example.invalid -c commit.gpgsign=false commit --quiet --message "the unit's work"
tip=$(git rev-parse HEAD)
printf 'ok 1\nok 2\n' > .slot/out/evidence/unit.log
sum=$(sha256sum .slot/out/evidence/unit.log | cut -d' ' -f1)
outside=no
if echo escaped 2>/dev/null > '%[2]s/escaped'; then outside=yes; fi
if echo forged 2>/dev/null > '%[3]s/run.json'; then outside=yes; fi
brief=$(head -c 6 .slot/in/brief.md)
printf '{"unit":"%%s","outcome":"done","head":"%%s","summary":"brief starts with %%s; wrote outside the workspace: %%s","evidence":[{"path":".slot/out/evidence/unit.log","sha256":"%%s","lines":2}]}\n' '%[1]s' "$tip" "$brief" "$outside" "$sum" > .slot/out/result.json
`
	// standInWrongOutcome reports an outcome the contract does not know.
	standInWrongOutcome = `printf '{"unit":"%[1]s","outcome":"finished","summary":"all good"}\n' > .slot/out/result.json
`
	// standInSilent ends without a result.
	standInSilent = `exit 0
`
	// standInEndless never ends by itself; its deadline ends it.
	standInEndless = `sleep 600
`
)

// newSandboxedSlotRun prepares a run of a stand-in harness under the real sandbox, with a
// deadline in seconds.
func newSandboxedSlotRun(t *testing.T, script string, deadline int) slotRun {
	t.Helper()
	run := newSlotRun(t, func(run slotRun) string {
		command, err := json.Marshal([]string{"/usr/bin/sh", "-c", fmt.Sprintf(script, slotTestUnit, run.outside, run.out)})
		if err != nil {
			t.Fatalf("Marshal(command) = %v, want nil", err)
		}
		return fmt.Sprintf(`{"name": "slot-1", "command": %s, "max_runtime_seconds": %d, "stop": {"signal": "TERM", "grace_seconds": 1}, "sandbox": {"network": "none", "memory_max": "512M", "tasks_max": 128}}`, command, deadline)
	})
	withTestShim(t)
	old := slotWaitInterval
	slotWaitInterval = 50 * time.Millisecond
	t.Cleanup(func() { slotWaitInterval = old })
	return run
}

// runSandboxed runs the command. Where the host cannot run a sandbox at all the test is
// skipped, except under CI, where the sandbox is part of the machine.
func runSandboxed(t *testing.T, run slotRun) (string, slot.Report, error) {
	t.Helper()
	printed, err := captureSlot(t, run.args)
	unavailable := ""
	if err != nil && strings.Contains(err.Error(), "cannot start:") {
		unavailable = err.Error()
	}
	var report slot.Report
	if body, readErr := os.ReadFile(filepath.Join(run.out, slot.ReportFile)); readErr == nil {
		if jsonErr := json.Unmarshal(body, &report); jsonErr != nil {
			t.Fatalf("Unmarshal(report) = %v, want nil", jsonErr)
		}
	}
	if report.Outcome == slot.OutcomeRefused {
		unavailable = report.Reason
	}
	if unavailable != "" {
		if os.Getenv("CI") == "true" {
			t.Fatalf("slot run under CI: the sandbox did not start: %s", unavailable)
		}
		t.Skipf("slot run skipped: the sandbox did not start here: %s", unavailable)
	}
	return printed, report, err
}

func slotUnit(t *testing.T, run slotRun) eventlog.Unit {
	t.Helper()
	cfg, err := config.Load(testContext(t), filepath.Join(run.out, slot.ConfigFile))
	if err != nil {
		t.Fatalf("Load(run's config) = %v, want nil", err)
	}
	record, err := newUnitsService(cfg)
	if err != nil {
		t.Fatalf("newUnitsService() = %v, want nil", err)
	}
	unit, err := record.Show(testContext(t), slotTestUnit)
	if err != nil {
		t.Fatalf("Show() = %v, want nil", err)
	}
	return unit
}

// One unit runs in a slot from the command to the record: the stand-in harness works inside
// the sandbox, and the runner takes its commit, its evidence and its result.
func TestSlotRunTakesTheWorkOfASandboxedRun(t *testing.T) {
	run := newSandboxedSlotRun(t, standInDone, 30)
	printed, report, err := runSandboxed(t, run)
	if err != nil {
		t.Fatalf("slot run = %v, want nil\n%s", err, printed)
	}
	branch := slot.Branch(slotTestUnit)
	head := slotGit(t, run.repo, "rev-parse", "refs/heads/"+branch)
	checkSandboxedRunReport(t, report, head)
	checkSandboxedRunArtifacts(t, run, head, branch, printed)
	checkSandboxedRunUnitAndStatus(t, run, branch)
}

func checkSandboxedRunReport(t *testing.T, report slot.Report, head string) {
	t.Helper()
	if report.Outcome != slot.OutcomeDone || report.Status != "review" || report.Head != head || !report.Published {
		t.Fatalf("report = %+v, want a done run published at %s", report, head)
	}
	if report.Job == nil || !report.Job.Recorded || report.Job.Code == nil || *report.Job.Code != 0 {
		t.Fatalf("job end = %+v, want a recorded exit with code 0", report.Job)
	}
	if want := "brief starts with # Unit; wrote outside the workspace: no"; report.Summary != want {
		t.Fatalf("summary = %q, want %q: the run reads its inputs and cannot leave its workspace", report.Summary, want)
	}
}

func checkSandboxedRunArtifacts(t *testing.T, run slotRun, head string, branch string, printed string) {
	t.Helper()
	if _, statErr := os.Lstat(filepath.Join(run.outside, "escaped")); !os.IsNotExist(statErr) {
		t.Fatalf("the run wrote outside its workspace (%v)", statErr)
	}
	if files := slotGit(t, run.repo, "show", "--name-only", "--format=", head); files != "third.txt" {
		t.Fatalf("the unit's commit holds %q, want third.txt alone: the contract's files are not committed", files)
	}
	for _, want := range []string{"unit\t" + slotTestUnit + "\treview\tdone\n", "branch\t" + branch + "\t" + head + "\tpublished\n", "report\t" + filepath.Join(run.out, slot.ReportFile) + "\n"} {
		if !strings.Contains(printed, want) {
			t.Errorf("printed %q, want it to hold %q", printed, want)
		}
	}
	copied, err := os.ReadFile(filepath.Join(run.out, slot.EvidenceCopies, "unit.log"))
	if err != nil || string(copied) != "ok 1\nok 2\n" {
		t.Fatalf("evidence copy = %q, %v; want what the run wrote", copied, err)
	}
}

func checkSandboxedRunUnitAndStatus(t *testing.T, run slotRun, branch string) {
	t.Helper()
	unit := slotUnit(t, run)
	if unit.Status != "review" || unit.Branch != branch || unit.Worktree != run.workspace || unit.Evidence == nil || unit.Evidence.Path != filepath.Join(run.out, slot.ReportFile) {
		t.Fatalf("unit = %+v, want review with the branch, the workspace and a pointer to the report", unit)
	}
	// The run's configuration reaches the job afterwards, as `jobs status` and `jobs stop` do.
	status := captureStdout(t, func() {
		if statusErr := runJobs([]string{"status", "--config", filepath.Join(run.out, slot.ConfigFile)}); statusErr != nil {
			t.Errorf("jobs status with the run's config = %v, want nil", statusErr)
		}
	})
	if !strings.Contains(status, "slot-1") || strings.Contains(status, "running") {
		t.Fatalf("jobs status = %q, want the slot's job, no longer running", status)
	}
}

// What a run leaves that the runner cannot take is refused, each for its own reason, and
// changes nothing in the repository.
func TestSlotRunRefusesWhatASandboxedRunLeft(t *testing.T) {
	cases := []struct {
		name     string
		script   string
		deadline int
		want     string
		end      string
	}{
		{"an outcome the contract does not know", standInWrongOutcome, 30, "is not valid", ""},
		{"no result", standInSilent, 30, "result.json", ""},
		{"a run that its deadline ended", standInEndless, 1, "result.json", "deadline"},
	}
	for i := 0; i < len(cases); i++ {
		t.Run(cases[i].name, func(t *testing.T) {
			checkSlotRunRefusedCase(t, cases[i].script, cases[i].deadline, cases[i].want, cases[i].end)
		})
	}
}

func checkSlotRunRefusedCase(t *testing.T, script string, deadline int, want string, end string) {
	t.Helper()
	run := newSandboxedSlotRun(t, script, deadline)
	before := slotGit(t, run.repo, "for-each-ref")
	printed, report, err := runSandboxed(t, run)
	if err == nil || !strings.Contains(err.Error(), "not-taken") || !strings.Contains(err.Error(), want) {
		t.Fatalf("slot run = %v, want it to fail as not-taken with %q", err, want)
	}
	if report.Outcome != slot.OutcomeNotTaken || report.Status != "blocked" || report.Published || report.Job == nil || report.Job.Reason != end {
		t.Fatalf("report = %+v with job end %+v, want not-taken, blocked, job reason %q", report, report.Job, end)
	}
	checkSlotRunRefusedOutputAndRefs(t, printed, run, before)
}

func checkSlotRunRefusedOutputAndRefs(t *testing.T, printed string, run slotRun, before string) {
	t.Helper()
	if !strings.Contains(printed, "unit\t"+slotTestUnit+"\tblocked\tnot-taken\n") || !strings.Contains(printed, "reason\t") {
		t.Fatalf("printed %q, want the blocked unit and the reason", printed)
	}
	if after := slotGit(t, run.repo, "for-each-ref"); after != before {
		t.Fatalf("refs = %q, want them unchanged %q", after, before)
	}
	if unit := slotUnit(t, run); unit.Status != "blocked" || unit.Evidence == nil {
		t.Fatalf("unit = %+v, want blocked with a pointer to the report", unit)
	}
}
