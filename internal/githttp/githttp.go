// Package githttp wraps net/http with retry-with-backoff for transient
// failures observed when talking to GitHub: primary and secondary rate
// limits (403 + X-RateLimit-Remaining: 0, 429 + Retry-After), 5xx
// upstream errors, and network blips like DNS failures and connection
// timeouts.
//
// Use [DefaultClient] for the common case. Retries respect the request
// context, so callers control the overall deadline as before — a
// retried request will not exceed ctx.Done(). Status codes that
// indicate a non-transient problem (401 unauthorized, 404 not found,
// non-rate-limit 403) are returned to the caller untouched on the
// first attempt.
package githttp

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Config tunes retry behaviour. The zero value is valid and used by
// [DefaultClient].
type Config struct {
	// MaxAttempts is the total number of attempts including the first.
	// Defaults to 5.
	MaxAttempts int
	// BaseDelay is the first backoff between attempts. Doubles each
	// attempt. Defaults to 500ms.
	BaseDelay time.Duration
	// MaxDelay caps a single backoff. Defaults to 30s.
	MaxDelay time.Duration
	// MaxRateLimitWait caps how long we'll honour a Retry-After or
	// X-RateLimit-Reset hint. Without a cap a primary rate limit can
	// suggest waits of an hour or more, which is rarely what an
	// interactive caller wants. Defaults to 90s.
	MaxRateLimitWait time.Duration
	// OnRetry, if set, is called before each backoff with the upcoming
	// attempt number, the wait duration, and a short reason string.
	// Useful for surfacing "retrying… (rate limited, waiting 12s)" in
	// CLI output.
	OnRetry func(attempt int, wait time.Duration, reason string)
	// Token, if set, is sent as `Authorization: Bearer <token>` on
	// requests to GitHub-controlled hosts only (api.github.com,
	// github.com, *.githubusercontent.com). Gating by host keeps the
	// PAT from leaking to a third-party host if a request is later
	// redirected off GitHub. The header is added only when the request
	// does not already carry an Authorization header — callers like
	// pat.probeGitHub that already supply their own token take precedence.
	Token string
}

func (c Config) withDefaults() Config {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.BaseDelay <= 0 {
		c.BaseDelay = 500 * time.Millisecond
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = 30 * time.Second
	}
	if c.MaxRateLimitWait <= 0 {
		c.MaxRateLimitWait = 90 * time.Second
	}
	return c
}

// DefaultClient returns a *http.Client wrapping http.DefaultTransport
// with default retry behaviour.
func DefaultClient() *http.Client {
	return Client(Config{})
}

// Client returns a *http.Client whose Transport applies retry on top
// of http.DefaultTransport.
func Client(cfg Config) *http.Client {
	return &http.Client{Transport: NewTransport(http.DefaultTransport, cfg)}
}

// Transport adds retry-with-backoff around an inner http.RoundTripper.
type Transport struct {
	Inner http.RoundTripper
	cfg   Config
}

// NewTransport wraps inner with retry behaviour. If inner is nil,
// http.DefaultTransport is used.
func NewTransport(inner http.RoundTripper, cfg Config) *Transport {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &Transport{Inner: inner, cfg: cfg.withDefaults()}
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.cfg.Token != "" && req.Header.Get("Authorization") == "" && isGitHubHost(req.URL.Host) {
		req.Header.Set("Authorization", "Bearer "+t.cfg.Token)
	}

	// A request body that can't be replayed forces single-shot
	// behaviour: we'd fail any retry with "http: ContentLength=N with
	// Body length 0". GET requests with nil body are always replayable.
	canReplay := req.Body == nil || req.Body == http.NoBody || req.GetBody != nil

	var (
		resp *http.Response
		err  error
	)
	for attempt := 1; attempt <= t.cfg.MaxAttempts; attempt++ {
		if attempt > 1 && req.GetBody != nil {
			body, gbErr := req.GetBody()
			if gbErr != nil {
				return nil, fmt.Errorf("githttp: GetBody on retry: %w", gbErr)
			}
			req.Body = body
		}

		resp, err = t.Inner.RoundTrip(req)
		if !canReplay || attempt == t.cfg.MaxAttempts {
			return resp, err
		}

		wait, reason, retry := classify(resp, err, t.cfg, attempt)
		if !retry {
			return resp, err
		}

		// Drain and close any prior response body so the underlying
		// connection can be reused on the next attempt.
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}

		if t.cfg.OnRetry != nil {
			t.cfg.OnRetry(attempt, wait, reason)
		}

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		}
	}
	return resp, err
}

