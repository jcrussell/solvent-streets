package factory

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/cache"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/ingest"
	"github.com/jcrussell/solvent-streets/internal/paths"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestNewPVMTTransport_TunedTimeouts locks in the transport
// timeout contract. The exact values are picked deliberately (10s connect/handshake,
// 30s response-header, 90s idle) — drift here is a behavior change that should
// be a deliberate change, not a silent edit. The most load-bearing item
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

// TestHttpClientFactory_CacheTTL pins where the HTTP cache TTL comes from: the
// config's [cache] ttl when set, else cache.DefaultTTL, including when there is
// no pvmt.toml at all. Any other config error fails the client rather than
// silently falling back to the default.
func TestHttpClientFactory_CacheTTL(t *testing.T) {
	loadErr := errors.New("bad toml")
	tests := map[string]struct {
		cfg     func() (*config.Config, error)
		want    time.Duration
		wantErr error
	}{
		"no config func": {cfg: nil, want: cache.DefaultTTL},
		"no pvmt.toml":   {cfg: func() (*config.Config, error) { return nil, config.ErrConfigNotFound }, want: cache.DefaultTTL},
		"unset":          {cfg: func() (*config.Config, error) { return &config.Config{}, nil }, want: cache.DefaultTTL},
		"set": {cfg: func() (*config.Config, error) {
			return &config.Config{Cache: config.CacheConfig{TTL: 6 * time.Hour}}, nil
		}, want: 6 * time.Hour},
		"broken config": {cfg: func() (*config.Config, error) { return nil, loadErr }, wantErr: loadErr},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newTestFactoryForHTTP(t)
			f.Config = tc.cfg
			client, err := f.HttpClient()
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("HttpClient err = %v; want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("HttpClient: %v", err)
			}
			ct, ok := client.Transport.(*cache.CachingTransport)
			if !ok {
				t.Fatalf("Transport = %T; want *cache.CachingTransport", client.Transport)
			}
			if ct.TTL != tc.want {
				t.Errorf("TTL = %s; want %s", ct.TTL, tc.want)
			}
		})
	}
}

// TestResolveOverpassURL pins where the Overpass endpoint comes from:
// PVMT_OVERPASS_URL over the config's [overpass] url over
// ingest.DefaultOverpassURL, the env layer applying with no pvmt.toml too. An
// unusable env value is dropped (config.EnvProblems reports it), and a broken
// config fails rather than silently querying the default instance.
func TestResolveOverpassURL(t *testing.T) {
	const fileURL = "https://overpass.example.org/api/interpreter"
	const envURL = "https://overpass.example.net/api/interpreter"
	loadErr := errors.New("bad toml")
	withURL := func() (*config.Config, error) {
		return &config.Config{Overpass: config.OverpassConfig{URL: fileURL}}, nil
	}
	tests := map[string]struct {
		cfg     func() (*config.Config, error)
		env     string
		want    string
		wantErr error
	}{
		"no config func":   {cfg: nil, want: ingest.DefaultOverpassURL},
		"no pvmt.toml":     {cfg: func() (*config.Config, error) { return nil, config.ErrConfigNotFound }, want: ingest.DefaultOverpassURL},
		"unset":            {cfg: func() (*config.Config, error) { return &config.Config{}, nil }, want: ingest.DefaultOverpassURL},
		"file":             {cfg: withURL, want: fileURL},
		"env over file":    {cfg: withURL, env: envURL, want: envURL},
		"env, no toml":     {cfg: func() (*config.Config, error) { return nil, config.ErrConfigNotFound }, env: envURL, want: envURL},
		"bad env dropped":  {cfg: withURL, env: "http://overpass.example.net/api/interpreter", want: fileURL},
		"broken config":    {cfg: func() (*config.Config, error) { return nil, loadErr }, wantErr: loadErr},
		"broken, with env": {cfg: func() (*config.Config, error) { return nil, loadErr }, env: envURL, wantErr: loadErr},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv(config.EnvOverpassURL, tc.env)
			f := &cmdutil.Factory{Config: tc.cfg}
			got, err := resolveOverpassURL(f)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v; want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveOverpassURL: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q; want %q", got, tc.want)
			}
		})
	}
}

