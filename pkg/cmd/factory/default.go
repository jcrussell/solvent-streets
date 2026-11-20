package factory

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jcrussell/solvent-streets/internal/build"
	"github.com/jcrussell/solvent-streets/internal/cache"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/ingest"
	"github.com/jcrussell/solvent-streets/internal/paths"
	"github.com/jcrussell/solvent-streets/internal/services"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// hintForConfigError attaches remediation text to known config-load failures
// and wraps validation errors as FlagError so the runner returns exit
// code 2. Returns err unchanged when no hint or wrap applies.
func hintForConfigError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, config.ErrConfigNotFound):
		return hint.Withf(err,
			"create a pvmt.toml in your project root, or cd into a directory that contains one; a minimal one:\n  [[cities]]\n  name = \"Oakland, CA\"\n  overpass = true")
	case errors.Is(err, config.ErrNoCities):
		return hint.Withf(cmdutil.FlagErrorf("%w", err),
			"add a [[cities]] section to pvmt.toml, e.g.:\n  [[cities]]\n  name = \"Oakland, CA\"\n  overpass = true")
	case errors.Is(err, config.ErrInvalidConfig):
		return cmdutil.FlagErrorf("%w", err)
	case errors.Is(err, fs.ErrPermission):
		return hint.Withf(err, "check filesystem permissions on the pvmt.toml path")
	default:
		return err
	}
}

// newPVMTTransport returns the base *http.Transport that backs every
// outbound pvmt HTTP request, with the timeout contract
// baked in. http.DefaultTransport covers most of these but lacks
// ResponseHeaderTimeout, so a server that finishes the TCP/TLS handshake
// then stalls before sending response headers can pin the connection for
// the lifetime of the process — ctx alone won't catch it because the
// transport never gets to a state where it polls ctx.Done().
//
// Per-request cancellation continues to flow through req.Context(); these
// timeouts are the safety net for network-level hangs ctx cannot reach.
// Dialer.Timeout is intentionally tighter than DefaultTransport's 30s —
// pvmt only talks to a handful of well-known endpoints (Overpass, ArcGIS,
// Nominatim) and a 10s connect ceiling fails fast on bad networks.
func newPVMTTransport() *http.Transport {
	return newTransport(30 * time.Second)
}

// newTransport is newPVMTTransport with the given ResponseHeaderTimeout.
func newTransport(headerTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
	}
}

// hostTransport sends requests for overpassHost through overpass and every
// other request through rest. Overpass alone holds its response headers
// until the query has run (up to resource.OverpassTimeoutS), so only it gets
// the longer header deadline (ingest.OverpassHeaderTimeout); ArcGIS,
// Nominatim and TIGERweb keep the 30 s one, which fails a dead server fast.
type hostTransport struct {
	overpassHost string
	overpass     http.RoundTripper
	rest         http.RoundTripper
}

func (t *hostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() == t.overpassHost {
		return t.overpass.RoundTrip(req)
	}
	return t.rest.RoundTrip(req)
}

// headerTimeoutIsFinal marks a timeout that arrives once the inner
// transport's ResponseHeaderTimeout has run out as not worth retrying
// (ingest.NoRetry). The Overpass header deadline is set above the queries'
// own [timeout:N], so reaching it means the server is dead, not slow, and
// each retry would cost the full deadline again: seven attempts made one
// dead request take about 18 minutes. Shorter
// timeouts (dial, TLS handshake) end before the deadline and stay
// retryable, as do 429/504 answers from an Overpass at capacity.
type headerTimeoutIsFinal struct {
	inner *http.Transport
}

func (t *headerTimeoutIsFinal) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.inner.RoundTrip(req)
	var ne net.Error
	if err != nil && errors.As(err, &ne) && ne.Timeout() &&
		time.Since(start) >= t.inner.ResponseHeaderTimeout {
		return resp, ingest.NoRetry(err)
	}
	return resp, err
}

// newBaseTransport is the innermost transport of the client stack.
// overpassHost is the configured Overpass endpoint's host
// (resolveOverpassURL), so the long header deadline follows the instance
// queries actually go to.
func newBaseTransport(overpassHost string) http.RoundTripper {
	return &hostTransport{
		overpassHost: overpassHost,
		overpass:     &headerTimeoutIsFinal{inner: newTransport(ingest.OverpassHeaderTimeout)},
		rest:         newPVMTTransport(),
	}
}

