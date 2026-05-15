package factory

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
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
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// hintForConfigError attaches remediation text to known config-load failures.
// Returns err unchanged when no hint applies.
func hintForConfigError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, config.ErrConfigNotFound):
		return cmdutil.Hintf(err,
			"create a pvmt.toml in your project root, or cd into a directory that contains one.\nminimal example:\n  [[cities]]\n  name = \"Oakland\"")
	case errors.Is(err, config.ErrNoCities):
		return cmdutil.Hintf(err,
			"add a [[cities]] section to pvmt.toml, e.g.:\n  [[cities]]\n  name = \"Oakland\"")
	case errors.Is(err, fs.ErrPermission):
		return cmdutil.Hintf(err, "check filesystem permissions on the pvmt.toml path")
	default:
		return err
	}
}

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
			ingest.RetryTransport(
				ingest.UserAgentTransport(http.DefaultTransport),
				ingest.DefaultRetryConfig(),
			),
			cacheDir,
			cacheTTL,
		)
		return &http.Client{
			Transport: transport,
			Timeout:   300 * time.Second,
		}, nil
	})
}

func configFactory() func() (*config.Config, error) {
	return sync.OnceValues(func() (*config.Config, error) {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		cfg, err := config.FindAndLoad(wd)
		return cfg, hintForConfigError(err)
	})
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
// returns a city-scoped Store. Shared between New and NewWithOptions.
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
		return root.ForCity(id), nil
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
