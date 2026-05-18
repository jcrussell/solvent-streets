package factory

import (
	"fmt"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/paths"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// TestNewPVMTTransport_TunedTimeouts locks in the byob-http-client.2 transport
// contract. The exact values are picked deliberately (10s connect/handshake,
// 30s response-header, 90s idle) — drift here is a behavior change that should
// be a conscious bead update, not a silent edit. The most load-bearing item
// is ResponseHeaderTimeout: http.DefaultTransport sets it to zero, leaving
// a stalled server able to pin the connection forever.
func TestNewPVMTTransport_TunedTimeouts(t *testing.T) {
	tp := newPVMTTransport()

	if tp.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v; want 10s", tp.TLSHandshakeTimeout)
	}
	if tp.ResponseHeaderTimeout != 30*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v; want 30s (DefaultTransport leaves this 0 — must be set)", tp.ResponseHeaderTimeout)
	}
	if tp.ExpectContinueTimeout != time.Second {
		t.Errorf("ExpectContinueTimeout = %v; want 1s", tp.ExpectContinueTimeout)
	}
	if tp.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %v; want 90s", tp.IdleConnTimeout)
	}
	if tp.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %d; want 100", tp.MaxIdleConns)
	}
	if tp.MaxIdleConnsPerHost != 10 {
		t.Errorf("MaxIdleConnsPerHost = %d; want 10", tp.MaxIdleConnsPerHost)
	}
	if tp.DialContext == nil {
		t.Error("DialContext is nil; must use a tuned net.Dialer.DialContext")
	}
	if tp.Proxy == nil {
		t.Error("Proxy is nil; must inherit http.ProxyFromEnvironment so HTTP(S)_PROXY still works")
	}
}

// TestHttpClientFactory_NoClientWideTimeout locks in the deliberate choice to
// leave http.Client.Timeout at 0. A non-zero client-wide timeout would kill
// streaming bodies (large Overpass exports, ArcGIS pages) regardless of the
// caller's context. Request lifetime is the caller's job via req.Context();
// network-level safety nets live on the transport (see TestNewPVMTTransport_*).
func TestHttpClientFactory_NoClientWideTimeout(t *testing.T) {
	f := newTestFactoryForHTTP(t)

	client, err := f.HttpClient()
	if err != nil {
		t.Fatalf("HttpClient: %v", err)
	}
	if client.Timeout != 0 {
		t.Errorf("client.Timeout = %v; want 0 (per-request ctx bounds lifetime, not a client-wide deadline)", client.Timeout)
	}
}

// TestHttpClientFactory_LazyCachedAcrossCalls locks in the sync.OnceValues
// half of the byob-http-client.2 contract: f.HttpClient() returns the same
// *http.Client on every call, so callers can hand it around without thinking
// about which one is "the" client and the cache directory is created at most
// once per process.
func TestHttpClientFactory_LazyCachedAcrossCalls(t *testing.T) {
	f := newTestFactoryForHTTP(t)

	first, err := f.HttpClient()
	if err != nil {
		t.Fatalf("HttpClient (first): %v", err)
	}
	second, err := f.HttpClient()
	if err != nil {
		t.Fatalf("HttpClient (second): %v", err)
	}
	if first != second {
		t.Errorf("HttpClient returned different *http.Client across calls; sync.OnceValues caching is broken")
	}
}

// TestHttpClientFactory_DefersConstructionUntilCalled locks in the laziness
// half of the contract. Commands like `pvmt --version` and `pvmt config show`
// must not touch the cache directory, so the factory must NOT resolve Paths
// (or any other dependency that would mkdir / read config) until something
// actually calls f.HttpClient(). A regression here would re-introduce
// startup-time filesystem work on every invocation.
func TestHttpClientFactory_DefersConstructionUntilCalled(t *testing.T) {
	calls := 0
	tmp := t.TempDir()
	f := &cmdutil.Factory{}
	f.Paths = func() (*paths.Paths, error) {
		calls++
		return &paths.Paths{Cache: tmp, Data: tmp}, nil
	}
	f.HttpClient = httpClientFactory(f, 24*time.Hour)

	if calls != 0 {
		t.Fatalf("Paths invoked %d times before HttpClient() was called; construction is not lazy", calls)
	}

	if _, err := f.HttpClient(); err != nil {
		t.Fatalf("HttpClient: %v", err)
	}
	if calls != 1 {
		t.Errorf("Paths invoked %d times after first HttpClient(); want 1", calls)
	}

	if _, err := f.HttpClient(); err != nil {
		t.Fatalf("HttpClient (cached): %v", err)
	}
	if calls != 1 {
		t.Errorf("Paths invoked %d times after second HttpClient(); want 1 (sync.OnceValues should cache)", calls)
	}
}

// TestHttpClientFactory_TransportChainIsCacheWrapped pins the outer layer of
// the wired chain. The disk cache must be the outermost wrapper so cache hits
// short-circuit the User-Agent and retry middlewares (byob-http-client.1).
// We do not walk the whole chain here — that belongs in the ingest package's
// transport tests — but the outer assertion guards against a refactor that
// re-orders the wrappers (e.g. ingest.NewTransport(cache.NewTransport(...))).
func TestHttpClientFactory_TransportChainIsCacheWrapped(t *testing.T) {
	f := newTestFactoryForHTTP(t)

	client, err := f.HttpClient()
	if err != nil {
		t.Fatalf("HttpClient: %v", err)
	}
	if client.Transport == nil {
		t.Fatal("client.Transport is nil")
	}
	gotType := fmt.Sprintf("%T", client.Transport)
	if gotType != "*cache.CachingTransport" {
		t.Errorf("outermost transport = %s; want *cache.CachingTransport (byob-http-client.1 chain order: cache wraps ingest wraps base)", gotType)
	}
}

func newTestFactoryForHTTP(t *testing.T) *cmdutil.Factory {
	t.Helper()
	tmp := t.TempDir()
	f := &cmdutil.Factory{}
	f.Paths = func() (*paths.Paths, error) {
		return &paths.Paths{Cache: tmp, Data: tmp}, nil
	}
	f.HttpClient = httpClientFactory(f, 24*time.Hour)
	return f
}
