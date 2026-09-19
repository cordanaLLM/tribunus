package publiccatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cordanaLLM/praetor/tribunus/catalog"
)

func TestFetch_Positive(t *testing.T) {
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleOpenRouter)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer or.Close()
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleLiteLLMPrices)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer llm.Close()

	res := Fetch(context.Background(), or.URL, llm.URL)
	if res.Status != catalog.StatusOK {
		t.Fatalf("Status = %v, detail = %q, want ok", res.Status, res.Detail)
	}
	if res.Count != 2 { // 1 openrouter + 1 litellm-prices
		t.Fatalf("Count = %d, want 2", res.Count)
	}
}

// TestFetch_Negative confirms one sub-source's total failure does not hide
// the other sub-source's records, and both are named in Detail.
func TestFetch_Negative(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleLiteLLMPrices)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer llm.Close()

	res := Fetch(context.Background(), "http://127.0.0.1:1", llm.URL)
	if res.Status != catalog.StatusOK {
		t.Fatalf("Status = %v, detail = %q, want ok (litellm-prices alone succeeded)", res.Status, res.Detail)
	}
	if res.Count != 1 {
		t.Fatalf("Count = %d, want 1", res.Count)
	}
	if !strings.Contains(res.Detail, "openrouter: fail") {
		t.Fatalf("Detail = %q, want it to name the openrouter failure", res.Detail)
	}
}

// TestFetch_Boundary covers both sub-sources failing: Fetch must report
// StatusFail rather than a false StatusOK with zero records.
func TestFetch_Boundary(t *testing.T) {
	res := Fetch(context.Background(), "http://127.0.0.1:1", "http://127.0.0.1:1")
	if res.Status != catalog.StatusFail {
		t.Fatalf("Status = %v, want fail when both sub-sources fail", res.Status)
	}
	if res.Count != 0 || len(res.Records) != 0 {
		t.Fatalf("got %d records, want 0", res.Count)
	}
}
