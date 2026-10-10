package releasewatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
)

func testWrite(w http.ResponseWriter, s string) {
	_, _ = w.Write([]byte(s)) //nolint:errcheck // test mock response write
}

func TestPlantedReleaseFilingAndSeenSetSecondPass(t *testing.T) {
	var postCount int32
	var postedBody string
	var postedTitle string

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases"):
			w.Header().Set("Content-Type", "application/json")
			testWrite(w, `[{"tag_name":"v1.0.0","html_url":"https://github.com/owner/upstream/releases/tag/v1.0.0","body":"notes","published_at":"2026-10-01T12:00:00Z"}]`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues"):
			w.Header().Set("Content-Type", "application/json")
			testWrite(w, `[]`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues"):
			atomic.AddInt32(&postCount, 1)
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var payload GitHubIssuePayload
			if err := json.Unmarshal(data, &payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			postedTitle = payload.Title
			postedBody = payload.Body
			w.WriteHeader(http.StatusCreated)
			testWrite(w, `{"number":1,"title":"title","body":"body","state":"open"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghServer.Close()

	stateDir := t.TempDir()
	cfg := config.Default()
	cfg.ReleaseWatch = config.ReleaseWatchConfig{
		StateDir:         stateDir,
		RateLimitFloor:   100,
		MaxActionsPerRun: 12,
		PerSourceCap:     3,
		Routes: []config.ReleaseWatchRoute{{
			Name:    "route-a",
			Sources: []config.ReleaseWatchSource{{GitHub: "owner/upstream"}},
			Sinks:   []config.ReleaseWatchSink{{GitHubIssue: &config.GitHubIssueSink{Repo: "owner/consumer"}}},
		}},
	}

	svc, err := NewService(cfg, ClientOptions{
		GitHubBase: ghServer.URL,
		LookupEnv: func(k string) (string, bool) {
			if k == "GITHUB_TOKEN" {
				return "fake-token", true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Pass 1: files issue
	if err = svc.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass 1: %v", err)
	}
	if atomic.LoadInt32(&postCount) != 1 {
		t.Fatalf("postCount = %d, want 1", postCount)
	}
	wantMarker := "<!-- release-watch: github:owner/upstream@v1.0.0 -->"
	if !strings.Contains(postedBody, wantMarker) {
		t.Fatalf("postedBody %q does not contain marker %q", postedBody, wantMarker)
	}
	if postedTitle != "Upstream release: owner/upstream v1.0.0" {
		t.Fatalf("postedTitle = %q", postedTitle)
	}

	// Check seen-set on disk
	diskState, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !diskState.Has("github:owner/upstream@v1.0.0") {
		t.Fatal("seen-set on disk does not contain github:owner/upstream@v1.0.0")
	}

	// Pass 2: seen-set prevents second POST
	if err = svc.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass 2: %v", err)
	}
	if atomic.LoadInt32(&postCount) != 1 {
		t.Fatalf("postCount after pass 2 = %d, want 1", postCount)
	}
}

func TestMarkerAlreadyPresentInExistingIssue(t *testing.T) {
	var postCalled int32
	var stdout bytes.Buffer

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases"):
			w.Header().Set("Content-Type", "application/json")
			testWrite(w, `[{"tag_name":"v1.0.0","html_url":"https://github.com/owner/upstream/releases/tag/v1.0.0","body":"notes","published_at":"2026-10-01T12:00:00Z"}]`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues"):
			w.Header().Set("Content-Type", "application/json")
			// Return closed issue containing marker
			testWrite(w, `[{"number":99,"title":"old","body":"<!-- release-watch: github:owner/upstream@v1.0.0 -->\nclosed","state":"closed"}]`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues"):
			atomic.AddInt32(&postCalled, 1)
			http.Error(w, "should not post", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghServer.Close()

	stateDir := t.TempDir()
	cfg := config.Default()
	cfg.ReleaseWatch = config.ReleaseWatchConfig{
		StateDir:         stateDir,
		RateLimitFloor:   100,
		MaxActionsPerRun: 12,
		PerSourceCap:     3,
		Routes: []config.ReleaseWatchRoute{{
			Name:    "route-a",
			Sources: []config.ReleaseWatchSource{{GitHub: "owner/upstream"}},
			Sinks:   []config.ReleaseWatchSink{{GitHubIssue: &config.GitHubIssueSink{Repo: "owner/consumer"}}},
		}},
	}

	svc, err := NewService(cfg, ClientOptions{
		GitHubBase: ghServer.URL,
		Stdout:     &stdout,
		LookupEnv: func(k string) (string, bool) {
			return "token", true
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err = svc.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	if atomic.LoadInt32(&postCalled) != 0 {
		t.Fatal("POST issues was called despite marker in existing closed issue")
	}
	if !strings.Contains(stdout.String(), "already filed github:owner/upstream@v1.0.0 in owner/consumer") {
		t.Fatalf("stdout %q missing already filed message", stdout.String())
	}
	diskState, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !diskState.Has("github:owner/upstream@v1.0.0") {
		t.Fatal("marker in existing issue was not marked seen on disk")
	}
}

func TestRestartMidPassAtomicSeenSet(t *testing.T) {
	var filedReleases []string
	deliverFailSecond := true

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases"):
			w.Header().Set("Content-Type", "application/json")
			// Return v2.0.0 (newer) and v1.0.0 (older); service orders oldest first
			testWrite(w, `[
				{"tag_name":"v2.0.0","html_url":"https://github.com/owner/upstream/releases/tag/v2.0.0","published_at":"2026-10-02T12:00:00Z"},
				{"tag_name":"v1.0.0","html_url":"https://github.com/owner/upstream/releases/tag/v1.0.0","published_at":"2026-10-01T12:00:00Z"}
			]`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues"):
			w.Header().Set("Content-Type", "application/json")
			testWrite(w, `[]`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues"):
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var p GitHubIssuePayload
			if err := json.Unmarshal(data, &p); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if strings.Contains(p.Body, "v1.0.0") {
				filedReleases = append(filedReleases, "v1.0.0")
				w.WriteHeader(http.StatusCreated)
				testWrite(w, `{"number":1}`)
				return
			}
			if deliverFailSecond {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			filedReleases = append(filedReleases, "v2.0.0")
			w.WriteHeader(http.StatusCreated)
			testWrite(w, `{"number":2}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghServer.Close()

	stateDir := t.TempDir()
	cfg := config.Default()
	cfg.ReleaseWatch = config.ReleaseWatchConfig{
		StateDir:         stateDir,
		RateLimitFloor:   100,
		MaxActionsPerRun: 12,
		PerSourceCap:     5,
		Routes: []config.ReleaseWatchRoute{{
			Name:    "route-a",
			Sources: []config.ReleaseWatchSource{{GitHub: "owner/upstream"}},
			Sinks:   []config.ReleaseWatchSink{{GitHubIssue: &config.GitHubIssueSink{Repo: "owner/consumer"}}},
		}},
	}

	opts := ClientOptions{
		GitHubBase: ghServer.URL,
		LookupEnv:  func(string) (string, bool) { return "token", true },
	}
	svc1, err := NewService(cfg, opts)
	if err != nil {
		t.Fatalf("NewService 1: %v", err)
	}

	// Pass 1: fails on 2nd delivery
	err = svc1.RunPass(context.Background())
	if err == nil {
		t.Fatal("RunPass 1 = nil, want failure for 2nd delivery")
	}

	// Assert 1st is seen on disk, 2nd is not
	diskState, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState 1: %v", err)
	}
	if !diskState.Has("github:owner/upstream@v1.0.0") {
		t.Fatal("expected v1.0.0 in seen set on disk")
	}
	if diskState.Has("github:owner/upstream@v2.0.0") {
		t.Fatal("v2.0.0 should not be in seen set on disk")
	}

	// Pass 2: restart with new service instance reading disk state
	deliverFailSecond = false
	filedReleases = nil
	svc2, err := NewService(cfg, opts)
	if err != nil {
		t.Fatalf("NewService 2: %v", err)
	}
	if err = svc2.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass 2: %v", err)
	}
	if len(filedReleases) != 1 || filedReleases[0] != "v2.0.0" {
		t.Fatalf("filedReleases on rerun = %v, want only [v2.0.0]", filedReleases)
	}
	diskState2, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState 2: %v", err)
	}
	if !diskState2.Has("github:owner/upstream@v2.0.0") {
		t.Fatal("v2.0.0 not recorded in seen set after pass 2")
	}
}

func TestSourceFailureScenariosAndTagsFallback(t *testing.T) {
	t.Run("500 401 and malformed JSON name source and continue others", func(t *testing.T) {
		hfServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			testWrite(w, `[{"id":"GoodOrg/Model1"}]`)
		}))
		defer hfServer.Close()

		for _, statusFail := range []int{500, 401} {
			t.Run(fmt.Sprintf("status_%d", statusFail), func(t *testing.T) {
				ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(statusFail)
				}))
				defer ghServer.Close()

				cfg := config.Default()
				cfg.ReleaseWatch = config.ReleaseWatchConfig{
					StateDir:         t.TempDir(),
					RateLimitFloor:   100,
					MaxActionsPerRun: 12,
					PerSourceCap:     3,
					Routes: []config.ReleaseWatchRoute{{
						Name: "rt",
						Sources: []config.ReleaseWatchSource{
							{GitHub: "fail/repo"},
							{HuggingFaceOrg: "GoodOrg"},
						},
						Sinks: []config.ReleaseWatchSink{{Ntfy: &config.NtfySink{Topic: "top"}}},
					}},
				}
				svc, err := NewService(cfg, ClientOptions{
					GitHubBase:      ghServer.URL,
					HuggingFaceBase: hfServer.URL,
					NtfyBase:        hfServer.URL,
				})
				if err != nil {
					t.Fatalf("NewService: %v", err)
				}
				err = svc.RunPass(context.Background())
				if err == nil || !strings.Contains(err.Error(), "github:fail/repo") {
					t.Fatalf("RunPass error = %v, want naming github:fail/repo", err)
				}
				// GoodOrg model should still have been processed and marked seen
				if !svc.State().Has("hf:GoodOrg/Model1") {
					t.Fatal("GoodOrg was not processed when other source failed")
				}
			})
		}

		t.Run("malformed_json", func(t *testing.T) {
			ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				testWrite(w, `{not valid json`)
			}))
			defer ghServer.Close()

			cfg := config.Default()
			cfg.ReleaseWatch = config.ReleaseWatchConfig{
				StateDir:         t.TempDir(),
				RateLimitFloor:   100,
				MaxActionsPerRun: 12,
				PerSourceCap:     3,
				Routes: []config.ReleaseWatchRoute{{
					Name:    "rt",
					Sources: []config.ReleaseWatchSource{{GitHub: "bad/json"}},
					Sinks:   []config.ReleaseWatchSink{{Ntfy: &config.NtfySink{Topic: "top"}}},
				}},
			}
			svc, err := NewService(cfg, ClientOptions{GitHubBase: ghServer.URL})
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}
			err = svc.RunPass(context.Background())
			if err == nil || !strings.Contains(err.Error(), "github:bad/json") {
				t.Fatalf("RunPass error = %v, want naming bad/json", err)
			}
		})
	})

	t.Run("empty releases list falls back to tags", func(t *testing.T) {
		var tagsCalled bool
		var releaseCalled bool
		ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(r.URL.Path, "/releases") {
				releaseCalled = true
				testWrite(w, `[]`)
				return
			}
			if strings.Contains(r.URL.Path, "/tags") {
				tagsCalled = true
				testWrite(w, `[{"name":"v0.9.0"}]`)
				return
			}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.NotFound(w, r)
		}))
		defer ghServer.Close()

		cfg := config.Default()
		cfg.ReleaseWatch = config.ReleaseWatchConfig{
			StateDir:         t.TempDir(),
			RateLimitFloor:   100,
			MaxActionsPerRun: 12,
			PerSourceCap:     3,
			Routes: []config.ReleaseWatchRoute{{
				Name:    "rt",
				Sources: []config.ReleaseWatchSource{{GitHub: "tags/fallback"}},
				Sinks:   []config.ReleaseWatchSink{{Ntfy: &config.NtfySink{Topic: "top"}}},
			}},
		}
		svc, err := NewService(cfg, ClientOptions{
			GitHubBase: ghServer.URL,
			NtfyBase:   ghServer.URL,
		})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		if err := svc.RunPass(context.Background()); err != nil {
			t.Fatalf("RunPass: %v", err)
		}
		if !releaseCalled || !tagsCalled {
			t.Fatalf("releaseCalled=%v, tagsCalled=%v", releaseCalled, tagsCalled)
		}
		if !svc.State().Has("github:tags/fallback@v0.9.0") {
			t.Fatal("tag was not marked seen")
		}
	})

	t.Run("empty releases and tags is not an error", func(t *testing.T) {
		ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			testWrite(w, `[]`)
		}))
		defer ghServer.Close()

		cfg := config.Default()
		cfg.ReleaseWatch = config.ReleaseWatchConfig{
			StateDir:         t.TempDir(),
			RateLimitFloor:   100,
			MaxActionsPerRun: 12,
			PerSourceCap:     3,
			Routes: []config.ReleaseWatchRoute{{
				Name:    "rt",
				Sources: []config.ReleaseWatchSource{{GitHub: "empty/repo"}},
				Sinks:   []config.ReleaseWatchSink{{Ntfy: &config.NtfySink{Topic: "top"}}},
			}},
		}
		svc, err := NewService(cfg, ClientOptions{GitHubBase: ghServer.URL, NtfyBase: ghServer.URL})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		if err := svc.RunPass(context.Background()); err != nil {
			t.Fatalf("RunPass = %v, want nil for empty valid response", err)
		}
	})
}

