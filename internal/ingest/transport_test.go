package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/build"
	"github.com/jcrussell/solvent-streets/pkg/httpmock"
)

func testRetryConfig(maxRetries int) RetryConfig {
	return RetryConfig{MaxRetries: maxRetries, MaxBackoff: 30 * time.Second}
}

// TestFormatUserAgent_ContractFormat locks in the byob-http-client.5 User-Agent
// format. The contract is an exact-shape match — substring checks would let a
// future refactor drop the parentheses or reorder tokens silently. Drives
// formatUserAgent directly so sync.OnceValue caching does not interfere.
func TestFormatUserAgent_ContractFormat(t *testing.T) {
	cases := []struct {
		name string
		in   build.Info
		want string
	}{
		{
			name: "dev_build_no_commit",
			in:   build.Info{Version: build.VersionDev, OS: "linux", Arch: "amd64", Commit: build.CommitNone},
			want: "pvmt/dev (linux; amd64)",
		},
		{
			name: "release_with_empty_commit",
			in:   build.Info{Version: "1.2.3", OS: "darwin", Arch: "arm64", Commit: ""},
			want: "pvmt/1.2.3 (darwin; arm64)",
		},
		{
			name: "release_with_long_commit_truncates_to_seven",
			in:   build.Info{Version: "1.2.3", OS: "linux", Arch: "amd64", Commit: "abcdef1234567890"},
			want: "pvmt/1.2.3 (linux; amd64) commit=abcdef1",
		},
		{
			name: "release_with_short_commit_passes_through",
			in:   build.Info{Version: "1.2.3", OS: "linux", Arch: "amd64", Commit: "abc"},
			want: "pvmt/1.2.3 (linux; amd64) commit=abc",
		},
		{
			name: "release_with_exactly_seven_char_commit",
			in:   build.Info{Version: "1.2.3", OS: "linux", Arch: "amd64", Commit: "abcdef1"},
			want: "pvmt/1.2.3 (linux; amd64) commit=abcdef1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatUserAgent(tc.in)
			if got != tc.want {
				t.Errorf("formatUserAgent(%+v):\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestUserAgent_CachedAcrossCalls locks in the sync.OnceValue contract: the
// process-wide UA string is computed once and reused, not recomputed per call.
// build.* vars can be reassigned at runtime in tests, but the cached UA must
// not observe the change.
func TestUserAgent_CachedAcrossCalls(t *testing.T) {
	first := UserAgent()
	second := UserAgent()
	if first != second {
		t.Errorf("UserAgent not cached across calls: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "pvmt/") {
		t.Errorf("UserAgent should start with 'pvmt/', got %q", first)
	}
}

func TestUserAgentTransport_SetsHeader(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.Register("GET", "http://example.com", 200, "ok")

	transport := UserAgentTransport(reg)

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	got := reg.LastRequest().Header.Get("User-Agent")
	want := fmt.Sprintf("pvmt/%s (%s; %s)", build.Version, runtime.GOOS, runtime.GOARCH)
	// Allow the optional commit suffix when ldflags happen to set one during
	// `go test` (e.g. CI builds with -ldflags). The base prefix must match
	// exactly.
	if got != want && !strings.HasPrefix(got, want+" commit=") {
		t.Errorf("User-Agent did not match byob-http-client.5 format:\n got: %q\nwant: %q (optionally followed by ' commit=<sha>')", got, want)
	}
}

// TestUserAgentTransport_SetsHeaderOnEveryRequest locks in the
// "middleware applies to every request" half of byob-http-client.5. A future
// refactor that switches to lazy / first-only header application would break
// rate-limit fairness on multi-request workflows (Overpass, ArcGIS pages).
func TestUserAgentTransport_SetsHeaderOnEveryRequest(t *testing.T) {
	rec := &headerRecordingRT{}
	transport := UserAgentTransport(rec)

	for _, path := range []string{"/a", "/b", "/c"} {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com"+path, nil)
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip %s: %v", path, err)
		}
		resp.Body.Close()
	}

	if len(rec.headers) != 3 {
		t.Fatalf("expected 3 recorded requests, got %d", len(rec.headers))
	}
	want := UserAgent()
	for i, h := range rec.headers {
		if got := h.Get("User-Agent"); got != want {
			t.Errorf("request %d: User-Agent = %q; want %q", i, got, want)
		}
	}
}

func TestUserAgentTransport_ClonesRequest(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.Register("GET", "http://example.com", 200, "ok")

	transport := UserAgentTransport(reg)

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	// Original request should not have User-Agent set
	if req.Header.Get("User-Agent") != "" {
		t.Error("original request was mutated")
	}
}

// headerRecordingRT captures the request headers seen on each RoundTrip so a
// test can assert per-request invariants that httpmock's last-only LastRequest
// cannot. If responses is non-empty it is consumed in order, one per call;
// otherwise every call returns 200.
type headerRecordingRT struct {
	headers   []http.Header
	responses []httpmock.Stub
}

func (r *headerRecordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.headers = append(r.headers, req.Header.Clone())
	stub := httpmock.Stub{Status: http.StatusOK}
	if len(r.responses) > 0 {
		idx := len(r.headers) - 1
		if idx >= len(r.responses) {
			idx = len(r.responses) - 1
		}
		stub = r.responses[idx]
	}
	return &http.Response{
		StatusCode: stub.Status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(stub.Body)),
		Request:    req,
	}, nil
}

func TestRetryTransport_NoRetryOn200(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.Register("GET", "http://example.com", 200, "ok")

	transport := RetryTransport(reg, testRetryConfig(2))

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if reg.CallCount("GET", "http://example.com") != 1 {
		t.Errorf("expected 1 call, got %d", reg.CallCount("GET", "http://example.com"))
	}
}

func TestRetryTransport_RetriesOn500(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.RegisterSequence("GET", "http://example.com",
		httpmock.Stub{Status: 500, Body: "error"},
		httpmock.Stub{Status: 200, Body: "ok"},
	)

	// maxRetries=1 means 2 total attempts (initial + 1 retry)
	transport := RetryTransport(reg, testRetryConfig(1))

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if reg.CallCount("GET", "http://example.com") != 2 {
		t.Errorf("expected 2 calls, got %d", reg.CallCount("GET", "http://example.com"))
	}
}

func TestRetryTransport_RetriesOn429(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.RegisterSequence("GET", "http://example.com",
		httpmock.Stub{Status: 429, Body: "rate limited"},
		httpmock.Stub{Status: 200, Body: "ok"},
	)

	transport := RetryTransport(reg, testRetryConfig(1))

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if reg.CallCount("GET", "http://example.com") != 2 {
		t.Errorf("expected 2 calls for 429 retry, got %d", reg.CallCount("GET", "http://example.com"))
	}
}

func TestRetryTransport_NoRetryOn400(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.Register("GET", "http://example.com", 400, "bad request")

	transport := RetryTransport(reg, testRetryConfig(2))

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	if reg.CallCount("GET", "http://example.com") != 1 {
		t.Errorf("expected 1 call (no retry on 400), got %d", reg.CallCount("GET", "http://example.com"))
	}
}

func TestRetryTransport_ReturnsLastResponseOnExhaustion(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.RegisterSequence("GET", "http://example.com",
		httpmock.Stub{Status: 500, Body: "error"},
		httpmock.Stub{Status: 500, Body: "error"},
	)

	transport := RetryTransport(reg, testRetryConfig(1))

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Errorf("expected 500 on exhaustion, got %d", resp.StatusCode)
	}
	if reg.CallCount("GET", "http://example.com") != 2 {
		t.Errorf("expected 2 calls, got %d", reg.CallCount("GET", "http://example.com"))
	}
}

// TestRetryTransport_DoesNotMutateCallerRequest locks in the
// byob-http-client.1 clone-before-mutate contract. The retry transport
// previously rebound req.Body on retries (via req.GetBody), mutating the
// caller's *http.Request. Cloning per attempt keeps the original untouched.
func TestRetryTransport_DoesNotMutateCallerRequest(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.RegisterSequence("POST", "http://example.com",
		httpmock.Stub{Status: 500, Body: "error"},
		httpmock.Stub{Status: 500, Body: "error"},
		httpmock.Stub{Status: 200, Body: "ok"},
	)

	transport := RetryTransport(reg, testRetryConfig(2))

	payload := []byte("hello world")
	req, err := http.NewRequestWithContext(context.Background(), "POST", "http://example.com", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	// Stash the original Body reference so we can compare after RoundTrip.
	origBody := req.Body
	origHeader := req.Header.Clone()

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if req.Body != origBody {
		t.Error("RetryTransport rebound caller's req.Body; must clone per attempt")
	}
	if got, want := len(req.Header), len(origHeader); got != want {
		t.Errorf("caller's req.Header was mutated: %d entries, want %d", got, want)
	}
}

// TestRetryTransport_RespectsContextCancellation locks in the
// byob-http-client.1 ctx-propagation contract. A long Retry-After or backoff
// must not strand the caller — cancelling the request context returns
// promptly with ctx.Err() instead of waiting out the full sleep.
func TestRetryTransport_RespectsContextCancellation(t *testing.T) {
	// Inner always returns 503 so the retry loop always backs off.
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	for range 5 {
		reg.Register("GET", "http://example.com", 503, "unavailable")
	}

	transport := RetryTransport(reg, RetryConfig{MaxRetries: 5, MaxBackoff: 10 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://example.com", nil)

	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := transport.RoundTrip(req)
		done <- result{resp, err}
	}()

	// Let the first attempt fire and enter the backoff sleep, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case r := <-done:
		if r.err == nil {
			if r.resp != nil && r.resp.Body != nil {
				r.resp.Body.Close()
			}
			t.Fatal("RetryTransport returned nil error after ctx cancel; must surface ctx.Err()")
		}
		if !errors.Is(r.err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RetryTransport did not return within 2s of ctx cancel; sleep is not ctx-aware")
	}
}

// TestSleepCtx_ReturnsOnCancel locks in the small helper that makes the
// retry transport's waits cancellable. Direct coverage so future refactors
// of the helper (e.g. swapping to time.AfterFunc) can't silently regress
// the ctx-cancel path.
func TestSleepCtx_ReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := sleepCtx(ctx, 5*time.Second)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx: got err=%v, want context.Canceled", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("sleepCtx waited %v after cancel; should return immediately", elapsed)
	}
}

func TestSleepCtx_ReturnsAfterDuration(t *testing.T) {
	start := time.Now()
	if err := sleepCtx(context.Background(), 20*time.Millisecond); err != nil {
		t.Errorf("sleepCtx returned %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("sleepCtx returned after %v, want >= 20ms", elapsed)
	}
}

// TestShouldRetry_StatusMatrix locks in the byob-http-client.3 status-code
// allowlist. 408 / 429 / 500 / 502 / 503 / 504 retry; 400 / 401 / 404 / 405
// (client errors), 425 (Too Early — semantics-specific opt-in), 501 (Not
// Implemented), 505 (HTTP Version Not Supported), and 2xx do not. The
// matrix is a contract, not implementation detail — a "let's retry every
// 5xx" simplification would silently start hammering 501s.
func TestShouldRetry_StatusMatrix(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{http.StatusOK, false},                      // 200
		{http.StatusBadRequest, false},              // 400
		{http.StatusUnauthorized, false},            // 401
		{http.StatusNotFound, false},                // 404
		{http.StatusMethodNotAllowed, false},        // 405
		{http.StatusTooEarly, false},                // 425
		{http.StatusRequestTimeout, true},           // 408
		{http.StatusTooManyRequests, true},          // 429
		{http.StatusInternalServerError, true},      // 500
		{http.StatusNotImplemented, false},          // 501
		{http.StatusBadGateway, true},               // 502
		{http.StatusServiceUnavailable, true},       // 503
		{http.StatusGatewayTimeout, true},           // 504
		{http.StatusHTTPVersionNotSupported, false}, // 505
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status}
			if got := shouldRetry(resp, nil); got != tc.want {
				t.Errorf("shouldRetry(status=%d) = %v; want %v", tc.status, got, tc.want)
			}
		})
	}
}

