package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
	"github.com/cordanaLLM/tribunus/internal/supervisor"
	"github.com/golusoris/golusoris/core/clock"
)

func TestHelperProcessShim(t *testing.T) {
	if os.Getenv("TRIBUNUS_TEST_SHIM") != "1" {
		return
	}
	os.Exit(runJobShim(argsAfterDash(os.Args)))
}

func TestRunJobsRejectsMissingAndUnknownCommand(t *testing.T) {
	if err := runJobs(nil); err == nil || !strings.Contains(err.Error(), "jobs command required") {
		t.Fatalf("runJobs(nil) = %v, want command required", err)
	}
	if err := runJobs([]string{"bogus"}); err == nil || !strings.Contains(err.Error(), "unknown jobs command: bogus") {
		t.Fatalf("runJobs(bogus) = %v, want unknown command", err)
	}
}

func TestRunJobsRequiresConfig(t *testing.T) {
	err := runJobs([]string{"status"})
	if err == nil || !strings.Contains(err.Error(), "jobs status: --config is required") {
		t.Fatalf("runJobs(status) = %v, want config required", err)
	}
}

func TestRunJobsAcceptsAllKnownCommands(t *testing.T) {
	for _, action := range []string{"start", "stop", "supervise"} {
		t.Run(action, func(t *testing.T) {
			err := runJobs([]string{action})
			if err == nil || !strings.Contains(err.Error(), "--config is required") {
				t.Fatalf("runJobs(%s) = %v, want config-required action path", action, err)
			}
		})
	}
}

func TestRunJobsActionRejectsParseNameLoadAndNewErrors(t *testing.T) {
	if err := runJobsAction("status", []string{"-bad"}); err == nil || !strings.Contains(err.Error(), "flag provided") {
		t.Fatalf("runJobsAction(bad flag) = %v, want parse error", err)
	}
	cfgPath := writeJobsConfig(t, "missing-key")
	if err := runJobsAction("status", []string{"--config", cfgPath, "one", "two"}); err == nil || !strings.Contains(err.Error(), "expected at most one name") {
		t.Fatalf("runJobsAction(two names) = %v, want name error", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	if err := runJobsAction("status", []string{"--config", missing}); err == nil || !strings.Contains(err.Error(), "stat") {
		t.Fatalf("runJobsAction(missing config) = %v, want stat error", err)
	}
	if err := runJobsAction("status", []string{"--config", cfgPath}); err == nil {
		t.Fatal("runJobsAction(missing key) = nil, want New signer error")
	}
}

func TestRunJobsActionStatusWithLoadedConfig(t *testing.T) {
	cfgPath := writeJobsConfig(t, "valid-key")
	out := captureStdout(t, func() {
		if err := runJobsAction("status", []string{"--config", cfgPath, "idle"}); err != nil {
			t.Fatalf("runJobsAction(status loaded) = %v, want nil", err)
		}
	})
	if !strings.HasPrefix(out, "idle\tdead\t0\t\t0\t") {
		t.Fatalf("status output = %q, want idle row", out)
	}
}

func TestRunJobsActionPassesPollInterval(t *testing.T) {
	cfgPath := writeJobsConfig(t, "valid-key")
	wantErr := errors.New("captured supervisor options")
	var got time.Duration
	old := newJobsSupervisor
	newJobsSupervisor = func(cfg config.Config, opts supervisor.Options) (*supervisor.Supervisor, error) {
		got = opts.PollInterval
		return nil, wantErr
	}
	t.Cleanup(func() { newJobsSupervisor = old })
	err := runJobsAction("status", []string{"--config", cfgPath, "--poll-interval-seconds", "7"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runJobsAction(poll capture) = %v, want sentinel", err)
	}
	if got != 7*time.Second {
		t.Fatalf("PollInterval = %s, want 7s", got)
	}
}

func TestJobsNameArgBounds(t *testing.T) {
	if name, err := jobsNameArg("supervise", nil); err != nil || name != "" {
		t.Fatalf("jobsNameArg(supervise) = %q, %v, want empty nil", name, err)
	}
	if name, err := jobsNameArg("status", []string{"one"}); err != nil || name != "one" {
		t.Fatalf("jobsNameArg(status one) = %q, %v, want one nil", name, err)
	}
	if _, err := jobsNameArg("start", []string{"one", "two"}); err == nil {
		t.Fatal("jobsNameArg(two names) = nil error, want bounds failure")
	}
}

func TestPrintJobsStatusWritesTabRows(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("status formatting test needs Linux supervisor support")
	}
	sup := testJobsSupervisor(t)
	out := captureStdout(t, func() {
		if err := printJobsStatus(testContext(t), sup, "idle"); err != nil {
			t.Fatalf("printJobsStatus() = %v, want nil", err)
		}
	})
	if !strings.HasPrefix(out, "idle\tdead\t0\t\t0\t") {
		t.Fatalf("status output = %q, want tab row", out)
	}
}

func TestRunLoadedJobsActionDispatchesStatus(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("status dispatch test needs Linux supervisor support")
	}
	sup := testJobsSupervisor(t)
	out := captureStdout(t, func() {
		if err := runLoadedJobsAction(testContext(t), sup, "status", "idle"); err != nil {
			t.Fatalf("runLoadedJobsAction(status) = %v, want nil", err)
		}
	})
	if !strings.HasPrefix(out, "idle\tdead\t0\t\t0\t") {
		t.Fatalf("status output = %q, want tab row", out)
	}
}

func TestRunLoadedJobsActionDispatchesStartStopSuperviseAndDefault(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("dispatch test needs Linux supervisor support")
	}
	sup := testJobsSupervisor(t)
	if err := runLoadedJobsAction(testContext(t), sup, "start", "idle"); err != nil {
		t.Fatalf("runLoadedJobsAction(start) = %v, want nil", err)
	}
	if st, err := sup.Status(testContext(t), "idle"); err != nil || st[0].State != supervisor.StateRunning {
		t.Fatalf("Status(after start) = %+v, %v, want running", st, err)
	}
	if err := runLoadedJobsAction(testContext(t), sup, "stop", "idle"); err != nil {
		t.Fatalf("runLoadedJobsAction(stop) = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(testContext(t))
	cancel()
	if err := runLoadedJobsAction(ctx, sup, "supervise", ""); err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("runLoadedJobsAction(supervise canceled) = %v, want context error", err)
	}
	if err := runLoadedJobsAction(testContext(t), sup, "bogus", ""); err == nil {
		t.Fatal("runLoadedJobsAction(bogus) = nil, want unknown command")
	}
}

func TestPrintJobsStatusReturnsSupervisorError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("status error test needs Linux supervisor support")
	}
	sup := testJobsSupervisor(t)
	err := printJobsStatus(testContext(t), sup, "missing")
	if err == nil || !strings.Contains(err.Error(), "unknown job") {
		t.Fatalf("printJobsStatus(missing) = %v, want unknown job", err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() = %v, want nil", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = old
	}()
	fn()
	if err = writer.Close(); err != nil {
		t.Fatalf("Close(writer) = %v, want nil", err)
	}
	var buf bytes.Buffer
	if _, err = io.Copy(&buf, reader); err != nil {
		t.Fatalf("Copy(stdout) = %v, want nil", err)
	}
	if err = reader.Close(); err != nil {
		t.Fatalf("Close(reader) = %v, want nil", err)
	}
	return buf.String()
}

