package litellmgateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cordanaLLM/praetor/tribunus/catalog"
)

func writeTokenFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return path
}

func TestFetch_Positive(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"cordana/auto","object":"model","created":1,"owned_by":"openai"},{"id":"cordana/chat","object":"model","created":1,"owned_by":"openai"}]}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer server.Close()

	tokenPath := writeTokenFile(t, "shhh-secret-token\n")
	res := Fetch(context.Background(), server.URL, tokenPath)

	if res.Status != catalog.StatusOK {
		t.Fatalf("Status = %v, detail = %q, want ok", res.Status, res.Detail)
	}
	if res.Count != 2 {
		t.Fatalf("Count = %d, want 2", res.Count)
	}
	if gotAuth != "Bearer shhh-secret-token" {
		t.Fatalf("Authorization header = %q, want trimmed token", gotAuth)
	}
	for _, rec := range res.Records {
		if err := rec.Validate(); err != nil {
			t.Fatalf("Validate() = %v", err)
		}
		if rec.AccessPath != catalog.AccessGateway {
			t.Fatalf("AccessPath = %v, want gateway", rec.AccessPath)
		}
	}
}

// TestFetch_NeverLeaksToken asserts the token never appears verbatim in any
// Result.Detail, across every failure path this source can hit.
func TestFetch_NeverLeaksToken(t *testing.T) {
	const secret = "do-not-print-me-12345"

	t.Run("http error", func(t *testing.T) {
		tokenPath := writeTokenFile(t, secret)
		res := Fetch(context.Background(), "http://127.0.0.1:1", tokenPath)
		if strings.Contains(res.Detail, secret) {
			t.Fatalf("Detail leaked the token: %q", res.Detail)
		}
	})

	t.Run("non-200 status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer server.Close()
		tokenPath := writeTokenFile(t, secret)
		res := Fetch(context.Background(), server.URL, tokenPath)
		if res.Status != catalog.StatusFail {
			t.Fatalf("Status = %v, want fail for HTTP 403", res.Status)
		}
		if strings.Contains(res.Detail, secret) {
			t.Fatalf("Detail leaked the token: %q", res.Detail)
		}
	})
}

func TestFetch_Negative(t *testing.T) {
	t.Run("missing token file", func(t *testing.T) {
		res := Fetch(context.Background(), "http://example.invalid", filepath.Join(t.TempDir(), "missing"))
		if res.Status != catalog.StatusFail {
			t.Fatalf("Status = %v, want fail for a missing token file", res.Status)
		}
	})

	t.Run("empty token file", func(t *testing.T) {
		tokenPath := writeTokenFile(t, "   \n")
		res := Fetch(context.Background(), "http://example.invalid", tokenPath)
		if res.Status != catalog.StatusFail {
			t.Fatalf("Status = %v, want fail for an empty token file", res.Status)
		}
	})

	t.Run("malformed json body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("{not json")) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
		}))
		defer server.Close()
		tokenPath := writeTokenFile(t, "tok")
		res := Fetch(context.Background(), server.URL, tokenPath)
		if res.Status != catalog.StatusFail {
			t.Fatalf("Status = %v, want fail for a malformed body", res.Status)
		}
	})
}

// TestFetch_Boundary covers the oversized-token-file bound and the
// zero-models response, both edges of the size/skip contract.
func TestFetch_Boundary(t *testing.T) {
	t.Run("token file exceeds bound", func(t *testing.T) {
		oversized := make([]byte, maxTokenFileBytes+1)
		for i := range oversized {
			oversized[i] = 'a'
		}
		tokenPath := writeTokenFile(t, string(oversized))
		res := Fetch(context.Background(), "http://example.invalid", tokenPath)
		if res.Status != catalog.StatusFail {
			t.Fatalf("Status = %v, want fail for an oversized token file", res.Status)
		}
	})

	t.Run("zero models is a skip not a fail", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data":[]}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
		}))
		defer server.Close()
		tokenPath := writeTokenFile(t, "tok")
		res := Fetch(context.Background(), server.URL, tokenPath)
		if res.Status != catalog.StatusSkip {
			t.Fatalf("Status = %v, want skip for zero models", res.Status)
		}
	})
}
