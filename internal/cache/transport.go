package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

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

func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cache", "pvmt", "http")
	return dir, os.MkdirAll(dir, 0o755)
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

	if t.TTL > 0 {
		if meta, body, ok := t.readCache(metaPath, bodyPath); ok {
			if time.Since(meta.Timestamp) < t.TTL {
				return buildResponse(req, meta, body), nil
			}
		}
	}

	resp, err := t.Wrapped.RoundTrip(req)
	if err != nil {
		return nil, err
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
	if err := os.WriteFile(metaPath, metaData, 0o644); err != nil {
		return
	}
	_ = os.WriteFile(bodyPath, body, 0o644) // best-effort cache write
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