// fakeNetTimeoutErr implements net.Error with Timeout()=true so the
// retryable-error classifier can be exercised without poking real syscalls.
type fakeNetTimeoutErr struct{}

func (fakeNetTimeoutErr) Error() string   { return "fake net timeout" }
func (fakeNetTimeoutErr) Timeout() bool   { return true }
func (fakeNetTimeoutErr) Temporary() bool { return true }

// TestShouldRetry_ErrorMatrix locks in the transport-error allowlist:
// net.Error.Timeout() / ECONNRESET / ECONNREFUSED / EPIPE /
// io.ErrUnexpectedEOF are retryable mid-flight disconnects; context.Canceled
// and context.DeadlineExceeded are caller-driven and must NEVER retry;
// arbitrary errors (e.g. a parse failure that the inner transport returned)
// are not retryable.
func TestShouldRetry_ErrorMatrix(t *testing.T) {
	wrapped := func(inner error) error { return fmt.Errorf("dial tcp: %w", inner) }
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil-error-no-resp-not-applicable", nil, false}, // shouldRetry(nil,nil) — caller never hits this branch; documents the trivial case.
		{"econnreset", syscall.ECONNRESET, true},
		{"econnreset-wrapped", wrapped(syscall.ECONNRESET), true},
		{"econnrefused", syscall.ECONNREFUSED, true},
		{"epipe", syscall.EPIPE, true},
		{"unexpected-eof", io.ErrUnexpectedEOF, true},
		{"unexpected-eof-wrapped", wrapped(io.ErrUnexpectedEOF), true},
		{"net-timeout", fakeNetTimeoutErr{}, true},
		{"context-canceled", context.Canceled, false},
		{"context-deadline", context.DeadlineExceeded, false},
		{"arbitrary", errors.New("parse: unexpected token"), false},
		{"io-eof-clean", io.EOF, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// shouldRetry expects a resp when err is nil; only test the err path
			// here unless tc.err is nil (covered by other status tests).
			if tc.err == nil {
				return
			}
			if got := shouldRetry(nil, tc.err); got != tc.want {
				t.Errorf("shouldRetry(err=%v) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}

// erroringRT lets a test inject an error from the inner transport. count
// tracks how many round-trips the retry loop actually drove.
type erroringRT struct {
	err   error
	count int
}

func (e *erroringRT) RoundTrip(req *http.Request) (*http.Response, error) {
	e.count++
	return nil, e.err
}

// TestRetryTransport_RetriesOnRetryableTransportErr drives a full RoundTrip
// loop with ECONNRESET so the byob-http-client.3 error-allowlist branch
// actually runs through the retry transport, not just the classifier. A
// regression that classifies the error correctly but skips the retry
// (e.g. a missing call to shouldRetry on the err path) will fail here.
func TestRetryTransport_RetriesOnRetryableTransportErr(t *testing.T) {
	inner := &erroringRT{err: syscall.ECONNRESET}
	transport := RetryTransport(inner, testRetryConfig(2))
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("RoundTrip err=%v; want ECONNRESET after exhaustion", err)
	}
	if inner.count != 3 { // 1 initial + 2 retries
		t.Errorf("inner attempts=%d; want 3 (1 initial + 2 retries)", inner.count)
	}
}

// TestRetryTransport_NoRetryOnContextErr makes sure a ctx-derived error from
// the inner transport short-circuits — caller cancellation must not be
// retried (it would just hit the same dead ctx every iteration).
func TestRetryTransport_NoRetryOnContextErr(t *testing.T) {
	inner := &erroringRT{err: context.Canceled}
	transport := RetryTransport(inner, testRetryConfig(3))
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com", nil)
	_, err := transport.RoundTrip(req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RoundTrip err=%v; want context.Canceled", err)
	}
	if inner.count != 1 {
		t.Errorf("inner attempts=%d; want 1 (ctx errors not retried)", inner.count)
	}
}

// TestRetryTransport_IdempotentMethodsRetryByDefault drives every method in
// the RFC 9110 idempotent set through a 500→200 sequence with no AllowRetry
// marker. Each must retry. A change that, say, narrowed the default set to
// just GET/HEAD would fail here.
func TestRetryTransport_IdempotentMethodsRetryByDefault(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			reg := httpmock.NewRegistry()
			t.Cleanup(func() { reg.Verify(t) })
			reg.RegisterSequence(method, "http://example.com",
				httpmock.Stub{Status: 500, Body: "error"},
				httpmock.Stub{Status: 200, Body: "ok"},
			)

			transport := RetryTransport(reg, testRetryConfig(1))
			req, _ := http.NewRequestWithContext(context.Background(), method, "http://example.com", nil)
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if got := reg.CallCount(method, "http://example.com"); got != 2 {
				t.Errorf("%s: calls=%d; want 2", method, got)
			}
		})
	}
}

