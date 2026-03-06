package ingest

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

type mockRoundTripper struct {
	responses []*http.Response
	errors    []error
	calls     int
	lastReq   *http.Request
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	idx := m.calls
	m.calls++
	m.lastReq = req
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	var err error
	if idx < len(m.errors) {
		err = m.errors[idx]
	}
	return m.responses[idx], err
}

func newMockResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewBufferString("ok")),
	}
}

func TestUserAgentTransport_SetsHeader(t *testing.T) {
	mock := &mockRoundTripper{
		responses: []*http.Response{newMockResponse(200)},
	}
	transport := UserAgentTransport(mock)

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	_, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if mock.lastReq.Header.Get("User-Agent") != userAgentString {
		t.Errorf("expected User-Agent %q, got %q", userAgentString, mock.lastReq.Header.Get("User-Agent"))
	}
}

func TestUserAgentTransport_ClonesRequest(t *testing.T) {
	mock := &mockRoundTripper{
		responses: []*http.Response{newMockResponse(200)},
	}
	transport := UserAgentTransport(mock)

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	_, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	// Original request should not have User-Agent set
	if req.Header.Get("User-Agent") != "" {
		t.Error("original request was mutated")
	}
}

func TestRetryTransport_NoRetryOn200(t *testing.T) {
	mock := &mockRoundTripper{
		responses: []*http.Response{newMockResponse(200)},
	}
	transport := RetryTransport(mock, 2)

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call, got %d", mock.calls)
	}
}

func TestRetryTransport_RetriesOn500(t *testing.T) {
	mock := &mockRoundTripper{
		responses: []*http.Response{
			newMockResponse(500),
			newMockResponse(200),
		},
	}
	// maxRetries=1 means 2 total attempts (initial + 1 retry)
	transport := RetryTransport(mock, 1)

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if mock.calls != 2 {
		t.Errorf("expected 2 calls, got %d", mock.calls)
	}
}

func TestRetryTransport_RetriesOn429(t *testing.T) {
	mock := &mockRoundTripper{
		responses: []*http.Response{
			newMockResponse(429),
			newMockResponse(200),
		},
	}
	transport := RetryTransport(mock, 1)

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if mock.calls != 2 {
		t.Errorf("expected 2 calls for 429 retry, got %d", mock.calls)
	}
}

func TestRetryTransport_NoRetryOn400(t *testing.T) {
	mock := &mockRoundTripper{
		responses: []*http.Response{newMockResponse(400)},
	}
	transport := RetryTransport(mock, 2)

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call (no retry on 400), got %d", mock.calls)
	}
}

func TestRetryTransport_ReturnsLastResponseOnExhaustion(t *testing.T) {
	mock := &mockRoundTripper{
		responses: []*http.Response{
			newMockResponse(500),
			newMockResponse(500),
		},
	}
	transport := RetryTransport(mock, 1)

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 500 {
		t.Errorf("expected 500 on exhaustion, got %d", resp.StatusCode)
	}
	if mock.calls != 2 {
		t.Errorf("expected 2 calls, got %d", mock.calls)
	}
}
