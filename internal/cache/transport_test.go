package cache

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

	// TTL=0 means always bypass
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

	key := cacheKey("https://example.test/x")
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