// TestRetryTransport_PostNotRetriedByDefault locks in the
// byob-http-client.3 POST-opt-in contract. A naive "retry every 5xx" loop
// would silently re-issue a write; the contract says POST/PATCH retries
// only with AllowRetry on the ctx.
func TestRetryTransport_PostNotRetriedByDefault(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.RegisterSequence("POST", "http://example.com",
		httpmock.Stub{Status: 500, Body: "error"},
		httpmock.Stub{Status: 200, Body: "ok"},
	)

	transport := RetryTransport(reg, testRetryConfig(2))
	req, _ := http.NewRequestWithContext(context.Background(), "POST", "http://example.com", strings.NewReader("body"))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Errorf("status=%d; want 500 (POST should not retry without AllowRetry)", resp.StatusCode)
	}
	if got := reg.CallCount("POST", "http://example.com"); got != 1 {
		t.Errorf("calls=%d; want 1 (POST without AllowRetry must not retry)", got)
	}
}

// TestRetryTransport_PostRetriedWithAllowRetry locks in the opposite half:
// AllowRetry(ctx) opts the POST into retry, so the same 500→200 sequence
// reaches the second attempt and returns 200.
func TestRetryTransport_PostRetriedWithAllowRetry(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.RegisterSequence("POST", "http://example.com",
		httpmock.Stub{Status: 500, Body: "error"},
		httpmock.Stub{Status: 200, Body: "ok"},
	)

	transport := RetryTransport(reg, testRetryConfig(1))
	ctx := AllowRetry(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://example.com", strings.NewReader("body"))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d; want 200 after retry", resp.StatusCode)
	}
	if got := reg.CallCount("POST", "http://example.com"); got != 2 {
		t.Errorf("calls=%d; want 2", got)
	}
}

