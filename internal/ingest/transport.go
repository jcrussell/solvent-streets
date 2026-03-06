package ingest

import (
	"math"
	"math/rand"
	"net/http"
	"time"
)

const userAgentString = "pvmt/1.0 (pavement-data-tool; https://github.com/solvent-streets/pvmt)"

type userAgentTransport struct {
	wrapped http.RoundTripper
}

func UserAgentTransport(wrapped http.RoundTripper) http.RoundTripper {
	return &userAgentTransport{wrapped: wrapped}
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", userAgentString)
	return t.wrapped.RoundTrip(req)
}

type retryTransport struct {
	wrapped    http.RoundTripper
	maxRetries int
}

func RetryTransport(wrapped http.RoundTripper, maxRetries int) http.RoundTripper {
	return &retryTransport{wrapped: wrapped, maxRetries: maxRetries}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		resp, err = t.wrapped.RoundTrip(req)
		if err == nil && resp.StatusCode < 500 && resp.StatusCode != 429 {
			return resp, nil
		}

		if attempt < t.maxRetries {
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			jitter := time.Duration(rand.Int63n(int64(time.Second)))
			time.Sleep(backoff + jitter)
		}

		if resp != nil && resp.Body != nil && (err != nil || attempt < t.maxRetries) {
			resp.Body.Close()
		}
	}

	return resp, err
}