// httpClientFactory builds the lazy closure that backs f.HttpClient:
//
//   - sync.OnceValues caches the constructed *http.Client for the process
//     lifetime, so commands that never hit the network (e.g. --version,
//     `pvmt config show`) pay nothing for HTTP setup.
//   - The transport stack is, outermost → innermost,
//     cache.NewTransport → ingest.NewTransport (UA → Retry) → newBaseTransport
//     (newPVMTTransport, with a longer header deadline for Overpass).
//     The disk cache wraps the ingest middleware chain so cache hits skip
//     retries and UA stamping entirely.
//   - Client.Timeout is deliberately 0. A client-wide timeout kills
//     streaming bodies mid-transfer (large Overpass exports, ArcGIS pages),
//     and the retry transport already bounds the per-request work via
//     MaxBackoff + MaxRetries. Total request lifetime is the caller's
//     job via req.Context().
func httpClientFactory(f *cmdutil.Factory) func() (*http.Client, error) {
	return sync.OnceValues(func() (*http.Client, error) {
		cacheTTL, err := resolveCacheTTL(f)
		if err != nil {
			return nil, err
		}
		base, err := baseTransportFor(f)
		if err != nil {
			return nil, err
		}
		p, err := f.Paths()
		if err != nil {
			return nil, err
		}
		cacheDir := p.HTTPCacheDir()
		if err := paths.EnsureDir(cacheDir); err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil, hint.Withf(err, "check filesystem permissions on %s", p.Cache)
			}
			return nil, err
		}
		transport := cache.NewTransport(
			ingest.NewTransport(base),
			cacheDir,
			cacheTTL,
		)
		return &http.Client{
			Transport: transport,
			Timeout:   0,
			// Re-run the SSRF guard on every redirect hop. The per-call
			// ingest.validatePublicHTTPURL only checks the INITIAL URL;
			// without this, a 302 to 169.254.169.254/127.0.0.1 from a
			// hostile or MITM'd upstream defeats the guard. This custom
			// hook REPLACES the stdlib default, so we re-add the 10-hop
			// cap. The AllowPrivate opt-in is carried on the request
			// context (set by ArcGISSource.Fetch, propagated across
			// redirects).
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("stopped after 10 redirects")
				}
				if ingest.AllowPrivateFromContext(req.Context()) {
					return nil
				}
				if err := ingest.ValidatePublicHTTPURL(req.Context(), req.URL.String()); err != nil {
					return fmt.Errorf("redirect blocked: %w", err)
				}
				return nil
			},
		}, nil
	})
}

// resolveCacheTTL is the config's [cache] ttl, or cache.DefaultTTL when it is
// unset or there is no pvmt.toml (a command that fetches without a config).
// Any other config error is returned: a broken pvmt.toml must not silently
// fall back to the default TTL.
func resolveCacheTTL(f *cmdutil.Factory) (time.Duration, error) {
	if f.Config == nil {
		return cache.DefaultTTL, nil
	}
	cfg, err := f.Config()
	if errors.Is(err, config.ErrConfigNotFound) {
		return cache.DefaultTTL, nil
	}
	if err != nil {
		return 0, err
	}
	if cfg.Cache.TTL > 0 {
		return cfg.Cache.TTL, nil
	}
	return cache.DefaultTTL, nil
}

// baseTransportFor is newBaseTransport for the configured Overpass endpoint's
// host (resolveOverpassURL).
func baseTransportFor(f *cmdutil.Factory) (http.RoundTripper, error) {
	overpassURL, err := resolveOverpassURL(f)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(overpassURL)
	if err != nil {
		return nil, fmt.Errorf("overpass endpoint: %w", err)
	}
	return newBaseTransport(u.Hostname()), nil
}

// resolveOverpassURL is the Overpass endpoint (config.OverpassURL:
// PVMT_OVERPASS_URL, else [overpass] url), or ingest.DefaultOverpassURL when
// neither is set or there is no pvmt.toml. The env layer applies without a
// pvmt.toml too. Any other config error is returned, as resolveCacheTTL does:
// a broken pvmt.toml must not silently send queries to the default instance.
// It does no network check; overpassURLFactory adds the SSRF guard.
func resolveOverpassURL(f *cmdutil.Factory) (string, error) {
	var cfg *config.Config
	if f.Config != nil {
		c, err := f.Config()
		switch {
		case errors.Is(err, config.ErrConfigNotFound):
		case err != nil:
			return "", err
		default:
			cfg = c
		}
	}
	if u, _ := config.OverpassURL(cfg); u != "" {
		return u, nil
	}
	return ingest.DefaultOverpassURL, nil
}

