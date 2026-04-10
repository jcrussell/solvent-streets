package httpmock

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
)

// Stub represents a single mock response.
type Stub struct {
	Status int
	Body   string
}

type entry struct {
	stubs []Stub
	index int
}

type Registry struct {
	mu      sync.Mutex
	entries map[string]*entry
	calls   map[string]int
	lastReq *http.Request
}

func NewRegistry() *Registry {
	return &Registry{
		entries: make(map[string]*entry),
		calls:   make(map[string]int),
	}
}

func (r *Registry) Register(method, url string, status int, body string) {
	r.RegisterSequence(method, url, Stub{Status: status, Body: body})
}

func (r *Registry) RegisterSequence(method, url string, stubs ...Stub) {
	key := method + " " + url
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[key] = &entry{stubs: stubs}
}

func (r *Registry) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.String()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[key]++
	r.lastReq = req
	if e, ok := r.entries[key]; ok {
		idx := e.index
		if idx >= len(e.stubs) {
			idx = len(e.stubs) - 1
		}
		e.index++
		stub := e.stubs[idx]
		return &http.Response{
			StatusCode: stub.Status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader([]byte(stub.Body))),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader([]byte("not found"))),
		Request:    req,
	}, nil
}

func (r *Registry) CallCount(method, url string) int {
	key := method + " " + url
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[key]
}

// LastRequest returns the most recent request seen by the registry.
func (r *Registry) LastRequest() *http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastReq
}

// Verify errors on any registered-but-uncalled endpoints.
func (r *Registry) Verify(tb testing.TB) {
	tb.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.entries {
		if r.calls[key] == 0 {
			tb.Errorf("httpmock: registered but never called: %s", key)
		}
	}
}

// Requests returns the total number of round-trip calls.
func (r *Registry) Requests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for _, n := range r.calls {
		total += n
	}
	return total
}

func (r *Registry) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("httpmock.Registry{entries: %d, calls: %d}", len(r.entries), len(r.calls))
}
