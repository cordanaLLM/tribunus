package publiccatalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/cordanaLLM/praetor/tribunus/catalog"
)

// SourceName identifies this source in Snapshot.SourceRuns and CLI flags.
// It covers two independent sub-fetches (OpenRouter, LiteLLM prices); see
// package doc for why they are not reported as separate CLI sources.
const SourceName = "public-catalog"

// Result is one Fetch attempt's outcome.
type Result struct {
	Records []catalog.Record
	Status  catalog.Status
	Count   int
	Detail  string
}

// Fetch runs both public-catalog sub-fetches and merges them. Each fails
// independently: one succeeding while the other fails still returns
// StatusOK with that sub-fetch's records and a Detail line naming the
// other's failure, because "OpenRouter is down" should never hide LiteLLM's
// price map (or vice versa).
func Fetch(ctx context.Context, openRouterURL, liteLLMPriceMapURL string) Result {
	var records []catalog.Record
	var notes []string

	orRecords, orErr := fetchOpenRouter(ctx, openRouterURL)
	if orErr != nil {
		notes = append(notes, "openrouter: fail: "+orErr.Error())
	} else {
		notes = append(notes, fmt.Sprintf("openrouter: ok (%d)", len(orRecords)))
		records = append(records, orRecords...)
	}

	llmRecords, llmErr := fetchLiteLLMPrices(ctx, liteLLMPriceMapURL)
	if llmErr != nil {
		notes = append(notes, "litellm-prices: fail: "+llmErr.Error())
	} else {
		notes = append(notes, fmt.Sprintf("litellm-prices: ok (%d)", len(llmRecords)))
		records = append(records, llmRecords...)
	}

	detail := strings.Join(notes, "; ")
	if orErr != nil && llmErr != nil {
		return Result{Status: catalog.StatusFail, Detail: detail}
	}
	if len(records) == 0 {
		return Result{Status: catalog.StatusSkip, Detail: detail}
	}
	return Result{Records: records, Status: catalog.StatusOK, Count: len(records), Detail: detail}
}
