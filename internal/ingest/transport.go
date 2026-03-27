package ingest

import (
	"math"
	"math/rand"
	"net/http"
	"strconv"
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

// RetryTransport wraps a transport with simple retry logic.
// Kept for backward compatibility; prefer RetryTransportWithConfig.
func RetryTransport(wrapped http.RoundTripper, maxRetries int) http.RoundTripper {
	return &retryTransport{
		wrapped: wrapped,
		cfg: RetryConfig{
			MaxRetries:    maxRetries,
			MaxBackoff:    30 * time.Second,
			UseRetryAfter: false,
		},
	}
}

// RetryTransportWithConfig wraps a transport with configurable retry/backoff.
func RetryTransportWithConfig(wrapped http.RoundTripper, cfg RetryConfig) http.RoundTripper {
	return &retryTransport{wrapped: wrapped, cfg: cfg}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 0; attempt <= t.cfg.MaxRetries; attempt++ {
		// Reset the request body for retries (POST bodies are consumed on first read)
		if attempt > 0 && req.GetBody != nil {
			body, bodyErr := req.GetBody()
			if bodyErr == nil {
				req.Body = body
			}
		}

		resp, err = t.wrapped.RoundTrip(req)
		if err == nil && resp.StatusCode < 500 && resp.StatusCode != 429 {
			return resp, nil
		}

		if attempt < t.cfg.MaxRetries {
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			jitter := time.Duration(rand.Int63n(int64(time.Second)))
			wait := backoff + jitter

			if t.cfg.UseRetryAfter && resp != nil {
				if ra := parseRetryAfter(resp); ra > 0 && ra > wait {
					wait = ra
				}
			}

			if wait > t.cfg.MaxBackoff {
				wait = t.cfg.MaxBackoff
			}

			time.Sleep(wait)
		}

		if resp != nil && resp.Body != nil && (err != nil || attempt < t.cfg.MaxRetries) {
			_ = resp.Body.Close()
		}
	}

	return resp, err
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
