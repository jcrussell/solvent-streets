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
	if req.Method != http.MethodGet {
		return t.Wrapped.RoundTrip(req)
	}

	key := cacheKey(req.URL.String())
	bodyPath := filepath.Join(t.Dir, key+".json")
	metaPath := filepath.Join(t.Dir, key+".meta")

	meta, cachedBody, haveCache := t.readCache(metaPath, bodyPath)
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
	// path against Overpass and ArcGIS on repeat runs. Per the
	// RoundTripper contract, RoundTrip shouldn't mutate the caller's
	// request, so we clone before stamping headers.
	if haveCache {
		etag := meta.Headers["Etag"]
		lastMod := meta.Headers["Last-Modified"]
		if etag != "" || lastMod != "" {
			req = req.Clone(req.Context())
			if etag != "" {
				req.Header.Set("If-None-Match", etag)
			}
			if lastMod != "" {
				req.Header.Set("If-Modified-Since", lastMod)
			}
		}
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024)) // 100MB limit
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

func cacheKey(url string) string {
	h := sha256.Sum256([]byte(url))
	return hex.EncodeToString(h[:])
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
