package cache

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jcrussell/solvent-streets/internal/fsutil"
	"github.com/jcrussell/solvent-streets/internal/httpio"
	"github.com/jcrussell/solvent-streets/internal/redact"
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
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
	Timestamp  time.Time           `json:"timestamp"`
	URL        string              `json:"url"`
}

// narrow chmods path to FilePerm when its mode lets anyone but the owner in,
// best-effort.
func narrow(path string) {
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&^FilePerm != 0 {
		_ = os.Chmod(path, FilePerm)
	}
}

// FilePerm is the mode of the cache's files: owner-only, since a request URL
// can carry a credential (an arcgis_url's ?token=) and a response can be
// private data. The directory is owner-only too (paths.EnsureDir). Entries an
// earlier pvmt wrote with wider modes are still read, and narrowed (and their
// URL redacted) when next read.
const FilePerm = 0o600

type CachingTransport struct {
	Wrapped http.RoundTripper
	Dir     string
	TTL     time.Duration
}

// DefaultTTL is how long a cached response is served before it is asked for
// again when the config sets no [cache] ttl. It is a week, not a day, because
// Overpass and Nominatim send no ETag or Last-Modified: a stale entry is a full
// re-download, so a day-long TTL meant a city re-run the next day (a retry, or a
// replay after an ingest bug is found) re-fetched everything and mixed a day of
// OpenStreetMap edits into the corpus. --force bypasses it for fresh data.
const DefaultTTL = 7 * 24 * time.Hour

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
	bodyPath := filepath.Join(t.Dir, key+bodyExt)
	metaPath := filepath.Join(t.Dir, key+metaExt)
	markerPath := filepath.Join(t.Dir, key+tooLargeExt)

	// Two things suppress the read entirely: TTL=0 (cache fully disabled)
	// and a per-request --force bypass (cache.WithBypass). Checking bypass
	// BEFORE readCache means --force never pays the disk read + unmarshal
	// only to discard the result. Leaving haveCache false also
	// disables the conditional-request / 304 path below, which would
	// otherwise re-serve the old body; the fresh response is still written
	// back to the cache.
	var (
		meta       *entryMeta
		cachedBody []byte
		haveCache  bool
	)
	if t.TTL > 0 && !bypassRequested(req.Context()) {
		// A response remembered as over the size cap is not asked for
		// again: the caller (fetchSplitting) splits it into requests that
		// are cached, so re-sending this one would only re-download a body
		// to throw away.
		// A marker from a build with a smaller cap says nothing about
		// whether the response fits this one, so it is ignored.
		if m, ok := readTooLarge(markerPath); ok && time.Since(m.Timestamp) < t.TTL && m.LimitBytes >= httpio.MaxResponseBodyBytes {
			return nil, fmt.Errorf("read response body: %w (remembered from %s; not asked for again until the cache ttl passes or --force)",
				httpio.ErrBodyTooLarge, m.Timestamp.UTC().Format(time.RFC3339))
		}
		meta, cachedBody, haveCache = t.readCache(metaPath, bodyPath)
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
		if errors.Is(err, httpio.ErrBodyTooLarge) && resp.StatusCode == http.StatusOK {
			t.writeTooLarge(markerPath, metaPath, bodyPath, redact.URL(req.URL.String()))
		}
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode == http.StatusOK {
		t.writeCache(metaPath, bodyPath, resp, body, redact.URL(req.URL.String()))
		_ = os.Remove(markerPath) // the key fits now; best-effort
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
	etag := firstHeaderValue(meta.Headers, "Etag")
	lastMod := firstHeaderValue(meta.Headers, "Last-Modified")
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

// firstHeaderValue returns the first value stored for key, or "" when the
// header is absent or has no values. Validators (ETag, Last-Modified) are
// single-valued in practice, so the first value is the right choice even
// though the cache stores every value for a header.
func firstHeaderValue(h map[string][]string, key string) string {
	if vs := h[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// A cache entry is two files named after its key: <key>.json.gz holds the
// gzip-compressed response body and <key>.meta its metadata. The key is a
// sha256 in hex. Bodies are compressed because upstream JSON shrinks about
// sixfold and a large city's responses run to hundreds of MB uncompressed.
//
// legacyBodyExt is the uncompressed <key>.json body written before v0.5.0.
// The transport never reads one (that key is a miss and is re-fetched into
// the new format); prune recognizes it only to sweep it as residue.
//
// A request whose 200 response was over httpio.MaxResponseBodyBytes has,
// instead of the pair, a <key>.toolarge marker (tooLargeMarker): it is
// remembered as too large, like any entry, for the TTL.
const (
	bodyExt       = ".json.gz"
	legacyBodyExt = ".json"
	metaExt       = ".meta"
	tooLargeExt   = ".toolarge"
	keyLen        = sha256.Size * 2
)

// tooLargeMarker is the content of a <key>.toolarge file.
type tooLargeMarker struct {
	Timestamp  time.Time `json:"timestamp"`
	URL        string    `json:"url"`
	LimitBytes int64     `json:"limit_bytes"`
}

// readTooLarge reads a too-large marker. A corrupt one is removed and
// reads as absent.
func readTooLarge(path string) (tooLargeMarker, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tooLargeMarker{}, false
	}
	var m tooLargeMarker
	if err := json.Unmarshal(data, &m); err != nil {
		_ = os.Remove(path)
		return tooLargeMarker{}, false
	}
	// A marker an earlier pvmt wrote: redact its URL and narrow it, as
	// readCache does for a .meta. Best-effort.
	if redact.URL(m.URL) != m.URL {
		fixed := m
		fixed.URL = redact.URL(m.URL)
		if data, err := json.Marshal(fixed); err == nil {
			_ = fsutil.WriteFile(path, data, FilePerm)
		}
	}
	narrow(path)
	return m, true
}

// writeTooLarge records that the key's response is over the cap, replacing
// any earlier entry for the key (which a fresh request means was stale).
func (t *CachingTransport) writeTooLarge(markerPath, metaPath, bodyPath, url string) {
	data, err := json.Marshal(tooLargeMarker{Timestamp: time.Now(), URL: url, LimitBytes: httpio.MaxResponseBodyBytes})
	if err != nil {
		return
	}
	if err := fsutil.WriteFile(markerPath, data, FilePerm); err != nil { // best-effort cache write
		return
	}
	_ = os.Remove(metaPath)
	_ = os.Remove(bodyPath)
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
		// Corrupt meta: the entry can never be served, so drop the whole
		// (meta, body) pair best-effort rather than letting it wedge this
		// key until manual cache clear. This is deliberately narrow — size
		// and age eviction are `pvmt cache prune`'s.
		_ = os.Remove(metaPath)
		_ = os.Remove(bodyPath)
		return nil, nil, false
	}
	body, err := readBody(bodyPath)
	if errors.Is(err, errCorruptBody) {
		// Same rule as a corrupt meta: this pair can never be served.
		_ = os.Remove(metaPath)
		_ = os.Remove(bodyPath)
		return nil, nil, false
	}
	if err != nil {
		return nil, nil, false
	}
	// An entry an earlier pvmt wrote: rewrite a .meta naming an unredacted
	// URL (writeCacheMeta redacts it, keeping the timestamp), and narrow
	// both files to FilePerm. Best-effort; the entry is served either way.
	if redact.URL(meta.URL) != meta.URL {
		t.writeCacheMeta(metaPath, meta)
	}
	narrow(metaPath)
	narrow(bodyPath)
	return &meta, body, true
}

// errCorruptBody marks a body file that exists but does not decompress.
var errCorruptBody = errors.New("corrupt cache body")

// readBody reads and decompresses one body file. Decompression is bounded by
// the same cap the transport applies to a live response, so a damaged or
// hostile file cannot inflate without limit.
func readBody(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errCorruptBody, err)
	}
	body, err := httpio.ReadAllLimit(zr, httpio.MaxResponseBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errCorruptBody, err)
	}
	return body, nil
}

