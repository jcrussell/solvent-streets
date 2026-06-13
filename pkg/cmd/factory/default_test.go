package factory

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/ingest"
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

// TestLazyConfig_DefersConstructionUntilCalled locks in the laziness
// half of the byob-config.3 contract: building the closure must not
// invoke the loader, so `pvmt --version` and `pvmt --help` never read
// the filesystem looking for pvmt.toml. A regression here would mean a
// broken pvmt.toml could prevent users from running `pvmt --help` to
// figure out what's wrong — the worst possible failure mode for a CLI.
func TestLazyConfig_DefersConstructionUntilCalled(t *testing.T) {
	calls := 0
	load := lazyConfig(func() (*config.Config, error) {
		calls++
		return &config.Config{}, nil
	})

	if calls != 0 {
		t.Fatalf("loader invoked %d time(s) before Config() was called; lazyConfig is not lazy", calls)
	}

	if _, err := load(); err != nil {
		t.Fatalf("Config (first): %v", err)
	}
	if calls != 1 {
		t.Fatalf("loader invoked %d time(s) after first Config(); want 1", calls)
	}
}

// TestLazyConfig_CachedAcrossCalls locks in the sync.OnceValues half
// of the contract: repeated f.Config() calls return the same *Config
// without re-reading the file. Without this guarantee, a long-running
// command that touches Config from multiple call sites (e.g.
// CurrentCity, UnitSystem, warnInvalidConfig) would re-walk the
// filesystem on every access.
func TestLazyConfig_CachedAcrossCalls(t *testing.T) {
	calls := 0
	want := &config.Config{}
	load := lazyConfig(func() (*config.Config, error) {
		calls++
		return want, nil
	})

	first, err := load()
	if err != nil {
		t.Fatalf("Config (first): %v", err)
	}
	second, err := load()
	if err != nil {
		t.Fatalf("Config (second): %v", err)
	}
	if first != second || first != want {
		t.Errorf("lazyConfig returned different *Config across calls; sync.OnceValues caching is broken")
	}
	if calls != 1 {
		t.Errorf("loader invoked %d time(s) across two Config() calls; want 1 (sync.OnceValues should cache)", calls)
	}
}

// TestLazyConfig_CachesErrors guards a subtle but important property:
// when the first load fails (broken pvmt.toml, permission denied,
// etc.), subsequent calls must return the *same* cached error rather
// than re-walking the filesystem each time. Callers like warnInvalidEnv
// followed by a subcommand's RunE will dereference Config repeatedly;
// without error caching, a single broken config would multiply
// filesystem hits with no upside, and the user would see the same
// error reported with different wrapping each time depending on what
// the filesystem looked like at that instant.
func TestLazyConfig_CachesErrors(t *testing.T) {
	calls := 0
	sentinel := errors.New("synthetic load failure")
	load := lazyConfig(func() (*config.Config, error) {
		calls++
		return nil, sentinel
	})

	_, err1 := load()
	_, err2 := load()
	if !errors.Is(err1, sentinel) || !errors.Is(err2, sentinel) {
		t.Errorf("expected sentinel error from both calls; got %v / %v", err1, err2)
	}
	if calls != 1 {
		t.Errorf("loader invoked %d time(s); want 1 (errors must be cached, not retried)", calls)
	}
}

