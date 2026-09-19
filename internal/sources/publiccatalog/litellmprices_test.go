package publiccatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sampleLiteLLMPrices mirrors what GET on the real price map returned live
// on 2026-09-18: sample_spec documents max_input_tokens as a *string*
// ("max input tokens, if the provider specifies it...") in a field every
// real model entry types as a number. A naive single-shot decode into
// map[string]litellmPriceEntry aborts on that mismatch before it reaches
// gpt-4 -- this fixture reproduces exactly that shape.
const sampleLiteLLMPrices = `{
  "sample_spec": {"input_cost_per_token": 0.0, "max_input_tokens": "max input tokens, if the provider specifies it. if not default to max_tokens", "litellm_provider": "one of https://docs.litellm.ai/docs/providers"},
  "gpt-4": {"input_cost_per_token": 0.00003, "output_cost_per_token": 0.00006, "max_input_tokens": 8192, "litellm_provider": "openai", "mode": "chat"}
}`

func TestFetchLiteLLMPrices_Positive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleLiteLLMPrices)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer server.Close()

	records, err := fetchLiteLLMPrices(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchLiteLLMPrices() = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1 (sample_spec skipped)", len(records))
	}
	rec := records[0]
	if rec.ModelID != "gpt-4" {
		t.Fatalf("ModelID = %q, want gpt-4", rec.ModelID)
	}
	if rec.PriceInPerM == nil || *rec.PriceInPerM != 30 {
		t.Fatalf("PriceInPerM = %v, want 30", rec.PriceInPerM)
	}
	if rec.ContextWindow == nil || *rec.ContextWindow != 8192 {
		t.Fatalf("ContextWindow = %v, want 8192", rec.ContextWindow)
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestFetchLiteLLMPrices_Negative(t *testing.T) {
	t.Run("unreachable", func(t *testing.T) {
		if _, err := fetchLiteLLMPrices(context.Background(), "http://127.0.0.1:1"); err == nil {
			t.Fatal("fetchLiteLLMPrices() = nil error, want failure for an unreachable host")
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("[]")) //nolint:errcheck // an array, not the expected object; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
		}))
		defer server.Close()
		if _, err := fetchLiteLLMPrices(context.Background(), server.URL); err == nil {
			t.Fatal("fetchLiteLLMPrices() = nil error, want failure for a body that is not an object")
		}
	})
}

// TestFetchLiteLLMPrices_OneMalformedEntrySkipped confirms a single model
// entry with a field the parser cannot decode is dropped on its own,
// without discarding every other model in the same response -- the general
// case behind the sample_spec fixture above.
func TestFetchLiteLLMPrices_OneMalformedEntrySkipped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"broken-model": {"max_input_tokens": "not a number"}, "gpt-4": {"input_cost_per_token": 0.00003, "litellm_provider": "openai"}}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer server.Close()

	records, err := fetchLiteLLMPrices(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchLiteLLMPrices() = %v", err)
	}
	if len(records) != 1 || records[0].ModelID != "gpt-4" {
		t.Fatalf("records = %+v, want only gpt-4 (broken-model skipped)", records)
	}
}

// TestFetchLiteLLMPrices_Boundary covers max_input_tokens vs the legacy
// max_tokens fallback: when max_input_tokens is absent, max_tokens must be
// used instead, matching the price map's own documented fallback order.
func TestFetchLiteLLMPrices_Boundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"legacy-model": {"max_tokens": 4096, "litellm_provider": "openai"}}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer server.Close()

	records, err := fetchLiteLLMPrices(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchLiteLLMPrices() = %v", err)
	}
	if len(records) != 1 || records[0].ContextWindow == nil || *records[0].ContextWindow != 4096 {
		t.Fatalf("records = %+v, want one record with ContextWindow=4096 from the max_tokens fallback", records)
	}
}
