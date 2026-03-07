package cache

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestCachingTransport_HitAndMiss(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_, _ = w.Write([]byte(`{"data":"hello"}`))
	}))
	defer srv.Close()

	dir, err := os.MkdirTemp("", "pvmt-cache-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ct := NewTransport(http.DefaultTransport, dir, time.Hour)
	client := &http.Client{Transport: ct}

	// First request — cache miss
	resp, err := client.Get(srv.URL + "/test")
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
	resp, err = client.Get(srv.URL + "/test")
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

func TestCachingTransport_ForceBypass(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_, _ = w.Write([]byte(`{"n":` + string(rune('0'+callCount)) + `}`))
	}))
	defer srv.Close()

	dir, err := os.MkdirTemp("", "pvmt-cache-force")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// TTL=0 means always bypass
	ct := NewTransport(http.DefaultTransport, dir, 0)
	client := &http.Client{Transport: ct}

	_, _ = client.Get(srv.URL + "/test")
	_, _ = client.Get(srv.URL + "/test")
	if callCount != 2 {
		t.Errorf("expected 2 calls with TTL=0, got %d", callCount)
	}
}
