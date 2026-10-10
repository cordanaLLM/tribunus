package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
	"github.com/cordanaLLM/tribunus/internal/eventlog"
	"github.com/jonboulle/clockwork"
)

type shimConfig struct {
	EventLogDir string
	SigningKey  string
	Name        string
	LockPath    string
	RecordPath  string
	LogPath     string
	Command     []string
	Sandbox     config.SandboxConfig
}

func RunShim(args []string) int {
	cfg, err := parseShimArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 2
	}
	if err = runShim(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func parseShimArgs(args []string) (shimConfig, error) {
	if len(args) > 0 && args[0] == "__job-shim" {
		args = args[1:]
	}
	var cfg shimConfig
	fs := flag.NewFlagSet("job-shim", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.EventLogDir, "event-log-dir", "", "")
	fs.StringVar(&cfg.SigningKey, "signing-key", "", "")
	fs.StringVar(&cfg.Name, "name", "", "")
	fs.StringVar(&cfg.LockPath, "lock", "", "")
	fs.StringVar(&cfg.RecordPath, "record", "", "")
	fs.StringVar(&cfg.LogPath, "log", "", "")
	fs.StringVar(&cfg.Sandbox.Mode, "sandbox-mode", "", "")
	fs.StringVar(&cfg.Sandbox.Reason, "sandbox-reason", "", "")
	fs.StringVar(&cfg.Sandbox.Workspace, "sandbox-workspace", "", "")
	fs.Var((*stringListFlag)(&cfg.Sandbox.Inputs), "sandbox-input", "")
	fs.Var((*stringListFlag)(&cfg.Sandbox.EnvAllow), "sandbox-env-allow", "")
	fs.StringVar(&cfg.Sandbox.Network, "sandbox-network", "", "")
	fs.StringVar(&cfg.Sandbox.MemoryMax, "sandbox-memory-max", "", "")
	fs.IntVar(&cfg.Sandbox.CPUWeight, "sandbox-cpu-weight", 0, "")
	fs.IntVar(&cfg.Sandbox.TasksMax, "sandbox-tasks-max", 0, "")
	if err := fs.Parse(args); err != nil {
		return shimConfig{}, fmt.Errorf("job-shim: parse: %w", err)
	}
	cfg.Command = fs.Args()
	if len(cfg.Command) > 0 && cfg.Command[0] == "--" {
		cfg.Command = cfg.Command[1:]
	}
	if err := checkShimConfig(cfg); err != nil {
		return shimConfig{}, err
	}
	return cfg, nil
}

func checkShimConfig(cfg shimConfig) error {
	if cfg.EventLogDir == "" || cfg.SigningKey == "" || cfg.Name == "" {
		return fmt.Errorf("job-shim: event log, signing key and name are required")
	}
	if cfg.LockPath == "" || cfg.RecordPath == "" || cfg.LogPath == "" {
		return fmt.Errorf("job-shim: lock, record and log paths are required")
	}
	if len(cfg.Command) == 0 || len(cfg.Command) > config.MaxJobArgs {
		return fmt.Errorf("job-shim: command must have 1..%d args", config.MaxJobArgs)
	}
	cfg.Sandbox = config.NormalizeSandbox(cfg.Sandbox)
	if err := config.ValidateSandbox(cfg.Sandbox); err != nil {
		return fmt.Errorf("job-shim: sandbox.%w", err)
	}
	return nil
}

func runShim(cfg shimConfig) error {
	lock, err := holdLock(cfg.LockPath)
	if err != nil {
		return err
	}
	defer closeLockForShim(lock)
	logFile, err := openAppendLog(cfg.LogPath)
	if err != nil {
		return err
	}
	defer closeLogForShim(logFile)
	// Linux Pdeathsig is tied to the creating thread, so keep the start thread
	// alive until the child exits or is killed on a shim error path.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	runCtx, cancel := context.WithTimeout(context.Background(), maxJobRuntime)
	defer cancel()
	command, err := buildJobCommand(cfg.Command, cfg.Sandbox, sandboxBuildOptions{
		Paths: sandboxPaths{EventLogDir: cfg.EventLogDir, SigningKey: cfg.SigningKey},
		Env:   os.LookupEnv,
	})
	if err != nil {
		return fmt.Errorf("job-shim: sandbox %s: %w", cfg.Name, err)
	}
	// #nosec G204 -- job command comes from validated operator config and is
	// executed as argv without a shell.
	cmd := exec.CommandContext(runCtx, command.Argv[0], command.Argv[1:]...)
	cmd.Env = command.Env
	cmd.Stdout, cmd.Stderr = logFile, logFile
	prepareProcess(cmd)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("job-shim: start %s: %w", cfg.Command[0], err)
	}
	started := time.Now().UTC().Format(time.RFC3339Nano)
	if err = writeRecord(cfg, cmd.Process.Pid, started); err != nil {
		return killAfterShimError(cmd.Process.Pid, err)
	}
	// The record comes first so that a stop during the wait can reach the job; job.started
	// is appended only for a job confirmed inside its scope.
	if err = confirmJobScope(cmd.Process.Pid, command.Scope, command.Argv[0]); err != nil {
		return killAfterShimError(cmd.Process.Pid, reportRefusal(logFile, cfg.Name, err))
	}
	writer, err := shimWriter(cfg)
	if err != nil {
		return killAfterShimError(cmd.Process.Pid, err)
	}
	if err = appendStarted(writer, cfg, cmd.Process.Pid, started); err != nil {
		return killAfterShimError(cmd.Process.Pid, err)
	}
	return waitAndRecordExit(writer, cfg, cmd)
}

func openAppendLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("job-shim: mkdir log dir %s: %w", filepath.Dir(path), err)
	}
	// #nosec G304 -- log path comes from validated operator config for this job.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("job-shim: open log %s: %w", path, err)
	}
	return f, nil
}

func writeRecord(cfg shimConfig, pid int, started string) error {
	if err := os.MkdirAll(filepath.Dir(cfg.RecordPath), 0o750); err != nil {
		return fmt.Errorf("job-shim: mkdir state dir: %w", err)
	}
	rec := jobRecord{Name: cfg.Name, PID: pid, ShimPID: os.Getpid(), StartedAt: started, LogPath: cfg.LogPath}
	body, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("job-shim: marshal record: %w", err)
	}
	// #nosec G304 -- record path is derived from configured state dir plus checked job name.
	return os.WriteFile(cfg.RecordPath, append(body, '\n'), 0o600)
}

func shimWriter(cfg shimConfig) (*eventlog.Writer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	clk := clockwork.NewRealClock()
	signer, err := eventlog.NewSignerFromKeyFile(ctx, cfg.SigningKey, cfg.EventLogDir, clk)
	if err != nil {
		return nil, err
	}
	return eventlog.Open(cfg.EventLogDir, signer, eventlog.Limits{Clock: clk})
}

func appendStarted(writer *eventlog.Writer, cfg shimConfig, pid int, started string) error {
	payload := map[string]any{"state": string(StateRunning), "pid": pid, "shim_pid": os.Getpid(), "since": started, "sandbox": sandboxStatus(cfg.Sandbox)}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("job-shim: marshal started: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = writer.Append(ctx, eventlog.Event{Type: "job.started", TaskID: cfg.Name, Payload: body})
	return err
}

type stringListFlag []string

func (flag *stringListFlag) String() string {
	return strings.Join(*flag, ",")
}

func (flag *stringListFlag) Set(value string) error {
	*flag = append(*flag, value)
	return nil
}

func waitAndRecordExit(writer *eventlog.Writer, cfg shimConfig, cmd *exec.Cmd) error {
	err := cmd.Wait()
	code := exitCode(err)
	payload := map[string]any{"state": string(StateDead), "code": code}
	body, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return fmt.Errorf("job-shim: marshal exited: %w", marshalErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, appendErr := writer.Append(ctx, eventlog.Event{Type: "job.exited", TaskID: cfg.Name, Payload: body}); appendErr != nil {
		return appendErr
	}
	return nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// reportRefusal writes why a started job was killed into the job's log, where its own
// output is read; the shim's stderr is not kept.
func reportRefusal(logFile io.Writer, name string, cause error) error {
	err := fmt.Errorf("job-shim: %s refused: %w", name, cause)
	if _, writeErr := fmt.Fprintf(logFile, "tribunus: %v\n", err); writeErr != nil {
		return errors.Join(err, fmt.Errorf("job-shim: write refusal to the job log: %w", writeErr))
	}
	return err
}

func killAfterShimError(pid int, cause error) error {
	if err := killStartedProcessGroup(pid); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func closeLockForShim(lock *heldLock) {
	if err := lock.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: close job lock: %v\n", err)
	}
}

func closeLogForShim(logFile *os.File) {
	if err := logFile.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: close job log: %v\n", err)
	}
}
