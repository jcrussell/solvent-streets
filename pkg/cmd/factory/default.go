package factory

import (
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"pvmt/internal/build"
	"pvmt/internal/cache"
	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/ingest"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"
)

func New() *cmdutil.Factory {
	ios := iostreams.System()

	var (
		httpOnce   sync.Once
		httpClient *http.Client
		httpErr    error

		dbOnce    sync.Once
		rootStore *db.RootStore
		dbErr     error

		cfgOnce sync.Once
		cfg     *config.Config
		cfgErr  error
	)

	f := &cmdutil.Factory{
		AppVersion:     build.Version,
		ExecutableName: "pvmt",
		IOStreams:      ios,
		HttpClient: func() (*http.Client, error) {
			httpOnce.Do(func() {
				cacheDir, err := cache.DefaultDir()
				if err != nil {
					httpErr = err
					return
				}
				transport := cache.NewTransport(
					ingest.RetryTransport(
						ingest.UserAgentTransport(http.DefaultTransport),
						3,
					),
					cacheDir,
					24*time.Hour,
				)
				httpClient = &http.Client{
					Transport: transport,
					Timeout:   120 * time.Second,
				}
			})
			return httpClient, httpErr
		},
		Config: func() (*config.Config, error) {
			cfgOnce.Do(func() {
				wd, err := os.Getwd()
				if err != nil {
					cfgErr = err
					return
				}
				cfg, cfgErr = config.FindAndLoad(wd)
			})
			return cfg, cfgErr
		},
	}

	f.RootDB = func() (*db.RootStore, error) {
		dbOnce.Do(func() {
			rootStore, dbErr = db.Open("")
		})
		return rootStore, dbErr
	}

	f.CurrentCity = func() (*config.CityConfig, error) {
		c, err := f.Config()
		if err != nil {
			return nil, err
		}
		if len(c.Cities) == 0 {
			return nil, fmt.Errorf("no cities configured")
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
		id, err := root.EnsureCity(city.Slug(), city.Name)
		if err != nil {
			return nil, err
		}
		return root.ForCity(id), nil
	}
}

// NewWithOptions creates a factory with custom cache TTL (0 = force bypass).
func NewWithOptions(cacheTTL time.Duration, dbPath string) *cmdutil.Factory {
	f := New()

	var (
		httpOnce   sync.Once
		httpClient *http.Client
		httpErr    error

		dbOnce    sync.Once
		rootStore *db.RootStore
		dbErr     error
	)

	f.HttpClient = func() (*http.Client, error) {
		httpOnce.Do(func() {
			cacheDir, err := cache.DefaultDir()
			if err != nil {
				httpErr = err
				return
			}
			transport := cache.NewTransport(
				ingest.RetryTransport(
					ingest.UserAgentTransport(http.DefaultTransport),
					3,
				),
				cacheDir,
				cacheTTL,
			)
			httpClient = &http.Client{
				Transport: transport,
				Timeout:   120 * time.Second,
			}
		})
		return httpClient, httpErr
	}

	if dbPath != "" {
		f.RootDB = func() (*db.RootStore, error) {
			dbOnce.Do(func() {
				rootStore, dbErr = db.Open(dbPath)
			})
			return rootStore, dbErr
		}
		// Rebuild CityDB to use updated RootDB
		f.CityDB = buildCityDB(f)
	}

	return f
}
