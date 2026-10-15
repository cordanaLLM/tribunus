// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

package admission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tekmora/epitrop/internal/sources/httpfetch"
)

const (
	defaultProbeTimeout     = 30 * time.Second
	maxProbeResponseBytes   = 1 << 20
	chatCompletionsEndpoint = "/v1/chat/completions"
)

// GatewayProber probes an OpenAI-compatible LiteLLM gateway using chat completions.
type GatewayProber struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	Messages  []chatMessage `json:"messages"`
}

type chatCompletionResponse struct {
	Model   string            `json:"model"`
	Choices []json.RawMessage `json:"choices"`
}

// Probe sends a ping chat completion to verify that the router alias is healthy.
func (p *GatewayProber) Probe(ctx context.Context, alias string) (ProbeResult, error) {
	if ctx == nil {
		return ProbeResult{}, errors.New("admission: nil context")
	}

	endpoint := strings.TrimRight(p.BaseURL, "/") + chatCompletionsEndpoint
	req, err := buildProbeRequest(ctx, endpoint, alias, p.Token)
	if err != nil {
		return ProbeResult{}, p.sanitizeError(err)
	}

	timeout := probeTimeoutFromContext(ctx)
	statusCode, header, body, err := httpfetch.Do(ctx, p.Client, req, timeout, maxProbeResponseBytes)
	if err != nil {
		return ProbeResult{}, p.sanitizeError(fmt.Errorf("admission: probe request %s: %w", endpoint, err))
	}

	if statusCode != http.StatusOK {
		return ProbeResult{}, fmt.Errorf("admission: probe returned HTTP %d", statusCode)
	}

	res, err := parseProbeResponse(body, header)
	if err != nil {
		return ProbeResult{}, p.sanitizeError(err)
	}
	return res, nil
}

func buildProbeRequest(ctx context.Context, endpoint, alias, token string) (*http.Request, error) {
	payload, err := json.Marshal(chatCompletionRequest{
		Model:     alias,
		MaxTokens: 1,
		Messages: []chatMessage{
			{Role: "user", Content: "ping"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("admission: marshal probe request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("admission: build probe request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func parseProbeResponse(body []byte, header http.Header) (ProbeResult, error) {
	var resp chatCompletionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ProbeResult{}, fmt.Errorf("admission: decode probe response: %w", err)
	}

	if resp.Model == "" {
		return ProbeResult{}, errors.New("admission: probe response missing model")
	}
	if len(resp.Choices) == 0 {
		return ProbeResult{}, errors.New("admission: probe response choices array is empty")
	}

	return ProbeResult{
		Model:        resp.Model,
		DeploymentID: header.Get("x-litellm-model-id"),
	}, nil
}

func probeTimeoutFromContext(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining
		}
	}
	return defaultProbeTimeout
}

func (p *GatewayProber) sanitizeError(err error) error {
	if err == nil || p.Token == "" {
		return err
	}
	if strings.Contains(err.Error(), p.Token) {
		sanitized := strings.ReplaceAll(err.Error(), p.Token, "[REDACTED]")
		return errors.New(sanitized)
	}
	return err
}
