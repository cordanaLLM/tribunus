package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func standInServers(t *testing.T) (*httptest.Server, *httptest.Server, *httptest.Server, *httptest.Server) {
	t.Helper()
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"local-model","details":{"context_length":4096}}]}`)) //nolint:errcheck // test httptest handler
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[]}`)) //nolint:errcheck // test httptest handler
		}
	}))
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"vendor-a/model-1","owned_by":"vendor-a"}]}`)) //nolint:errcheck // test httptest handler
	}))
	openrouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"vendor-a/model-2","pricing":{"prompt":"0.00003","completion":"0.00006"}}]}`)) //nolint:errcheck // test httptest handler
	}))
	litellmPrices := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model-2":{"input_cost_per_token":0.00003,"litellm_provider":"vendor-a"}}`)) //nolint:errcheck // test httptest handler
	}))
	t.Cleanup(func() {
		ollama.Close()
		gateway.Close()
		openrouter.Close()
		litellmPrices.Close()
	})
	return ollama, gateway, openrouter, litellmPrices
}

func TestSystemSyncAndShow_Positive(t *testing.T) {
	bin := buildCLI(t)
	ollama, gateway, openrouter, litellmPrices := standInServers(t)
	sessionsDir, tokenFile := setupSyncFixtures(t)
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")

	args := []string{
		"sync",
		"--sources=codex-local,litellm-gateway,ollama-local,public-catalog",
		"--sessions-dir=" + sessionsDir,
		"--ollama=" + ollama.URL,
		"--litellm-base=" + gateway.URL,
		"--litellm-token-file=" + tokenFile,
		"--openrouter-url=" + openrouter.URL,
		"--litellm-prices-url=" + litellmPrices.URL,
		"--out=" + snapshotPath,
	}

	stdout, stderr, code, err := runSystemCLI(t, bin, args...)
	if err != nil || code != 0 {
		t.Fatalf("sync exited with code %d, err %v: %s\n%s", code, err, stdout, stderr)
	}
	wantSync := strings.Join([]string{
		"codex-local      ok   count=1",
		"litellm-gateway  ok   count=1",
		"ollama-local     ok   count=1",
		"public-catalog   ok   count=2 openrouter: ok (1); litellm-prices: ok (1)",
		"wrote 5 records to " + snapshotPath,
		"",
	}, "\n")
	if strings.ReplaceAll(stdout, "\r\n", "\n") != wantSync {
		t.Fatalf("sync stdout =\n%s\nwant:\n%s", stdout, wantSync)
	}
	checkSnapshotExists(t, snapshotPath)

	stdoutShow, stderrShow, codeShow, errShow := runSystemCLI(t, bin, "show", "--in="+snapshotPath)
	if errShow != nil || codeShow != 0 {
		t.Fatalf("show exited with code %d, err %v: %s\n%s", codeShow, errShow, stdoutShow, stderrShow)
	}
	wantShow := strings.Join([]string{
		"MODEL             ACCESS            CTX   IN/M  OUT/M  SOURCE                         KIND",
		"codex-cli/codex   subscription_cli  -     -     -      codex-local                    measured",
		"vendor-a/model-1  gateway           -     -     -      litellm-gateway                measured",
		"local-model       local             4096  -     -      ollama-local                   measured",
		"vendor-a/model-2  api               -     30    60     public-catalog:openrouter      declared",
		"model-2           api               -     30    -      public-catalog:litellm-prices  declared",
		"",
		"5 records, generated <TIMESTAMP>",
		"  codex-local      ok   count=1",
		"  litellm-gateway  ok   count=1",
		"  ollama-local     ok   count=1",
		"  public-catalog   ok   count=2 openrouter: ok (1); litellm-prices: ok (1)",
		"",
	}, "\n")

	reTimestamp := regexp.MustCompile(`generated \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[^\n]*`)
	normalizedShow := reTimestamp.ReplaceAllString(strings.ReplaceAll(stdoutShow, "\r\n", "\n"), "generated <TIMESTAMP>")
	if normalizedShow != wantShow {
		t.Fatalf("show stdout =\n%s\nwant:\n%s", normalizedShow, wantShow)
	}
}

func checkSnapshotExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("snapshot file is empty")
	}
}

func TestSystemSyncAndShow_Negative(t *testing.T) {
	bin := buildCLI(t)
	out := filepath.Join(t.TempDir(), "should-not-exist.json")

	t.Run("sync unknown source", func(t *testing.T) {
		_, stderr, code, err := runSystemCLI(t, bin, "sync", "--sources=bogus-source", "--out="+out)
		if err == nil || code != 1 {
			t.Fatalf("sync bogus code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "unknown source") {
			t.Fatalf("sync bogus stderr = %q, want unknown source error", stderr)
		}
		if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
			t.Fatalf("snapshot file was created on failure: %v", statErr)
		}
	})

	t.Run("show missing file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nonexistent.json")
		_, stderr, code, err := runSystemCLI(t, bin, "show", "--in="+missing)
		if err == nil || code != 1 {
			t.Fatalf("show missing code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "open snapshot") {
			t.Fatalf("show missing stderr = %q, want open snapshot error", stderr)
		}
	})
}