// validatePublicHTTPURL is the SSRF guard overpassURLFactory applies; a var
// so tests can stand in for its DNS lookup, which unit tests may not make.
var validatePublicHTTPURL = ingest.ValidatePublicHTTPURL

// overpassURLFactory builds the lazy closure that backs f.OverpassURL: the
// endpoint resolveOverpassURL picks, resolved once per process. A non-default
// endpoint is operator-controlled, so it passes the SSRF guard
// (ingest.ValidatePublicHTTPURL) here, after config load and before the
// first Overpass fetch, and the run says on stderr which instance it uses
// (`overpass endpoint: <host>`), once. The default endpoint is fixed, so it
// skips both, and a run that configures nothing behaves exactly as before.
func overpassURLFactory(f *cmdutil.Factory) func() (string, error) {
	return sync.OnceValues(func() (string, error) {
		u, err := resolveOverpassURL(f)
		if err != nil || u == ingest.DefaultOverpassURL {
			return u, err
		}
		if err := validatePublicHTTPURL(f.RequestContext(), u); err != nil {
			return "", hint.Withf(fmt.Errorf("overpass endpoint %s: %w", u, err),
				"point [overpass] url or %s at a public Overpass instance, or unset both to use %s",
				config.EnvOverpassURL, ingest.DefaultOverpassHost)
		}
		parsed, err := url.Parse(u)
		if err != nil {
			return "", fmt.Errorf("overpass endpoint: %w", err)
		}
		if f.IOStreams != nil {
			fmt.Fprintf(f.IOStreams.ErrOut, "overpass endpoint: %s\n", parsed.Host)
		}
		return u, nil
	})
}

// configFactory builds the lazy closure that backs f.Config:
//
//   - sync.OnceValues caches the loaded *config.Config (and any error)
//     for the process lifetime, so commands which dereference Config
//     more than once pay one filesystem walk + parse total.
//   - The actual load is deferred to first call. Commands that never
//     touch the config — `pvmt --version`, `pvmt --help`, `pvmt
//     completion`, the __complete completion path, and the commands
//     that only touch the database or the cache — therefore pay
//     nothing for config discovery and survive a broken or absent
//     pvmt.toml. Nothing in the PersistentPreRunE middleware chain may
//     dereference this closure, or that guarantee evaporates for every
//     command at once.
//   - Errors are cached too: a broken config file fails once and
//     returns the same wrapped error on every subsequent call,
//     instead of re-walking the filesystem on each access.
//
// It is also where the config's own load warnings reach the user.
// internal/config has no IOStreams and cannot print, so it records
// non-fatal notices on the Config (today: per-city calibration
// superseded by an earlier [[include]]) and this closure emits them —
// the same "collect in config, print at the CLI boundary" split that
// warnInvalidEnv uses for silently-ignored PVMT_* env vars. Emitting
// here rather than from a middleware is what keeps the notices scoped:
// they appear exactly when a command actually reads the config, so
// `pvmt cache prune` and `pvmt gc` stay silent, and they cannot appear
// twice because the emission sits inside the sync.OnceValues body.
//
// loadConfigFromCwd is split out so tests can inject a counter-aware
// loader through lazyConfig without bypassing the once/lazy contract.
func configFactory(ios *iostreams.IOStreams) func() (*config.Config, error) {
	return lazyConfig(warnAfterLoad(ios, loadConfigFromCwd))
}

// warnAfterLoad wraps a loader so the config's own non-fatal notices are
// printed the once, right after the load that produced them. Split from
// configFactory so tests can drive it with a counting or failing loader
// without bypassing the once/lazy contract, exactly as loadConfigFromCwd is
// split out for lazyConfig.
//
// Nil-tolerant on both the streams and the config: a failed load (cfg == nil)
// is simply silent here, because the command that needed the config is the one
// that reports the failure.
func warnAfterLoad(ios *iostreams.IOStreams, load func() (*config.Config, error)) func() (*config.Config, error) {
	return func() (*config.Config, error) {
		cfg, err := load()
		if ios == nil || cfg == nil {
			return cfg, err
		}
		for _, w := range cfg.LoadWarnings() {
			cmdutil.Warnf(ios, "%s", w)
		}
		return cfg, err
	}
}

// loadConfigFromCwd finds and loads pvmt.toml from the working directory,
// then checks the service names it uses against the services registry
// (services.ValidateConfig), which internal/config cannot import. Both
// failures surface through hintForConfigError, so a typo'd service fails
// every command that reads the config with the invalid-config exit code.
func loadConfigFromCwd() (*config.Config, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	cfg, err := config.FindAndLoad(wd)
	if err == nil {
		if err = services.ValidateConfig(cfg); err != nil {
			cfg = nil
		}
	}
	return cfg, hintForConfigError(err)
}