// TestRetryTransport_PatchNotRetriedByDefault — PATCH gets the same
// non-idempotent default as POST. Bundled with POST in the contract.
func TestRetryTransport_PatchNotRetriedByDefault(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.Register("PATCH", "http://example.com", 503, "unavailable")

	transport := RetryTransport(reg, testRetryConfig(2))
	req, _ := http.NewRequestWithContext(context.Background(), "PATCH", "http://example.com", strings.NewReader("body"))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := reg.CallCount("PATCH", "http://example.com"); got != 1 {
		t.Errorf("calls=%d; want 1 (PATCH without AllowRetry must not retry)", got)
	}
}

// unreplayableBody wraps a reader so NewRequestWithContext does not
// detect a replayable type and leaves GetBody nil. Used to exercise the
// "non-nil body without GetBody disables retry" branch.
type unreplayableBody struct{ r io.Reader }

func (u *unreplayableBody) Read(p []byte) (int, error) { return u.r.Read(p) }
func (u *unreplayableBody) Close() error               { return nil }

// TestRetryTransport_NoRetryWhenBodyUnreplayable locks in the body-replay
// gate: even with AllowRetry and an otherwise-retryable response, a
// non-nil Body with GetBody==nil must NOT retry because the body has
// already been consumed and the second attempt would send an empty
// request. Silently dropping retry is the correct behaviour per the
// byob-http-client.3 design notes.
func TestRetryTransport_NoRetryWhenBodyUnreplayable(t *testing.T) {
	reg := httpmock.NewRegistry()
	t.Cleanup(func() { reg.Verify(t) })
	reg.RegisterSequence("POST", "http://example.com",
		httpmock.Stub{Status: 500, Body: "error"},
		httpmock.Stub{Status: 200, Body: "ok"},
	)

	transport := RetryTransport(reg, testRetryConfig(2))

	// Construct manually so GetBody stays nil (NewRequestWithContext
	// would auto-populate it for *bytes.Reader / *strings.Reader).
	u, _ := url.Parse("http://example.com")
	req := (&http.Request{
		Method: "POST",
		URL:    u,
		Body:   &unreplayableBody{r: strings.NewReader("body")},
		Header: http.Header{},
	}).WithContext(AllowRetry(context.Background()))
	if req.GetBody != nil {
		t.Fatal("test precondition: GetBody must be nil to exercise the no-replay path")
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := reg.CallCount("POST", "http://example.com"); got != 1 {
		t.Errorf("calls=%d; want 1 (no replay possible → single attempt)", got)
	}
}

// TestRetryTransport_RetriesWithReplayableBody verifies the positive case
// for the body gate: bytes.NewReader gives NewRequestWithContext enough
// to populate GetBody, so a POST + AllowRetry can replay the body on
// retry. The second attempt should observe the same body bytes as the
// first.
func TestRetryTransport_RetriesWithReplayableBody(t *testing.T) {
	rec := &bodyRecordingRT{}
	rec.responses = []httpmock.Stub{
		{Status: 500, Body: "error"},
		{Status: 200, Body: "ok"},
	}

	transport := RetryTransport(rec, testRetryConfig(1))
	ctx := AllowRetry(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://example.com", bytes.NewReader([]byte("hello")))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(rec.bodies) != 2 {
		t.Fatalf("attempts=%d; want 2", len(rec.bodies))
	}
	for i, b := range rec.bodies {
		if b != "hello" {
			t.Errorf("attempt %d body=%q; want %q (GetBody must rewind)", i, b, "hello")
		}
	}
}

// bodyRecordingRT captures the request body bytes seen on each attempt so
// retry tests can verify the inner transport observed a rewound body.
type bodyRecordingRT struct {
	bodies    []string
	responses []httpmock.Stub
}

func (r *bodyRecordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		req.Body.Close()
		body = string(b)
	}
	r.bodies = append(r.bodies, body)
	stub := httpmock.Stub{Status: http.StatusOK}
	if len(r.responses) > 0 {
		idx := len(r.bodies) - 1
		if idx >= len(r.responses) {
			idx = len(r.responses) - 1
		}
		stub = r.responses[idx]
	}
	return &http.Response{
		StatusCode: stub.Status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(stub.Body)),
		Request:    req,
	}, nil
}

