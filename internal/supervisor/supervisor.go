package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
	"github.com/cordanaLLM/tribunus/internal/eventlog"
	"github.com/golusoris/golusoris/core/clock"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
	"github.com/jonboulle/clockwork"
)

const (
	StateRunning JobRunState = "running"
	StateDead    JobRunState = "dead"
	StateStopped JobRunState = "stopped"

	defaultPollInterval = time.Second
	maxJobRuntime       = 365 * 24 * time.Hour
	maxStatusPolls      = 100
	maxSuperviseTicks   = 1 << 30
)

var (
	ErrUnknownJob   = errors.New("supervisor: unknown job")
	ErrNotSupported = errors.New("supervisor: not-supported")
)

type JobRunState string

type JobStatus struct {
	Name     string
	State    JobRunState
	PID      int
	ShimPID  int
	Since    string
	Restarts int
	Sandbox  string
}

type jobSignalTarget struct {
	Name       string
	PID        int
	ShimPID    int
	RecordPath string
	LockPath   string
}

type Options struct {
	Clock        clock.Clock
	ShimCommand  []string
	PollInterval time.Duration
}

type Supervisor struct {
	cfg          config.Config
	signer       *corereceipt.Signer
	clk          clock.Clock
	shimCommand  []string
	pollInterval time.Duration
}

func New(cfg config.Config, opts Options) (*Supervisor, error) {
	if err := platformSupported(); err != nil {
		return nil, err
	}
	if opts.Clock == nil {
		opts.Clock = clockwork.NewRealClock()
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = defaultPollInterval
	}
	if len(opts.ShimCommand) == 0 {
		path, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("supervisor: executable: %w", err)
		}
		opts.ShimCommand = []string{path}
	}
	if err := checkConfig(cfg); err != nil {
		return nil, err
	}
	cfg = normalizeConfig(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	signer, err := eventlog.NewSignerFromKeyFile(ctx, cfg.EventLog.SigningKeyPath, cfg.EventLog.Dir, opts.Clock)
	if err != nil {
		return nil, err
	}
	return &Supervisor{cfg: cfg, signer: signer, clk: opts.Clock, shimCommand: opts.ShimCommand, pollInterval: opts.PollInterval}, nil
}

func (s *Supervisor) PublicKey() string {
	return s.signer.PublicKey()
}

func (s *Supervisor) Start(ctx context.Context, name string) error {
	if err := readyContext(ctx, "start"); err != nil {
		return err
	}
	if name == "" {
		for i := 0; i < len(s.cfg.Jobs); i++ {
			if err := s.startOne(ctx, s.cfg.Jobs[i].Name); err != nil {
				return err
			}
		}
		return nil
	}
	return s.startOne(ctx, name)
}

func (s *Supervisor) startOne(ctx context.Context, name string) error {
	job, err := s.job(name)
	if err != nil {
		return err
	}
	if running, err := s.isRunning(job); err != nil || running {
		return err
	}
	before, err := s.replayState(ctx)
	if err != nil {
		return err
	}
	cmd := s.shimCmd(ctx, job)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("supervisor: start shim for %s: %w", job.Name, err)
	}
	shimPID := cmd.Process.Pid
	if err = cmd.Process.Release(); err != nil {
		return fmt.Errorf("supervisor: release shim for %s: %w", job.Name, err)
	}
	return s.waitStarted(ctx, job.Name, before.Jobs[job.Name], shimPID)
}

func (s *Supervisor) Status(ctx context.Context, name string) ([]JobStatus, error) {
	if err := readyContext(ctx, "status"); err != nil {
		return nil, err
	}
	jobs, err := s.selectedJobs(name)
	if err != nil {
		return nil, err
	}
	state, err := s.replayState(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]JobStatus, 0, len(jobs))
	for i := 0; i < len(jobs); i++ {
		out = append(out, s.statusFor(jobs[i], state.Jobs[jobs[i].Name]))
	}
	return out, nil
}

