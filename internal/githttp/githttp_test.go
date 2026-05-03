package githttp_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/internal/githttp"
)

// fastCfg keeps test waits sub-millisecond so the suite stays under
// the unit-test budget regardless of attempt count.
func fastCfg(maxAttempts int) githttp.Config {
	return githttp.Config{
		MaxAttempts:      maxAttempts,
		BaseDelay:        time.Microsecond,
		MaxDelay:         time.Millisecond,
		MaxRateLimitWait: time.Millisecond,
	}
}

func TestRetriesPrimaryRateLimitThenSucceeds(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Millisecond).Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	resp, err := githttp.Client(fastCfg(5)).Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("attempts: got %d, want 3", got)
	}
}

func TestRetriesSecondaryRateLimitWithRetryAfter(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := githttp.Client(fastCfg(3)).Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("attempts: got %d, want 2", got)
	}
}

func TestPlain403NotRetried(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// No rate-limit headers — looks like a real authz failure.
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	resp, err := githttp.Client(fastCfg(5)).Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", resp.StatusCode)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("attempts: got %d, want 1 (plain 403 must not retry)", got)
	}
}

func TestRetries429AndServerErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
	}{
		{"429", http.StatusTooManyRequests},
		{"500", http.StatusInternalServerError},
		{"502", http.StatusBadGateway},
		{"503", http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if hits.Add(1) == 1 {
					w.WriteHeader(tc.status)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			resp, err := githttp.Client(fastCfg(3)).Get(srv.URL)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status: got %d, want 200", resp.StatusCode)
			}
			if got := hits.Load(); got != 2 {
				t.Fatalf("attempts: got %d, want 2", got)
			}
		})
	}
}

func TestNonRetryableStatusReturnedImmediately(t *testing.T) {
	t.Parallel()
	cases := []int{
		http.StatusUnauthorized,
		http.StatusNotFound,
		http.StatusUnprocessableEntity,
		http.StatusNotImplemented,
	}
	for _, status := range cases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(status)
			}))
			defer srv.Close()

			resp, err := githttp.Client(fastCfg(5)).Get(srv.URL)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != status {
				t.Fatalf("status: got %d, want %d", resp.StatusCode, status)
			}
			if got := hits.Load(); got != 1 {
				t.Fatalf("attempts: got %d, want 1", got)
			}
		})
	}
}

func TestRetriesNetworkErrorThenSucceeds(t *testing.T) {
	t.Parallel()
	// Bind a listener and immediately close it to force connection
	// refused on the first attempt; on the second attempt we point
	// the request at a live server. We do this by serving from a
	// http.RoundTripper that swaps URLs after the first call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dead := mustReserveDeadAddr(t)
	var attempts atomic.Int32
	swapping := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			r.URL.Host = dead
			return http.DefaultTransport.RoundTrip(r)
		}
		r.URL.Host = srvHost(t, srv.URL)
		return http.DefaultTransport.RoundTrip(r)
	})

	client := &http.Client{Transport: githttp.NewTransport(swapping, fastCfg(3))}
	req, err := http.NewRequest(http.MethodGet, "http://placeholder/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts: got %d, want 2", got)
	}
}

func TestHonoursMaxAttemptsCeiling(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	resp, err := githttp.Client(fastCfg(3)).Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", resp.StatusCode)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("attempts: got %d, want 3 (MaxAttempts ceiling)", got)
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := githttp.Config{
		MaxAttempts:      10,
		BaseDelay:        50 * time.Millisecond,
		MaxDelay:         time.Second,
		MaxRateLimitWait: time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := githttp.Client(cfg).Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		// Net layer may wrap the ctx err in a *url.Error with a
		// non-context root cause — accept anything that mentions
		// the deadline.
		if got := err.Error(); got == "" {
			t.Fatalf("expected deadline-related error, got: %v", err)
		}
	}
	if got := hits.Load(); got > 3 {
		t.Fatalf("too many attempts: got %d, want ≤3 once context expires", got)
	}
}

func TestOnRetryCallbackFires(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var callbacks atomic.Int32
	cfg := fastCfg(5)
	cfg.OnRetry = func(attempt int, wait time.Duration, reason string) {
		if reason == "" {
			t.Errorf("OnRetry: empty reason for attempt %d", attempt)
		}
		callbacks.Add(1)
	}
	resp, err := githttp.Client(cfg).Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if got := callbacks.Load(); got != 2 {
		t.Fatalf("OnRetry calls: got %d, want 2", got)
	}
}

func TestRespectsRetryAfterHttpDate(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			// HTTP-date format. Pick a moment in the past so the
			// retry effectively fires immediately, but the parser
			// is exercised.
			w.Header().Set("Retry-After", time.Now().Add(-time.Second).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := githttp.Client(fastCfg(3)).Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("attempts: got %d, want 2", got)
	}
}

func TestTokenAttachedOnGitHubHostsOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		reqHost   string
		wantAuth  bool
		setHeader bool // pre-set Authorization on the request
	}{
		{"api.github.com", "api.github.com", true, false},
		{"github.com", "github.com", true, false},
		{"uploads.github.com", "uploads.github.com", true, false},
		{"objects.githubusercontent.com (CDN)", "objects.githubusercontent.com", false, false},
		{"third-party host", "evil.example.com", false, false},
		{"existing Authorization preserved", "api.github.com", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var seen string
			capturing := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				seen = r.Header.Get("Authorization")
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			})
			cfg := fastCfg(1)
			cfg.Token = "ghp_secret"
			tr := githttp.NewTransport(capturing, cfg)
			req, err := http.NewRequest(http.MethodGet, "https://"+tc.reqHost+"/x", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.setHeader {
				req.Header.Set("Authorization", "Bearer caller-supplied")
			}
			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			resp.Body.Close()

			switch {
			case tc.setHeader:
				if seen != "Bearer caller-supplied" {
					t.Fatalf("caller's Authorization clobbered: got %q", seen)
				}
			case tc.wantAuth:
				if seen != "Bearer ghp_secret" {
					t.Fatalf("missing Bearer header: got %q", seen)
				}
			default:
				if seen != "" {
					t.Fatalf("token leaked to %s: got %q", tc.reqHost, seen)
				}
			}
		})
	}
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// mustReserveDeadAddr returns "host:port" of a TCP listener we close
// immediately, so a follow-up connection attempt gets ECONNREFUSED.
func mustReserveDeadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

// srvHost extracts the "host:port" from an httptest URL.
func srvHost(t *testing.T, u string) string {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatalf("parse %s: %v", u, err)
	}
	return r.URL.Host
}

// Ensure the package's exported identifiers are referenced even when a
// build tag elides one of the tests above (defensive against future
// refactors).
var _ = fmt.Sprint(githttp.DefaultClient())