// TestBaseTransportFor_FollowsOverpassEndpoint pins that the long Overpass
// header deadline goes to the configured endpoint's host, and to
// overpass-api.de when none is configured.
func TestBaseTransportFor_FollowsOverpassEndpoint(t *testing.T) {
	t.Setenv(config.EnvOverpassURL, "")
	for url, want := range map[string]string{
		"": ingest.DefaultOverpassHost,
		"https://overpass.example.org:8443/api/interpreter": "overpass.example.org",
	} {
		f := &cmdutil.Factory{Config: func() (*config.Config, error) {
			return &config.Config{Overpass: config.OverpassConfig{URL: url}}, nil
		}}
		rt, err := baseTransportFor(f)
		if err != nil {
			t.Fatalf("baseTransportFor(%q): %v", url, err)
		}
		if got := rt.(*hostTransport).overpassHost; got != want {
			t.Errorf("url %q: Overpass host = %q; want %q", url, got, want)
		}
	}
}

// TestOverpassURLFactory_GuardsAndAnnouncesNonDefault pins what f.OverpassURL
// adds to resolveOverpassURL: a non-default endpoint passes the SSRF guard
// once and is named on stderr once; a guard failure stops it with a hint; the
// default endpoint is neither checked nor announced, so a run that configures
// nothing prints and resolves exactly what it did before.
func TestOverpassURLFactory_GuardsAndAnnouncesNonDefault(t *testing.T) {
	const custom = "https://overpass.example.org/api/interpreter"
	stubGuard := func(t *testing.T, err error) *int {
		t.Helper()
		calls := 0
		orig := validatePublicHTTPURL
		validatePublicHTTPURL = func(_ context.Context, u string) error {
			calls++
			if u != custom {
				t.Errorf("guard checked %q; want %q", u, custom)
			}
			return err
		}
		t.Cleanup(func() { validatePublicHTTPURL = orig })
		return &calls
	}
	newFactory := func(url string) (*cmdutil.Factory, *bytes.Buffer) {
		ios, _, _, stderr := iostreams.Test()
		return &cmdutil.Factory{
			IOStreams: ios,
			Config: func() (*config.Config, error) {
				return &config.Config{Overpass: config.OverpassConfig{URL: url}}, nil
			},
		}, stderr
	}
	t.Setenv(config.EnvOverpassURL, "")

	t.Run("default", func(t *testing.T) {
		calls := stubGuard(t, nil)
		f, stderr := newFactory("")
		get := overpassURLFactory(f)
		got, err := get()
		if err != nil || got != ingest.DefaultOverpassURL {
			t.Fatalf("got (%q, %v); want the default", got, err)
		}
		if *calls != 0 || stderr.Len() != 0 {
			t.Errorf("default endpoint: %d guard calls, stderr %q; want none", *calls, stderr.String())
		}
	})
	t.Run("custom", func(t *testing.T) {
		calls := stubGuard(t, nil)
		f, stderr := newFactory(custom)
		get := overpassURLFactory(f)
		for range 2 {
			got, err := get()
			if err != nil || got != custom {
				t.Fatalf("got (%q, %v); want %q", got, err, custom)
			}
		}
		if *calls != 1 {
			t.Errorf("guard ran %d times; want once", *calls)
		}
		if got := stderr.String(); got != "overpass endpoint: overpass.example.org\n" {
			t.Errorf("stderr = %q; want one endpoint line", got)
		}
	})
	t.Run("guard refuses", func(t *testing.T) {
		guardErr := errors.New("resolves to a private address")
		stubGuard(t, guardErr)
		f, stderr := newFactory(custom)
		_, err := overpassURLFactory(f)()
		if !errors.Is(err, guardErr) {
			t.Fatalf("err = %v; want the guard's", err)
		}
		if _, ok := errors.AsType[*hint.ErrHint](err); !ok {
			t.Errorf("err %v carries no hint", err)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr = %q; a refused endpoint must not be announced", stderr.String())
		}
	})
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
	f.HttpClient = httpClientFactory(f)

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
// short-circuit the User-Agent and retry middlewares.
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
		t.Errorf("outermost transport = %s; want *cache.CachingTransport (chain order: cache wraps ingest wraps base)", gotType)
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
// guard: the per-call validatePublicHTTPURL only
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

// TestRootDBWithClose_ClosesOnlyIfOpened: the shutdown
// close func must (a) be a no-op when the DB was never opened — so DB-free
// commands don't create/close the database — and (b) actually close the
// underlying handle when RootDB was opened during the run.
func TestRootDBWithClose_ClosesOnlyIfOpened(t *testing.T) {
	t.Run("never opened is a no-op", func(t *testing.T) {
		var opens int
		base := sync.OnceValues(func() (*db.RootStore, error) {
			opens++
			return db.Open(context.Background(), filepath.Join(t.TempDir(), "pvmt.db"))
		})
		_, closeFn := rootDBWithClose(base)

		if err := closeFn(); err != nil {
			t.Fatalf("close on never-opened DB: %v", err)
		}
		if opens != 0 {
			t.Errorf("close opened the DB %d time(s); want 0 (must not open just to close)", opens)
		}
	})

	t.Run("closes after open", func(t *testing.T) {
		var opens int
		base := sync.OnceValues(func() (*db.RootStore, error) {
			opens++
			return db.Open(context.Background(), filepath.Join(t.TempDir(), "pvmt.db"))
		})
		get, closeFn := rootDBWithClose(base)

		store, err := get()
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if opens != 1 {
			t.Fatalf("opens = %d; want 1", opens)
		}
		if err := closeFn(); err != nil {
			t.Fatalf("close: %v", err)
		}
		// A query after Close must fail because the connection pool is closed.
		if _, err := store.ListCities(context.Background()); err == nil {
			t.Error("expected error querying a closed RootStore; got nil")
		}
	})
}

func newTestFactoryForHTTP(t *testing.T) *cmdutil.Factory {
	t.Helper()
	allowLoopbackDials(t)
	tmp := t.TempDir()
	f := &cmdutil.Factory{}
	f.Paths = func() (*paths.Paths, error) {
		return &paths.Paths{Cache: tmp, Data: tmp}, nil
	}
	f.HttpClient = httpClientFactory(f)
	return f
}

// TestConfigFactory_EmitsLoadWarningsOnce covers the visible half of the
// [[include]] merge: internal/config has no IOStreams, so it records
// its non-fatal notices on the Config and the lazy Config closure prints them.
// Emitting from here rather than from a PersistentPreRunE middleware is what
// keeps them lazy and scoped — see configFactory — so this test pins all three
// properties at once: nothing is printed before the first call, everything is
// printed on it, and nothing is repeated on the second.
func TestConfigFactory_EmitsLoadWarningsOnce(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	city := "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"
	write("metro/pvmt.toml", "[grid]\nhex_edge_m = 80\n"+city)
	write("national/pvmt.toml", "[grid]\nhex_edge_m = 55\n"+city)
	top := write("all/pvmt.toml",
		"[[include]]\npath = \"../metro/pvmt.toml\"\n[[include]]\npath = \"../national/pvmt.toml\"\n")

	ios, _, _, errBuf := iostreams.Test()
	calls := 0
	load := lazyConfig(warnAfterLoad(ios, func() (*config.Config, error) {
		calls++
		return config.Load(top)
	}))

	if errBuf.String() != "" || calls != 0 {
		t.Fatalf("closure construction must not load or print; calls=%d out=%q", calls, errBuf.String())
	}

	cfg, err := load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.LoadWarnings()) != 1 {
		t.Fatalf("want 1 load warning on the config, got %q", cfg.LoadWarnings())
	}
	first := errBuf.String()
	for _, want := range []string{"warning: ", "Reno, NV", "hex_edge_m (80 vs 55)", "metro/pvmt.toml", "national/pvmt.toml"} {
		if !strings.Contains(first, want) {
			t.Errorf("stderr %q missing %q", first, want)
		}
	}

	if _, err := load(); err != nil {
		t.Fatalf("load (second): %v", err)
	}
	if got := errBuf.String(); got != first {
		t.Errorf("warnings repeated on the second Config() call:\nfirst:  %q\nsecond: %q", first, got)
	}
	if calls != 1 {
		t.Errorf("loader invoked %d time(s); want 1", calls)
	}
}

