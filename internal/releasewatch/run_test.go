package releasewatch

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
)

func oneRouteConfig(t *testing.T, floor int) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.ReleaseWatch = config.ReleaseWatchConfig{
		StateDir: t.TempDir(), RateLimitFloor: floor, MaxActionsPerRun: 12, PerSourceCap: 3,
		Routes: []config.ReleaseWatchRoute{{
			Name:    "rt",
			Sources: []config.ReleaseWatchSource{{GitHub: "owner/upstream"}},
			Sinks:   []config.ReleaseWatchSink{{GitHubIssue: &config.GitHubIssueSink{Repo: "owner/consumer"}}},
		}},
	}
	return cfg
}

// TestRunLoopKeepsPollingAndStopsCleanly: the loop makes a pass every interval until the
// context is cancelled, and a cancel (a supervisor stop) is not an error.
func TestRunLoopKeepsPollingAndStopsCleanly(t *testing.T) {
	var releaseGets int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/releases") {
			atomic.AddInt32(&releaseGets, 1)
		}
		testWrite(w, `[]`)
	}))
	defer gh.Close()
	svc, err := NewService(oneRouteConfig(t, 100), ClientOptions{GitHubBase: gh.URL, Stdout: &bytes.Buffer{}, LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx, false, 10*time.Millisecond) }()
	for i := 0; i < 200 && atomic.LoadInt32(&releaseGets) < 3; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if err != nil {
		t.Fatalf("Run after cancel = %v, want nil: a stop is not a failure", err)
	}
	if got := atomic.LoadInt32(&releaseGets); got < 3 {
		t.Fatalf("passes = %d, want at least 3 before the cancel", got)
	}
}

// TestThrottleLastsOnePass: a pass that hit the rate-limit floor must not silence later
// passes once GitHub reports quota again.
func TestThrottleLastsOnePass(t *testing.T) {
	var remaining atomic.Int32
	remaining.Store(5)
	var gets int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&gets, 1)
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(int(remaining.Load())))
		testWrite(w, `[]`)
	}))
	defer gh.Close()
	svc, err := NewService(oneRouteConfig(t, 100), ClientOptions{GitHubBase: gh.URL, Stdout: &bytes.Buffer{}, LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err = svc.RunPass(context.Background()); err != nil {
		t.Fatalf("throttled pass = %v, want nil", err)
	}
	remaining.Store(5000)
	before := atomic.LoadInt32(&gets)
	if err = svc.RunPass(context.Background()); err != nil {
		t.Fatalf("second pass = %v, want nil", err)
	}
	if atomic.LoadInt32(&gets) == before {
		t.Fatal("second pass sent no request: the throttle of the first pass stuck")
	}
}

// TestTokenNeverCrossesScheme: the token goes to the GitHub base scheme and host only, and a
// redirect that changes either drops it.
func TestTokenNeverCrossesScheme(t *testing.T) {
	c, err := NewClient(ClientOptions{GitHubBase: "https://api.example.invalid", LookupEnv: func(string) (string, bool) { return "tok", true }})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	plain, _ := url.Parse("http://api.example.invalid/repos/o/r") //nolint:errcheck // constant URL
	if c.isGitHubBase(plain) {
		t.Fatal("isGitHubBase(http on the GitHub host) = true, want false: the token would travel unencrypted")
	}
	for _, target := range []string{"http://api.example.invalid/x", "https://other.example.invalid/x"} {
		req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
		if reqErr != nil {
			t.Fatalf("NewRequest: %v", reqErr)
		}
		req.Header.Set("Authorization", "Bearer tok")
		if err = c.httpClient.CheckRedirect(req, []*http.Request{req}); err != nil {
			t.Fatalf("CheckRedirect(%s) = %v, want nil", target, err)
		}
		if req.Header.Get("Authorization") != "" {
			t.Fatalf("redirect to %s kept Authorization", target)
		}
	}
}

// TestThrottleStartsBelowTheFloorNotAtIt: remaining equal to the floor still allows requests.
func TestThrottleStartsBelowTheFloorNotAtIt(t *testing.T) {
	var gets int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&gets, 1)
		w.Header().Set("X-RateLimit-Remaining", "100")
		testWrite(w, `[]`)
	}))
	defer gh.Close()
	cfg := oneRouteConfig(t, 100)
	cfg.ReleaseWatch.Routes = append(cfg.ReleaseWatch.Routes, config.ReleaseWatchRoute{
		Name:    "rt2",
		Sources: []config.ReleaseWatchSource{{GitHub: "owner/second"}},
		Sinks:   []config.ReleaseWatchSink{{GitHubIssue: &config.GitHubIssueSink{Repo: "owner/consumer"}}},
	})
	svc, err := NewService(cfg, ClientOptions{GitHubBase: gh.URL, Stdout: &bytes.Buffer{}, LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err = svc.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass = %v, want nil", err)
	}
	// Each route's empty release list falls back to tags: two requests per route.
	if got := atomic.LoadInt32(&gets); got != 4 {
		t.Fatalf("requests = %d, want 4: remaining at the floor must not throttle", got)
	}
}

func TestSeenSetIsBounded(t *testing.T) {
	st, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for i := 0; i <= MaxSeenRecords; i++ {
		st.seen[strconv.Itoa(i)] = time.Unix(int64(i), 0).UTC().Format(time.RFC3339)
	}
	if err = st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if st.Count() != MaxSeenRecords || st.Has("0") || !st.Has(strconv.Itoa(MaxSeenRecords)) {
		t.Fatalf("after Save: count=%d has-oldest=%v, want %d entries with the oldest dropped", st.Count(), st.Has("0"), MaxSeenRecords)
	}
}

// TestRunLoopReportsItsRuntimeBound: reaching the runtime bound ends the loop with an
// error, unlike a stop, so a restart policy starts a fresh process.
func TestRunLoopReportsItsRuntimeBound(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { testWrite(w, `[]`) }))
	defer gh.Close()
	svc, err := NewService(oneRouteConfig(t, 100), ClientOptions{GitHubBase: gh.URL, Stdout: &bytes.Buffer{}, LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err = svc.Run(ctx, false, 10*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run past its bound = %v, want DeadlineExceeded", err)
	}
}