func lazyConfig(load func() (*config.Config, error)) func() (*config.Config, error) {
	return sync.OnceValues(load)
}

func rootDBFactory(f *cmdutil.Factory, path string) func() (*db.RootStore, error) {
	return sync.OnceValues(func() (*db.RootStore, error) {
		resolved := path
		if resolved == "" {
			p, err := f.Paths()
			if err != nil {
				return nil, err
			}
			if err := paths.EnsureDir(p.Data); err != nil {
				if errors.Is(err, fs.ErrPermission) {
					return nil, hint.Withf(err, "check filesystem permissions on %s", p.Data)
				}
				return nil, err
			}
			resolved = filepath.Join(p.Data, db.FileName)
		}
		store, err := db.Open(resolved)
		if err != nil && errors.Is(err, fs.ErrPermission) {
			return nil, hint.Withf(err, "check filesystem permissions on %s", resolved)
		}
		return store, err
	})
}

// rootDBWithClose wraps a lazy RootDB accessor with an "opened" tracker and a
// matching close func. The close func releases the underlying handle only if
// the accessor was ever called successfully, so commands that never open the
// DB (--version, --help) don't create/close it just to shut down. The tracker
// is an atomic.Bool so concurrent RootDB() callers under -race stay safe.
func rootDBWithClose(open func() (*db.RootStore, error)) (func() (*db.RootStore, error), func() error) {
	var opened atomic.Bool
	get := func() (*db.RootStore, error) {
		store, err := open()
		if err == nil && store != nil {
			opened.Store(true)
		}
		return store, err
	}
	closeFn := func() error {
		if !opened.Load() {
			return nil
		}
		// open is sync.OnceValues-backed, so this returns the already-opened
		// handle without re-running Open.
		// open is sync.OnceValues-backed and opened==true means the first call
		// succeeded, so this returns that handle; the error is ignored on the
		// close path (nothing to close if it were somehow non-nil).
		store, _ := open()
		if store == nil {
			return nil
		}
		return store.Close()
	}
	return get, closeFn
}

func New() *cmdutil.Factory {
	ios := iostreams.System()

	// LogLevel defaults to Warn (quiet by default).
	// PersistentPreRunE on root mutates it after parsing -v / --log-level.
	lvl := new(slog.LevelVar)
	lvl.Set(slog.LevelWarn)
	logger := slog.New(slog.NewTextHandler(ios.ErrOut, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(logger)

	f := &cmdutil.Factory{
		AppVersion:     build.Version,
		ExecutableName: "pvmt",
		IOStreams:      ios,
		Config:         configFactory(ios),
		Logger:         logger,
		LogLevel:       lvl,
		Prompter:       prompt.NewLive(ios),
		Paths: sync.OnceValues(func() (*paths.Paths, error) {
			return paths.Resolve("pvmt")
		}),
	}

	f.HttpClient = httpClientFactory(f)
	f.OverpassURL = overpassURLFactory(f)
	f.RootDB, f.CloseRootDB = rootDBWithClose(rootDBFactory(f, ""))

	f.CityFlagSet = func() bool { return false }

	f.UnitSystem = func() units.System { return cmdutil.ConfigUnitSystem(f.Config) }

	f.CurrentCity = func() (*config.CityConfig, error) {
		c, err := f.Config()
		if err != nil {
			return nil, err
		}
		if len(c.Cities) == 0 {
			return nil, hintForConfigError(config.ErrNoCities)
		}
		return &c.Cities[0], nil
	}

	f.CityDB = buildCityDB(f)

	return f
}

// buildCityDB returns a CityDB closure that resolves the current city and
// returns its cmdutil.PinnedCityStore.
func buildCityDB(f *cmdutil.Factory) func() (db.Store, error) {
	return func() (db.Store, error) {
		city, err := f.CurrentCity()
		if err != nil {
			return nil, err
		}
		root, err := f.RootDB()
		if err != nil {
			return nil, err
		}
		// Honor the per-invocation context installed by the root command's
		// PersistentPreRunE (finding 0nyu). Falls back to Background when no
		// request ctx was set (e.g. a command exempt from PersistentPreRunE).
		return cmdutil.PinnedCityStore(f.RequestContext(), root, f.Config, city)
	}
}
