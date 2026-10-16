//go:build linux

package supervisor

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const (
	soakCycles = 200
	// A start and a stop write a handful of records; the bound only fails a cycle that writes
	// far more, or records that keep growing.
	soakRecordsPerCycle = 4
	soakRecordBytes     = 4 << 10
	// The supervisor and the test helpers hold a few descriptors of their own; the bound only
	// fails a leak that grows with the cycles.
	soakFDGrowth = 8
)

// TestSoakSupervisorStartStop starts and stops a job a few hundred times through the shim, then
// checks that no child, zombie or file descriptor is left over and that the event log grew by
// a bounded number of records each cycle. Opt-in: EPITROP_SOAK=1. The job runs with the
// sandbox off, like the other supervisor tests, so the soak needs no namespaces.
func TestSoakSupervisorStartStop(t *testing.T) {
	if os.Getenv("EPITROP_SOAK") != "1" {
		t.Skip("soak test: set EPITROP_SOAK=1 to run it")
	}
	job := testJob("soak", []string{"/bin/sleep", "30"})
	sup := testSupervisor(t, job)
	fdStart := openFDs(t)
	start := time.Now()
	for i := 0; i < soakCycles; i++ {
		if err := sup.Start(testContext(t), job.Name); err != nil {
			t.Fatalf("cycle %d: Start() = %v, want nil", i, err)
		}
		if err := sup.Stop(testContext(t), job.Name); err != nil {
			t.Fatalf("cycle %d: Stop() = %v, want nil", i, err)
		}
	}
	t.Logf("soak: %d start and stop cycles took %s", soakCycles, time.Since(start))
	// The bound only ends a run in which a shim is never collected.
	waitUntil(func() bool { return len(childrenOfThisProcess(t)) == 0 })
	requireNoChildren(t)
	requireFDsBounded(t, fdStart)
	requireEventLogBounded(t, filepath.Join(sup.cfg.EventLog.Dir, "events"))
}

// childrenOfThisProcess returns every child of the test process, zombies included.
func childrenOfThisProcess(t *testing.T) []int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("ReadDir(/proc) = %v, want nil", err)
	}
	self := strconv.Itoa(os.Getpid())
	var pids []int
	for i := 0; i < len(entries); i++ {
		pid, convErr := strconv.Atoi(entries[i].Name())
		if convErr != nil {
			continue
		}
		// A process that exited between the listing and this read is no child any more.
		if _, ppid, _ := zombieChildDiagnostic(pid); ppid == self {
			pids = append(pids, pid)
		}
	}
	return pids
}

func requireNoChildren(t *testing.T) {
	t.Helper()
	children := childrenOfThisProcess(t)
	zombies := zombieChildren(t)
	t.Logf("soak: %d children left, %d of them zombies, bound 0", len(children), len(zombies))
	for pid := range zombies {
		comm, ppid, state := zombieChildDiagnostic(pid)
		t.Errorf("pid %d (%s) is a zombie child (PPid %s, State %s), want every shim collected", pid, comm, ppid, state)
	}
	if len(children) > len(zombies) {
		t.Errorf("%d live children %v after every job stopped, want none", len(children)-len(zombies), children)
	}
}

func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("ReadDir(/proc/self/fd) = %v, want nil", err)
	}
	return len(entries)
}

func requireFDsBounded(t *testing.T, start int) {
	t.Helper()
	end := openFDs(t)
	t.Logf("soak: open file descriptors %d -> %d, bound +%d", start, end, soakFDGrowth)
	if end > start+soakFDGrowth {
		t.Errorf("open file descriptors grew from %d to %d, want at most +%d", start, end, soakFDGrowth)
	}
}

func requireEventLogBounded(t *testing.T, eventsDir string) {
	t.Helper()
	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		t.Fatalf("ReadDir(%s) = %v, want nil", eventsDir, err)
	}
	var size int64
	for i := 0; i < len(entries); i++ {
		info, infoErr := entries[i].Info()
		if infoErr != nil {
			t.Fatalf("stat %s: %v", entries[i].Name(), infoErr)
		}
		size += info.Size()
	}
	maxFiles := soakCycles * soakRecordsPerCycle
	t.Logf("soak: event log %d files, %d bytes, bound %d files, %d bytes", len(entries), size, maxFiles, int64(maxFiles)*soakRecordBytes)
	if len(entries) > maxFiles || size > int64(maxFiles)*soakRecordBytes {
		t.Errorf("event log = %d files, %d bytes, want at most %d files, %d bytes", len(entries), size, maxFiles, int64(maxFiles)*soakRecordBytes)
	}
}
