package ingest

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/pkg/httpmock"
)

func testRetryConfig(maxRetries int) RetryConfig {
	return RetryConfig{MaxRetries: maxRetries, MaxBackoff: 30 * time.Second}
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
	ua := reg.LastRequest().Header.Get("User-Agent")
	if !strings.HasPrefix(ua, "pvmt/") {
		t.Errorf("expected User-Agent to start with 'pvmt/', got %q", ua)
	}
	if !strings.Contains(ua, runtime.GOOS) || !strings.Contains(ua, runtime.GOARCH) {
		t.Errorf("expected User-Agent to contain os/arch (%s/%s), got %q", runtime.GOOS, runtime.GOARCH, ua)
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