func testJobsSupervisor(t *testing.T) *supervisor.Supervisor {
	t.Helper()
	base := jobsTempDir(t)
	keyPath := filepath.Join(base, "seed.hex")
	seed := bytes.Repeat([]byte{4}, 32)
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(seed)), 0o600); err != nil {
		t.Fatalf("WriteFile(key) = %v, want nil", err)
	}
	cfg := config.Default()
	cfg.EventLog.Dir = filepath.Join(base, "state")
	cfg.EventLog.SigningKeyPath = keyPath
	cfg.Jobs = []config.JobConfig{{
		Name:     "idle",
		Command:  []string{"sleep", "30"},
		LogPath:  filepath.Join(base, "idle.log"),
		Schedule: "always",
		Sandbox:  config.SandboxConfig{Mode: "off", Reason: "command dispatch unit test"},
	}}
	if err := os.MkdirAll(filepath.Join(cfg.EventLog.Dir, ".git"), 0o700); err != nil {
		t.Fatalf("MkdirAll(state/.git) = %v, want nil", err)
	}
	sup, err := supervisor.New(cfg, supervisor.Options{
		Clock:       clock.NewFake(),
		ShimCommand: []string{os.Args[0], "-test.run=TestHelperProcessShim", "--"},
	})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	t.Setenv("TRIBUNUS_TEST_SHIM", "1")
	return sup
}

func writeJobsConfig(t *testing.T, keyMode string) string {
	t.Helper()
	base := jobsTempDir(t)
	keyPath := filepath.Join(base, "seed.hex")
	if keyMode == "valid-key" {
		seed := bytes.Repeat([]byte{5}, 32)
		if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(seed)), 0o600); err != nil {
			t.Fatalf("WriteFile(key) = %v, want nil", err)
		}
	}
	path := filepath.Join(base, "config.json")
	stateDir := filepath.Join(base, "state")
	if err := os.MkdirAll(filepath.Join(stateDir, ".git"), 0o700); err != nil {
		t.Fatalf("MkdirAll(state/.git) = %v, want nil", err)
	}
	body := `{"event_log":{"dir":"` + stateDir + `","signing_key_path":"` + keyPath + `"},"jobs":[{"name":"idle","command":["/bin/true"],"log_path":"` + filepath.Join(base, "idle.log") + `","sandbox":{"mode":"off","reason":"jobs cli unit test"}}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(config) = %v, want nil", err)
	}
	return path
}

func jobsTempDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("GOCACHE")
	if root == "" {
		root = t.TempDir()
	}
	base, err := os.MkdirTemp(root, "tribunus-jobs-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp(%s) = %v, want nil", root, err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Fatalf("RemoveAll(%s) = %v, want nil", base, err)
		}
	})
	return base
}

func argsAfterDash(args []string) []string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return args[i+1:]
		}
	}
	return args
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