// TestConfigFactory_SilentWhenNothingToSay pins the other half: a config that
// merges cleanly prints nothing, and a config that fails to load prints
// nothing either — the command that needed it reports that failure, and a
// command that never dereferences the closure (as lazy config allows) must be
// able to run to completion beside a broken pvmt.toml without a word.
func TestConfigFactory_SilentWhenNothingToSay(t *testing.T) {
	t.Run("clean config", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "pvmt.toml")
		if err := os.WriteFile(p, []byte("[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ios, _, _, errBuf := iostreams.Test()
		load := lazyConfig(warnAfterLoad(ios, func() (*config.Config, error) { return config.Load(p) }))
		if _, err := load(); err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := errBuf.String(); got != "" {
			t.Errorf("a config with no disagreements must print nothing, got %q", got)
		}
	})

	t.Run("broken config", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "pvmt.toml")
		if err := os.WriteFile(p, []byte("this is not ][ valid toml"), 0o644); err != nil {
			t.Fatal(err)
		}
		ios, _, _, errBuf := iostreams.Test()
		load := lazyConfig(warnAfterLoad(ios, func() (*config.Config, error) { return config.Load(p) }))
		if _, err := load(); err == nil {
			t.Fatal("expected a load error for invalid TOML")
		}
		if got := errBuf.String(); got != "" {
			t.Errorf("a failed load must not print a warning here, got %q", got)
		}
	})

	t.Run("never dereferenced", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "pvmt.toml"), []byte("this is not ][ valid toml"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		ios, _, _, errBuf := iostreams.Test()
		f := &cmdutil.Factory{IOStreams: ios, Config: configFactory(ios)}
		// Stand-in for a config-free command: it uses the factory without ever
		// asking for the config. Nothing may be read or printed.
		if f.Config == nil {
			t.Fatal("Config closure not wired")
		}
		if got := errBuf.String(); got != "" {
			t.Errorf("a command that never reads the config must see nothing, got %q", got)
		}
	})
}

