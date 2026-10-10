package supervisor

import (
	"context"
	"os/exec"
	"strconv"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
)

func (s *Supervisor) shimCmd(ctx context.Context, job config.JobConfig) *exec.Cmd {
	args := make([]string, 0, len(s.shimCommand)+len(job.Command)+16)
	args = append(args, s.shimCommand[1:]...)
	args = append(args,
		"__job-shim",
		"--event-log-dir", s.cfg.EventLog.Dir,
		"--signing-key", s.cfg.EventLog.SigningKeyPath,
		"--name", job.Name,
		"--lock", s.lockPath(job.Name),
		"--record", s.recordPath(job.Name),
		"--log", job.LogPath,
		"--max-runtime-seconds", strconv.Itoa(job.MaxRuntimeSeconds),
		"--stop-signal", job.Stop.Signal,
		"--stop-grace-seconds", strconv.Itoa(job.Stop.GraceSeconds),
	)
	args = append(args, sandboxFlagArgs(job.Sandbox)...)
	args = append(args, "--")
	args = append(args, job.Command...)
	// #nosec G204 -- the shim path is this executable by default, and job
	// command arguments are passed as argv after "--"; no shell is invoked.
	cmd := exec.CommandContext(ctx, s.shimCommand[0], args...)
	cmd.Cancel = nil
	return cmd
}

func (s *Supervisor) isRunning(job config.JobConfig) (bool, error) {
	return lockHeld(s.lockPath(job.Name))
}

func (s *Supervisor) waitStopped(ctx context.Context, job config.JobConfig) bool {
	grace := job.Stop.GraceSeconds
	if grace < 0 {
		grace = 0
	}
	limit := grace*100 + 1
	for i := 0; i < limit; i++ {
		running, err := s.isRunning(job)
		if err == nil && !running {
			return true
		}
		if grace <= 0 {
			return false
		}
		if !sleepContext(ctx, 10*time.Millisecond) {
			return false
		}
	}
	return false
}

func (s *Supervisor) waitUnlocked(ctx context.Context, job config.JobConfig, limit int) bool {
	for i := 0; i < limit; i++ {
		running, err := s.isRunning(job)
		if err == nil && !running {
			return true
		}
		if !sleepContext(ctx, 10*time.Millisecond) {
			return false
		}
	}
	return false
}
