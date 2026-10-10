package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
	"github.com/cordanaLLM/tribunus/internal/eventlog"
)

type jobRecord struct {
	Name      string `json:"name"`
	PID       int    `json:"pid"`
	ShimPID   int    `json:"shim_pid"`
	StartedAt string `json:"started_at"`
	LogPath   string `json:"log_path"`
}

var jobRecordReadFile = os.ReadFile

func (s *Supervisor) replayState(ctx context.Context) (eventlog.State, error) {
	events := filepath.Join(s.cfg.EventLog.Dir, "events")
	if _, err := os.Stat(events); os.IsNotExist(err) {
		return eventlog.State{Jobs: map[string]eventlog.Job{}}, nil
	} else if err != nil {
		return eventlog.State{}, fmt.Errorf("supervisor: stat %s: %w", events, err)
	}
	limits := eventlog.Limits{Clock: s.clk, MaxReplayRecords: s.cfg.EventLog.MaxReplayRecords}
	return eventlog.ReplayStable(ctx, s.cfg.EventLog.Dir, s.signer.PublicKey(), limits, JobReducer)
}

func JobReducer(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	if st.Jobs == nil {
		st.Jobs = map[string]eventlog.Job{}
	}
	switch rec.Type {
	case "job.started", "job.adopted":
		return reduceJobStarted(st, rec)
	case "job.exited":
		return reduceJobExited(st, rec)
	case "job.refused":
		return reduceJobRefused(st, rec)
	case "job.restarted":
		return reduceJobRestarted(st, rec)
	case "job.stopped":
		return reduceJobStopped(st, rec)
	}
	return st, nil
}

func reduceJobStarted(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	var payload struct {
		State   string `json:"state"`
		PID     int    `json:"pid"`
		ShimPID int    `json:"shim_pid"`
		Since   string `json:"since"`
		Sandbox string `json:"sandbox"`
	}
	if err := json.Unmarshal(rec.Payload, &payload); err != nil {
		return eventlog.State{}, fmt.Errorf("job payload: %w", err)
	}
	job := st.Jobs[rec.TaskID]
	job.Name, job.State, job.PID = rec.TaskID, payload.State, payload.PID
	job.ShimPID, job.Since, job.LastEvent = payload.ShimPID, payload.Since, rec.Type
	job.Sandbox = payload.Sandbox
	st.Jobs[rec.TaskID] = job
	return st, nil
}

func reduceJobExited(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	var payload struct {
		State  string `json:"state"`
		Code   *int   `json:"code,omitempty"`
		Reason string `json:"reason,omitempty"`
	}
	if err := json.Unmarshal(rec.Payload, &payload); err != nil {
		return eventlog.State{}, fmt.Errorf("job payload: %w", err)
	}
	job := st.Jobs[rec.TaskID]
	job.Name, job.State, job.LastExitCode = rec.TaskID, payload.State, payload.Code
	job.LastReason, job.LastEvent = payload.Reason, rec.Type
	st.Jobs[rec.TaskID] = job
	return st, nil
}

