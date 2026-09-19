package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cordanaLLM/praetor/tribunus/catalog"
)

func TestSelectSources_Positive(t *testing.T) {
	got, err := selectSources("codex-local, ollama-local")
	if err != nil {
		t.Fatalf("selectSources() = %v", err)
	}
	if len(got) != 2 || got[0] != "codex-local" || got[1] != "ollama-local" {
		t.Fatalf("selectSources() = %v", got)
	}
}

func TestSelectSources_Negative(t *testing.T) {
	if _, err := selectSources("not-a-real-source"); err == nil {
		t.Fatal("selectSources() = nil error, want failure for an unknown source name")
	}
}

func TestSelectSources_Boundary(t *testing.T) {
	if _, err := selectSources(""); err == nil {
		t.Fatal("selectSources() = nil error, want failure for an empty --sources value")
	}
	if _, err := selectSources(" , , "); err == nil {
		t.Fatal("selectSources() = nil error, want failure when every field is blank")
	}
}

func TestWriteSnapshotFile_Positive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "snapshot.json")
	if err := writeSnapshotFile(path, []byte(`{}`)); err != nil {
		t.Fatalf("writeSnapshotFile() = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat written file: %v", err)
	}
}

func TestWriteSnapshotFile_Negative(t *testing.T) {
	// A parent path that is itself a regular file cannot become a directory.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := writeSnapshotFile(filepath.Join(blocker, "snapshot.json"), []byte(`{}`))
	if err == nil {
		t.Fatal("writeSnapshotFile() = nil error, want failure when a parent path component is a file")
	}
}

const fixtureRateLimitsLine = `{"timestamp":"2026-09-13T12:56:47.753Z","ordinal":1,"type":"event_msg","payload":{"rate_limits":{"limit_id":"codex","primary":{"used_percent":10.0,"window_minutes":10080,"resets_at":1789858494},"plan_type":"pro"}}}`

// TestRunSync_Positive wires every source to a local fixture (httptest
// servers and a temp sessions directory) and confirms the written snapshot
// round-trips with the expected total record count.
func TestRunSync_Positive(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"local-model","details":{"context_length":4096}}]}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[]}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
		}
	}))
	defer ollama.Close()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"cordana/auto","owned_by":"openai"}]}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer gateway.Close()

	openrouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"openai/gpt-4","pricing":{"prompt":"0.00003","completion":"0.00006"}}]}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer openrouter.Close()

	litellmPrices := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"gpt-4":{"input_cost_per_token":0.00003,"litellm_provider":"openai"}}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer litellmPrices.Close()

	sessionsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sessionsDir, "session.jsonl"), []byte(fixtureRateLimitsLine), 0o600); err != nil {
		t.Fatalf("setup fixture: %v", err)
	}

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatalf("setup token: %v", err)
	}

	out := filepath.Join(t.TempDir(), "out.json")
	args := []string{
		"--sessions-dir=" + sessionsDir,
		"--ollama=" + ollama.URL,
		"--litellm-base=" + gateway.URL,
		"--litellm-token-file=" + tokenFile,
		"--openrouter-url=" + openrouter.URL,
		"--litellm-prices-url=" + litellmPrices.URL,
		"--out=" + out,
	}
	if err := runSync(args); err != nil {
		t.Fatalf("runSync() = %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	snap, err := catalog.ParseSnapshot(data)
	if err != nil {
		t.Fatalf("ParseSnapshot() = %v", err)
	}
	// codex-local(1) + litellm-gateway(1) + ollama-local(1) + public-catalog(2)
	if len(snap.Records) != 5 {
		t.Fatalf("got %d records, want 5: %+v", len(snap.Records), snap.Records)
	}
	if len(snap.SourceRuns) != 4 {
		t.Fatalf("got %d source runs, want 4", len(snap.SourceRuns))
	}
	for _, run := range snap.SourceRuns {
		if run.Status != catalog.StatusOK {
			t.Fatalf("source %s status = %v, detail = %q, want ok", run.Source, run.Status, run.Detail)
		}
	}
}

func TestRunSync_Negative(t *testing.T) {
	err := runSync([]string{"--sources=bogus-source"})
	if err == nil {
		t.Fatal("runSync() = nil error, want failure for an unknown source")
	}
}

// TestRunSync_Boundary confirms a source missing its required flags degrades
// to a skip line rather than aborting the whole sync.
func TestRunSync_Boundary(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.json")
	err := runSync([]string{
		"--sources=litellm-gateway",
		"--out=" + out,
	})
	if err != nil {
		t.Fatalf("runSync() = %v, want a completed run with a skipped source", err)
	}
	data, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("read snapshot: %v", readErr)
	}
	snap, parseErr := catalog.ParseSnapshot(data)
	if parseErr != nil {
		t.Fatalf("ParseSnapshot() = %v", parseErr)
	}
	if len(snap.SourceRuns) != 1 || snap.SourceRuns[0].Status != catalog.StatusSkip {
		t.Fatalf("SourceRuns = %+v, want one skipped litellm-gateway run", snap.SourceRuns)
	}
	if !strings.Contains(snap.SourceRuns[0].Detail, "--litellm-base") {
		t.Fatalf("Detail = %q, want it to name the missing flags", snap.SourceRuns[0].Detail)
	}
}
