// Package httpclient provides the shared HTTP client used by every store
// adapter and by the Telegram notifier. It centralizes timeouts, connection
// pooling, retries with exponential backoff and jitter, Retry-After handling,
// and response body limits.
package httpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultMaxBodyBytes bounds how much of a response body GameDrop will read.
const DefaultMaxBodyBytes = 8 << 20 // 8 MiB

// Options configures a Client.
type Options struct {
	// Timeout applies to a single HTTP request.
	Timeout time.Duration
	// MaxRetries is the number of retries after the first attempt.
	MaxRetries int
	// UserAgent identifies GameDrop to servers.
	UserAgent string
	// MaxBodyBytes overrides DefaultMaxBodyBytes when non-zero.
	MaxBodyBytes int64
	// BaseDelay is the first backoff delay; defaults to 500ms.
	BaseDelay time.Duration
	// MaxDelay caps a single backoff delay; defaults to 30s.
	MaxDelay time.Duration
}

// Client is a reusable, retrying HTTP client.
type Client struct {
	http         *http.Client
	maxRetries   int
	userAgent    string
	maxBodyBytes int64
	baseDelay    time.Duration
	maxDelay     time.Duration
}

// New builds a Client with connection pooling and a retrying transport policy.
func New(opts Options) *Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	baseDelay := opts.BaseDelay
	if baseDelay <= 0 {
		baseDelay = 500 * time.Millisecond
	}
	maxDelay := opts.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultMaxBodyBytes
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
	}

	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("httpclient: stopped after %d redirects", len(via))
				}
				return nil
			},
		},
		maxRetries:   max(0, opts.MaxRetries),
		userAgent:    opts.UserAgent,
		maxBodyBytes: maxBody,
		baseDelay:    baseDelay,
		maxDelay:     maxDelay,
	}
}

// StatusError is returned when a response has an unexpected status code.
type StatusError struct {
	StatusCode int
	URL        string
	Body       string
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 200 {
		body = body[:200] + "..."
	}
	if body == "" {
		return fmt.Sprintf("httpclient: %s returned status %d", e.URL, e.StatusCode)
	}
	return fmt.Sprintf("httpclient: %s returned status %d: %s", e.URL, e.StatusCode, body)
}

// Temporary reports whether the failure is worth retrying.
func (e *StatusError) Temporary() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// Get performs a GET request with retries and returns the response. The caller
// must close the body.
func (c *Client) Get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("httpclient: build request: %w", err)
	}
	return c.Do(ctx, req)
}

// GetJSON performs a GET request, validates the status, and decodes a JSON body
// into dst. Large bodies are truncated rather than exhausting memory.
func (c *Client) GetJSON(ctx context.Context, rawURL string, dst any) error {
	resp, err := c.Get(ctx, rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.statusError(resp)
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, c.maxBodyBytes))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("httpclient: decode %s: %w", redactURL(rawURL), err)
	}
	return nil
}

// Do performs a request, retrying transient failures. Non-idempotent methods
// (anything other than GET/HEAD/PUT/DELETE/OPTIONS) are never retried
// automatically; callers that need retry semantics for POST implement their own
// bounded policy (the Telegram notifier does).
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" && c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.8")
	}

	// Non-idempotent requests are sent exactly once and the response is handed
	// back untouched so callers (for example the Telegram sender) can read the
	// status and body themselves. They must implement their own bounded retry.
	if !isIdempotent(req.Method) {
		return c.http.Do(req)
	}

	attempts := 1 + c.maxRetries

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		resp, err := c.http.Do(req)
		if err == nil && !shouldRetryStatus(resp.StatusCode) {
			// Non-retryable statuses are returned with an intact body so the
			// caller can report or inspect them.
			return resp, nil
		}

		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !isRetryableNetError(err) {
				return nil, err
			}
			lastErr = err
		} else {
			// Do not read the body yet: a retry needs it intact for draining,
			// and the final attempt reports it below.
			lastErr = &StatusError{StatusCode: resp.StatusCode, URL: redactURL(req.URL.String())}
		}

		if attempt == attempts {
			if resp != nil {
				statusErr := c.statusError(resp)
				resp.Body.Close()
				return nil, statusErr
			}
			return nil, lastErr
		}

		delay := c.backoff(attempt)
		if resp != nil {
			if ra, ok := retryAfter(resp); ok {
				delay = ra
			}
			// Drain a bounded amount so the connection can be reused.
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}

// statusError builds a StatusError and consumes a small part of the body so it
// can be included in logs.
func (c *Client) statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	err := &StatusError{
		StatusCode: resp.StatusCode,
		URL:        redactURL(resp.Request.URL.String()),
		Body:       string(body),
	}
	if ra, ok := retryAfter(resp); ok {
		err.RetryAfter = ra
	}
	return err
}

// backoff returns an exponentially growing delay with jitter.
func (c *Client) backoff(attempt int) time.Duration {
	delay := c.baseDelay << uint(attempt-1)
	if delay > c.maxDelay || delay <= 0 {
		delay = c.maxDelay
	}
	jitter := time.Duration(rand.Int64N(int64(delay/2) + 1))
	return delay/2 + jitter
}

func isIdempotent(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

func shouldRetryStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func isRetryableNetError(err error) bool {
	var netErr net.Error
	if ok := asNetError(err, &netErr); ok {
		return true
	}
	// A closed connection mid-response is transient.
	return strings.Contains(err.Error(), "connection reset") ||
		strings.Contains(err.Error(), "unexpected EOF")
}

func asNetError(err error, target *net.Error) bool {
	for err != nil {
		if ne, ok := err.(net.Error); ok {
			*target = ne
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// retryAfter parses the Retry-After header, in seconds or HTTP date form.
func retryAfter(resp *http.Response) (time.Duration, bool) {
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// redactURL strips query strings, which can contain store tokens, before a URL
// reaches a log line or an error message.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery = ""
	return u.String()
}