func TestDraftAndPrereleaseSkipped(t *testing.T) {
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/releases") {
			testWrite(w, `[
				{"tag_name":"v1.0-draft","draft":true,"prerelease":false},
				{"tag_name":"v1.0-rc1","draft":false,"prerelease":true},
				{"tag_name":"v1.0.0","draft":false,"prerelease":false}
			]`)
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer ghServer.Close()

	cfg := config.Default()
	cfg.ReleaseWatch = config.ReleaseWatchConfig{
		StateDir:         t.TempDir(),
		RateLimitFloor:   100,
		MaxActionsPerRun: 12,
		PerSourceCap:     5,
		Routes: []config.ReleaseWatchRoute{{
			Name:    "rt",
			Sources: []config.ReleaseWatchSource{{GitHub: "owner/repo"}},
			Sinks:   []config.ReleaseWatchSink{{Ntfy: &config.NtfySink{Topic: "top"}}},
		}},
	}
	svc, err := NewService(cfg, ClientOptions{GitHubBase: ghServer.URL, NtfyBase: ghServer.URL})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass: %v", err)
	}

	if svc.State().Has("github:owner/repo@v1.0-draft") {
		t.Fatal("draft release was marked seen")
	}
	if svc.State().Has("github:owner/repo@v1.0-rc1") {
		t.Fatal("prerelease was marked seen")
	}
	if !svc.State().Has("github:owner/repo@v1.0.0") {
		t.Fatal("regular release was not marked seen")
	}
}