// TestNew_DoesNotLoadConfigEagerly proves the contract through the
// real entry point: factory.New() must not consult the filesystem
// for pvmt.toml, even when no config exists anywhere up the tree.
// We chdir into an isolated tempdir whose parents (under /tmp) carry
// no pvmt.toml, call New(), and assert it returns a usable factory
// without surfacing config errors. Without lazy loading, an absent
// pvmt.toml would either error out of New() (impossible — New has no
// return error) or surface on first access of any field that
// transitively touches Config, breaking commands like --version that
// have no business reading config.
func TestNew_DoesNotLoadConfigEagerly(t *testing.T) {
	tmp := t.TempDir()
	// Sanity: no pvmt.toml exists in tmp or its parents under t.TempDir's
	// root. (t.TempDir lives under os.TempDir, which is not a project root.)
	if _, err := os.Stat(filepath.Join(tmp, "pvmt.toml")); !os.IsNotExist(err) {
		t.Fatalf("expected no pvmt.toml in tempdir; got stat err=%v", err)
	}

	t.Chdir(tmp)

	f := New()
	if f == nil {
		t.Fatal("New returned nil factory")
	}
	if f.Config == nil {
		t.Fatal("New left Config closure unset; lazy contract requires the closure exist after New")
	}

	// Now actually dereference Config — the laziness contract says this
	// is when the load must happen. Without a pvmt.toml anywhere up the
	// tree, the loader should surface ErrConfigNotFound (wrapped with a
	// hint). If we instead saw "no error", that would mean New ate the
	// error silently somewhere — also a regression.
	_, err := f.Config()
	if err == nil {
		t.Fatal("Config() in empty tempdir returned no error; expected ErrConfigNotFound")
	}
	if !errors.Is(err, config.ErrConfigNotFound) {
		t.Errorf("Config() returned %v; want errors.Is(err, ErrConfigNotFound)", err)
	}
}

// TestHttpClient_RejectsRedirectToPrivate pins the redirect-layer SSRF
// guard (solvent-streets-a2z8.1): the per-call validatePublicHTTPURL only
// checks the INITIAL URL, so a 302 to a loopback/link-local/RFC1918 host
// must be blocked by the client's CheckRedirect re-validation. We drive
// CheckRedirect directly (it is what the stdlib invokes on each hop),
// asserting the redirect TARGET is rejected, AllowPrivate exempts it, and
// the 10-hop cap is preserved.
func TestHttpClient_RejectsRedirectToPrivate(t *testing.T) {
	f := newTestFactoryForHTTP(t)
	client, err := f.HttpClient()
	if err != nil {
		t.Fatalf("HttpClient: %v", err)
	}

	// Drive the CheckRedirect hook directly: stdlib calls it with the
	// pending redirect request. A target resolving to a private/link-local
	// host must be refused, and the AllowPrivate context must exempt it.
	target, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	if err := client.CheckRedirect(target, nil); err == nil {
		t.Fatal("expected CheckRedirect to reject redirect to link-local 169.254.169.254")
	} else if !strings.Contains(err.Error(), "redirect blocked") {
		t.Errorf("expected 'redirect blocked' error, got: %v", err)
	}

	// With AllowPrivate on the context, the same target is permitted.
	allowed := target.WithContext(ingest.WithAllowPrivate(context.Background()))
	if err := client.CheckRedirect(allowed, nil); err != nil {
		t.Errorf("AllowPrivate context should permit private redirect, got: %v", err)
	}

	// The 10-hop cap is preserved even for public targets.
	pub, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/", nil)
	via := make([]*http.Request, 10)
	if err := client.CheckRedirect(pub, via); err == nil || !strings.Contains(err.Error(), "10 redirects") {
		t.Errorf("expected 10-hop cap to fire, got: %v", err)
	}
}

// TestHttpClient_RedirectToPrivateBlockedEndToEnd exercises the full
// client.Do path: a public (httptest, 127.0.0.1) endpoint returns a 302
// to a link-local IMDS address; the request fails because CheckRedirect
// re-validates the hop. This confirms the stdlib actually invokes our
// hook across redirects (not just that the hook is correct in isolation).
func TestHttpClient_RedirectToPrivateBlockedEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	f := newTestFactoryForHTTP(t)
	client, err := f.HttpClient()
	if err != nil {
		t.Fatalf("HttpClient: %v", err)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/start", nil)
	resp, err := client.Do(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected redirect to link-local to be blocked, got nil error")
	}
	if !strings.Contains(err.Error(), "redirect blocked") {
		t.Errorf("expected 'redirect blocked' in error, got: %v", err)
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
