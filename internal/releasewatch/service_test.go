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

	"github.com/tekmora/epitrop/internal/config"
)

func testWrite(w http.ResponseWriter, s string) {
	_, _ = w.Write([]byte(s)) //nolint:errcheck // test mock response write
}

func readIssuePayload(w http.ResponseWriter, r *http.Request) (GitHubIssuePayload, bool) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return GitHubIssuePayload{}, false
	}
	var p GitHubIssuePayload
	if err := json.Unmarshal(data, &p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return GitHubIssuePayload{}, false
	}
	return p, true
}

func testGitHubNtfyConfig(t *testing.T, repo string, perSourceCap int) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.ReleaseWatch = config.ReleaseWatchConfig{
		StateDir:         t.TempDir(),
		RateLimitFloor:   100,
		MaxActionsPerRun: 12,
		PerSourceCap:     perSourceCap,
		Routes: []config.ReleaseWatchRoute{{
			Name:    "rt",
			Sources: []config.ReleaseWatchSource{{GitHub: repo}},
			Sinks:   []config.ReleaseWatchSink{{Ntfy: &config.NtfySink{Topic: "top"}}},
		}},
	}
	return cfg
}

func tokenRuleConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.ReleaseWatch = config.ReleaseWatchConfig{
		StateDir:         t.TempDir(),
		RateLimitFloor:   10,
		MaxActionsPerRun: 12,
		PerSourceCap:     3,
		Routes: []config.ReleaseWatchRoute{
			{
				Name:    "rt-gh",
				Sources: []config.ReleaseWatchSource{{GitHub: "owner/upstream"}},
				Sinks:   []config.ReleaseWatchSink{{Ntfy: &config.NtfySink{Topic: "topic"}}},
			},
			{
				Name:    "rt-hf",
				Sources: []config.ReleaseWatchSource{{HuggingFaceOrg: "TestOrg"}},
				Sinks:   []config.ReleaseWatchSink{{Ntfy: &config.NtfySink{Topic: "topic"}}},
			},
		},
	}
	return cfg
}

func TestPlantedReleaseFilingAndSeenSetSecondPass(t *testing.T) {
	var postCount int32
	var postedBody string
	var postedTitle string

	ghServer := newPlantedReleaseServer(&postCount, &postedTitle, &postedBody)
	defer ghServer.Close()

	cfg := oneRouteConfig(t, 100)
	stateDir := cfg.ReleaseWatch.StateDir

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
	assertPlantedReleasePass1(t, atomic.LoadInt32(&postCount), postedBody, postedTitle, stateDir)

	// Pass 2: seen-set prevents second POST
	if err = svc.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass 2: %v", err)
	}
	if atomic.LoadInt32(&postCount) != 1 {
		t.Fatalf("postCount after pass 2 = %d, want 1", postCount)
	}
}

