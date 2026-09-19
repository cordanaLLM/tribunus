// Package litellmgateway lists models a LiteLLM gateway exposes to the
// caller's token via GET /v1/models.
//
// Verified live against https://litellm.ai.cauda.dev on 2026-09-18 with the
// operator's agent token: HTTP 200, {"data":[{"id":"cordana/auto",
// "object":"model","created":...,"owned_by":"openai"}, ...]}, 28 entries.
// /model/info returned 403 for the same token, so this source cannot see
// price/context metadata for gateway models in slice 1 (see design doc,
// sources table) -- every record it produces says so via Record.Absent.
//
// The bearer token is read from a file and used only in the Authorization
// header; it is never logged, wrapped into an error, or otherwise surfaced.
package litellmgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cordanaLLM/praetor/tribunus/catalog"
)

// SourceName identifies this source in Snapshot.SourceRuns and CLI flags.
const SourceName = "litellm-gateway"

const (
	// maxTokenFileBytes bounds the token file read (HISS-02).
	maxTokenFileBytes = 4096
	// maxResponseBytes bounds the /v1/models response read (HISS-02).
	maxResponseBytes = 8 << 20
	// maxModels bounds how many entries Fetch will turn into records (HISS-02).
	maxModels = 5000
	// requestTimeout bounds the HTTP round trip (HISS-02).
	requestTimeout = 15 * time.Second
)

// Result is one Fetch attempt's outcome.
type Result struct {
	Records []catalog.Record
	Status  catalog.Status
	Count   int
	Detail  string
}

// Fetch reads the bearer token from tokenFile and lists models at
// baseURL+"/v1/models". Any failure -- unreadable token file, network
// error, non-200 response, malformed body -- is reported as StatusFail with
// a detail string that never contains the token.
func Fetch(ctx context.Context, baseURL, tokenFile string) Result {
	token, err := readToken(tokenFile)
	if err != nil {
		return Result{Status: catalog.StatusFail, Detail: err.Error()}
	}
	if token == "" {
		return Result{Status: catalog.StatusFail, Detail: fmt.Sprintf("token file %s is empty", tokenFile)}
	}

	models, err := listModels(ctx, baseURL, token)
	if err != nil {
		return Result{Status: catalog.StatusFail, Detail: err.Error()}
	}
	if len(models) == 0 {
		return Result{Status: catalog.StatusSkip, Detail: fmt.Sprintf("%s/v1/models returned no models", baseURL)}
	}

	fetchedAt := time.Now().UTC()
	records := make([]catalog.Record, 0, len(models))
	for _, m := range models {
		records = append(records, toRecord(m, fetchedAt))
	}
	return Result{Records: records, Status: catalog.StatusOK, Count: len(records)}
}

// readToken reads and trims tokenFile. Errors name the path, never contents.
func readToken(tokenFile string) (token string, err error) {
	// #nosec G304 -- tokenFile is a CLI flag the operator supplies directly, the
	// same trust level as the repository's own config.go manifest-path convention.
	f, err := os.Open(tokenFile)
	if err != nil {
		return "", fmt.Errorf("litellm-gateway: open token file %s: %w", tokenFile, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("litellm-gateway: stat token file %s: %w", tokenFile, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("litellm-gateway: token file %s is not a regular file", tokenFile)
	}
	if info.Size() > maxTokenFileBytes {
		return "", fmt.Errorf("litellm-gateway: token file %s exceeds %d bytes", tokenFile, maxTokenFileBytes)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxTokenFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("litellm-gateway: read token file %s: %w", tokenFile, err)
	}
	if len(data) > maxTokenFileBytes {
		return "", fmt.Errorf("litellm-gateway: token file %s exceeds %d bytes", tokenFile, maxTokenFileBytes)
	}
	return strings.TrimSpace(string(data)), nil
}

type modelEntry struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
}

type modelsResponse struct {
	Data []modelEntry `json:"data"`
}

// listModels performs the bounded GET and decodes the response body.
func listModels(ctx context.Context, baseURL, token string) (entries []modelEntry, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("litellm-gateway: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("litellm-gateway: request %s/v1/models: %w", baseURL, err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("litellm-gateway: %s/v1/models returned HTTP %d", baseURL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("litellm-gateway: read response body: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("litellm-gateway: response exceeds %d bytes", maxResponseBytes)
	}

	var parsed modelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("litellm-gateway: decode response: %w", err)
	}
	if len(parsed.Data) > maxModels {
		return nil, fmt.Errorf("litellm-gateway: response lists more than %d models", maxModels)
	}
	return parsed.Data, nil
}

func toRecord(m modelEntry, fetchedAt time.Time) catalog.Record {
	return catalog.Record{
		ModelID:    m.ID,
		Provider:   m.OwnedBy,
		AccessPath: catalog.AccessGateway,
		Provenance: catalog.Provenance{
			Source:    SourceName,
			FetchedAt: fetchedAt,
			Kind:      catalog.KindMeasured,
		},
		Absent: map[string]string{
			"context_window":  "gateway /v1/models does not expose it; /model/info returned 403 for the agent token",
			"price_in_per_m":  "gateway /v1/models does not expose it; /model/info returned 403 for the agent token",
			"price_out_per_m": "gateway /v1/models does not expose it; /model/info returned 403 for the agent token",
			"limits":          "gateway /v1/models does not expose rpm/tpm/daily caps",
		},
	}
}