// classify decides whether a (resp, err) pair from one attempt should
// trigger another, how long to wait, and a short human-facing reason.
// attempt is the just-completed attempt number (1-indexed).
func classify(resp *http.Response, err error, cfg Config, attempt int) (time.Duration, string, bool) {
	if err != nil {
		if isTransientNetErr(err) {
			return jitter(backoff(cfg, attempt)), fmt.Sprintf("network error: %v", err), true
		}
		return 0, "", false
	}
	if resp == nil {
		return 0, "", false
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return capWait(retryAfter(resp, cfg, attempt), cfg, attempt), "429 too many requests", true
	case resp.StatusCode == http.StatusForbidden && isRateLimit(resp):
		return capWait(rateLimitReset(resp, cfg, attempt), cfg, attempt), "403 rate limited", true
	case resp.StatusCode >= 500 && resp.StatusCode <= 599 && resp.StatusCode != http.StatusNotImplemented:
		return jitter(backoff(cfg, attempt)), fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode)), true
	}
	return 0, "", false
}

// isRateLimit recognises both GitHub's primary rate limit (header
// X-RateLimit-Remaining: 0) and the secondary rate limit (returned as
// 403 with a Retry-After hint and no X-RateLimit-Remaining: 0). Plain
// 403s caused by an invalid PAT or insufficient scope have neither
// signal and are not retried — we want the user to learn about a bad
// token immediately.
func isRateLimit(resp *http.Response) bool {
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	if resp.Header.Get("Retry-After") != "" {
		return true
	}
	return false
}

func isTransientNetErr(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// net.OpError catches connection refused, connection reset,
	// no route to host — everything below the HTTP layer.
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	// io.ErrUnexpectedEOF surfaces when the server hangs up before
	// finishing the response headers — typically a load-balancer
	// hiccup, worth one more shot.
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return false
}

// retryAfter parses the Retry-After header. Per RFC 7231 it can be a
// delta in seconds or an HTTP date. GitHub uses seconds; we fall back
// to a fresh backoff if the header is malformed or absent.
func retryAfter(resp *http.Response, cfg Config, attempt int) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return jitter(backoff(cfg, attempt))
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return jitter(backoff(cfg, attempt))
}

// rateLimitReset prefers Retry-After (set by GitHub for secondary
// limits) and falls back to X-RateLimit-Reset, an absolute Unix epoch.
func rateLimitReset(resp *http.Response, cfg Config, attempt int) time.Duration {
	if resp.Header.Get("Retry-After") != "" {
		return retryAfter(resp, cfg, attempt)
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if epoch, err := strconv.ParseInt(v, 10, 64); err == nil {
			d := time.Until(time.Unix(epoch, 0))
			if d > 0 {
				return d
			}
		}
	}
	return jitter(backoff(cfg, attempt))
}

// capWait clamps a server-suggested wait to MaxRateLimitWait. We'd
// rather give up and surface the error than block an interactive CLI
// for an hour.
func capWait(d time.Duration, cfg Config, attempt int) time.Duration {
	if d <= 0 {
		return jitter(backoff(cfg, attempt))
	}
	if d > cfg.MaxRateLimitWait {
		return cfg.MaxRateLimitWait
	}
	return d
}

// backoff returns BaseDelay * 2^(attempt-1), capped at MaxDelay.
// Pre-jitter so callers can wrap the result in jitter() if desired.
func backoff(cfg Config, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := cfg.BaseDelay
	for i := 1; i < attempt; i++ {
		d *= 2
		if d <= 0 || d > cfg.MaxDelay {
			return cfg.MaxDelay
		}
	}
	if d > cfg.MaxDelay {
		return cfg.MaxDelay
	}
	return d
}

// isGitHubHost returns true for the hosts where attaching a GitHub PAT
// is both necessary (for rate-limit relief) and safe. The release-asset
// CDN at *.githubusercontent.com is intentionally excluded: those URLs
// arrive as redirects from github.com, are pre-signed with their own
// query-string credentials, and Go's http.Client strips the
// Authorization header on cross-origin redirects anyway. Adding the
// header there would be both unnecessary and a chance to leak a token
// off a host we control.
func isGitHubHost(host string) bool {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	switch strings.ToLower(host) {
	case "api.github.com", "github.com", "uploads.github.com", "codeload.github.com":
		return true
	}
	return false
}

// jitter applies ±25% uniform jitter so retries from many concurrent
// callers don't synchronise into a thundering herd. math/rand/v2 is
// fine here — this is timing decorrelation, not anything a caller
// could exploit by predicting.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	span := int64(d) / 2
	if span <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(span)-span/2) //nolint:gosec // not security-sensitive
}
