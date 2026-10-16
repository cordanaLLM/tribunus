package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
	"github.com/jonboulle/clockwork"

	"github.com/tekmora/epitrop/internal/eventlog"
)

// v010FixtureSandbox is the sandbox the v0.1.0 shim recorded for fixture-job in job.started.
const v010FixtureSandbox = "off: fixture: writes one line and exits"

// v010JobRecord is the job record as v0.1.0 wrote it, kept apart from jobRecord, so a
// renamed or dropped field in jobRecord shows up as a difference.
type v010JobRecord struct {
	Name      string `json:"name"`
	PID       int    `json:"pid"`
	ShimPID   int    `json:"shim_pid"`
	StartedAt string `json:"started_at"`
	LogPath   string `json:"log_path"`
}

// v010JobsDir copies the jobs fixture that testdata/v0.1.0/generate.sh wrote with the
// published v0.1.0 binary (one run of fixture-job in an event log of its own) and returns
// the copy, so a replay's lock file never lands in the fixture.
func v010JobsDir(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "v0.1.0", "jobs"))
	if err != nil {
		t.Fatalf("filepath.Abs(testdata/v0.1.0/jobs) = %v", err)
	}
	dst := filepath.Join(t.TempDir(), "jobs")
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("os.CopyFS(%s) = %v", src, err)
	}
	return dst
}

// readV010Record reads the v0.1.0 job record in its v0.1.0 shape and returns it with the
// names of the fields the file holds.
func readV010Record(t *testing.T, path string) (v010JobRecord, []string) {
	t.Helper()
	body, err := os.ReadFile(path) // #nosec G304 -- path is the copied test fixture.
	if err != nil {
		t.Fatalf("ReadFile(%s) = %v", path, err)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("json.Unmarshal(%s) into fields = %v", path, err)
	}
	var rec v010JobRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("json.Unmarshal(%s) into the v0.1.0 record = %v", path, err)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return rec, keys
}

func TestReadJobRecordReadsTheV010Record(t *testing.T) {
	path := filepath.Join(v010JobsDir(t), "jobs", "fixture-job.json")
	v010, keys := readV010Record(t, path)
	if want := []string{"log_path", "name", "pid", "shim_pid", "started_at"}; !slices.Equal(keys, want) {
		t.Fatalf("v0.1.0 job record fields = %v, want %v", keys, want)
	}
	rec, ok := readJobRecord(path)
	if !ok {
		t.Fatalf("readJobRecord(v0.1.0 record) reports no record")
	}
	if want := jobRecord(v010); rec != want || rec.Name != "fixture-job" || rec.PID == 0 || rec.ShimPID == 0 || rec.StartedAt == "" || rec.LogPath == "" {
		t.Fatalf("readJobRecord(v0.1.0 record) = %+v, want %+v with every field set", rec, want)
	}
}

// replayV010Job replays the v0.1.0 jobs fixture in dir with the current reducer and returns
// fixture-job.
func replayV010Job(t *testing.T, dir string) eventlog.Job {
	t.Helper()
	clk := clockwork.NewRealClock()
	signer, err := corereceipt.NewSignerFromSeed(bytes.Repeat([]byte{7}, 32), clk)
	if err != nil {
		t.Fatalf("NewSignerFromSeed() = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	limits := eventlog.Limits{Clock: clk, LockTimeout: 5 * time.Second}
	st, err := eventlog.ReplayStable(ctx, dir, signer.PublicKey(), limits, JobReducer)
	if err != nil {
		t.Fatalf("ReplayStable(v0.1.0 jobs) = %v", err)
	}
	job, ok := st.Jobs["fixture-job"]
	if !ok {
		t.Fatalf("replayed jobs = %v, want fixture-job", st.Jobs)
	}
	return job
}

// TestJobReducerReplaysTheV010Events: the current reducer reads the run v0.1.0 recorded, and
// its job.started agrees with the job record on the start time, which is how status ties a
// running job's record to its sandbox.
func TestJobReducerReplaysTheV010Events(t *testing.T) {
	dir := v010JobsDir(t)
	job := replayV010Job(t, dir)
	exitedWithZero := job.LastExitCode != nil && *job.LastExitCode == 0
	if job.State != string(StateDead) || job.LastEvent != "job.exited" || !exitedWithZero || job.Sandbox != v010FixtureSandbox {
		t.Fatalf("fixture-job = %+v, want dead after job.exited with code 0 and sandbox %q", job, v010FixtureSandbox)
	}
	rec, ok := readJobRecord(filepath.Join(dir, "jobs", "fixture-job.json"))
	if !ok {
		t.Fatalf("readJobRecord(v0.1.0 record) reports no record")
	}
	if job.PID != rec.PID || job.ShimPID != rec.ShimPID {
		t.Fatalf("fixture-job pids = %d/%d, record pids = %d/%d", job.PID, job.ShimPID, rec.PID, rec.ShimPID)
	}
	if got := runningSandbox(rec, job); got != v010FixtureSandbox {
		t.Fatalf("runningSandbox(v0.1.0 record, replayed job) = %q, want %q", got, v010FixtureSandbox)
	}
}
