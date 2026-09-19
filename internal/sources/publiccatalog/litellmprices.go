package publiccatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cordanaLLM/praetor/tribunus/catalog"
	"github.com/cordanaLLM/praetor/tribunus/internal/sources/httpfetch"
)

// DefaultLiteLLMPriceMapURL is LiteLLM's published, unauthenticated model
// price/context map, verified live on 2026-09-18.
const DefaultLiteLLMPriceMapURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// litellmSampleSpecKey is the one key in the price map that documents the
// schema rather than naming a model; it is skipped, not parsed as a record.
const litellmSampleSpecKey = "sample_spec"

type litellmPriceEntry struct {
	InputCostPerToken  *float64 `json:"input_cost_per_token"`
	OutputCostPerToken *float64 `json:"output_cost_per_token"`
	MaxInputTokens     *int64   `json:"max_input_tokens"`
	MaxTokens          *int64   `json:"max_tokens"`
	LiteLLMProvider    string   `json:"litellm_provider"`
	Mode               string   `json:"mode"`
}

// fetchLiteLLMPrices fetches and parses LiteLLM's public price map.
//
// The map is decoded key-by-key rather than in one map[string]litellmPriceEntry
// shot: verified live on 2026-09-18, the map's own "sample_spec" key documents
// each field with a human-readable *string* ("max input tokens, if the
// provider specifies it...") in a field the model entries type as an int64.
// A single json.Unmarshal into a typed map aborts on that first mismatch and
// discards every real model with it. Decoding entry-by-entry keeps that one
// documentation key (and any one malformed model entry) from taking down
// every other model in the same response.
func fetchLiteLLMPrices(ctx context.Context, url string) ([]catalog.Record, error) {
	body, err := httpfetch.Get(ctx, url, requestTimeout, maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("litellm-prices: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("litellm-prices: decode response: %w", err)
	}
	if len(raw) > maxEntries {
		return nil, fmt.Errorf("litellm-prices: response lists more than %d models", maxEntries)
	}

	fetchedAt := time.Now().UTC()
	records := make([]catalog.Record, 0, len(raw))
	for id, msg := range raw {
		if id == "" || id == litellmSampleSpecKey {
			continue
		}
		var e litellmPriceEntry
		if err := json.Unmarshal(msg, &e); err != nil {
			continue // one malformed entry does not cost every other model its record
		}
		records = append(records, litellmPriceRecord(id, e, fetchedAt))
	}
	return records, nil
}

func litellmPriceRecord(id string, e litellmPriceEntry, fetchedAt time.Time) catalog.Record {
	rec := catalog.Record{
		ModelID:    id,
		Provider:   e.LiteLLMProvider,
		AccessPath: catalog.AccessAPI,
		Provenance: catalog.Provenance{
			Source:    "public-catalog:litellm-prices",
			FetchedAt: fetchedAt,
			Kind:      catalog.KindDeclared,
		},
		Absent: map[string]string{
			"limits": "LiteLLM's public price map does not report per-key rpm/tpm/daily caps",
		},
	}
	if e.InputCostPerToken != nil {
		p := *e.InputCostPerToken * 1_000_000
		rec.PriceInPerM = &p
	}
	if e.OutputCostPerToken != nil {
		p := *e.OutputCostPerToken * 1_000_000
		rec.PriceOutPerM = &p
	}
	cw := contextWindowFrom(e)
	if cw != nil {
		rec.ContextWindow = cw
	}
	if e.Mode != "" {
		rec.Capabilities = []string{"mode:" + e.Mode}
	}
	return rec
}

// contextWindowFrom picks max_input_tokens when present, falling back to
// the legacy max_tokens field, matching LiteLLM's own documented fallback
// order (see the price map's "sample_spec" entry, verified live 2026-09-18).
func contextWindowFrom(e litellmPriceEntry) *int64 {
	if e.MaxInputTokens != nil {
		return e.MaxInputTokens
	}
	return e.MaxTokens
}
