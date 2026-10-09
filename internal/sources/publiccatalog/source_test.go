package publiccatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cordanaLLM/tribunus/catalog"
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
	if res.Status != catalog.StatusDegraded {
		t.Fatalf("Status = %v, detail = %q, want degraded (litellm-prices alone succeeded)", res.Status, res.Detail)
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

func TestFetch_ReportsMalformedLiteLLMEntries(t *testing.T) {
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`)) //nolint:errcheck // test server response
	}))
	defer or.Close()
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"vendor-a/model-1":{"input_cost_per_token":0.00001},"vendor-b/model-2":{"max_input_tokens":"broken"}}`)) //nolint:errcheck // test server response
	}))
	defer llm.Close()

	res := Fetch(context.Background(), or.URL, llm.URL)
	if res.Status != catalog.StatusDegraded {
		t.Fatalf("Status = %v, detail = %q, want degraded", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "malformed=1") {
		t.Fatalf("Detail = %q, want malformed=1", res.Detail)
	}
}

func TestFetch_EmptyPublicCatalogResponsesFail(t *testing.T) {
	t.Run("openrouter data list", func(t *testing.T) {
		or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data":[]}`)) //nolint:errcheck // test server response
		}))
		defer or.Close()
		llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(sampleLiteLLMPrices)) //nolint:errcheck // test server response
		}))
		defer llm.Close()

		res := Fetch(context.Background(), or.URL, llm.URL)
		if res.Status != catalog.StatusDegraded {
			t.Fatalf("Status = %v, detail = %q, want degraded", res.Status, res.Detail)
		}
		if !strings.Contains(res.Detail, "openrouter: empty response") {
			t.Fatalf("Detail = %q, want openrouter empty response", res.Detail)
		}
	})

	t.Run("litellm price map only sample_spec", func(t *testing.T) {
		or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(sampleOpenRouter)) //nolint:errcheck // test server response
		}))
		defer or.Close()
		llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"sample_spec":{"litellm_provider":"doc"}}`)) //nolint:errcheck // test server response
		}))
		defer llm.Close()

		res := Fetch(context.Background(), or.URL, llm.URL)
		if res.Status != catalog.StatusDegraded {
			t.Fatalf("Status = %v, detail = %q, want degraded", res.Status, res.Detail)
		}
		if !strings.Contains(res.Detail, "litellm-prices: empty response") {
			t.Fatalf("Detail = %q, want litellm-prices empty response", res.Detail)
		}
	})
}

func TestLiteLLMDetailReportsMalformedEntries(t *testing.T) {
	got := liteLLMDetail(liteLLMPriceResult{
		Records:   make([]catalog.Record, 1),
		Malformed: 1,
	})
	if !strings.Contains(got, "malformed=1") {
		t.Fatalf("liteLLMDetail() = %q, want malformed=1", got)
	}
}
