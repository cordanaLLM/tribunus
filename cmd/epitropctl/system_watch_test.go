package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tekmora/epitrop/internal/releasewatch"
)

func writeSystemWatchConfig(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	cfg := map[string]any{
		"release_watch": map[string]any{
			"state_dir":        stateDir,
			"rate_limit_floor": 10,
			"routes": []any{
				map[string]any{
					"name":    "system-test-route",
					"sources": []any{map[string]any{"github": "owner/upstream"}},
					"sinks":   []any{map[string]any{"ntfy": map[string]any{"topic": "system-test-topic"}}},
				},
			},
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal(watch config) = %v", err)
	}
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile(watch config) = %v", err)
	}
	return cfgPath, stateDir
}

const v1ReleaseJSON = `[{"tag_name":"v1.0.0","html_url":"https://github.com/owner/upstream/releases/tag/v1.0.0","body":"v1 notes","published_at":"2026-10-01T12:00:00Z"}]`
const v2ReleasesJSON = `[{"tag_name":"v1.1.0","html_url":"https://github.com/owner/upstream/releases/tag/v1.1.0","body":"v2 notes","published_at":"2026-10-02T12:00:00Z"},{"tag_name":"v1.0.0","html_url":"https://github.com/owner/upstream/releases/tag/v1.0.0","body":"v1 notes","published_at":"2026-10-01T12:00:00Z"}]`

func newSystemWatchStandIns(t *testing.T, addRelease *atomic.Int32, ntfyDeliveries *atomic.Int32) (*httptest.Server, *httptest.Server) {
	t.Helper()
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/owner/upstream/releases" {
			if addRelease.Load() == 1 {
				_, _ = w.Write([]byte(v2ReleasesJSON)) //nolint:errcheck // test httptest handler
			} else {
				_, _ = w.Write([]byte(v1ReleaseJSON)) //nolint:errcheck // test httptest handler
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	ntfyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "system-test-topic") {
			ntfyDeliveries.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(func() {
		ghServer.Close()
		ntfyServer.Close()
	})
	return ghServer, ntfyServer
}

func systemWatchSeed(t *testing.T, bin, cfgPath, ghURL, ntfyURL, stateDir string, ntfyDeliveries *atomic.Int32) {
	t.Helper()
	stdout, stderr, code, err := runSystemCLI(t, bin, "watch", "seed",
		"--config="+cfgPath, "--github-base="+ghURL, "--ntfy-base="+ntfyURL)
	if err != nil || code != 0 {
		t.Fatalf("watch seed code = %d, err = %v: %s\n%s", code, err, stdout, stderr)
	}
	if ntfyDeliveries.Load() != 0 {
		t.Fatalf("ntfy deliveries during seed = %d, want 0", ntfyDeliveries.Load())
	}
	state, err := releasewatch.LoadState(stateDir)
	if err != nil || state.Count() != 1 || !state.Has("github:owner/upstream@v1.0.0") {
		t.Fatalf("LoadState() after seed = %v, count = %d, want 1 seeded release", err, state.Count())
	}
}

func systemWatchRunOnce(t *testing.T, bin, cfgPath, ghURL, ntfyURL, stateDir string, ntfyDeliveries *atomic.Int32) {
	t.Helper()
	stdout, stderr, code, err := runSystemCLI(t, bin, "watch", "run",
		"--config="+cfgPath, "--once", "--github-base="+ghURL, "--ntfy-base="+ntfyURL)
	if err != nil || code != 0 {
		t.Fatalf("watch run code = %d, err = %v: %s\n%s", code, err, stdout, stderr)
	}
	if ntfyDeliveries.Load() != 1 {
		t.Fatalf("ntfy deliveries after run = %d, want 1", ntfyDeliveries.Load())
	}
	state, err := releasewatch.LoadState(stateDir)
	if err != nil || state.Count() != 2 || !state.Has("github:owner/upstream@v1.1.0") {
		t.Fatalf("LoadState() after run = %v, count = %d, want 2 releases recorded", err, state.Count())
	}
}

func TestSystemWatch_Positive(t *testing.T) {
	bin := buildCLI(t)
	cfgPath, stateDir := writeSystemWatchConfig(t)
	var addRelease atomic.Int32
	var ntfyDeliveries atomic.Int32
	ghServer, ntfyServer := newSystemWatchStandIns(t, &addRelease, &ntfyDeliveries)

	systemWatchSeed(t, bin, cfgPath, ghServer.URL, ntfyServer.URL, stateDir, &ntfyDeliveries)
	addRelease.Store(1)
	systemWatchRunOnce(t, bin, cfgPath, ghServer.URL, ntfyServer.URL, stateDir, &ntfyDeliveries)
}

func checkWatch500Negative(t *testing.T, bin, cfgPath, stateDir string) {
	t.Helper()
	gh500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer gh500.Close()

	stateBefore, err := releasewatch.LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState() before 500 = %v", err)
	}
	countBefore := stateBefore.Count()

	_, stderr500, code500, err500 := runSystemCLI(t, bin, "watch", "run",
		"--config="+cfgPath, "--once", "--github-base="+gh500.URL)
	if err500 == nil || code500 == 0 {
		t.Fatalf("watch run 500 code = %d, err = %v, want non-zero exit; stderr = %s", code500, err500, stderr500)
	}
	stateAfter, err := releasewatch.LoadState(stateDir)
	if err != nil || stateAfter.Count() != countBefore {
		t.Fatalf("state changed on 500 error: count was %d, now %d (err: %v)", countBefore, stateAfter.Count(), err)
	}
}

func checkWatchBadURLNegative(t *testing.T, bin, cfgPath string) {
	t.Helper()
	_, stderrBadURL, codeBadURL, errBadURL := runSystemCLI(t, bin, "watch", "seed",
		"--config="+cfgPath, "--github-base=not-a-valid-url")
	if errBadURL == nil || codeBadURL == 0 {
		t.Fatalf("watch seed bad URL code = %d, want non-zero exit", codeBadURL)
	}
	if !strings.Contains(stderrBadURL, "must be an absolute http(s) URL") {
		t.Fatalf("stderr = %q, want invalid URL error", stderrBadURL)
	}
}

func checkWatchFlagCases(t *testing.T, bin string) {
	t.Helper()
	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"missing command", []string{"watch"}, 1, "watch command required"},
		{"unknown command", []string{"watch", "bogus"}, 1, "unknown watch command: bogus"},
		{"run missing config flag", []string{"watch", "run"}, 1, "watch run: --config is required"},
		{"run bad flag exit 2", []string{"watch", "run", "--bad-flag"}, 2, "flag provided but not defined"},
	}
	for i := 0; i < len(cases); i++ {
		c := cases[i]
		t.Run(c.name, func(t *testing.T) {
			_, stderr, code, err := runSystemCLI(t, bin, c.args...)
			if err == nil || code != c.code {
				t.Fatalf("%s code = %d, err = %v, want %d", c.name, code, err, c.code)
			}
			if !strings.Contains(stderr, c.want) {
				t.Fatalf("stderr = %q, want %q", stderr, c.want)
			}
		})
	}
}

func TestSystemWatch_Negative(t *testing.T) {
	bin := buildCLI(t)
	cfgPath, stateDir := writeSystemWatchConfig(t)

	t.Run("stand-in 500 error", func(t *testing.T) {
		checkWatch500Negative(t, bin, cfgPath, stateDir)
	})
	t.Run("invalid base URL", func(t *testing.T) {
		checkWatchBadURLNegative(t, bin, cfgPath)
	})
	checkWatchFlagCases(t, bin)
}
