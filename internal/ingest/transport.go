// Package ingest's transport layer implements the byob-http-client.1
// contract: pavement-data ingest reaches the network via composed
// http.RoundTripper middlewares over http.DefaultTransport — not
// hashicorp/go-retryablehttp, go-resty/resty, or any other opaque wrapper.
// Keeping each concern (user-agent stamping, retry/backoff) in a separate
// RoundTripper lets us swap, instrument, or skip layers independently and
// keeps the dependency footprint inside the standard library.
//
// Canonical order, outermost → innermost:
//
//	UserAgent → Retry → http.DefaultTransport
//
// UserAgent is outermost so the header is set once and every retry attempt
// inherits it. Retry is in the middle so each attempt is forwarded as a
// distinct round-trip to the inner transport. The disk cache in
// internal/cache sits outside this chain — cache hits return without
// consuming retry budget.
//
// Middleware authors must:
//
//   - Clone the inbound request before mutating headers or body so the
//     caller's *http.Request stays untouched (req.Clone(req.Context())).
//   - Respect req.Context() during waits (use sleepCtx, not time.Sleep)
//     and propagate the inner transport's error verbatim.
//
// Use NewTransport to wire the canonical chain rather than hand-composing
// it at the call site — that keeps the order encoded in one place.
package ingest

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jcrussell/solvent-streets/internal/build"
)

// NewTransport wires the byob-http-client.1 canonical middleware chain over
// inner. Pass http.DefaultTransport (or a test stand-in) as inner.
//
//	UserAgent → Retry(DefaultRetryConfig) → inner
//
// The disk cache in internal/cache wraps the result from the outside; it is
// deliberately not part of this chain because cache hits should bypass UA
// stamping and retries entirely.
func NewTransport(inner http.RoundTripper) http.RoundTripper {
	return UserAgentTransport(RetryTransport(inner, DefaultRetryConfig()))
}

// UserAgent returns the outbound User-Agent string in the format prescribed
// by byob-http-client.5:
//
//	pvmt/<version> (<goos>; <goarch>)[ commit=<short>]
//
// The commit suffix is appended only when build.Commit holds a real value
// (not build.CommitNone and not empty). The result is computed once at first
// call from build.Current() and cached for the process lifetime via
// sync.OnceValue, so every outbound request sees the same string.
var UserAgent = sync.OnceValue(func() string {
	return formatUserAgent(build.Current())
})

// formatUserAgent renders the User-Agent contract for an arbitrary build.Info.
// Split out from UserAgent so tests can pin the exact format without racing
// the sync.OnceValue cache or mutating build.* package vars.
func formatUserAgent(i build.Info) string {
	ua := fmt.Sprintf("pvmt/%s (%s; %s)", i.Version, i.OS, i.Arch)
	if i.Commit != "" && i.Commit != build.CommitNone {
		commit := i.Commit
		if len(commit) > 7 {
			commit = commit[:7]
		}
		ua += " commit=" + commit
	}
	return ua
}

type userAgentTransport struct {
	wrapped http.RoundTripper
}

// UserAgentTransport wraps a RoundTripper so every outbound request carries
// the byob-http-client.5 User-Agent header. The inbound request is cloned
// before mutation so callers' http.Request values are never modified.
func UserAgentTransport(wrapped http.RoundTripper) http.RoundTripper {
	return &userAgentTransport{wrapped: wrapped}
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", UserAgent())
	return t.wrapped.RoundTrip(req)
}

// RetryConfig controls retry behavior for the transport.
type RetryConfig struct {
	MaxRetries    int
	MaxBackoff    time.Duration
	UseRetryAfter bool
}

// DefaultRetryConfig returns retry settings tuned for Overpass API rate limits.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:    6,
		MaxBackoff:    90 * time.Second,
		UseRetryAfter: true,
	}
}

type retryTransport struct {
	wrapped http.RoundTripper
	cfg     RetryConfig
}

// RetryTransport wraps a transport with configurable retry/backoff.
func RetryTransport(wrapped http.RoundTripper, cfg RetryConfig) http.RoundTripper {
	return &retryTransport{wrapped: wrapped, cfg: cfg}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 0; attempt <= t.cfg.MaxRetries; attempt++ {
		// Clone per attempt so the caller's *http.Request is never mutated
		// (byob-http-client.1) and so a retry's body-rewind only touches the
		// per-attempt copy.
		attemptReq := rewoundClone(req, attempt)

		resp, err = t.wrapped.RoundTrip(attemptReq)
		if !t.shouldRetry(resp, err) {
			return resp, err
		}

		if attempt >= t.cfg.MaxRetries {
			closeBody(resp)
			break
		}

		wait := t.backoffWait(attempt, resp)
		// Close the prior body before sleeping so a long Retry-After does
		// not keep the connection pinned for the full backoff window.
		closeBody(resp)
		resp = nil
		if sleepErr := sleepCtx(req.Context(), wait); sleepErr != nil {
			return nil, sleepErr
		}
	}

	return resp, err
}

// rewoundClone clones req for an attempt; on retries (attempt > 0) it also
// rewinds the body via req.GetBody so the inner transport sees a fresh
// reader. The clone keeps the byob-http-client.1 promise that the caller's
// *http.Request is never mutated.
func rewoundClone(req *http.Request, attempt int) *http.Request {
	clone := req.Clone(req.Context())
	if attempt == 0 || clone.GetBody == nil {
		return clone
	}
	if body, err := clone.GetBody(); err == nil {
		clone.Body = body
	}
	return clone
}

func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

// sleepCtx waits for d to elapse or ctx to be cancelled, whichever happens
// first. Returns ctx.Err() on cancellation, nil otherwise. This is how
// byob-http-client.1 middlewares honour the "propagate ctx.Err() from the
// inner transport" contract during their own waits.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *retryTransport) shouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	return resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
}

func (t *retryTransport) backoffWait(attempt int, resp *http.Response) time.Duration {
	backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	jitter := time.Duration(rand.Int63n(int64(time.Second))) //nolint:gosec // G404: jitter does not need crypto rand
	wait := backoff + jitter

	if t.cfg.UseRetryAfter && resp != nil {
		if ra := parseRetryAfter(resp); ra > 0 && ra > wait {
			wait = ra
		}
	}

	if wait > t.cfg.MaxBackoff {
		wait = t.cfg.MaxBackoff
	}
	return wait
}

// parseRetryAfter reads the Retry-After header as an integer number of seconds.
func parseRetryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
