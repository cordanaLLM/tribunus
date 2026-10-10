package releasewatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cordanaLLM/tribunus/internal/sources/httpfetch"
)

type ClientOptions struct {
	GitHubBase      string
	HuggingFaceBase string
	NtfyBase        string
	LookupEnv       func(string) (string, bool)
	RateLimitFloor  int
	Stdout          io.Writer
	HTTPClient      *http.Client
}

type Client struct {
	gitHubBaseURL      *url.URL
	huggingFaceBaseURL *url.URL
	ntfyBaseURL        *url.URL
	lookupEnv          func(string) (string, bool)
	rateLimitFloor     int
	stdout             io.Writer
	httpClient         *http.Client

	mu        sync.Mutex
	throttled bool
}

func parseBaseURL(raw string, defaultURL string, name string) (*url.URL, error) {
	if raw == "" {
		raw = defaultURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s base %q: %w", name, raw, err)
	}
	return u, nil
}

func setupHTTPClient(opts ClientOptions, ghURL *url.URL) *http.Client {
	var baseClient *http.Client
	if opts.HTTPClient != nil {
		c := *opts.HTTPClient
		baseClient = &c
	} else {
		baseClient = &http.Client{Timeout: 30 * time.Second}
	}
	origCheckRedirect := baseClient.CheckRedirect
	baseClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Scheme != ghURL.Scheme || !strings.EqualFold(req.URL.Host, ghURL.Host) {
			req.Header.Del("Authorization")
		}
		if origCheckRedirect != nil {
			return origCheckRedirect(req, via)
		}
		return nil
	}
	return baseClient
}

func NewClient(opts ClientOptions) (*Client, error) {
	ghURL, err := parseBaseURL(opts.GitHubBase, DefaultGitHubAPIBase, "github")
	if err != nil {
		return nil, err
	}
	hfURL, err := parseBaseURL(opts.HuggingFaceBase, DefaultHuggingFaceAPIBase, "huggingface")
	if err != nil {
		return nil, err
	}
	ntfyURL, err := parseBaseURL(opts.NtfyBase, DefaultNtfyBase, "ntfy")
	if err != nil {
		return nil, err
	}

	lookupEnv := opts.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	floor := opts.RateLimitFloor
	if floor <= 0 {
		floor = 100
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	return &Client{
		gitHubBaseURL:      ghURL,
		huggingFaceBaseURL: hfURL,
		ntfyBaseURL:        ntfyURL,
		lookupEnv:          lookupEnv,
		rateLimitFloor:     floor,
		stdout:             stdout,
		httpClient:         setupHTTPClient(opts, ghURL),
	}, nil
}

func (c *Client) GitHubBaseURL() string {
	return c.gitHubBaseURL.String()
}

func (c *Client) HuggingFaceBaseURL() string {
	return c.huggingFaceBaseURL.String()
}

func (c *Client) NtfyBaseURL() string {
	return c.ntfyBaseURL.String()
}

func (c *Client) GitHubToken() string {
	token, ok := c.lookupEnv("GITHUB_TOKEN")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

func (c *Client) IsThrottled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.throttled
}

// ResetThrottle clears the rate-limit throttle at the start of a pass.
func (c *Client) ResetThrottle() {
	c.mu.Lock()
	c.throttled = false
	c.mu.Unlock()
}

func (c *Client) isGitHubBase(target *url.URL) bool {
	return target.Scheme == c.gitHubBaseURL.Scheme && strings.EqualFold(target.Host, c.gitHubBaseURL.Host)
}

func (c *Client) DoRequest(ctx context.Context, req *http.Request, timeout time.Duration, maxBytes int) (int, http.Header, []byte, error) {
	c.mu.Lock()
	if c.throttled && c.isGitHubBase(req.URL) {
		c.mu.Unlock()
		return 0, nil, nil, ErrThrottled
	}
	c.mu.Unlock()

	if c.isGitHubBase(req.URL) {
		token := c.GitHubToken()
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	} else {
		req.Header.Del("Authorization")
	}

	status, header, body, err := httpfetch.Do(ctx, c.httpClient, req, timeout, maxBytes)
	if err != nil {
		return status, header, body, err
	}

	if c.isGitHubBase(req.URL) {
		c.checkRateLimit(header)
	}

	return status, header, body, nil
}

func (c *Client) checkRateLimit(header http.Header) {
	remainingStr := header.Get("X-RateLimit-Remaining")
	if remainingStr == "" {
		return
	}
	remaining, err := strconv.Atoi(remainingStr)
	if err != nil || remaining >= c.rateLimitFloor {
		return
	}
	resetStr := header.Get("X-RateLimit-Reset")
	var resetTime time.Time
	if resetSec, parseErr := strconv.ParseInt(resetStr, 10, 64); parseErr == nil {
		resetTime = time.Unix(resetSec, 0).UTC()
	} else {
		resetTime = time.Now().UTC()
	}
	c.mu.Lock()
	c.throttled = true
	c.mu.Unlock()
	c.Logf("release-watch: throttled remaining=%d reset=%s\n", remaining, resetTime.Format(time.RFC3339))
}

func (c *Client) Logf(format string, a ...any) {
	if _, err := fmt.Fprintf(c.stdout, format, a...); err != nil {
		return
	}
}
