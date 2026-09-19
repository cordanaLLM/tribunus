package httpfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGet_Positive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello")) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer server.Close()

	body, err := Get(context.Background(), server.URL, time.Second, 1024)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q, want %q", body, "hello")
	}
}

func TestGet_Negative(t *testing.T) {
	t.Run("unreachable", func(t *testing.T) {
		if _, err := Get(context.Background(), "http://127.0.0.1:1", time.Second, 1024); err == nil {
			t.Fatal("Get() = nil error, want failure for an unreachable host")
		}
	})

	t.Run("non-200 status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()
		if _, err := Get(context.Background(), server.URL, time.Second, 1024); err == nil {
			t.Fatal("Get() = nil error, want failure for a non-200 status")
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("late")) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
		}))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Get(ctx, server.URL, time.Second, 1024); err == nil {
			t.Fatal("Get() = nil error, want failure for an already-canceled context")
		}
	})
}

// TestGet_Boundary confirms the byte cap refuses a response that is exactly
// one byte over maxBytes, and accepts one exactly at the cap.
func TestGet_Boundary(t *testing.T) {
	t.Run("exceeds cap by one byte", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("a", 11))) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
		}))
		defer server.Close()
		if _, err := Get(context.Background(), server.URL, time.Second, 10); err == nil {
			t.Fatal("Get() = nil error, want failure for a response one byte over the cap")
		}
	})

	t.Run("exactly at cap", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("a", 10))) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
		}))
		defer server.Close()
		body, err := Get(context.Background(), server.URL, time.Second, 10)
		if err != nil {
			t.Fatalf("Get() = %v, want success for a response exactly at the cap", err)
		}
		if len(body) != 10 {
			t.Fatalf("len(body) = %d, want 10", len(body))
		}
	})
}
