package cache

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/httpio"
)

// TestCachingTransport_OversizedBodyNotCached pins the cache-path half of the
// ruk4 fix: an over-limit 200 must error (never silently truncate) and must NOT
// be written to the disk cache, so it isn't served as a fresh hit until TTL.
func TestCachingTransport_OversizedBodyNotCached(t *testing.T) {
	prev := httpio.MaxResponseBodyBytes
	httpio.MaxResponseBodyBytes = 8
	t.Cleanup(func() { httpio.MaxResponseBodyBytes = prev })

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		_, _ = w.Write([]byte(`{"data":"way over the tiny limit"}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	client := &http.Client{Transport: NewTransport(http.DefaultTransport, dir, time.Hour)}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/big", nil)
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected error on oversized body, got nil")
	}
	if !errors.Is(err, httpio.ErrBodyTooLarge) {
		t.Errorf("expected ErrBodyTooLarge, got %v", err)
	}

	// Nothing must have been persisted to the cache dir.
	var files int
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			files++
		}
		return nil
	})
	if files != 0 {
		t.Errorf("oversized 200 must not be cached; found %d cache files", files)
	}

	// A second request must hit the origin again (proving it wasn't cached).
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/big", nil)
	if resp2, err := client.Do(req2); err == nil {
		_ = resp2.Body.Close()
	}
	if callCount != 2 {
		t.Errorf("expected 2 origin calls (no cache), got %d", callCount)
	}
}

func TestCachingTransport_HitAndMiss(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_, _ = w.Write([]byte(`{"data":"hello"}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()

	ct := NewTransport(http.DefaultTransport, dir, time.Hour)
	client := &http.Client{Transport: ct}

	// First request — cache miss
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `{"data":"hello"}` {
		t.Errorf("unexpected body: %s", body)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}

	// Second request — cache hit
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	resp, err = client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `{"data":"hello"}` {
		t.Errorf("unexpected body: %s", body)
	}
	if callCount != 1 {
		t.Errorf("expected still 1 call, got %d", callCount)
	}
	if resp.Header.Get("X-Pvmt-Cache") != "hit" {
		t.Error("expected cache hit header")
	}
}

// TestCachingTransport_Revalidate304 pins the conditional-revalidation
// path: when a cached entry past TTL carries an ETag (or Last-Modified)
// validator, the next request sends If-None-Match and a 304 response
// renews the cached timestamp without re-downloading the body.
// Regression for solvent-streets-f367.
func TestCachingTransport_Revalidate304(t *testing.T) {
	const etag = `"abc123"`
	callCount := 0
	got304 := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get("If-None-Match") == etag {
			got304++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = w.Write([]byte(`{"data":"hello"}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()

	// TTL of 1ns means the second request always sees a stale entry,
	// so the revalidation path fires.
	ct := NewTransport(http.DefaultTransport, dir, time.Nanosecond)
	client := &http.Client{Transport: ct}

	// First request — prime the cache (200, ETag captured).
	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()
	if string(body) != `{"data":"hello"}` {
		t.Fatalf("first response body unexpected: %s", body)
	}

	// Wait long enough that the TTL ages out.
	time.Sleep(time.Millisecond)

	// Second request — should send If-None-Match and serve cached body
	// from the 304.
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if string(body) != `{"data":"hello"}` {
		t.Errorf("304 path should serve cached body, got: %s", body)
	}
	if got304 != 1 {
		t.Errorf("expected 1 conditional-revalidation request, got %d", got304)
	}
	if callCount != 2 {
		t.Errorf("expected 2 server calls (200 then 304), got %d", callCount)
	}
	if resp2.Header.Get("X-Pvmt-Cache") != "hit" {
		t.Error("304 path should mark response as cache hit")
	}
}

// TestCachingTransport_Revalidate200 covers the other branch of the
// conditional-request rewrite: when the server replies 200 to the
// conditional request, the new body replaces the cached entry.
func TestCachingTransport_Revalidate200(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("ETag", `"v`+string(rune('0'+callCount))+`"`)
		_, _ = w.Write([]byte(`{"v":` + string(rune('0'+callCount)) + `}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	ct := NewTransport(http.DefaultTransport, dir, time.Nanosecond)
	client := &http.Client{Transport: ct}

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	resp1, _ := client.Do(req1)
	body1, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()
	if string(body1) != `{"v":1}` {
		t.Fatalf("first body: %s", body1)
	}

	time.Sleep(time.Millisecond)

	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	resp2, _ := client.Do(req2)
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if string(body2) != `{"v":2}` {
		t.Errorf("expected refreshed body {\"v\":2}, got %s", body2)
	}
	if callCount != 2 {
		t.Errorf("expected 2 server calls, got %d", callCount)
	}
}

func TestCachingTransport_ForceBypass(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_, _ = w.Write([]byte(`{"n":` + string(rune('0'+callCount)) + `}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()

	// TTL=0 disables the cache entirely: readCache is skipped, so no
	// entry is ever served and every request hits the origin.
	ct := NewTransport(http.DefaultTransport, dir, 0)
	client := &http.Client{Transport: ct}

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	resp1, _ := client.Do(req1)
	if resp1 != nil && resp1.Body != nil {
		resp1.Body.Close()
	}
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	resp2, _ := client.Do(req2)
	if resp2 != nil && resp2.Body != nil {
		resp2.Body.Close()
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls with TTL=0, got %d", callCount)
	}
}

// TestCachingTransport_WithBypass pins the --force contract: a request
// whose context carries WithBypass re-fetches from the origin even when a
// warm (within-TTL) cache entry exists, and the fresh response is written
// back. Regression for solvent-streets-2a7n.20.
func TestCachingTransport_WithBypass(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Serve an ETag so a naive bypass that only skips the TTL-hit
		// return (but leaves the conditional/304 path live) would still
		// re-serve the stale body via 304 — this test would then catch it.
		w.Header().Set("ETag", `"v`+string(rune('0'+callCount))+`"`)
		_, _ = w.Write([]byte(`{"n":` + string(rune('0'+callCount)) + `}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	ct := NewTransport(http.DefaultTransport, dir, time.Hour) // long TTL: cache is warm
	client := &http.Client{Transport: ct}

	// Prime the warm cache.
	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()
	if string(body1) != `{"n":1}` {
		t.Fatalf("first body: %s", body1)
	}

	// Sanity: a plain request now hits the warm cache (no new server call).
	reqHit, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	respHit, _ := client.Do(reqHit)
	_ = respHit.Body.Close()
	if callCount != 1 {
		t.Fatalf("expected warm cache hit (1 call), got %d", callCount)
	}

	// Bypass request must re-fetch despite the warm cache and get fresh body.
	req2, _ := http.NewRequestWithContext(WithBypass(context.Background()), http.MethodGet, srv.URL+"/test", nil)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if callCount != 2 {
		t.Errorf("expected bypass to issue a fresh fetch (2 calls), got %d", callCount)
	}
	if string(body2) != `{"n":2}` {
		t.Errorf("expected fresh body {\"n\":2}, got %s", body2)
	}
	if resp2.Header.Get("X-Pvmt-Cache") == "hit" {
		t.Error("bypass response should not be marked a cache hit")
	}
}

// TestWriteCache_MetaNotWrittenWhenBodyFails pins the body-first commit
// ordering: if the body write fails, the meta must NOT be written, so a
// prior consistent (meta,body) pair is never replaced by new-meta/old-body.
// Regression for solvent-streets-2a7n.24.
func TestWriteCache_MetaNotWrittenWhenBodyFails(t *testing.T) {
	dir := t.TempDir()
	ct := NewTransport(http.DefaultTransport, dir, time.Hour)

	key := cacheKey(http.MethodGet, "https://example.test/x", nil)
	bodyPath := filepath.Join(dir, key+".json")
	metaPath := filepath.Join(dir, key+".meta")

	// Force the body write to fail by making bodyPath an existing
	// directory — WriteFile's final rename onto it cannot succeed.
	if err := os.Mkdir(bodyPath, 0o755); err != nil {
		t.Fatal(err)
	}

	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	ct.writeCache(metaPath, bodyPath, resp, []byte(`fresh-body`), "https://example.test/x")

	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Errorf("meta must not be committed when body write fails (stat err=%v)", err)
	}
}

// TestCachingTransport_MultiValuedHeaderRoundTrip pins 69vw: a response
// with repeated headers (Set-Cookie, Vary) must round-trip through the
// disk cache with every value preserved, not collapsed to one.
func TestCachingTransport_MultiValuedHeaderRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.Header().Add("Vary", "Accept")
		w.Header().Add("Vary", "Origin")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	client := &http.Client{Transport: NewTransport(http.DefaultTransport, dir, time.Hour)}

	// Prime the cache.
	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/c", nil)
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp1.Body.Close()

	// Second request is served from cache; check every header value survived.
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/c", nil)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.Header.Get("X-Pvmt-Cache") != "hit" {
		t.Fatalf("expected a cache hit, got header %q", resp2.Header.Get("X-Pvmt-Cache"))
	}

	cookies := resp2.Header.Values("Set-Cookie")
	if len(cookies) != 2 || cookies[0] != "a=1" || cookies[1] != "b=2" {
		t.Errorf("Set-Cookie not round-tripped with both values in order: %#v", cookies)
	}
	if vary := resp2.Header.Values("Vary"); len(vary) != 2 {
		t.Errorf("Vary not round-tripped with both values: %#v", vary)
	}
}

// TestCachingTransport_BypassSkipsCacheRead pins jk9e: a --force (WithBypass)
// request must not read the cache at all. We seed a corrupt entry that
// readCache would delete (b136); under bypass readCache is never called, so
// the corrupt files survive. Origin returns 500 so writeCache does not
// recreate them, making the "never read" behavior observable.
func TestCachingTransport_BypassSkipsCacheRead(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("err"))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	client := &http.Client{Transport: NewTransport(http.DefaultTransport, dir, time.Hour)}

	key := cacheKey(http.MethodGet, srv.URL+"/x", nil)
	metaPath := filepath.Join(dir, key+".meta")
	bodyPath := filepath.Join(dir, key+".json")
	if err := os.WriteFile(metaPath, []byte("{ not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bodyPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequestWithContext(WithBypass(context.Background()), http.MethodGet, srv.URL+"/x", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if callCount != 1 {
		t.Fatalf("bypass should hit origin once, got %d", callCount)
	}

	// Bypass skipped readCache, so the corrupt entry was neither read nor
	// deleted.
	if _, err := os.Stat(metaPath); err != nil {
		t.Errorf("bypass must not read/delete the cache entry; corrupt meta gone: %v", err)
	}
}

// TestReadCache_RemovesCorruptEntry pins b136 (partial): readCache deletes a
// meta/body pair whose meta fails to unmarshal, best-effort.
func TestReadCache_RemovesCorruptEntry(t *testing.T) {
	dir := t.TempDir()
	ct := NewTransport(http.DefaultTransport, dir, time.Hour)

	key := cacheKey(http.MethodGet, "https://example.test/x", nil)
	metaPath := filepath.Join(dir, key+".meta")
	bodyPath := filepath.Join(dir, key+".json")
	if err := os.WriteFile(metaPath, []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bodyPath, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, ok := ct.readCache(metaPath, bodyPath); ok {
		t.Fatal("corrupt entry must not be reported as a hit")
	}
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Errorf("corrupt meta should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(bodyPath); !os.IsNotExist(err) {
		t.Errorf("corrupt body should be removed, stat err=%v", err)
	}
}

// postRequest builds a POST with a form-encoded body mirroring the
// Overpass call shape (Content-Type + string body).
func postRequest(ctx context.Context, url, body string) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// TestCachingTransport_POSTCachedByBody pins yvlv.32: POSTs are cached,
// keyed by method+url+body. Two identical POSTs to the same URL result
// in a single upstream call (the second is a cache hit), while a POST
// with a different body to the same URL is a distinct entry (a miss).
func TestCachingTransport_POSTCachedByBody(t *testing.T) {
	callCount := 0
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		_, _ = w.Write([]byte(`{"n":` + string(rune('0'+callCount)) + `}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	ct := NewTransport(http.DefaultTransport, dir, time.Hour)
	client := &http.Client{Transport: ct}

	// First POST — cache miss, upstream sees the body.
	resp1, err := client.Do(postRequest(context.Background(), srv.URL+"/api", "data=alpha"))
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()
	if callCount != 1 {
		t.Fatalf("expected 1 upstream call after first POST, got %d", callCount)
	}
	if lastBody != "data=alpha" {
		t.Fatalf("upstream did not receive restored body, got %q", lastBody)
	}
	if string(body1) != `{"n":1}` {
		t.Fatalf("first body: %s", body1)
	}

	// Second identical POST — cache hit, no new upstream call.
	resp2, err := client.Do(postRequest(context.Background(), srv.URL+"/api", "data=alpha"))
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if callCount != 1 {
		t.Errorf("expected identical POST served from cache (still 1 call), got %d", callCount)
	}
	if string(body2) != `{"n":1}` {
		t.Errorf("cached POST body should match first, got %s", body2)
	}
	if resp2.Header.Get("X-Pvmt-Cache") != "hit" {
		t.Error("expected identical POST to be a cache hit")
	}

	// Different body, same URL — must not collide, a fresh upstream call.
	resp3, err := client.Do(postRequest(context.Background(), srv.URL+"/api", "data=beta"))
	if err != nil {
		t.Fatal(err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	_ = resp3.Body.Close()
	if callCount != 2 {
		t.Errorf("expected different-body POST to miss (2 calls), got %d", callCount)
	}
	if lastBody != "data=beta" {
		t.Errorf("upstream did not receive second distinct body, got %q", lastBody)
	}
	if string(body3) != `{"n":2}` {
		t.Errorf("distinct POST should get fresh body, got %s", body3)
	}
}

// TestCachingTransport_POSTBypass confirms POST caching still honors both
// bypass mechanisms: WithBypass and TTL=0 each force a re-fetch.
func TestCachingTransport_POSTBypass(t *testing.T) {
	t.Run("WithBypass", func(t *testing.T) {
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte("ok"))
		}))
		t.Cleanup(srv.Close)

		ct := NewTransport(http.DefaultTransport, t.TempDir(), time.Hour)
		client := &http.Client{Transport: ct}

		resp1, err := client.Do(postRequest(context.Background(), srv.URL+"/api", "data=q"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp1.Body.Close()

		resp2, err := client.Do(postRequest(WithBypass(context.Background()), srv.URL+"/api", "data=q"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp2.Body.Close()
		if callCount != 2 {
			t.Errorf("WithBypass POST should re-fetch (2 calls), got %d", callCount)
		}
	})

	t.Run("TTL0", func(t *testing.T) {
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte("ok"))
		}))
		t.Cleanup(srv.Close)

		ct := NewTransport(http.DefaultTransport, t.TempDir(), 0)
		client := &http.Client{Transport: ct}

		resp1, err := client.Do(postRequest(context.Background(), srv.URL+"/api", "data=q"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp1.Body.Close()

		resp2, err := client.Do(postRequest(context.Background(), srv.URL+"/api", "data=q"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp2.Body.Close()
		if callCount != 2 {
			t.Errorf("TTL=0 POST should always re-fetch (2 calls), got %d", callCount)
		}
	})
}

// TestCachingTransport_TTL0NoRevalidate pins yvlv.35: with TTL=0 the
// cache is fully disabled — no conditional validators are sent and no
// cached body is served, even when the handler emits an ETag. The prior
// TTL=0 test only passed because its handler emitted no validator; this
// variant makes the handler emit an ETag so a lingering 304 path would
// be caught.
func TestCachingTransport_TTL0NoRevalidate(t *testing.T) {
	callCount := 0
	condCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get("If-None-Match") != "" {
			condCount++
		}
		w.Header().Set("ETag", `"fixed-etag"`)
		_, _ = w.Write([]byte(`{"n":` + string(rune('0'+callCount)) + `}`))
	}))
	t.Cleanup(srv.Close)

	ct := NewTransport(http.DefaultTransport, t.TempDir(), 0)
	client := &http.Client{Transport: ct}

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()
	if string(body1) != `{"n":1}` {
		t.Fatalf("first body: %s", body1)
	}

	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()

	if condCount != 0 {
		t.Errorf("TTL=0 must not send conditional validators, saw %d", condCount)
	}
	if callCount != 2 {
		t.Errorf("TTL=0 must always hit origin (2 calls), got %d", callCount)
	}
	if string(body2) != `{"n":2}` {
		t.Errorf("TTL=0 must serve fresh origin body, got %s (a 304 would have re-served {\"n\":1})", body2)
	}
	if resp2.Header.Get("X-Pvmt-Cache") == "hit" {
		t.Error("TTL=0 response must not be marked a cache hit")
	}
}
