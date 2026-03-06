package httpmock

import (
	"bytes"
	"io"
	"net/http"
	"sync"
)

type Registry struct {
	mu        sync.Mutex
	responses map[string]*http.Response
	calls     map[string]int
}

func NewRegistry() *Registry {
	return &Registry{
		responses: make(map[string]*http.Response),
		calls:     make(map[string]int),
	}
}

func (r *Registry) Register(method, url string, status int, body string) {
	key := method + " " + url
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses[key] = &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func (r *Registry) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.String()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[key]++
	if resp, ok := r.responses[key]; ok {
		return resp, nil
	}
	return &http.Response{
		StatusCode: 404,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewBufferString("not found")),
		Request:    req,
	}, nil
}

func (r *Registry) CallCount(method, url string) int {
	key := method + " " + url
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[key]
}
