package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jcrussell/solvent-streets/internal/httpio"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// bypassKey is the context key carrying the per-request "skip the read
// cache" flag set by WithBypass. It is checked at the top of RoundTrip.
type bypassKey struct{}

// WithBypass marks ctx so the CachingTransport skips serving (and
// revalidating against) any cached entry for requests derived from it —
// the fresh response is still written back to the cache. Used by
// `ingest --force` to honor its documented "bypass HTTP cache" contract.
func WithBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, bypassKey{}, true)
}

func bypassRequested(ctx context.Context) bool {
	v, _ := ctx.Value(bypassKey{}).(bool)
	return v
}

type entryMeta struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Timestamp  time.Time         `json:"timestamp"`
	URL        string            `json:"url"`
}

type CachingTransport struct {
	Wrapped http.RoundTripper
	Dir     string
	TTL     time.Duration
}

func NewTransport(wrapped http.RoundTripper, dir string, ttl time.Duration) *CachingTransport {
	return &CachingTransport{
		Wrapped: wrapped,
		Dir:     dir,
		TTL:     ttl,
	}
}

func (t *CachingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Only GET and POST are cached. Both are read-shaped for this tool:
	// Overpass fetches POST because their query bodies are too large for
	// a URL, but are semantically reads. Other methods (PUT/DELETE/…) are
	// genuinely uncacheable and pass straight through.
	if req.Method != http.MethodGet && req.Method != http.MethodPost {
		return t.Wrapped.RoundTrip(req)
	}

	// Reading req.Body consumes it, so buffer it and restore a fresh
	// reader (plus GetBody/ContentLength) before handing the request on
	// to the wrapped RoundTripper on a cache miss. The buffered bytes are
	// also folded into the cache key so POSTs with different bodies land
	// in different entries.
	bodyBytes, err := readAndRestoreBody(req)
	if err != nil {
		return nil, err
	}

	key := cacheKey(req.Method, req.URL.String(), bodyBytes)
	bodyPath := filepath.Join(t.Dir, key+".json")
	metaPath := filepath.Join(t.Dir, key+".meta")

	// TTL=0 is a true bypass: skip reading the cache entirely so no
	// conditional validators are sent and no cached body is served.
	var (
		meta       *entryMeta
		cachedBody []byte
		haveCache  bool
	)
	if t.TTL > 0 {
		meta, cachedBody, haveCache = t.readCache(metaPath, bodyPath)
	}
	if bypassRequested(req.Context()) {
		// --force: ignore any cached entry entirely. Forcing haveCache
		// false here also disables the conditional-request / 304 path
		// below (transport.go), which would otherwise re-serve the old
		// body. The fresh response is still written back to the cache.
		haveCache = false
	}
	if t.TTL > 0 && haveCache && time.Since(meta.Timestamp) < t.TTL {
		return buildResponse(req, meta, cachedBody), nil
	}

	// Past TTL (or no entry). If validators are stored on the cached
	// meta, send a conditional request — a 304 lets us refresh the
	// timestamp without re-downloading the body, which is the cheap
	// path against Overpass and ArcGIS on repeat runs.
	if haveCache {
		req = withConditionalValidators(req, meta)
	}

	resp, err := t.Wrapped.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotModified && haveCache {
		_ = resp.Body.Close()
		meta.Timestamp = time.Now()
		t.writeCacheMeta(metaPath, *meta)
		return buildResponse(req, meta, cachedBody), nil
	}

	// Read with an overflow-detecting limit: a plain io.LimitReader returns a
	// clean EOF at exactly the cap, so a truncated 200 would be written to the
	// disk cache and served as a fresh hit until TTL. ReadAllLimit errors
	// instead, and the error short-circuits before writeCache below — a
	// truncated body is never persisted.
	body, err := httpio.ReadAllLimit(resp.Body, httpio.MaxResponseBodyBytes)
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode == http.StatusOK {
		t.writeCache(metaPath, bodyPath, resp, body, req.URL.String())
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// withConditionalValidators returns a clone of req stamped with
// If-None-Match / If-Modified-Since headers from the cached meta's
// validators, enabling a cheap 304 revalidation. Per the RoundTripper
// contract, RoundTrip must not mutate the caller's request, so the
// request is cloned before headers are set. When the meta carries no
// validators the original request is returned unchanged.
func withConditionalValidators(req *http.Request, meta *entryMeta) *http.Request {
	etag := meta.Headers["Etag"]
	lastMod := meta.Headers["Last-Modified"]
	if etag == "" && lastMod == "" {
		return req
	}
	req = req.Clone(req.Context())
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastMod != "" {
		req.Header.Set("If-Modified-Since", lastMod)
	}
	return req
}

// cacheKey derives the on-disk cache filename from the request method,
// URL, and body. Folding the body in lets POSTs with identical URLs but
// different query bodies (e.g. Overpass quadrant requests) map to
// distinct entries. The bytes are joined with newlines — unambiguous
// because method and URL never contain one.
func cacheKey(method, url string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{'\n'})
	h.Write([]byte(url))
	h.Write([]byte{'\n'})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// readAndRestoreBody buffers req.Body and restores it so the wrapped
// RoundTripper can still send it on a cache miss. A nil body (typical
// GET) yields nil bytes and leaves the request untouched. GetBody and
// ContentLength are reset to match the buffered bytes so retries and the
// underlying transport see a consistent request.
func readAndRestoreBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	bodyBytes, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	req.ContentLength = int64(len(bodyBytes))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyBytes)), nil
	}
	return bodyBytes, nil
}

func (t *CachingTransport) readCache(metaPath, bodyPath string) (*entryMeta, []byte, bool) {
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, nil, false
	}
	var meta entryMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return nil, nil, false
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		return nil, nil, false
	}
	return &meta, body, true
}

// writeCacheMeta replaces the .meta file in place — used by the 304
// revalidation path to bump Timestamp without touching the body.
func (t *CachingTransport) writeCacheMeta(metaPath string, meta entryMeta) {
	data, err := json.Marshal(meta)
	if err != nil {
		return
	}
	_ = cmdutil.WriteFile(metaPath, data, 0o644) // best-effort cache write
}

func (t *CachingTransport) writeCache(metaPath, bodyPath string, resp *http.Response, body []byte, url string) {
	headers := make(map[string]string)
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}
	meta := entryMeta{
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Timestamp:  time.Now(),
		URL:        url,
	}
	metaData, err := json.Marshal(meta)
	if err != nil {
		return
	}
	// Write the body FIRST and treat the meta write as the commit
	// record. Each WriteFile is atomic per file (temp+fsync+rename) but
	// the pair is not transactional; if meta committed before the body,
	// a failure/kill in between would pair the new meta (fresh
	// timestamp, new validators) with the OLD body and serve it as a
	// fresh hit forever. Committing meta only after a successful body
	// write means a body failure leaves the previous (consistent) pair
	// intact and the entry simply re-fetches. (solvent-streets-2a7n.24)
	if err := cmdutil.WriteFile(bodyPath, body, 0o644); err != nil {
		return
	}
	_ = cmdutil.WriteFile(metaPath, metaData, 0o644) // commit; best-effort
}

func buildResponse(req *http.Request, meta *entryMeta, body []byte) *http.Response {
	header := make(http.Header)
	for k, v := range meta.Headers {
		header.Set(k, v)
	}
	header.Set("X-Pvmt-Cache", "hit")
	return &http.Response{
		StatusCode: meta.StatusCode,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}
}