func newPlantedReleaseServer(postCount *int32, postedTitle, postedBody *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases"):
			w.Header().Set("Content-Type", "application/json")
			testWrite(w, `[{"tag_name":"v1.0.0","html_url":"https://github.com/owner/upstream/releases/tag/v1.0.0","body":"notes","published_at":"2026-10-01T12:00:00Z"}]`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues"):
			w.Header().Set("Content-Type", "application/json")
			testWrite(w, `[]`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues"):
			atomic.AddInt32(postCount, 1)
			payload, ok := readIssuePayload(w, r)
			if !ok {
				return
			}
			*postedTitle = payload.Title
			*postedBody = payload.Body
			w.WriteHeader(http.StatusCreated)
			testWrite(w, `{"number":1,"title":"title","body":"body","state":"open"}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func assertPlantedReleasePass1(t *testing.T, postCount int32, postedBody, postedTitle, stateDir string) {
	t.Helper()
	if postCount != 1 {
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
}

func TestMarkerAlreadyPresentInExistingIssue(t *testing.T) {
	var postCalled int32
	var stdout bytes.Buffer

	ghServer := newMarkerPresentServer(&postCalled)
	defer ghServer.Close()

	cfg := oneRouteConfig(t, 100)
	stateDir := cfg.ReleaseWatch.StateDir

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

func newMarkerPresentServer(postCalled *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases"):
			w.Header().Set("Content-Type", "application/json")
			testWrite(w, `[{"tag_name":"v1.0.0","html_url":"https://github.com/owner/upstream/releases/tag/v1.0.0","body":"notes","published_at":"2026-10-01T12:00:00Z"}]`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues"):
			w.Header().Set("Content-Type", "application/json")
			// Return closed issue containing marker
			testWrite(w, `[{"number":99,"title":"old","body":"<!-- release-watch: github:owner/upstream@v1.0.0 -->\nclosed","state":"closed"}]`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues"):
			atomic.AddInt32(postCalled, 1)
			http.Error(w, "should not post", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestRestartMidPassAtomicSeenSet(t *testing.T) {
	var filedReleases []string
	deliverFailSecond := true

	ghServer := newRestartMidPassServer(&filedReleases, &deliverFailSecond)
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
	assertRestartMidPass1(t, svc1, stateDir)

	// Pass 2: restart with new service instance reading disk state
	deliverFailSecond = false
	filedReleases = nil
	svc2, err := NewService(cfg, opts)
	if err != nil {
		t.Fatalf("NewService 2: %v", err)
	}
	assertRestartMidPass2(t, svc2, stateDir, &filedReleases)
}

func newRestartMidPassServer(filedReleases *[]string, deliverFailSecond *bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases"):
			w.Header().Set("Content-Type", "application/json")
			testWrite(w, `[
				{"tag_name":"v2.0.0","html_url":"https://github.com/owner/upstream/releases/tag/v2.0.0","published_at":"2026-10-02T12:00:00Z"},
				{"tag_name":"v1.0.0","html_url":"https://github.com/owner/upstream/releases/tag/v1.0.0","published_at":"2026-10-01T12:00:00Z"}
			]`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues"):
			w.Header().Set("Content-Type", "application/json")
			testWrite(w, `[]`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues"):
			handleRestartMidPassPost(w, r, filedReleases, deliverFailSecond)
		default:
			http.NotFound(w, r)
		}
	}))
}

func handleRestartMidPassPost(w http.ResponseWriter, r *http.Request, filedReleases *[]string, deliverFailSecond *bool) {
	p, ok := readIssuePayload(w, r)
	if !ok {
		return
	}
	if strings.Contains(p.Body, "v1.0.0") {
		*filedReleases = append(*filedReleases, "v1.0.0")
		w.WriteHeader(http.StatusCreated)
		testWrite(w, `{"number":1}`)
		return
	}
	if *deliverFailSecond {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	*filedReleases = append(*filedReleases, "v2.0.0")
	w.WriteHeader(http.StatusCreated)
	testWrite(w, `{"number":2}`)
}

func assertRestartMidPass1(t *testing.T, svc1 *Service, stateDir string) {
	t.Helper()
	err := svc1.RunPass(context.Background())
	if err == nil {
		t.Fatal("RunPass 1 = nil, want failure for 2nd delivery")
	}

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
}

func assertRestartMidPass2(t *testing.T, svc2 *Service, stateDir string, filedReleases *[]string) {
	t.Helper()
	if err := svc2.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass 2: %v", err)
	}
	if len(*filedReleases) != 1 || (*filedReleases)[0] != "v2.0.0" {
		t.Fatalf("filedReleases on rerun = %v, want only [v2.0.0]", *filedReleases)
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
		testSourceFailureScenarios(t)
	})

	t.Run("empty releases list falls back to tags", func(t *testing.T) {
		testEmptyReleasesFallsBackToTags(t)
	})

	t.Run("empty releases and tags is not an error", func(t *testing.T) {
		testEmptyReleasesAndTagsNotAnError(t)
	})
}

func testSourceFailureScenarios(t *testing.T) {
	t.Helper()
	hfServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		testWrite(w, `[{"id":"GoodOrg/Model1"}]`)
	}))
	defer hfServer.Close()

	for _, statusFail := range []int{500, 401} {
		t.Run(fmt.Sprintf("status_%d", statusFail), func(t *testing.T) {
			testSourceFailureStatus(t, statusFail, hfServer.URL)
		})
	}

	t.Run("malformed_json", func(t *testing.T) {
		testSourceFailureMalformedJSON(t)
	})
}

func testSourceFailureStatus(t *testing.T, statusFail int, hfServerURL string) {
	t.Helper()
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
		HuggingFaceBase: hfServerURL,
		NtfyBase:        hfServerURL,
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
}

func testSourceFailureMalformedJSON(t *testing.T) {
	t.Helper()
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		testWrite(w, `{not valid json`)
	}))
	defer ghServer.Close()

	cfg := testGitHubNtfyConfig(t, "bad/json", 3)
	svc, err := NewService(cfg, ClientOptions{GitHubBase: ghServer.URL})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	err = svc.RunPass(context.Background())
	if err == nil || !strings.Contains(err.Error(), "github:bad/json") {
		t.Fatalf("RunPass error = %v, want naming bad/json", err)
	}
}

func testEmptyReleasesFallsBackToTags(t *testing.T) {
	t.Helper()
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

	cfg := testGitHubNtfyConfig(t, "tags/fallback", 3)
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
}

func testEmptyReleasesAndTagsNotAnError(t *testing.T) {
	t.Helper()
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		testWrite(w, `[]`)
	}))
	defer ghServer.Close()

	cfg := testGitHubNtfyConfig(t, "empty/repo", 3)
	svc, err := NewService(cfg, ClientOptions{GitHubBase: ghServer.URL, NtfyBase: ghServer.URL})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass = %v, want nil for empty valid response", err)
	}
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

	cfg := testGitHubNtfyConfig(t, "owner/repo", 5)
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
	cfg := tokenRuleConfig(t)

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

	assertTokenHeaderIsolation(t, ghAuthHeader, hfAuthHeader, ntfyAuthHeader, secretToken)
	assertTokenRedirectAndLogLeakIsolation(t, svc, ghServer.URL+"/redirect-test", redirectAuthHeader, secretToken, &stdout, &stderr)
}

func assertTokenHeaderIsolation(t *testing.T, ghAuth, hfAuth, ntfyAuth, secretToken string) {
	t.Helper()
	if ghAuth != "Bearer "+secretToken {
		t.Fatalf("ghAuthHeader = %q, want Bearer %s", ghAuth, secretToken)
	}
	if hfAuth != "" {
		t.Fatalf("hfAuthHeader = %q, want empty", hfAuth)
	}
	if ntfyAuth != "" {
		t.Fatalf("ntfyAuthHeader = %q, want empty", ntfyAuth)
	}
}

func assertTokenRedirectAndLogLeakIsolation(t *testing.T, svc *Service, redirectURL, redirectAuthHeader, secretToken string, stdout, stderr *bytes.Buffer) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, redirectURL, nil)
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

	cfg := oneRouteConfig(t, 100)
	stateDir := cfg.ReleaseWatch.StateDir

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
		testPerSourceCapRespected(t)
	})

	t.Run("max_actions_per_run stops filing across sources", func(t *testing.T) {
		testMaxActionsPerRunStopsFiling(t)
	})
}

func testPerSourceCapRespected(t *testing.T) {
	t.Helper()
	var posted []string
	var stdout bytes.Buffer

	ghServer := newCapsPerSourceServer(&posted)
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

	assertPerSourceCapResults(t, posted, stdout.String(), svc.State())
}

func newCapsPerSourceServer(posted *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/releases") {
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
			handleCapsPerSourcePost(w, r, posted)
			return
		}
		http.NotFound(w, r)
	}))
}

func handleCapsPerSourcePost(w http.ResponseWriter, r *http.Request, posted *[]string) {
	p, ok := readIssuePayload(w, r)
	if !ok {
		return
	}
	*posted = append(*posted, p.Title)
	w.WriteHeader(http.StatusCreated)
	testWrite(w, `{"number":1}`)
}

func assertPerSourceCapResults(t *testing.T, posted []string, stdout string, state *State) {
	t.Helper()
	if len(posted) != 2 {
		t.Fatalf("len(posted) = %d, want 2 (per_source_cap)", len(posted))
	}
	// Oldest first: v1.0.0 and v2.0.0
	if !strings.Contains(posted[0], "v1.0.0") || !strings.Contains(posted[1], "v2.0.0") {
		t.Fatalf("posted releases = %v, want v1.0.0 then v2.0.0", posted)
	}
	if !strings.Contains(stdout, "release-watch: deferred github:owner/upstream@v3.0.0 (cap)") {
		t.Fatalf("stdout missing deferred message for v3.0.0: %s", stdout)
	}
	if state.Has("github:owner/upstream@v3.0.0") {
		t.Fatal("deferred id v3.0.0 was marked seen")
	}
}

func testMaxActionsPerRunStopsFiling(t *testing.T) {
	t.Helper()
	var postedCount int32
	ghServer := newMaxActionsServer(&postedCount)
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
}

func newMaxActionsServer(postedCount *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			atomic.AddInt32(postedCount, 1)
			w.WriteHeader(http.StatusCreated)
			testWrite(w, `{"number":1}`)
			return
		}
		http.NotFound(w, r)
	}))
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

	assertSeedOperationResults(t, atomic.LoadInt32(&postCalled), stdout.String(), stateDir)
}

func assertSeedOperationResults(t *testing.T, postCalled int32, stdout string, stateDir string) {
	t.Helper()
	if postCalled != 0 {
		t.Fatal("Seed posted an issue; want 0 issues posted")
	}

	if !strings.Contains(stdout, "release-watch: seeded github:owner/repo@v1.0.0") {
		t.Fatalf("stdout missing seeded github:owner/repo@v1.0.0: %s", stdout)
	}
	if !strings.Contains(stdout, "release-watch: seeded hf:Org/Model1") {
		t.Fatalf("stdout missing seeded hf:Org/Model1: %s", stdout)
	}

	diskState, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !diskState.Has("github:owner/repo@v1.0.0") || !diskState.Has("hf:Org/Model1") {
		t.Fatal("seen-set on disk missing seeded items")
	}
}