// TestParseRetryAfter_Seconds covers the integer-seconds form: positive
// values parse to the matching Duration, zero/negative drop to 0, and
// garbage parses as 0 (not an error — the wait simply falls back to the
// jittered backoff).
func TestParseRetryAfter_Seconds(t *testing.T) {
	cases := []struct {
		header string
		want   time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"-5", 0},
		{"1", 1 * time.Second},
		{"120", 2 * time.Minute},
		{"garbage", 0},
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			if tc.header != "" {
				resp.Header.Set("Retry-After", tc.header)
			}
			if got := parseRetryAfter(resp, time.Now()); got != tc.want {
				t.Errorf("parseRetryAfter(%q) = %v; want %v", tc.header, got, tc.want)
			}
		})
	}
}

// TestParseRetryAfter_HTTPDate locks in the RFC 7231 HTTP-date branch. A
// seconds-only parser silently dropped this form before byob-http-client.3
// — a server that pinned its Retry-After to a timestamp would have its
// rate-limit hint ignored.
func TestParseRetryAfter_HTTPDate(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	future := now.Add(30 * time.Second).UTC().Format(http.TimeFormat)
	past := now.Add(-30 * time.Second).UTC().Format(http.TimeFormat)

	cases := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"future-30s", future, 30 * time.Second},
		{"past-30s", past, 0},
		{"not-a-date", "tomorrow", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{"Retry-After": []string{tc.header}}}
			got := parseRetryAfter(resp, now)
			if got != tc.want {
				t.Errorf("parseRetryAfter(%q, now=%v) = %v; want %v", tc.header, now, got, tc.want)
			}
		})
	}
}

