package ingest

import (
	"bytes"
	"context"
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
// cannot.
type headerRecordingRT struct {
	headers []http.Header
}

func (r *headerRecordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.headers = append(r.headers, req.Header.Clone())
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(nil)),
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
