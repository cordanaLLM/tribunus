// Package httpfetch performs a bounded, plain GET request. It is the one
// shared implementation of "request a URL, cap the response, report the
// first failure" that ollamalocal and publiccatalog both need: extracted
// after praetor's own dedupe scan flagged the two sources' identical
// boundedGet functions (HISS-19, one behavior, one implementation).
package httpfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Get performs a GET against url with a per-call deadline (timeout) and a
// bounded response read (maxBytes), so a source's HTTP call can neither hang
// nor buffer an unbounded response (HISS-02). Callers choose their own
// timeout and byte cap, since sources genuinely differ here (a local Ollama
// daemon warrants a shorter timeout than a public catalog fetch).
func Get(ctx context.Context, url string, timeout time.Duration, maxBytes int) (body []byte, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", url, err)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", url, err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read response body from %s: %w", url, err)
	}
	if len(body) > maxBytes {
		return nil, fmt.Errorf("%s response exceeds %d bytes", url, maxBytes)
	}
	return body, nil
}