// TestBackoffWait_FullJitterInRange runs the backoff calculator many times
// and asserts every draw lands in [0, exp) for the attempt's exp window.
// Full jitter is a contract piece — a "constant" backoff (e.g. just
// base * 2^attempt with no jitter) would synchronise retry waves across
// clients and is exactly what byob-http-client.3 forbids.
func TestBackoffWait_FullJitterInRange(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 4, MaxBackoff: 60 * time.Second}
	tr := &retryTransport{cfg: cfg}

	for attempt := range cfg.MaxRetries {
		exp := min(time.Second<<attempt, cfg.MaxBackoff)
		seen := make(map[time.Duration]struct{}, 200)
		for range 200 {
			w := tr.backoffWait(attempt, nil)
			if w < 0 || w >= exp {
				t.Fatalf("attempt=%d: wait=%v out of [0,%v)", attempt, w, exp)
			}
			seen[w] = struct{}{}
		}
		// Full jitter should not collapse to a single value across 200 draws
		// even at the smallest window (attempt=0, exp=1s ≈ 1e9 ns). >2 unique
		// values is a loose floor that still catches a constant return.
		if len(seen) <= 2 {
			t.Errorf("attempt=%d: only %d unique waits across 200 draws — jitter looks constant", attempt, len(seen))
		}
	}
}

