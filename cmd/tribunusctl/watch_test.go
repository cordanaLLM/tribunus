package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
	"github.com/cordanaLLM/tribunus/internal/releasewatch"
)

func TestRunWatchRejectsMissingAndUnknownCommand(t *testing.T) {
	if err := runWatch(nil); err == nil || !strings.Contains(err.Error(), "watch command required") {
		t.Fatalf("runWatch(nil) = %v, want command required", err)
	}
	if err := runWatch([]string{"bogus"}); err == nil || !strings.Contains(err.Error(), "unknown watch command: bogus") {
		t.Fatalf("runWatch(bogus) = %v, want unknown command", err)
	}
}

func TestRunWatchRequiresConfig(t *testing.T) {
	for _, action := range []string{"run", "seed"} {
		t.Run(action, func(t *testing.T) {
			err := runWatch([]string{action})
			if err == nil || !strings.Contains(err.Error(), "--config is required") {
				t.Fatalf("runWatch(%s) = %v, want config required", action, err)
			}
		})
	}
}

func TestRunWatchActionBadFlagsExit2(t *testing.T) {
	for _, action := range []string{"run", "seed"} {
		t.Run(action, func(t *testing.T) {
			err := runWatchAction(action, []string{"-bad-flag"})
			if err == nil || !strings.Contains(err.Error(), "flag provided") {
				t.Fatalf("runWatchAction(%s, -bad-flag) = %v, want flag error", action, err)
			}
			var ec interface{ ExitCode() int }
			if !errors.As(err, &ec) || ec.ExitCode() != 2 {
				t.Fatalf("runWatchAction(%s) error exit code = %v, want 2", action, ec)
			}
		})
	}
}

func TestCLIWatchBadFlagsExit2Subprocess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	bin := filepath.Join(t.TempDir(), "tribunusctl")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build tribunusctl: %v\n%s", err, out)
	}

	cmd := exec.CommandContext(ctx, bin, "watch", "run", "--bad-flag")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatal("tribunusctl watch run --bad-flag = nil error, want exit 2")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("exit code = %v, want 2; stderr = %s", err, stderr.String())
	}
}

func TestRunWatchActionDispatchRunOnceAndSeed(t *testing.T) {
	stateDir := t.TempDir()
	cfgContent := `{"release_watch": {"state_dir": "` + stateDir + `", "routes": [{"name": "rt", "sources": [{"github": "a/b"}], "sinks": [{"github_issue": {"repo": "c/d"}}]}]}}`
	cfgPath := filepath.Join(stateDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	oldNewService := newWatchService
	t.Cleanup(func() { newWatchService = oldNewService })

	t.Run("dispatch run --once", func(t *testing.T) {
		var calledAction string
		var gotOnce bool
		newWatchService = func(cfg config.Config, opts releasewatch.ClientOptions) (*releasewatch.Service, error) {
			svc, err := releasewatch.NewService(cfg, opts)
			if err != nil {
				return nil, err
			}
			calledAction = "run"
			gotOnce = true
			return svc, errors.New("sentinel-run-dispatched")
		}
		err := runWatchAction("run", []string{"--config", cfgPath, "--once"})
		if err == nil || !strings.Contains(err.Error(), "sentinel-run-dispatched") {
			t.Fatalf("runWatchAction(run --once) = %v, want sentinel", err)
		}
		if calledAction != "run" || !gotOnce {
			t.Fatalf("calledAction=%s, gotOnce=%v", calledAction, gotOnce)
		}
	})

	t.Run("dispatch seed", func(t *testing.T) {
		var calledAction string
		newWatchService = func(cfg config.Config, opts releasewatch.ClientOptions) (*releasewatch.Service, error) {
			svc, err := releasewatch.NewService(cfg, opts)
			if err != nil {
				return nil, err
			}
			calledAction = "seed"
			return svc, errors.New("sentinel-seed-dispatched")
		}
		err := runWatchAction("seed", []string{"--config", cfgPath})
		if err == nil || !strings.Contains(err.Error(), "sentinel-seed-dispatched") {
			t.Fatalf("runWatchAction(seed) = %v, want sentinel", err)
		}
		if calledAction != "seed" {
			t.Fatalf("calledAction=%s, want seed", calledAction)
		}
	})
}
