//go:build linux

package supervisor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/tekmora/epitrop/internal/config"
)

// Start leaves a running job alone and reports success. StartOnce says that the caller got
// no run of its own, and starts nothing.
func TestStartOnceRefusesAJobThatRuns(t *testing.T) {
	sup := testSupervisor(t, testJob("daemon", longJobCommand(t)))
	if err := sup.StartOnce(testContext(t), "daemon"); err != nil {
		t.Fatalf("StartOnce(idle job) = %v, want nil", err)
	}
	err := sup.StartOnce(testContext(t), "daemon")
	if !errors.Is(err, ErrRunning) || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("StartOnce(running job) = %v, want ErrRunning naming the job", err)
	}
	if starts := countEvents(t, sup.cfg.EventLog.Dir, "job.started"); starts != 1 {
		t.Fatalf("job.started events = %d, want 1: the second call starts nothing", starts)
	}
	if err = sup.Stop(testContext(t), "daemon"); err != nil {
		t.Fatalf("Stop() = %v, want nil", err)
	}
	waitForState(t, sup, "daemon", StateStopped)
	if err = sup.StartOnce(testContext(t), "daemon"); err != nil {
		t.Fatalf("StartOnce(stopped job) = %v, want nil", err)
	}
	if starts := countEvents(t, sup.cfg.EventLog.Dir, "job.started"); starts != 2 {
		t.Fatalf("job.started events = %d, want 2", starts)
	}
}

func TestStartOnceRefusesWhatStartRefuses(t *testing.T) {
	sup := testSupervisor(t, testJob("daemon", longJobCommand(t)))
	if err := sup.StartOnce(testContext(t), "missing"); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("StartOnce(unknown job) = %v, want ErrUnknownJob", err)
	}
	if err := sup.StartOnce(context.Background(), "daemon"); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("StartOnce(no deadline) = %v, want it refused", err)
	}
	if err := sup.StartOnce(canceledContext(t), "daemon"); err == nil {
		t.Fatal("StartOnce(ended context) = nil, want it refused")
	}
	if starts := countEvents(t, sup.cfg.EventLog.Dir, "job.started"); starts != 0 {
		t.Fatalf("job.started events = %d, want none", starts)
	}
}

// Recorded tells how a job ended: Status only says that it no longer runs.
func TestRecordedTellsHowAJobEnded(t *testing.T) {
	job := testJob("once", []string{os.Args[0], "-test.run=TestHelperProcessJob"})
	sup := testSupervisor(t, job)
	checkRecordedBeforeStart(t, sup)
	t.Setenv("EPITROP_TEST_JOB", "exit-7")
	if err := sup.StartOnce(testContext(t), "once"); err != nil {
		t.Fatalf("StartOnce() = %v, want nil", err)
	}
	waitForState(t, sup, "once", StateDead)
	checkRecordedAfterExit(t, sup)
}

func checkRecordedBeforeStart(t *testing.T, sup *Supervisor) {
	t.Helper()
	before, err := sup.Recorded(testContext(t), "once")
	if err != nil || before.LastEvent != "" || before.Since != "" {
		t.Fatalf("Recorded(never started) = %+v, %v; want an empty record", before, err)
	}
}

func checkRecordedAfterExit(t *testing.T, sup *Supervisor) {
	t.Helper()
	if !waitUntil(func() bool {
		rec, recErr := sup.Recorded(testContext(t), "once")
		return recErr == nil && rec.LastEvent == "job.exited"
	}) {
		t.Fatalf("no job.exited record within %s", testWaitBound)
	}
	rec, err := sup.Recorded(testContext(t), "once")
	if err != nil {
		t.Fatalf("Recorded() = %v, want nil", err)
	}
	if rec.LastExitCode == nil || *rec.LastExitCode != 7 || rec.Since == "" || rec.Name != "once" {
		t.Fatalf("Recorded() = %+v, want the exit with code 7 after a start", rec)
	}
}

func TestRecordedRefusals(t *testing.T) {
	sup := testSupervisor(t, testJob("daemon", longJobCommand(t)))
	if _, err := sup.Recorded(testContext(t), "missing"); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("Recorded(unknown job) = %v, want ErrUnknownJob", err)
	}
	if _, err := sup.Recorded(context.Background(), "daemon"); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Recorded(no deadline) = %v, want it refused", err)
	}
}

// Preflight looks for the sandbox's tools the way the shim will, and starts nothing.
func TestPreflightFindsWhatASandboxedStartNeeds(t *testing.T) {
	sandboxed := testJob("boxed", longJobCommand(t))
	sandboxed.Sandbox = config.SandboxConfig{Mode: "enforce", Workspace: t.TempDir()}
	sup := testSupervisor(t, testJob("plain", longJobCommand(t)), sandboxed)
	if err := sup.Preflight("missing"); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("Preflight(unknown job) = %v, want ErrUnknownJob", err)
	}
	previous := sandboxLookPath
	t.Cleanup(func() { sandboxLookPath = previous })
	sandboxLookPath = func(name string) (string, error) {
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	if err := sup.Preflight("plain"); err != nil {
		t.Fatalf("Preflight(job without a sandbox) = %v, want nil: it needs no tool", err)
	}
	err := sup.Preflight("boxed")
	if err == nil || !strings.Contains(err.Error(), "systemd-run") {
		t.Fatalf("Preflight(sandboxed job, no tools) = %v, want the first missing tool named", err)
	}
	if starts := countEvents(t, sup.cfg.EventLog.Dir, "job.started"); starts != 0 {
		t.Fatalf("job.started events = %d, want none: a preflight starts nothing", starts)
	}
	sandboxLookPath = previous
	want := sandboxPreflight(sandboxed.Sandbox)
	if got := sup.Preflight("boxed"); (got == nil) != (want == nil) {
		t.Fatalf("Preflight(sandboxed job) = %v, the shim's own lookup gives %v", got, want)
	}
}