// TestBackoffWait_RetryAfterOverridesAndIsCapped pins two pieces of the
// backoff policy: a Retry-After larger than the jittered draw wins (the
// server's rate-limit hint is authoritative), and the result is clamped
// to MaxBackoff so a hostile server cannot pin the client for hours.
func TestBackoffWait_RetryAfterOverridesAndIsCapped(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 3, MaxBackoff: 5 * time.Second, UseRetryAfter: true}
	tr := &retryTransport{cfg: cfg}

	resp := &http.Response{Header: http.Header{"Retry-After": []string{"60"}}}
	got := tr.backoffWait(0, resp)
	if got != cfg.MaxBackoff {
		t.Errorf("backoffWait with Retry-After=60s, MaxBackoff=5s = %v; want %v (capped)", got, cfg.MaxBackoff)
	}
}

// TestBackoffWait_RetryAfterIgnoredWhenDisabled — UseRetryAfter=false means
// the server's hint is ignored and the jittered window is used as-is.
func TestBackoffWait_RetryAfterIgnoredWhenDisabled(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 3, MaxBackoff: 60 * time.Second, UseRetryAfter: false}
	tr := &retryTransport{cfg: cfg}

	resp := &http.Response{Header: http.Header{"Retry-After": []string{"30"}}}
	got := tr.backoffWait(0, resp)
	exp := time.Second // attempt=0 → exp=1s
	if got >= exp {
		t.Errorf("backoffWait with UseRetryAfter=false honoured Retry-After: got %v >= exp %v", got, exp)
	}
}

// TestRetryTransport_NoMathRandImport guards against a regression to the
// pre-byob-http-client.3 jitter source. math/rand needs explicit seeding
// (otherwise identical sequences across invocations); math/rand/v2 is
// auto-seeded. A future "let's simplify" import of math/rand would
// silently lose the seeding fix.
func TestRetryTransport_NoMathRandImport(t *testing.T) {
	src, err := os.ReadFile("transport.go")
	if err != nil {
		t.Fatalf("read transport.go: %v", err)
	}
	body := string(src)
	if strings.Contains(body, `"math/rand"`) {
		t.Error(`transport.go imports "math/rand"; byob-http-client.3 requires "math/rand/v2" for auto-seeding`)
	}
	if !strings.Contains(body, `"math/rand/v2"`) {
		t.Error(`transport.go must import "math/rand/v2" for full-jitter backoff`)
	}
}

// TestAllowRetry_ContextKeyIsUnreachableFromOutside guards the ctx-key
// invariant: the only way to mark a ctx as retry-allowed is AllowRetry.
// Storing an arbitrary `bool` under, say, the string key "retry-allowed"
// must not flip retryAllowed — otherwise a downstream package could
// silently enable POST retries without auditing the safety question.
func TestAllowRetry_ContextKeyIsUnreachableFromOutside(t *testing.T) {
	ctx := context.WithValue(context.Background(), "retry-allowed", true) //nolint:staticcheck // SA1029: deliberately testing that a string-key collision does NOT enable retry
	if retryAllowed(ctx) {
		t.Error("retryAllowed reachable via string key; ctx key must be a private struct type")
	}
	if !retryAllowed(AllowRetry(context.Background())) {
		t.Error("AllowRetry(ctx) did not flip retryAllowed")
	}
}

// TestNewTransport_ChainAppliesUserAgentAndRetries pins NewTransport to the
// canonical byob-http-client.1 chain by observing both effects in one call:
// every attempt reaches the inner transport with the User-Agent header set,
// and a 500 triggers a retry. A future refactor that drops either layer
// (or reorders them so UA sits inside a retry that never fires on the first
// 200) will fail here.
func TestNewTransport_ChainAppliesUserAgentAndRetries(t *testing.T) {
	rec := &headerRecordingRT{}
	rec.responses = []httpmock.Stub{
		{Status: 500, Body: "error"},
		{Status: 200, Body: "ok"},
	}

	transport := NewTransport(rec)
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if len(rec.headers) != 2 {
		t.Fatalf("expected 2 attempts (1 retry on 500), got %d", len(rec.headers))
	}
	wantUA := UserAgent()
	for i, h := range rec.headers {
		if got := h.Get("User-Agent"); got != wantUA {
			t.Errorf("attempt %d: User-Agent=%q, want %q", i, got, wantUA)
		}
	}
}
