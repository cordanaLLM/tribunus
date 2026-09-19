// Package publiccatalog reads two published, unauthenticated model catalogs:
// OpenRouter's public models API and LiteLLM's public model price map.
// Neither call carries a credential; both are plain GETs.
//
// Both URLs and shapes below were verified live on 2026-09-18 (not taken
// from memory), per the design doc's requirement to confirm this source
// before coding:
//
//   - OpenRouter: GET https://openrouter.ai/api/v1/models -> HTTP 200,
//     {"data":[{"id":"...","name":"...","context_length":262144,
//     "architecture":{"input_modalities":["text","image"],
//     "output_modalities":["text"]},
//     "pricing":{"prompt":"0.0000025","completion":"0.0000075"}, ...}]}.
//     "pricing" values are USD per token as decimal strings.
//
//   - LiteLLM price map: GET
//     https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json
//     -> HTTP 200, a JSON object keyed by model id, e.g.
//     {"gpt-4":{"input_cost_per_token":0.00003,"output_cost_per_token":
//     0.00006,"max_input_tokens":8192,"max_output_tokens":4096,
//     "litellm_provider":"openai","mode":"chat"}, ...}. The single key
//     "sample_spec" documents the field names and is not a model; it is
//     skipped explicitly rather than treated as a broken record.
//
// OpenRouter and LiteLLM name models under incompatible id schemes (e.g.
// "openai/gpt-4" vs "gpt-4"), and slice 1 does not attempt to reconcile
// them -- that is catalog-merge/routing work the design doc marks out of
// scope. Each sub-fetch contributes its own records, tagged with its own
// provenance source ("public-catalog:openrouter" or
// "public-catalog:litellm-prices") so origin stays traceable even though
// the CLI reports both under the single "public-catalog" source name.
package publiccatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cordanaLLM/praetor/tribunus/catalog"
	"github.com/cordanaLLM/praetor/tribunus/internal/sources/httpfetch"
)

// DefaultOpenRouterURL is the OpenRouter public models endpoint verified
// live on 2026-09-18.
const DefaultOpenRouterURL = "https://openrouter.ai/api/v1/models"

const (
	// maxResponseBytes bounds each catalog response read (HISS-02).
	maxResponseBytes = 16 << 20
	// maxEntries bounds how many entries one sub-fetch turns into records
	// (HISS-02). Both catalogs run in the low thousands today.
	maxEntries = 5000
	// requestTimeout bounds each HTTP round trip (HISS-02).
	requestTimeout = 20 * time.Second
)

type openRouterArchitecture struct {
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
}

type openRouterPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

type openRouterEntry struct {
	ID            string                 `json:"id"`
	ContextLength int64                  `json:"context_length"`
	Architecture  openRouterArchitecture `json:"architecture"`
	Pricing       openRouterPricing      `json:"pricing"`
}

type openRouterResponse struct {
	Data []openRouterEntry `json:"data"`
}

// fetchOpenRouter fetches and parses the OpenRouter public models list.
func fetchOpenRouter(ctx context.Context, url string) ([]catalog.Record, error) {
	body, err := httpfetch.Get(ctx, url, requestTimeout, maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	var parsed openRouterResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("openrouter: decode response: %w", err)
	}
	if len(parsed.Data) > maxEntries {
		return nil, fmt.Errorf("openrouter: response lists more than %d models", maxEntries)
	}

	fetchedAt := time.Now().UTC()
	records := make([]catalog.Record, 0, len(parsed.Data))
	for _, e := range parsed.Data {
		if e.ID == "" {
			continue
		}
		records = append(records, openRouterRecord(e, fetchedAt))
	}
	return records, nil
}

func openRouterRecord(e openRouterEntry, fetchedAt time.Time) catalog.Record {
	rec := catalog.Record{
		ModelID:    e.ID,
		AccessPath: catalog.AccessAPI,
		Provenance: catalog.Provenance{
			Source:    "public-catalog:openrouter",
			FetchedAt: fetchedAt,
			Kind:      catalog.KindDeclared,
		},
		Absent: map[string]string{
			"limits": "OpenRouter's public models list does not report per-key rpm/tpm/daily caps",
		},
	}
	if e.ContextLength > 0 {
		cw := e.ContextLength
		rec.ContextWindow = &cw
	}
	if p, ok := perTokenToPerM(e.Pricing.Prompt); ok {
		rec.PriceInPerM = &p
	}
	if p, ok := perTokenToPerM(e.Pricing.Completion); ok {
		rec.PriceOutPerM = &p
	}
	for _, m := range e.Architecture.InputModalities {
		rec.Capabilities = append(rec.Capabilities, "in:"+m)
	}
	for _, m := range e.Architecture.OutputModalities {
		rec.Capabilities = append(rec.Capabilities, "out:"+m)
	}
	return rec
}

// perTokenToPerM converts a USD-per-token decimal string to USD-per-million-
// tokens. An empty or unparsable value is reported absent, never guessed.
func perTokenToPerM(raw string) (float64, bool) {
	if raw == "" {
		return 0, false
	}
	var perToken float64
	if _, err := fmt.Sscanf(raw, "%g", &perToken); err != nil {
		return 0, false
	}
	return perToken * 1_000_000, true
}
