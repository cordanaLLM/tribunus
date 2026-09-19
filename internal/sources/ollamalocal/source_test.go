package ollamalocal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cordanaLLM/praetor/tribunus/catalog"
)

func newServer(t *testing.T, tags, ps string, psStatus int) *httptest.Server {
	t.Helper()
	if psStatus == 0 {
		psStatus = http.StatusOK
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(tags)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
		case "/api/ps":
			w.WriteHeader(psStatus)
			_, _ = w.Write([]byte(ps)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

const sampleTags = `{"models":[{"name":"hf.co/unsloth/Qwen3.8-27B-GGUF:UD-Q4_K_M","model":"hf.co/unsloth/Qwen3.8-27B-GGUF:UD-Q4_K_M","modified_at":"2026-08-22T20:09:26Z","size":17395587762,"details":{"parameter_size":"27.3B","quantization_level":"Q4_K_M","context_length":262144},"capabilities":["tools","thinking"]},{"name":"gpt-oss:20b","details":{"parameter_size":"20.9B","quantization_level":"MXFP4","context_length":32768},"capabilities":["completion"]}]}`

const samplePS = `{"models":[{"name":"gpt-oss:20b","size":13000056503,"expires_at":"2026-09-18T19:44:37Z","size_vram":13000056503,"context_length":32768}]}`

func TestFetch_Positive(t *testing.T) {
	server := newServer(t, sampleTags, samplePS, http.StatusOK)
	defer server.Close()

	res := Fetch(context.Background(), server.URL)
	if res.Status != catalog.StatusOK {
		t.Fatalf("Status = %v, detail = %q, want ok", res.Status, res.Detail)
	}
	if res.Count != 2 {
		t.Fatalf("Count = %d, want 2", res.Count)
	}
	var found bool
	for _, rec := range res.Records {
		if err := rec.Validate(); err != nil {
			t.Fatalf("Validate() = %v", err)
		}
		if rec.ModelID == "gpt-oss:20b" {
			found = true
			if !contains(rec.Capabilities, "loaded") {
				t.Fatalf("Capabilities = %v, want loaded flag for a model /api/ps reports", rec.Capabilities)
			}
			if rec.ContextWindow == nil || *rec.ContextWindow != 32768 {
				t.Fatalf("ContextWindow = %v, want 32768", rec.ContextWindow)
			}
		}
	}
	if !found {
		t.Fatal("gpt-oss:20b record not found")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestFetch_Negative(t *testing.T) {
	t.Run("tags endpoint unreachable", func(t *testing.T) {
		res := Fetch(context.Background(), "http://127.0.0.1:1")
		if res.Status != catalog.StatusFail {
			t.Fatalf("Status = %v, want fail when /api/tags is unreachable", res.Status)
		}
	})

	t.Run("malformed tags body", func(t *testing.T) {
		server := newServer(t, "{not json", samplePS, http.StatusOK)
		defer server.Close()
		res := Fetch(context.Background(), server.URL)
		if res.Status != catalog.StatusFail {
			t.Fatalf("Status = %v, want fail for a malformed /api/tags body", res.Status)
		}
	})

	t.Run("ps failure degrades, does not fail the source", func(t *testing.T) {
		server := newServer(t, sampleTags, "", http.StatusInternalServerError)
		defer server.Close()
		res := Fetch(context.Background(), server.URL)
		if res.Status != catalog.StatusOK {
			t.Fatalf("Status = %v, detail = %q, want ok with a note when only /api/ps fails", res.Status, res.Detail)
		}
		if res.Detail == "" {
			t.Fatal("Detail = empty, want a note explaining the /api/ps failure")
		}
	})
}

// TestFetch_Boundary covers the zero-installed-models response, which must
// read as a skip rather than an empty-but-ok result.
func TestFetch_Boundary(t *testing.T) {
	server := newServer(t, `{"models":[]}`, samplePS, http.StatusOK)
	defer server.Close()
	res := Fetch(context.Background(), server.URL)
	if res.Status != catalog.StatusSkip {
		t.Fatalf("Status = %v, want skip for zero installed models", res.Status)
	}
}
