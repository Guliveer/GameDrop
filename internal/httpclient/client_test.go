package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testClient() *Client {
	return New(Options{
		Timeout:      2 * time.Second,
		MaxRetries:   2,
		UserAgent:    "GameDropTest/1.0",
		BaseDelay:    time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		MaxBodyBytes: 1 << 20,
	})
}

func TestRetriesOn429AndHonorsRetryAfter(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	var payload struct {
		OK bool `json:"ok"`
	}
	if err := testClient().GetJSON(context.Background(), srv.URL, &payload); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if !payload.OK {
		t.Error("payload not decoded")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
}

func TestRetriesOn503ThenFails(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := testClient().Get(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for an exhausted retry budget")
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("requests = %d, want 3 (initial + 2 retries)", got)
	}
}

func TestDoesNotRetryClientError(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	resp, err := testClient().Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("requests = %d, want 1 (4xx must not retry)", got)
	}

	// GetJSON converts the unexpected status into a descriptive error.
	err = testClient().GetJSON(context.Background(), srv.URL, &struct{}{})
	var statusErr *StatusError
	if !asStatusError(err, &statusErr) {
		t.Fatalf("expected StatusError, got %T: %v", err, err)
	}
	if statusErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d", statusErr.StatusCode)
	}
}

func TestDoesNotRetryPost(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := testClient().Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("requests = %d, want 1 (POST must not be retried)", got)
	}
}

func TestSetsUserAgent(t *testing.T) {
	var agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent = r.Header.Get("User-Agent")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	if err := testClient().GetJSON(context.Background(), srv.URL, &struct{}{}); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if agent != "GameDropTest/1.0" {
		t.Errorf("User-Agent = %q", agent)
	}
}

func TestContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := testClient().Get(ctx, srv.URL); err == nil {
		t.Fatal("expected a context error")
	}
}

func TestRedactsQueryInErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	err := testClient().GetJSON(context.Background(), srv.URL+"?token=secret", &struct{}{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if contains(err.Error(), "secret") {
		t.Errorf("error leaks a query parameter: %v", err)
	}
}

func asStatusError(err error, target **StatusError) bool {
	for err != nil {
		if se, ok := err.(*StatusError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
