package publiccatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const sampleOpenRouter = `{"data":[{"id":"openai/gpt-4","name":"GPT-4","context_length":8192,"architecture":{"input_modalities":["text"],"output_modalities":["text"]},"pricing":{"prompt":"0.00003","completion":"0.00006"}},{"id":"","name":"skip me, no id"}]}`

func TestFetchOpenRouter_Positive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleOpenRouter)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer server.Close()

	records, err := fetchOpenRouter(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchOpenRouter() = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1 (entry with empty id skipped)", len(records))
	}
	rec := records[0]
	if rec.ModelID != "openai/gpt-4" {
		t.Fatalf("ModelID = %q", rec.ModelID)
	}
	if rec.PriceInPerM == nil || *rec.PriceInPerM != 30 {
		t.Fatalf("PriceInPerM = %v, want 30 (0.00003 * 1e6)", rec.PriceInPerM)
	}
	if rec.PriceOutPerM == nil || *rec.PriceOutPerM != 60 {
		t.Fatalf("PriceOutPerM = %v, want 60", rec.PriceOutPerM)
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestFetchOpenRouter_Negative(t *testing.T) {
	t.Run("unreachable", func(t *testing.T) {
		if _, err := fetchOpenRouter(context.Background(), "http://127.0.0.1:1"); err == nil {
			t.Fatal("fetchOpenRouter() = nil error, want failure for an unreachable host")
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("{not json")) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
		}))
		defer server.Close()
		if _, err := fetchOpenRouter(context.Background(), server.URL); err == nil {
			t.Fatal("fetchOpenRouter() = nil error, want failure for a malformed body")
		}
	})
}

// TestFetchOpenRouter_Boundary covers a pricing value the parser cannot
// read: it must be dropped as absent, not crash or fabricate a price.
func TestFetchOpenRouter_Boundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"weird/model","pricing":{"prompt":"not-a-number","completion":""}}]}`)) //nolint:errcheck // test httptest server response; a write failure here would fail the test's own HTTP round trip, not silently corrupt anything
	}))
	defer server.Close()

	records, err := fetchOpenRouter(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchOpenRouter() = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if records[0].PriceInPerM != nil || records[0].PriceOutPerM != nil {
		t.Fatalf("PriceInPerM/PriceOutPerM = %v/%v, want both nil for unparsable pricing", records[0].PriceInPerM, records[0].PriceOutPerM)
	}
}
