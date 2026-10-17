package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/logsetup"
	"github.com/tekmora/epitrop/internal/mcp"
	"github.com/tekmora/epitrop/internal/supervisor"
	"github.com/tekmora/epitrop/internal/units"
)

const unitID = "tekmora/epitrop#35"

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// writeConfig writes a signing key and a config with one job that never runs, and returns
// the config path.
func writeConfig(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	keyPath := filepath.Join(base, "seed.hex")
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(bytes.Repeat([]byte{9}, 32))), 0o600); err != nil {
		t.Fatalf("WriteFile(key) = %v", err)
	}
	cfgPath := filepath.Join(base, "config.json")
	body := fmt.Sprintf(`{"event_log":{"dir":%s,"signing_key_path":%s},"jobs":[{"name":"idle","command":["sleep","30"],"log_path":%s,"schedule":"always","sandbox":{"mode":"off","reason":"server test"}}]}`,
		jsonQuote(filepath.Join(base, "state")), jsonQuote(keyPath), jsonQuote(filepath.Join(base, "idle.log")))
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(config) = %v", err)
	}
	return cfgPath
}

// recordUnit writes one unit through the same event log the server reads.
func recordUnit(t *testing.T, cfgPath string) {
	t.Helper()
	cfg, err := config.Load(testContext(t), cfgPath)
	if err != nil {
		t.Fatalf("config.Load() = %v", err)
	}
	svc, err := logsetup.UnitsService(cfg)
	if err != nil {
		t.Fatalf("UnitsService() = %v", err)
	}
	branch := "feat/35-mcp-readonly"
	if _, err := svc.Record(testContext(t), unitID, units.RecordPayload{Status: "implementing", Branch: &branch}); err != nil {
		t.Fatalf("Record() = %v", err)
	}
}

// stdioClient runs serve on pipes and connects a client over the same line framing a
// client process uses on the server's stdin and stdout. It returns the session and a
// function that ends the client and returns serve's error.
func stdioClient(t *testing.T, args []string) (*sdkmcp.ClientSession, func() error) {
	t.Helper()
	ctx := testContext(t)
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, args, serverIn, serverOut) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, &sdkmcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatalf("client Connect() = %v", err)
	}
	return cs, func() error {
		if err := cs.Close(); err != nil {
			return fmt.Errorf("client Close: %w", err)
		}
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func structured(t *testing.T, res *sdkmcp.CallToolResult, out any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error %+v, want a result", res.Content)
	}
	body, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func TestServeReadsBackOverStdio(t *testing.T) {
	cfgPath := writeConfig(t)
	recordUnit(t, cfgPath)
	cs, stop := stdioClient(t, []string{"--config=" + cfgPath})
	ctx := testContext(t)

	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: mcp.ToolUnitStatus, Arguments: map[string]any{"id": unitID}})
	if err != nil {
		t.Fatalf("CallTool(unit_status) = %v", err)
	}
	var unit mcp.UnitStatusOut
	structured(t, res, &unit)
	if unit.ID != unitID || unit.Status != "implementing" || unit.Branch != "feat/35-mcp-readonly" {
		t.Fatalf("unit_status = %+v, want the recorded unit", unit)
	}

	res, err = cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: mcp.ToolJobsStatus, Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool(jobs_status) = %v", err)
	}
	checkJobs(t, res)

	if err := stop(); err != nil {
		t.Fatalf("serve after client close = %v, want nil", err)
	}
}

// checkJobs reads the configured job back where the platform has a supervisor, and the
// reason where it has none.
func checkJobs(t *testing.T, res *sdkmcp.CallToolResult) {
	t.Helper()
	if runtime.GOOS != "linux" {
		if !res.IsError {
			t.Fatalf("jobs_status on %s = %+v, want the not-supported tool error", runtime.GOOS, res)
		}
		return
	}
	var jobs mcp.JobsStatusOut
	structured(t, res, &jobs)
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].Name != "idle" || jobs.Jobs[0].State != "dead" || jobs.Jobs[0].Sandbox != "off: server test" {
		t.Fatalf("jobs_status = %+v, want the never-started idle job dead with its sandbox", jobs)
	}
}

func TestServeRefusesBadArguments(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.json")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no config", nil, "--config is required"},
		{"extra argument", []string{"--config=" + missing, "extra"}, `unexpected argument "extra"`},
		{"unknown flag", []string{"--listen=:1"}, "flag provided but not defined"},
		{"absent config", []string{"--config=" + missing}, "absent.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newServer(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("newServer(%q) = %v, want an error containing %q", tc.args, err, tc.want)
			}
		})
	}
}

func TestServeEndsWithContext(t *testing.T) {
	cfgPath := writeConfig(t)
	ctx, cancel := context.WithCancel(testContext(t))
	serverIn, clientOut := io.Pipe()
	_, serverOut := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, []string{"--config=" + cfgPath}, serverIn, serverOut) }()
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("serve after cancel = %v, want nil", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not return after its context ended")
	}
	if err := clientOut.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestJobListerFallsBackOnlyWhenNotSupported(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantStart bool
	}{
		{"not supported", fmt.Errorf("%w: no flock", supervisor.ErrNotSupported), true},
		{"other error", errors.New("supervisor: job \"idle\": bad stop signal"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := newSupervisor
			t.Cleanup(func() { newSupervisor = orig })
			newSupervisor = func(config.Config, supervisor.Options) (*supervisor.Supervisor, error) { return nil, tc.err }
			jobs, err := jobLister(config.Default())
			if !tc.wantStart {
				if err == nil || !errors.Is(err, tc.err) {
					t.Fatalf("jobLister() = %v, %v; want the error %v", jobs, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("jobLister() = %v, want nil", err)
			}
			if _, statusErr := jobs.Status(testContext(t), ""); !errors.Is(statusErr, supervisor.ErrNotSupported) {
				t.Fatalf("Status() = %v, want ErrNotSupported", statusErr)
			}
		})
	}
}