func TestRateLimitThrottling(t *testing.T) {
	var ghRequests int32
	var stdout bytes.Buffer

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&ghRequests, 1)
		w.Header().Set("X-RateLimit-Remaining", "42")
		w.Header().Set("X-RateLimit-Reset", "1789858494")
		w.Header().Set("Content-Type", "application/json")
		testWrite(w, `[{"tag_name":"v1.0.0","published_at":"2026-10-01T12:00:00Z"}]`)
	}))
	defer ghServer.Close()

	cfg := config.Default()
	cfg.ReleaseWatch = config.ReleaseWatchConfig{
		StateDir:         t.TempDir(),
		RateLimitFloor:   100, // Remaining 42 is below floor 100
		MaxActionsPerRun: 12,
		PerSourceCap:     5,
		Routes: []config.ReleaseWatchRoute{
			{
				Name:    "rt1",
				Sources: []config.ReleaseWatchSource{{GitHub: "owner/repo1"}},
				Sinks:   []config.ReleaseWatchSink{{GitHubIssue: &config.GitHubIssueSink{Repo: "c/d"}}},
			},
			{
				Name:    "rt2",
				Sources: []config.ReleaseWatchSource{{GitHub: "owner/repo2"}},
				Sinks:   []config.ReleaseWatchSink{{GitHubIssue: &config.GitHubIssueSink{Repo: "c/d"}}},
			},
		},
	}

	svc, err := NewService(cfg, ClientOptions{
		GitHubBase: ghServer.URL,
		Stdout:     &stdout,
		LookupEnv:  func(string) (string, bool) { return "token", true },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	err = svc.RunPass(context.Background())
	if err != nil {
		t.Fatalf("RunPass = %v, want nil error on throttle", err)
	}

	// First request went through, then throttled stopped all further requests
	if got := atomic.LoadInt32(&ghRequests); got != 1 {
		t.Fatalf("ghRequests = %d, want exactly 1 request before throttling", got)
	}
	wantLog := "release-watch: throttled remaining=42 reset="
	if !strings.Contains(stdout.String(), wantLog) {
		t.Fatalf("stdout %q does not contain %q", stdout.String(), wantLog)
	}
}

func TestTokenRuleAndRedirectIsolation(t *testing.T) {
	const secretToken = "super-secret-github-token-12345"

	var ghAuthHeader string
	var hfAuthHeader string
	var ntfyAuthHeader string
	var redirectAuthHeader string

	redirectDestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		testWrite(w, `[]`)
	}))
	defer redirectDestServer.Close()

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ghAuthHeader = r.Header.Get("Authorization")
		if strings.Contains(r.URL.Path, "/redirect-test") {
			http.Redirect(w, r, redirectDestServer.URL, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		testWrite(w, `[]`)
	}))
	defer ghServer.Close()

	hfServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hfAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		testWrite(w, `[{"id":"TestOrg/ModelA"}]`)
	}))
	defer hfServer.Close()

	ntfyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ntfyAuthHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfyServer.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Default()
	cfg.ReleaseWatch = config.ReleaseWatchConfig{
		StateDir:         t.TempDir(),
		RateLimitFloor:   10,
		MaxActionsPerRun: 12,
		PerSourceCap:     3,
		Routes: []config.ReleaseWatchRoute{
			{
				Name: "rt-gh",
				Sources: []config.ReleaseWatchSource{
					{GitHub: "owner/upstream"},
				},
				Sinks: []config.ReleaseWatchSink{{Ntfy: &config.NtfySink{Topic: "topic"}}},
			},
			{
				Name: "rt-hf",
				Sources: []config.ReleaseWatchSource{
					{HuggingFaceOrg: "TestOrg"},
				},
				Sinks: []config.ReleaseWatchSink{{Ntfy: &config.NtfySink{Topic: "topic"}}},
			},
		},
	}

	svc, err := NewService(cfg, ClientOptions{
		GitHubBase:      ghServer.URL,
		HuggingFaceBase: hfServer.URL,
		NtfyBase:        ntfyServer.URL,
		Stdout:          &stdout,
		LookupEnv: func(k string) (string, bool) {
			if k == "GITHUB_TOKEN" {
				return secretToken, true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err = svc.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass: %v", err)
	}

	// 1. Authorization is present on GitHub-base requests
	if ghAuthHeader != "Bearer "+secretToken {
		t.Fatalf("ghAuthHeader = %q, want Bearer %s", ghAuthHeader, secretToken)
	}

	// 2. Authorization is ABSENT on HF requests
	if hfAuthHeader != "" {
		t.Fatalf("hfAuthHeader = %q, want empty", hfAuthHeader)
	}

	// 3. Authorization is ABSENT on ntfy requests
	if ntfyAuthHeader != "" {
		t.Fatalf("ntfyAuthHeader = %q, want empty", ntfyAuthHeader)
	}

	// 4. Cross-host redirect arrives WITHOUT Authorization
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ghServer.URL+"/redirect-test", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	_, _, _, err = svc.client.DoRequest(context.Background(), req, 5*time.Second, 1024)
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if redirectAuthHeader != "" {
		t.Fatalf("redirect to different host leaked Authorization header: %q", redirectAuthHeader)
	}

	// 5. Secret token NEVER appears in captured stdout or stderr
	if strings.Contains(stdout.String(), secretToken) {
		t.Fatalf("stdout leaked secret token: %s", stdout.String())
	}
	if strings.Contains(stderr.String(), secretToken) {
		t.Fatalf("stderr leaked secret token: %s", stderr.String())
	}
}

// TestFilingWithoutGitHubTokenErrors: without a token the watch refuses to file before it
// sends anything, and says why; the release stays unseen.
func TestFilingWithoutGitHubTokenErrors(t *testing.T) {
	var posts int32
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			atomic.AddInt32(&posts, 1)
			w.WriteHeader(http.StatusCreated)
			testWrite(w, `{"number":1}`)
		case strings.Contains(r.URL.Path, "/releases"):
			testWrite(w, `[{"tag_name":"v1.0.0","published_at":"2026-10-01T12:00:00Z"}]`)
		case strings.Contains(r.URL.Path, "/issues"):
			testWrite(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghServer.Close()

	stateDir := t.TempDir()
	cfg := config.Default()
	cfg.ReleaseWatch = config.ReleaseWatchConfig{
		StateDir:         stateDir,
		RateLimitFloor:   100,
		MaxActionsPerRun: 12,
		PerSourceCap:     3,
		Routes: []config.ReleaseWatchRoute{{
			Name:    "rt",
			Sources: []config.ReleaseWatchSource{{GitHub: "owner/upstream"}},
			Sinks:   []config.ReleaseWatchSink{{GitHubIssue: &config.GitHubIssueSink{Repo: "owner/consumer"}}},
		}},
	}

	var stdout bytes.Buffer
	svc, err := NewService(cfg, ClientOptions{
		GitHubBase: ghServer.URL,
		Stdout:     &stdout,
		LookupEnv:  func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err = svc.RunPass(context.Background()); err == nil {
		t.Fatal("RunPass without GITHUB_TOKEN = nil error, want error when filing")
	}
	if got := atomic.LoadInt32(&posts); got != 0 {
		t.Fatalf("POSTs without a token = %d, want 0: refuse before sending", got)
	}
	if !strings.Contains(stdout.String(), ErrMissingToken.Error()) {
		t.Fatalf("stdout = %q, want the missing-token cause logged", stdout.String())
	}

	diskState, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if diskState.Has("github:owner/upstream@v1.0.0") {
		t.Fatal("release was marked seen despite delivery failure without token")
	}
}

func TestCapsEnforcement(t *testing.T) {
	t.Run("per_source_cap respected and deferred logged", func(t *testing.T) {
		var posted []string
		var stdout bytes.Buffer

		ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(r.URL.Path, "/releases") {
				// 4 releases (newest to oldest)
				testWrite(w, `[
					{"tag_name":"v4.0.0","published_at":"2026-10-04T12:00:00Z"},
					{"tag_name":"v3.0.0","published_at":"2026-10-03T12:00:00Z"},
					{"tag_name":"v2.0.0","published_at":"2026-10-02T12:00:00Z"},
					{"tag_name":"v1.0.0","published_at":"2026-10-01T12:00:00Z"}
				]`)
				return
			}
			if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues") {
				testWrite(w, `[]`)
				return
			}
			if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues") {
				data, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				var p GitHubIssuePayload
				if err := json.Unmarshal(data, &p); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				posted = append(posted, p.Title)
				w.WriteHeader(http.StatusCreated)
				testWrite(w, `{"number":1}`)
				return
			}
			http.NotFound(w, r)
		}))
		defer ghServer.Close()

		cfg := config.Default()
		cfg.ReleaseWatch = config.ReleaseWatchConfig{
			StateDir:         t.TempDir(),
			RateLimitFloor:   100,
			MaxActionsPerRun: 10,
			PerSourceCap:     2, // Only 2 oldest releases acted on
			Routes: []config.ReleaseWatchRoute{{
				Name:    "rt",
				Sources: []config.ReleaseWatchSource{{GitHub: "owner/upstream"}},
				Sinks:   []config.ReleaseWatchSink{{GitHubIssue: &config.GitHubIssueSink{Repo: "owner/consumer"}}},
			}},
		}

		svc, err := NewService(cfg, ClientOptions{
			GitHubBase: ghServer.URL,
			Stdout:     &stdout,
			LookupEnv:  func(string) (string, bool) { return "token", true },
		})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}

		if err := svc.RunPass(context.Background()); err != nil {
			t.Fatalf("RunPass: %v", err)
		}

		if len(posted) != 2 {
			t.Fatalf("len(posted) = %d, want 2 (per_source_cap)", len(posted))
		}
		// Oldest first: v1.0.0 and v2.0.0
		if !strings.Contains(posted[0], "v1.0.0") || !strings.Contains(posted[1], "v2.0.0") {
			t.Fatalf("posted releases = %v, want v1.0.0 then v2.0.0", posted)
		}
		if !strings.Contains(stdout.String(), "release-watch: deferred github:owner/upstream@v3.0.0 (cap)") {
			t.Fatalf("stdout missing deferred message for v3.0.0: %s", stdout.String())
		}
		if svc.State().Has("github:owner/upstream@v3.0.0") {
			t.Fatal("deferred id v3.0.0 was marked seen")
		}
	})

	t.Run("max_actions_per_run stops filing across sources", func(t *testing.T) {
		var postedCount int32
		ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(r.URL.Path, "/releases") {
				testWrite(w, `[
					{"tag_name":"v2.0.0","published_at":"2026-10-02T12:00:00Z"},
					{"tag_name":"v1.0.0","published_at":"2026-10-01T12:00:00Z"}
				]`)
				return
			}
			if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues") {
				testWrite(w, `[]`)
				return
			}
			if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues") {
				atomic.AddInt32(&postedCount, 1)
				w.WriteHeader(http.StatusCreated)
				testWrite(w, `{"number":1}`)
				return
			}
			http.NotFound(w, r)
		}))
		defer ghServer.Close()

		cfg := config.Default()
		cfg.ReleaseWatch = config.ReleaseWatchConfig{
			StateDir:         t.TempDir(),
			RateLimitFloor:   100,
			MaxActionsPerRun: 3, // Total cap is 3 issues
			PerSourceCap:     2,
			Routes: []config.ReleaseWatchRoute{
				{
					Name:    "rt1",
					Sources: []config.ReleaseWatchSource{{GitHub: "owner/repo1"}},
					Sinks:   []config.ReleaseWatchSink{{GitHubIssue: &config.GitHubIssueSink{Repo: "c/d1"}}},
				},
				{
					Name:    "rt2",
					Sources: []config.ReleaseWatchSource{{GitHub: "owner/repo2"}},
					Sinks:   []config.ReleaseWatchSink{{GitHubIssue: &config.GitHubIssueSink{Repo: "c/d2"}}},
				},
			},
		}

		svc, err := NewService(cfg, ClientOptions{
			GitHubBase: ghServer.URL,
			LookupEnv:  func(string) (string, bool) { return "token", true },
		})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}

		if err := svc.RunPass(context.Background()); err != nil {
			t.Fatalf("RunPass: %v", err)
		}
		if got := atomic.LoadInt32(&postedCount); got != 3 {
			t.Fatalf("postedCount = %d, want max_actions_per_run = 3", got)
		}
	})
}