// TestHintForConfigError_ExampleConfigLoads keeps the example config that the
// config-not-found and no-cities hints print loadable. A user who pastes it
// must get a working config, not a "no data source" validation error.
func TestHintForConfigError_ExampleConfigLoads(t *testing.T) {
	for _, sentinel := range []error{config.ErrConfigNotFound, config.ErrNoCities} {
		var h *hint.ErrHint
		if !errors.As(hintForConfigError(sentinel), &h) {
			t.Fatalf("%v: no hint attached", sentinel)
		}
		_, example, ok := strings.Cut(h.Hint, "\n  [[cities]]")
		if !ok {
			t.Fatalf("%v: hint has no example config: %q", sentinel, h.Hint)
		}
		path := filepath.Join(t.TempDir(), "pvmt.toml")
		if err := os.WriteFile(path, []byte("[[cities]]"+strings.ReplaceAll(example, "\n  ", "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := config.Load(path); err != nil {
			t.Errorf("%v: the hint's example config does not load: %v", sentinel, err)
		}
	}
}

// TestHostTransport_OverpassWaitsLongerForHeaders:
// Overpass holds its headers until the query has run, so its requests get a
// header deadline above the queries' [timeout:N]; every other host keeps the
// short one, so a dead server still fails fast.
func TestHostTransport_OverpassWaitsLongerForHeaders(t *testing.T) {
	allowLoopbackDials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond) // a query still running
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	tp := &hostTransport{
		overpassHost: "127.0.0.1",
		overpass:     newTransport(2 * time.Second),
		rest:         newTransport(50 * time.Millisecond),
	}
	client := &http.Client{Transport: tp}

	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("Overpass-host request failed: %v; it must wait out a slow query", err)
	}
	_ = resp.Body.Close()
	if _, err := client.Get(fmt.Sprintf("http://localhost:%d/", port)); err == nil {
		t.Fatal("other-host request waited past its 50 ms header deadline")
	}

	base := newBaseTransport("overpass.example.org").(*hostTransport)
	if base.overpassHost != "overpass.example.org" {
		t.Errorf("Overpass host = %q, want the configured endpoint's host", base.overpassHost)
	}
	if got := base.overpass.(*headerTimeoutIsFinal).inner.ResponseHeaderTimeout; got != ingest.OverpassHeaderTimeout {
		t.Errorf("Overpass header timeout = %v, want %v", got, ingest.OverpassHeaderTimeout)
	}
	if got := base.rest.(*http.Transport).ResponseHeaderTimeout; got != 30*time.Second {
		t.Errorf("other hosts' header timeout = %v, want 30s", got)
	}
}

