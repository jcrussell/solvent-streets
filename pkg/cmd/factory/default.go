package factory

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jcrussell/solvent-streets/internal/build"
	"github.com/jcrussell/solvent-streets/internal/cache"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/ingest"
	"github.com/jcrussell/solvent-streets/internal/paths"
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
		return cmdutil.Hintf(err,
			"create a pvmt.toml in your project root, or cd into a directory that contains one.\nminimal example:\n  [[cities]]\n  name = \"Oakland\"")
	case errors.Is(err, config.ErrNoCities):
		return cmdutil.Hintf(cmdutil.FlagErrorf("%w", err),
			"add a [[cities]] section to pvmt.toml, e.g.:\n  [[cities]]\n  name = \"Oakland\"")
	case errors.Is(err, config.ErrInvalidConfig):
		return cmdutil.FlagErrorf("%w", err)
	case errors.Is(err, fs.ErrPermission):
		return cmdutil.Hintf(err, "check filesystem permissions on the pvmt.toml path")
	default:
		return err
	}
}

// newPVMTTransport returns the base *http.Transport that backs every
// outbound pvmt HTTP request, with the byob-http-client.2 timeout contract
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
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
	}
}

// httpClientFactory builds the lazy closure that backs f.HttpClient per the
// byob-http-client.2 contract:
//
//   - sync.OnceValues caches the constructed *http.Client for the process
//     lifetime, so commands that never hit the network (e.g. --version,
//     `pvmt config show`) pay nothing for HTTP setup.
//   - The transport stack is, outermost → innermost,
//     cache.NewTransport → ingest.NewTransport (UA → Retry) → newPVMTTransport.
//     The disk cache wraps the ingest middleware chain so cache hits skip
//     retries and UA stamping entirely (byob-http-client.1).
//   - Client.Timeout is deliberately 0. A client-wide timeout kills
//     streaming bodies mid-transfer (large Overpass exports, ArcGIS pages),
//     and the retry transport already bounds the per-request work via
//     MaxBackoff + MaxRetries. Total request lifetime is the caller's
//     job via req.Context().
func httpClientFactory(f *cmdutil.Factory, cacheTTL time.Duration) func() (*http.Client, error) {
	return sync.OnceValues(func() (*http.Client, error) {
		p, err := f.Paths()
		if err != nil {
			return nil, err
		}
		cacheDir := filepath.Join(p.Cache, "http")
		if err := paths.EnsureDir(cacheDir); err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil, cmdutil.Hintf(err, "check filesystem permissions on %s", p.Cache)
			}
			return nil, err
		}
		transport := cache.NewTransport(
			ingest.NewTransport(newPVMTTransport()),
			cacheDir,
			cacheTTL,
		)
		return &http.Client{
			Transport: transport,
			Timeout:   0,
		}, nil
	})
}

// configFactory builds the lazy closure that backs f.Config per the
// byob-config.3 contract:
//
//   - sync.OnceValues caches the loaded *config.Config (and any error)
//     for the process lifetime, so commands which dereference Config
//     more than once pay one filesystem walk + parse total.
//   - The actual load is deferred to first call. Commands that never
//     touch the config — `pvmt --version`, `pvmt --help`, `pvmt
//     completion`, the __complete completion path — therefore pay
//     nothing for config discovery and survive a broken or absent
//     pvmt.toml. PersistentPreRunE's skipMiddleware list keeps those
//     commands from going through warnInvalidConfig, which is the
//     other place the closure would otherwise be triggered.
//   - Errors are cached too: a broken config file fails once and
//     returns the same wrapped error on every subsequent call,
//     instead of re-walking the filesystem on each access.
//
// loadConfigFromCwd is split out so tests can inject a counter-aware
// loader through lazyConfig without bypassing the once/lazy contract.
func configFactory() func() (*config.Config, error) {
	return lazyConfig(loadConfigFromCwd)
}

func loadConfigFromCwd() (*config.Config, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	cfg, err := config.FindAndLoad(wd)
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
					return nil, cmdutil.Hintf(err, "check filesystem permissions on %s", p.Data)
				}
				return nil, err
			}
			resolved = filepath.Join(p.Data, "pvmt.db")
		}
		store, err := db.Open(resolved)
		if err != nil && errors.Is(err, fs.ErrPermission) {
			return nil, cmdutil.Hintf(err, "check filesystem permissions on %s", resolved)
		}
		return store, err
	})
}

func New() *cmdutil.Factory {
	ios := iostreams.System()

	// LogLevel defaults to Warn (byob-logging.3: quiet by default).
	// PersistentPreRunE on root mutates it after parsing -v / --log-level.
	lvl := new(slog.LevelVar)
	lvl.Set(slog.LevelWarn)
	logger := slog.New(slog.NewTextHandler(ios.ErrOut, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(logger)

	f := &cmdutil.Factory{
		AppVersion:     build.Version,
		ExecutableName: "pvmt",
		IOStreams:      ios,
		Config:         configFactory(),
		Logger:         logger,
		LogLevel:       lvl,
		Prompter:       prompt.NewLive(ios),
		Paths: sync.OnceValues(func() (*paths.Paths, error) {
			return paths.Resolve("pvmt")
		}),
	}

	f.HttpClient = httpClientFactory(f, 24*time.Hour)
	f.RootDB = rootDBFactory(f, "")

	f.CityFlagSet = func() bool { return false }

	f.UnitSystem = func() units.System {
		c, err := f.Config()
		if err != nil {
			return units.Imperial
		}
		return c.UnitSystem()
	}

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
// returns a city-scoped Store. The store is auto-pinned to the loaded
// config's hash via Store.WithConfigHash so unpinned snapshot-aware
// reads (status, forecast, etc.) only see snapshots written by the
// same config — preventing slug-sharing examples from reading each
// other's data. If config load fails the pin is silently skipped,
// preserving today's behavior for commands that work without a config.
// Shared between New and NewWithOptions.
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
		id, err := root.EnsureCity(context.Background(), city.Slug(), city.Name)
		if err != nil {
			return nil, err
		}
		store := root.ForCity(id)
		if cfg, cfgErr := f.Config(); cfgErr == nil && cfg != nil {
			store = store.WithConfigHash(cfg.Hash())
		}
		return store, nil
	}
}

// NewWithOptions creates a factory with custom cache TTL (0 = force bypass).
func NewWithOptions(cacheTTL time.Duration, dbPath string) *cmdutil.Factory {
	f := New()
	f.HttpClient = httpClientFactory(f, cacheTTL)
	if dbPath != "" {
		f.RootDB = rootDBFactory(f, dbPath)
		// Rebuild CityDB to use updated RootDB
		f.CityDB = buildCityDB(f)
	}
	return f
}
