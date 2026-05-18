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
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"sync"
	"syscall"
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

// allowRetryKey marks a context as opting non-idempotent requests
// (POST / PATCH) into the retry transport's backoff loop. Unexported so
// callers can only set it via AllowRetry; ctx values keyed by an
// unexported type are unreachable from any other package.
type allowRetryKey struct{}

// AllowRetry returns a context that opts non-idempotent requests (POST /
// PATCH) into the retry transport's backoff loop per byob-http-client.3.
// GET / HEAD / PUT / DELETE / OPTIONS are retried by default — the
// RFC 9110 idempotent set — and do not need the marker.
//
// Use it for POSTs the server treats as idempotent (a POST with an
// Idempotency-Key header, or a read-only POST like Overpass that uses
// POST only because the query body is too large for a URL). Setting it
// on a write that the server is not prepared to deduplicate will silently
// duplicate the write on every retry — the whole reason non-idempotent
// methods are off by default.
func AllowRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, allowRetryKey{}, true)
}

// retryAllowed reports whether ctx has been marked with AllowRetry.
func retryAllowed(ctx context.Context) bool {
	v, _ := ctx.Value(allowRetryKey{}).(bool)
	return v
}

// methodIsIdempotent reports whether m is in the RFC 9110 idempotent set
// that the retry transport retries by default: GET, HEAD, PUT, DELETE,
// OPTIONS. POST / PATCH (and any custom method) require AllowRetry on the
// request context to be eligible. DELETE is included despite a common
// misconception that it is not idempotent — re-issuing DELETE on a missing
// resource is a 404, not a duplicated side effect.
func methodIsIdempotent(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	}
	return false
}

// requestRetryable returns whether the request itself is eligible for
// retry per byob-http-client.3. Two gates: (1) the method is idempotent
// or the caller opted in via AllowRetry, and (2) the body can be
// replayed — nil body or a non-nil GetBody. A streaming body with no
// GetBody is silently downgraded to a single attempt, since the stdlib
// consumes the body on the first round-trip and there is nothing to
// rewind on a retry.
func requestRetryable(req *http.Request) bool {
	if !methodIsIdempotent(req.Method) && !retryAllowed(req.Context()) {
		return false
	}
	return req.Body == nil || req.GetBody != nil
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	canRetry := requestRetryable(req)

	var resp *http.Response
	var err error

	for attempt := 0; attempt <= t.cfg.MaxRetries; attempt++ {
		// Clone per attempt so the caller's *http.Request is never mutated
		// (byob-http-client.1) and so a retry's body-rewind only touches the
		// per-attempt copy.
		attemptReq := rewoundClone(req, attempt)

		resp, err = t.wrapped.RoundTrip(attemptReq)
		if !canRetry {
			return resp, err
		}
		if !shouldRetry(resp, err) {
			return resp, err
		}
		if attempt >= t.cfg.MaxRetries {
			// Exhausted — return the last response/error so the caller
			// can inspect it. Leaving resp.Body open here matches the
			// RoundTripper contract: the caller owns the body.
			return resp, err
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

// shouldRetry classifies a response or transport error per
// byob-http-client.3. Status allowlist: 408, 429, 500, 502, 503, 504 —
// chosen because each is either a documented transient condition (server
// overload, rate limit, request timeout) or a hop in front of one
// (502/504 from a load balancer). 501 (Not Implemented), 505 (HTTP
// Version Not Supported), and 425 (Too Early) are deliberately excluded
// — they describe a state the server will not exit by retrying.
//
// Error allowlist: net.Error.Timeout(), syscall.ECONNRESET,
// syscall.ECONNREFUSED, syscall.EPIPE, io.ErrUnexpectedEOF — each
// indicates a transport-level disconnect, not a request the server
// rejected. context.Canceled and context.DeadlineExceeded are
// surfaced from the caller's ctx and must never be retried.
func shouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return isRetryableErr(err)
	}
	switch resp.StatusCode {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	return false
}

func isRetryableErr(err error) bool {
	// Caller-driven cancellation is never retryable — the ctx itself is
	// the signal to stop.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return false
}

// backoffWait computes the wait before the next attempt per
// byob-http-client.3: bounded exponential backoff with full jitter.
// wait ∈ [0, min(base * 2^attempt, MaxBackoff)). Retry-After (if
// honoured and larger) overrides the jittered value but is still capped
// by MaxBackoff so a hostile server cannot pin the client.
//
// Uses math/rand/v2 — auto-seeded since Go 1.22, so successive process
// invocations do not share the deterministic sequence math/rand gives
// without explicit seeding.
func (t *retryTransport) backoffWait(attempt int, resp *http.Response) time.Duration {
	exp := time.Second << attempt
	if exp <= 0 || exp > t.cfg.MaxBackoff {
		exp = t.cfg.MaxBackoff
	}
	var wait time.Duration
	if exp > 0 {
		wait = rand.N(exp) //nolint:gosec // G404: jitter does not need crypto rand
	}

	if t.cfg.UseRetryAfter && resp != nil {
		if ra := parseRetryAfter(resp, time.Now()); ra > wait {
			wait = ra
		}
	}

	if wait > t.cfg.MaxBackoff {
		wait = t.cfg.MaxBackoff
	}
	return wait
}

// parseRetryAfter reads RFC 7231 §7.1.3: either delta-seconds
// (an unsigned integer, e.g. "120") or an HTTP-date (e.g. "Wed, 21 Oct
// 2015 07:28:00 GMT"). HTTP-date is converted to a duration relative to
// now; a date in the past returns 0. now is injected so tests can pin
// the conversion deterministically.
func parseRetryAfter(resp *http.Response, now time.Time) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil {
		if d := when.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
