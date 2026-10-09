package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cordanaLLM/tribunus/catalog"
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
	if _, err := selectSources("codex-local,codex-local"); err == nil {
		t.Fatal("selectSources() = nil error, want failure for duplicate source")
	}
	if _, err := selectSources("codex-local,litellm-gateway,ollama-local,public-catalog,codex-local"); err == nil {
		t.Fatal("selectSources() = nil error, want failure beyond source bound")
	}
}

func TestSelectedRecordCap_Boundary(t *testing.T) {
	total, err := selectedRecordCap(allSourceNames)
	if err != nil {
		t.Fatalf("selectedRecordCap() = %v", err)
	}
	if total > catalog.MaxSnapshotRecords {
		t.Fatalf("selected source cap = %d, want <= %d", total, catalog.MaxSnapshotRecords)
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
		_, _ = w.Write([]byte(`{"data":[{"id":"vendor-a/model-1","owned_by":"vendor-a"}]}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer gateway.Close()

	openrouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"vendor-a/model-2","pricing":{"prompt":"0.00003","completion":"0.00006"}}]}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer openrouter.Close()

	litellmPrices := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model-2":{"input_cost_per_token":0.00003,"litellm_provider":"vendor-a"}}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
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
	if !bytes.Contains(data, []byte(`"schema_version": 1`)) {
		t.Fatalf("snapshot JSON missing schema_version 1: %s", data)
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

// TestRunSync_GatewayUnsetBase_ZeroRequests asserts that when --litellm-base is
// unset, litellm-gateway is skipped and any gateway server receives zero requests.
func TestRunSync_GatewayUnsetBase_ZeroRequests(t *testing.T) {
	var requestCount int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatalf("setup token: %v", err)
	}

	out := filepath.Join(t.TempDir(), "out.json")
	err := runSync([]string{
		"--sources=litellm-gateway",
		"--litellm-token-file=" + tokenFile,
		"--out=" + out,
	})
	if err != nil {
		t.Fatalf("runSync() = %v, want ok with skipped source", err)
	}
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("httptest server received %d requests, want 0", got)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	snap, err := catalog.ParseSnapshot(data)
	if err != nil {
		t.Fatalf("ParseSnapshot() = %v", err)
	}
	if len(snap.SourceRuns) != 1 || snap.SourceRuns[0].Status != catalog.StatusSkip {
		t.Fatalf("SourceRuns = %+v, want one skipped run", snap.SourceRuns)
	}
	if !strings.Contains(snap.SourceRuns[0].Detail, "--litellm-base") {
		t.Fatalf("Detail = %q, want mention of --litellm-base", snap.SourceRuns[0].Detail)
	}
}

func TestRunSync_RejectsInvalidRecordsAndWritesRest(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"","owned_by":"vendor-a"},{"id":"vendor-a/model-1","owned_by":"vendor-a"}]}`)) //nolint:errcheck // test server response
	}))
	defer gateway.Close()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatalf("setup token: %v", err)
	}

	out := filepath.Join(t.TempDir(), "out.json")
	err := runSync([]string{
		"--sources=litellm-gateway",
		"--litellm-base=" + gateway.URL,
		"--litellm-token-file=" + tokenFile,
		"--out=" + out,
	})
	if err != nil {
		t.Fatalf("runSync() = %v, want ok with invalid record rejected", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	snap, err := catalog.ParseSnapshot(data)
	if err != nil {
		t.Fatalf("ParseSnapshot() = %v", err)
	}
	if len(snap.Records) != 1 || snap.Records[0].ModelID != "vendor-a/model-1" {
		t.Fatalf("Records = %+v, want only the valid record", snap.Records)
	}
	if len(snap.SourceRuns) != 1 || !strings.Contains(snap.SourceRuns[0].Detail, "rejected=1") {
		t.Fatalf("SourceRuns = %+v, want rejected=1 detail", snap.SourceRuns)
	}
	if snap.SourceRuns[0].Status != catalog.StatusDegraded {
		t.Fatalf("Status = %v, want degraded after rejecting one invalid record", snap.SourceRuns[0].Status)
	}
}

func TestRunSync_ErrorsWhenEverySourceFails(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.json")
	err := runSync([]string{
		"--sources=public-catalog",
		"--openrouter-url=http://127.0.0.1:1",
		"--litellm-prices-url=http://127.0.0.1:1",
		"--out=" + out,
	})
	if err == nil {
		t.Fatal("runSync() = nil, want error when every source fails")
	}
	if !strings.Contains(err.Error(), "every selected source failed") {
		t.Fatalf("error = %q, want every selected source failed", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("stat output = %v, want no snapshot written", statErr)
	}
}

func TestRejectInvalidRecords_Boundary(t *testing.T) {
	run := sourceOutcome{
		Records: []catalog.Record{
			{
				ModelID:    "",
				AccessPath: catalog.AccessGateway,
				Provenance: catalog.Provenance{
					Source:    "test-source",
					FetchedAt: time.Unix(1, 0).UTC(),
					Kind:      catalog.KindMeasured,
				},
			},
			{
				ModelID:    "vendor-a/model-1",
				AccessPath: catalog.AccessGateway,
				Provenance: catalog.Provenance{
					Source:    "test-source",
					FetchedAt: time.Unix(1, 0).UTC(),
					Kind:      catalog.KindMeasured,
				},
			},
		},
		Status: catalog.StatusOK,
		Count:  2,
	}

	got := rejectInvalidRecords(run)
	if got.Status != catalog.StatusDegraded {
		t.Fatalf("Status = %v, want degraded with one valid record remaining", got.Status)
	}
	if got.Count != 1 || len(got.Records) != 1 || got.Records[0].ModelID != "vendor-a/model-1" {
		t.Fatalf("outcome = %+v, want only the valid record", got)
	}
	if !strings.Contains(got.Detail, "rejected=1") {
		t.Fatalf("Detail = %q, want rejected=1", got.Detail)
	}
}

func TestRejectInvalidRecords_DetailIsBounded(t *testing.T) {
	run := sourceOutcome{Status: catalog.StatusOK}
	for i := 0; i < 10; i++ {
		run.Records = append(run.Records, catalog.Record{
			AccessPath: catalog.AccessGateway,
			Provenance: catalog.Provenance{Source: "test-source", FetchedAt: time.Unix(1, 0).UTC(), Kind: catalog.KindMeasured},
		})
	}
	run.Count = len(run.Records)

	got := rejectInvalidRecords(run)
	if n := strings.Count(got.Detail, "rejected record"); n > 3 {
		t.Fatalf("Detail names %d rejected records, want at most 3: %q", n, got.Detail)
	}
	if !strings.Contains(got.Detail, "rejected=10") {
		t.Fatalf("Detail = %q, want the full count rejected=10", got.Detail)
	}
}