// reduceJobRefused records a start the shim refused: the job is not running, and the reason
// is kept. ShimPID and Since stay as they are; they belong to the last recorded start, and
// startedBy reads them.
func reduceJobRefused(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	var payload struct {
		State   string `json:"state"`
		ShimPID int    `json:"shim_pid"`
		At      string `json:"at"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Payload, &payload); err != nil {
		return eventlog.State{}, fmt.Errorf("job payload: %w", err)
	}
	job := st.Jobs[rec.TaskID]
	job.Name, job.State, job.LastEvent = rec.TaskID, payload.State, rec.Type
	job.LastReason, job.LastExitCode = payload.Reason, nil
	job.RefusedShimPID, job.RefusedAt = payload.ShimPID, payload.At
	st.Jobs[rec.TaskID] = job
	return st, nil
}

func reduceJobRestarted(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	job := st.Jobs[rec.TaskID]
	job.Name, job.Restarts, job.LastEvent = rec.TaskID, job.Restarts+1, rec.Type
	st.Jobs[rec.TaskID] = job
	return st, nil
}

func reduceJobStopped(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	job := st.Jobs[rec.TaskID]
	job.Name, job.State, job.LastEvent = rec.TaskID, string(StateStopped), rec.Type
	st.Jobs[rec.TaskID] = job
	return st, nil
}

func (s *Supervisor) statusFor(job config.JobConfig, replayed eventlog.Job) JobStatus {
	rec, hasRecord := readJobRecord(s.recordPath(job.Name))
	running := false
	if hasRecord {
		held, err := lockHeld(s.lockPath(job.Name))
		running = err == nil && held
	}
	if running {
		return JobStatus{Name: job.Name, State: StateRunning, PID: rec.PID, ShimPID: rec.ShimPID, Since: rec.StartedAt, Restarts: replayed.Restarts, Sandbox: runningSandbox(rec, replayed)}
	}
	if replayed.State == string(StateStopped) {
		return JobStatus{Name: job.Name, State: StateStopped, Restarts: replayed.Restarts, Sandbox: sandboxStatus(job.Sandbox)}
	}
	return JobStatus{Name: job.Name, State: StateDead, Restarts: replayed.Restarts, Sandbox: sandboxStatus(job.Sandbox)}
}

// runningSandbox names the sandbox a running job was started under, as its shim recorded
// it in job.started, never the current config, which may have changed since. Until the
// shim's event for this start is replayed, the answer is unknown.
func runningSandbox(rec jobRecord, replayed eventlog.Job) string {
	if replayed.Since != rec.StartedAt || replayed.Sandbox == "" {
		return "unknown"
	}
	return replayed.Sandbox
}

func readJobRecord(path string) (jobRecord, bool) {
	body, err := jobRecordReadFile(path) // #nosec G304 -- path is derived from configured state dir plus checked job name.
	if err != nil {
		return jobRecord{}, false
	}
	var rec jobRecord
	if err = json.Unmarshal(body, &rec); err != nil {
		return jobRecord{}, false
	}
	return rec, true
}

func (s *Supervisor) appendJobEvent(ctx context.Context, typ string, name string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("supervisor: marshal %s: %w", typ, err)
	}
	writer, err := eventlog.Open(s.cfg.EventLog.Dir, s.signer, eventlog.Limits{Clock: s.clk, MaxReplayRecords: s.cfg.EventLog.MaxReplayRecords})
	if err != nil {
		return err
	}
	_, err = writer.Append(ctx, eventlog.Event{Type: typ, TaskID: name, Payload: body})
	return err
}

func (s *Supervisor) reconcileJob(ctx context.Context, job config.JobConfig, st eventlog.Job) error {
	if job.Schedule != "always" {
		return nil
	}
	status := s.statusFor(job, st)
	if status.State == StateRunning {
		return s.adoptIfFreshStart(ctx, job, st, status)
	}
	if st.State == string(StateRunning) {
		return s.recordLost(ctx, job)
	}
	if st.LastEvent == "" {
		return s.Start(ctx, job.Name)
	}
	if shouldRestart(job, st) {
		return s.restartJob(ctx, job)
	}
	return nil
}

func (s *Supervisor) adoptIfFreshStart(ctx context.Context, job config.JobConfig, st eventlog.Job, status JobStatus) error {
	if st.LastEvent != "job.started" {
		return nil
	}
	payload := map[string]any{"state": string(StateRunning), "pid": status.PID, "shim_pid": status.ShimPID, "since": status.Since, "sandbox": status.Sandbox}
	return s.appendJobEvent(ctx, "job.adopted", job.Name, payload)
}

func (s *Supervisor) recordLost(ctx context.Context, job config.JobConfig) error {
	payload := map[string]any{"state": string(StateDead), "reason": "lost"}
	return s.appendJobEvent(ctx, "job.exited", job.Name, payload)
}

func (s *Supervisor) restartJob(ctx context.Context, job config.JobConfig) error {
	payload := map[string]any{"state": string(StateDead)}
	if err := s.appendJobEvent(ctx, "job.restarted", job.Name, payload); err != nil {
		return err
	}
	if job.Restart.BackoffSeconds > 0 && !sleepContext(ctx, time.Duration(job.Restart.BackoffSeconds)*time.Second) {
		return ctx.Err()
	}
	return s.Start(ctx, job.Name)
}

func shouldRestart(job config.JobConfig, st eventlog.Job) bool {
	if st.Restarts >= job.Restart.MaxRestarts {
		return false
	}
	if job.Restart.Policy == "always" && st.State != string(StateStopped) {
		return true
	}
	if job.Restart.Policy != "on-failure" {
		return false
	}
	return st.LastExitCode == nil || *st.LastExitCode != 0 || st.LastReason == "lost"
}