// TestOverpassHeaderTimeout_NotRetried: a server
// that holds its headers past the Overpass header deadline is dead, so the
// full client stack makes one attempt, not seven.
func TestOverpassHeaderTimeout_NotRetried(t *testing.T) {
	allowLoopbackDials(t)
	var attempts atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	client := &http.Client{Transport: ingest.NewTransport(&headerTimeoutIsFinal{inner: newTransport(100 * time.Millisecond)})}
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("request succeeded; want a header timeout")
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("err = %v; want it to still read as a net.Error timeout", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1: a header timeout must not be retried", got)
	}
}

// TestRootDBFactory_CanceledRequestStopsTheWait: RootDB opens the database
// under the request context, so a Ctrl-C while another process holds the new
// file's write lock stops the wait instead of running out its budget.
func TestRootDBFactory_CanceledRequestStopsTheWait(t *testing.T) {
	if testing.Short() {
		t.Skip("holds a write lock for seconds")
	}
	path := filepath.Join(t.TempDir(), db.FileName)
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	f := &cmdutil.Factory{}
	f.SetRequestContext(ctx)
	start := time.Now()
	store, err := rootDBFactory(f, path)()
	if err == nil {
		_ = store.Close()
		t.Fatal("RootDB opened a database another process holds the write lock on")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Errorf("RootDB returned after %v, want soon after the cancel", waited)
	}
}

// TestHttpClient_RefusesHTTPSDowngradeRedirect: a redirect from https to
// plain http is refused, even under AllowPrivate, while http to https and
// https to https are followed.
func TestHttpClient_RefusesHTTPSDowngradeRedirect(t *testing.T) {
	f := newTestFactoryForHTTP(t)
	client, err := f.HttpClient()
	if err != nil {
		t.Fatalf("HttpClient: %v", err)
	}
	ctx := ingest.WithAllowPrivate(context.Background())
	req := func(u string) *http.Request {
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		return r
	}
	if err := client.CheckRedirect(req("http://10.0.0.1/q"), []*http.Request{req("https://10.0.0.1/q")}); err == nil {
		t.Error("CheckRedirect followed https to http")
	}
	if err := client.CheckRedirect(req("https://10.0.0.1/q"), []*http.Request{req("http://10.0.0.1/q")}); err != nil {
		t.Errorf("http to https: %v", err)
	}
	if err := client.CheckRedirect(req("https://10.0.0.2/q"), []*http.Request{req("https://10.0.0.1/q")}); err != nil {
		t.Errorf("https to https: %v", err)
	}
}

// allowLoopbackDials lets transports built during the test dial a loopback
// httptest server past the PublicDialer.
func allowLoopbackDials(t *testing.T) {
	t.Helper()
	prev := dialExempt
	dialExempt = netip.Addr.IsLoopback
	t.Cleanup(func() { dialExempt = prev })
}

// TestNewTransport_RefusesPrivateDial: the transport refuses to connect to a
// loopback address even when the URL was never checked (a DNS answer that
// changed after the check), unless the request opted in with AllowPrivate.
func TestNewTransport_RefusesPrivateDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{Transport: newTransport(5 * time.Second)}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	_, err := client.Do(req)
	if _, ok := errors.AsType[*ingest.BlockedAddressError](err); !ok {
		t.Fatalf("dial to loopback = %v, want a BlockedAddressError", err)
	}

	req, _ = http.NewRequestWithContext(ingest.WithAllowPrivate(context.Background()), http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("AllowPrivate dial to loopback: %v", err)
	}
	_ = resp.Body.Close()
}
