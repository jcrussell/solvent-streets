package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
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
