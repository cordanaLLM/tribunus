package releasewatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tekmora/epitrop/internal/config"
)

// TestAnonymousSinkSearchFailsWithMissingToken (T1): sink search 404, 401, 403 without token
// fails with ErrMissingToken naming repo and HTTP status.
func TestAnonymousSinkSearchFailsWithMissingToken(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			testAnonymousSinkSearchMissingTokenForStatus(t, status)
		})
	}
}

func testDeliverIssue(t *testing.T, handler http.HandlerFunc, token string) (*State, bool, error) {
	t.Helper()
	gh := httptest.NewServer(handler)
	t.Cleanup(gh.Close)

	client, err := NewClient(ClientOptions{
		GitHubBase: gh.URL,
		LookupEnv: func(k string) (string, bool) {
			if token != "" && k == "GITHUB_TOKEN" {
				return token, true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	state, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	sink := &config.GitHubIssueSink{Repo: "owner/consumer"}
	item := ReleaseItem{ID: "github:owner/upstream@v1.0.0"}

	filed, deliverErr := DeliverGitHubIssue(context.Background(), client, state, sink, item)
	return state, filed, deliverErr
}

func testAnonymousSinkSearchMissingTokenForStatus(t *testing.T, status int) {
	t.Helper()
	_, _, err := testDeliverIssue(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}, "")
	if err == nil {
		t.Fatal("DeliverGitHubIssue = nil error, want ErrMissingToken")
	}
	if !errors.Is(err, ErrMissingToken) {
		t.Fatalf("errors.Is(err, ErrMissingToken) = false, err = %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "owner/consumer") || !strings.Contains(msg, strconv.Itoa(status)) {
		t.Fatalf("err %q does not contain repo and status %d", msg, status)
	}
}

// TestTokenPresentSinkSearch404NotMissingToken (T2): token present, search 404
// returns plain search error, NOT ErrMissingToken.
func TestTokenPresentSinkSearch404NotMissingToken(t *testing.T) {
	_, _, err := testDeliverIssue(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}, "test-token")
	if err == nil {
		t.Fatal("DeliverGitHubIssue = nil error, want 404 error")
	}
	if errors.Is(err, ErrMissingToken) {
		t.Fatalf("errors.Is(err, ErrMissingToken) = true with token present, err = %v", err)
	}
	want := "search issues in owner/consumer returned HTTP 404"
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
}

// TestAnonymousSinkSearch500NotMissingToken (T3 boundary 1): HTTP 500 without token
// returns plain search error, NOT ErrMissingToken.
func TestAnonymousSinkSearch500NotMissingToken(t *testing.T) {
	_, _, err := testDeliverIssue(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}, "")
	if err == nil {
		t.Fatal("DeliverGitHubIssue = nil error, want 500 error")
	}
	if errors.Is(err, ErrMissingToken) {
		t.Fatalf("errors.Is(err, ErrMissingToken) = true on 500, err = %v", err)
	}
	want := "search issues in owner/consumer returned HTTP 500"
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
}

// TestAnonymousSinkSearchMarkerFoundMarksSeen (T3 boundary 2): HTTP 200 with marker
// marks item seen without filing and returns nil error even without token.
func TestAnonymousSinkSearchMarkerFoundMarksSeen(t *testing.T) {
	marker := issueMarker("github:owner/upstream@v1.0.0")
	state, filed, err := testDeliverIssue(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		testWrite(w, fmt.Sprintf(`[{"number":1,"body":%q}]`, "Existing issue\n\n"+marker))
	}, "")
	if err != nil {
		t.Fatalf("DeliverGitHubIssue = %v, want nil", err)
	}
	if filed {
		t.Fatal("filed = true, want false for already filed issue")
	}
	if !state.Has("github:owner/upstream@v1.0.0") {
		t.Fatal("item was not marked seen")
	}
}

func runRateLimitFloorTest(t *testing.T, floor int, sendLimit bool) (int, string) {
	t.Helper()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if sendLimit {
			w.Header().Set("X-RateLimit-Limit", "60")
		}
		w.Header().Set("X-RateLimit-Remaining", "59")
		w.Header().Set("X-RateLimit-Reset", "1893456000")
		testWrite(w, `[]`)
	}))
	defer gh.Close()

	var stdout bytes.Buffer
	client, err := NewClient(ClientOptions{
		GitHubBase:     gh.URL,
		RateLimitFloor: floor,
		Stdout:         &stdout,
		LookupEnv:      func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	for i := 0; i < 2; i++ {
		client.ResetThrottle()
		req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet, gh.URL+"/test", nil)
		if reqErr != nil {
			t.Fatalf("NewRequest: %v", reqErr)
		}
		if _, _, _, doErr := client.DoRequest(context.Background(), req, time.Second, 1024); doErr != nil {
			t.Fatalf("DoRequest: %v", doErr)
		}
	}

	count := 0
	matchedLine := ""
	lines := strings.Split(stdout.String(), "\n")
	for i := 0; i < len(lines); i++ {
		if strings.Contains(lines[i], "rate_limit_floor=") {
			count++
			matchedLine = lines[i]
		}
	}
	return count, matchedLine
}