func TestSeedOperation(t *testing.T) {
	var postCalled int32
	var stdout bytes.Buffer

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/releases") {
			testWrite(w, `[{"tag_name":"v1.0.0"}]`)
			return
		}
		if r.Method == http.MethodPost {
			atomic.AddInt32(&postCalled, 1)
		}
		http.NotFound(w, r)
	}))
	defer ghServer.Close()

	hfServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		testWrite(w, `[{"id":"Org/Model1"}]`)
	}))
	defer hfServer.Close()

	stateDir := t.TempDir()
	cfg := config.Default()
	cfg.ReleaseWatch = config.ReleaseWatchConfig{
		StateDir:         stateDir,
		RateLimitFloor:   100,
		MaxActionsPerRun: 12,
		PerSourceCap:     3,
		Routes: []config.ReleaseWatchRoute{{
			Name: "rt",
			Sources: []config.ReleaseWatchSource{
				{GitHub: "owner/repo"},
				{HuggingFaceOrg: "Org"},
			},
			Sinks: []config.ReleaseWatchSink{
				{GitHubIssue: &config.GitHubIssueSink{Repo: "c/d"}},
				{Ntfy: &config.NtfySink{Topic: "topic"}},
			},
		}},
	}

	svc, err := NewService(cfg, ClientOptions{
		GitHubBase:      ghServer.URL,
		HuggingFaceBase: hfServer.URL,
		Stdout:          &stdout,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err = svc.Seed(context.Background()); err != nil {
		t.Fatalf("Seed(): %v", err)
	}

	if atomic.LoadInt32(&postCalled) != 0 {
		t.Fatal("Seed posted an issue; want 0 issues posted")
	}

	if !strings.Contains(stdout.String(), "release-watch: seeded github:owner/repo@v1.0.0") {
		t.Fatalf("stdout missing seeded github:owner/repo@v1.0.0: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "release-watch: seeded hf:Org/Model1") {
		t.Fatalf("stdout missing seeded hf:Org/Model1: %s", stdout.String())
	}

	diskState, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !diskState.Has("github:owner/repo@v1.0.0") || !diskState.Has("hf:Org/Model1") {
		t.Fatal("seen-set on disk missing seeded items")
	}
}