func (s *Supervisor) Stop(ctx context.Context, name string) error {
	if err := readyContext(ctx, "stop"); err != nil {
		return err
	}
	if name == "" {
		for i := 0; i < len(s.cfg.Jobs); i++ {
			if err := s.stopOne(ctx, s.cfg.Jobs[i].Name); err != nil {
				return err
			}
		}
		return nil
	}
	return s.stopOne(ctx, name)
}

func (s *Supervisor) stopOne(ctx context.Context, name string) error {
	job, err := s.job(name)
	if err != nil {
		return err
	}
	status := s.statusFor(job, eventlog.Job{})
	if status.State != StateRunning {
		return s.appendJobEvent(ctx, "job.stopped", job.Name, map[string]any{"state": string(StateStopped)})
	}
	target, err := s.signalTarget(job)
	if err != nil {
		return err
	}
	if err = signalProcessGroup(target, job.Stop.Signal); err != nil {
		return err
	}
	if !s.waitStopped(ctx, job) {
		if err = killProcess(target); err != nil {
			return err
		}
		if !s.waitUnlocked(ctx, job, maxStatusPolls) {
			return fmt.Errorf("supervisor: stop %s: lock stayed held after SIGKILL", job.Name)
		}
	}
	return s.appendJobEvent(ctx, "job.stopped", job.Name, map[string]any{"state": string(StateStopped)})
}

func (s *Supervisor) signalTarget(job config.JobConfig) (jobSignalTarget, error) {
	recordPath := s.recordPath(job.Name)
	rec, ok := readJobRecord(recordPath)
	if !ok {
		return jobSignalTarget{}, fmt.Errorf("supervisor: record %s: missing or invalid", recordPath)
	}
	return jobSignalTarget{
		Name:       job.Name,
		PID:        rec.PID,
		ShimPID:    rec.ShimPID,
		RecordPath: recordPath,
		LockPath:   s.lockPath(job.Name),
	}, nil
}

func (s *Supervisor) Supervise(ctx context.Context) error {
	if err := readyContext(ctx, "supervise"); err != nil {
		return err
	}
	for tick := 0; tick < maxSuperviseTicks; tick++ {
		if err := s.Reconcile(ctx); err != nil {
			return err
		}
		if !sleepContext(ctx, s.pollInterval) {
			return ctx.Err()
		}
	}
	return fmt.Errorf("supervisor: supervise exceeded %d ticks", maxSuperviseTicks)
}

func (s *Supervisor) Reconcile(ctx context.Context) error {
	if err := readyContext(ctx, "reconcile"); err != nil {
		return err
	}
	state, err := s.replayState(ctx)
	if err != nil {
		return err
	}
	for i := 0; i < len(s.cfg.Jobs); i++ {
		if err = s.reconcileJob(ctx, s.cfg.Jobs[i], state.Jobs[s.cfg.Jobs[i].Name]); err != nil {
			return err
		}
	}
	return nil
}

// waitStarted waits until the event log records a start by the shim just spawned. It
// does not require the job to still be running: a job that started and already exited
// did start, and polling for "running" would miss it between two polls.
func (s *Supervisor) waitStarted(ctx context.Context, name string, before eventlog.Job, shimPID int) error {
	var lastErr error
	for i := 0; i < maxStatusPolls; i++ {
		state, err := s.replayState(ctx)
		if err == nil && startedBy(state.Jobs[name], before, shimPID) {
			return nil
		}
		lastErr = err
		if !sleepContext(ctx, 10*time.Millisecond) {
			return ctx.Err()
		}
	}
	if lastErr != nil {
		return fmt.Errorf("supervisor: %s did not start: %w", name, lastErr)
	}
	return fmt.Errorf("supervisor: %s did not start", name)
}

// startedBy reports whether job holds a start by shimPID recorded after before was read.
// Only job.started and job.adopted set ShimPID and Since (both from the shim's own record),
// and later events keep both, so a start followed by an exit still counts. A changed start
// time is what proves the start is new; a reused shim PID cannot pass on an old record.
func startedBy(job eventlog.Job, before eventlog.Job, shimPID int) bool {
	if job.ShimPID != shimPID || job.Since == "" {
		return false
	}
	return job.Since != before.Since
}
