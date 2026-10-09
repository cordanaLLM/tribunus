// Package httpfetch performs a bounded, plain GET request. It is the one
// shared implementation of "request a URL, cap the response, report the
// first failure" that every catalog source needs: extracted after praetor's
// own dedupe scan flagged the sources' identical boundedGet functions
// (HISS-19, one behavior, one implementation).
//
// The client and the byte cap come from golusoris httpx/client
// (docs/adr/0004-golusoris-pinned-base.md): this package only adds the
// per-call deadline, the status check and the error context the sources
// report.
package httpfetch

import (
	"context"
	"fmt"
	"net/http"
	"time"

	gerrors "github.com/golusoris/golusoris/core/errors"
	"github.com/golusoris/golusoris/httpx/client"
)

// clientName labels the outbound client in golusoris breaker logs and OTel
// spans.
const clientName = "tribunus.httpfetch"

// Get performs a GET against url with a per-call deadline (timeout) and a
// bounded response read (maxBytes), so a source's HTTP call can neither hang
// nor buffer an unbounded response (HISS-02). Callers choose their own
// timeout and byte cap, since sources genuinely differ here (a local Ollama
// daemon warrants a shorter timeout than a public catalog fetch). A body
// over maxBytes fails with an error wrapping client.ErrBodyTooLarge; a
// non-positive maxBytes is refused.
func Get(ctx context.Context, url string, timeout time.Duration, maxBytes int) ([]byte, error) {
	return GetWithHeader(ctx, url, nil, timeout, maxBytes)
}

// GetWithHeader is Get with extra request headers, for a source that must
// authenticate (litellm-gateway's bearer token). Header values are sent,
// never logged or wrapped into an error.
func GetWithHeader(ctx context.Context, url string, header http.Header, timeout time.Duration, maxBytes int) (body []byte, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", url, err)
	}
	if header != nil {
		req.Header = header.Clone()
	}
	resp, err := client.New(client.Options{Name: clientName, Timeout: timeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", url, err)
	}
	// Closure rather than a bare defer so the bodyclose linter sees the body
	// reach a closer.
	defer func() { gerrors.CloseJoin(resp.Body, &err, "close response body from "+url) }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	body, err = client.ReadAllBounded(resp.Body, int64(maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read response body from %s: %w", url, err)
	}
	return body, nil
}
