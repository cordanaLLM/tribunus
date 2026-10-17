//go:build linux

package supervisor

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A shim that exits releases the job's lock before the kernel gives its job another parent.
// A stop that validates its target in that moment sees the job and its shim alive and the
// lock free (#227). These tests put the stop in that moment: the shim probe of the call
// numbered killAt kills the shim and returns once the lock is free, as a shim in the middle
// of its exit looks.

func TestStopOfAJobWhoseShimExitsDuringTheSignalIsDone(t *testing.T) {
	stopWhileShimExits(t, 1, 0)
}

func TestStopOfAJobWhoseShimExitsDuringTheKillIsDone(t *testing.T) {
	stopWhileShimExits(t, 2, 1)
}

// TestStopRefusalStandsWhileTheLockIsHeld is the negative case: a refusal while the shim
// still holds the lock is a target the stop cannot check, and stays an error.
func TestStopRefusalStandsWhileTheLockIsHeld(t *testing.T) {
	sup := testSupervisor(t, testJob("probe", longJobCommand(t)))
	if err := sup.Start(testContext(t), "probe"); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	rec := withRecordingSignaller(t)
	withShimProbe(t, unix.ESRCH)
	err := sup.Stop(testContext(t), "probe")
	if err == nil || !strings.Contains(err.Error(), "is not alive") {
		t.Fatalf("Stop(shim probe fails, lock held) = %v, want the refusal", err)
	}
	if len(*rec) != 0 {
		t.Fatalf("signals = %+v, want none", *rec)
	}
	if st := statusOne(t, sup, "probe"); st.State != StateRunning {
		t.Fatalf("Status(after the refusal) = %s, want running", st.State)
	}
}

// stopWhileShimExits stops a job whose shim exits during the killAt-th shim probe. The stop
// must succeed with wantSignals signals sent before it, and none at the job after the lock
// is free; the shim's exit ends the job.
func stopWhileShimExits(t *testing.T, killAt int, wantSignals int) {
	t.Helper()
	job := testJob("probe", longJobCommand(t))
	job.Stop.GraceSeconds = 0
	sup := testSupervisor(t, job)
	if err := sup.Start(testContext(t), "probe"); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	rec, ok := readJobRecord(sup.recordPath("probe"))
	if !ok {
		t.Fatal("job record missing after Start")
	}
	signals := withRecordingSignaller(t)
	withShimExitOnProbe(t, sup.lockPath("probe"), killAt)
	if err := sup.Stop(testContext(t), "probe"); err != nil {
		t.Fatalf("Stop(shim exits during probe %d) = %v, want nil", killAt, err)
	}
	if len(*signals) != wantSignals {
		t.Fatalf("signals = %+v, want %d", *signals, wantSignals)
	}
	if st := statusOne(t, sup, "probe"); st.State != StateStopped {
		t.Fatalf("Status(after stop) = %s, want stopped", st.State)
	}
	waitProcessGone(t, rec.PID)
}

// withShimExitOnProbe makes the killAt-th shim probe kill the shim and wait until the lock
// at lockPath is free. Every other probe is the real one.
func withShimExitOnProbe(t *testing.T, lockPath string, killAt int) {
	t.Helper()
	old := shimProcessProbe
	calls := 0
	shimProcessProbe = func(pid int, sig unix.Signal) error {
		calls++
		if calls != killAt {
			return old(pid, sig)
		}
		if err := unix.Kill(pid, unix.SIGKILL); err != nil {
			t.Errorf("kill shim %d: %v", pid, err)
		}
		waitLockFree(t, lockPath)
		return nil
	}
	t.Cleanup(func() { shimProcessProbe = old })
}

func waitLockFree(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 400; i++ {
		held, err := lockHeld(path)
		if err == nil && !held {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("lock %s still held 2s after the shim was killed", path)
}

func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if gone, err := processGone(pid); err == nil && gone {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %d still running 2s after its shim was killed", pid)
}