// gzipBody compresses a response body for the cache.
func gzipBody(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(body); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeCacheMeta replaces the .meta file in place — used by the 304
// revalidation path to bump Timestamp without touching the body.
func (t *CachingTransport) writeCacheMeta(metaPath string, meta entryMeta) {
	meta.URL = redact.URL(meta.URL) // an entry written before URLs were redacted
	data, err := json.Marshal(meta)
	if err != nil {
		return
	}
	_ = fsutil.WriteFile(metaPath, data, FilePerm) // best-effort cache write
}

func (t *CachingTransport) writeCache(metaPath, bodyPath string, resp *http.Response, body []byte, url string) {
	// Store every value for each header. resp.Header is already
	// map[string][]string; collapsing to a single value (as
	// resp.Header.Get does) would drop repeated headers like Set-Cookie
	// and Vary.
	headers := make(map[string][]string, len(resp.Header))
	for k, vs := range resp.Header {
		cp := make([]string, len(vs))
		copy(cp, vs)
		headers[k] = cp
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
	// intact and the entry simply re-fetches.
	compressed, err := gzipBody(body)
	if err != nil {
		return
	}
	if err := fsutil.WriteFile(bodyPath, compressed, FilePerm); err != nil {
		return
	}
	_ = fsutil.WriteFile(metaPath, metaData, FilePerm) // commit; best-effort
}

func buildResponse(req *http.Request, meta *entryMeta, body []byte) *http.Response {
	header := make(http.Header)
	for k, vs := range meta.Headers {
		for _, v := range vs {
			header.Add(k, v)
		}
	}
	header.Set("X-Pvmt-Cache", "hit")
	return &http.Response{
		StatusCode: meta.StatusCode,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}
}
