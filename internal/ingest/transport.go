package ingest

import (
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jcrussell/solvent-streets/internal/build"
)

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

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 0; attempt <= t.cfg.MaxRetries; attempt++ {
		if attempt > 0 && req.GetBody != nil {
			body, bodyErr := req.GetBody()
			if bodyErr == nil {
				req.Body = body
			}
		}

		resp, err = t.wrapped.RoundTrip(req)
		if !t.shouldRetry(resp, err) {
			return resp, err
		}

		if attempt < t.cfg.MaxRetries {
			time.Sleep(t.backoffWait(attempt, resp))
		}

		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}

	return resp, err
}

func (t *retryTransport) shouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	return resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
}

func (t *retryTransport) backoffWait(attempt int, resp *http.Response) time.Duration {
	backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	jitter := time.Duration(rand.Int63n(int64(time.Second))) //nolint:gosec // G404: jitter does not need crypto rand
	wait := backoff + jitter

	if t.cfg.UseRetryAfter && resp != nil {
		if ra := parseRetryAfter(resp); ra > 0 && ra > wait {
			wait = ra
		}
	}

	if wait > t.cfg.MaxBackoff {
		wait = t.cfg.MaxBackoff
	}
	return wait
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