// TestRateLimitFloorVsLimitWarning (T4): floor vs limit logs once when floor >= limit.
func TestRateLimitFloorVsLimitWarning(t *testing.T) {
	t.Run("floor 61 limit 60 logs once", func(t *testing.T) {
		count, line := runRateLimitFloorTest(t, 61, true)
		if count != 1 {
			t.Fatalf("rate_limit_floor log line count = %d, want 1", count)
		}
		if !strings.Contains(line, "61") || !strings.Contains(line, "60") {
			t.Fatalf("log line %q must contain floor 61 and limit 60", line)
		}
	})

	t.Run("floor 60 limit 60 equal logs once", func(t *testing.T) {
		count, line := runRateLimitFloorTest(t, 60, true)
		if count != 1 {
			t.Fatalf("rate_limit_floor equal case log line count = %d, want 1", count)
		}
		if !strings.Contains(line, "60") {
			t.Fatalf("log line %q must contain 60", line)
		}
	})

	t.Run("floor 59 limit 60 just below logs nothing", func(t *testing.T) {
		count, _ := runRateLimitFloorTest(t, 59, true)
		if count != 0 {
			t.Fatalf("rate_limit_floor log line count = %d, want 0", count)
		}
	})

	t.Run("missing limit header logs nothing", func(t *testing.T) {
		count, _ := runRateLimitFloorTest(t, 61, false)
		if count != 0 {
			t.Fatalf("rate_limit_floor log line count = %d, want 0", count)
		}
	})
}

// TestAnonymousServicePassLogsMissingTokenOn404Sink (T5): a service-level pass
// without token and a 404 sink logs a delivery failure naming GITHUB_TOKEN.
func TestAnonymousServicePassLogsMissingTokenOn404Sink(t *testing.T) {
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/releases"):
			testWrite(w, `[{"tag_name":"v1.0.0","published_at":"2026-10-01T12:00:00Z"}]`)
		case strings.Contains(r.URL.Path, "/issues"):
			w.WriteHeader(http.StatusNotFound)
			testWrite(w, `{"message":"Not Found"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghServer.Close()

	cfg := oneRouteConfig(t, 100)
	var stdout bytes.Buffer
	svc, err := NewService(cfg, ClientOptions{
		GitHubBase: ghServer.URL,
		Stdout:     &stdout,
		LookupEnv:  func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	err = svc.RunPass(context.Background())
	if err == nil {
		t.Fatal("RunPass without GITHUB_TOKEN on 404 sink = nil error, want error")
	}

	out := stdout.String()
	deliveryLine := ""
	lines := strings.Split(out, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.Contains(lines[i], "delivery") && strings.Contains(lines[i], "failed:") {
			deliveryLine = lines[i]
			break
		}
	}
	if deliveryLine == "" {
		t.Fatalf("stdout %q has no delivery failure line", out)
	}
	if !strings.Contains(deliveryLine, "GITHUB_TOKEN") {
		t.Fatalf("delivery failure line %q does not name GITHUB_TOKEN", deliveryLine)
	}
}
