package factory

import (
	"net/http"
	"sync"
	"time"

	"pvmt/internal/build"
	"pvmt/internal/cache"
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

		dbOnce  sync.Once
		dbStore db.Store
		dbErr   error
	)

	return &cmdutil.Factory{
		AppVersion:     build.Version,
		ExecutableName: "pvmt",
		IOStreams:       ios,
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
		DB: func() (db.Store, error) {
			dbOnce.Do(func() {
				dbStore, dbErr = db.Open("")
			})
			return dbStore, dbErr
		},
	}
}

// NewWithOptions creates a factory with custom cache TTL (0 = force bypass).
func NewWithOptions(cacheTTL time.Duration, dbPath string) *cmdutil.Factory {
	f := New()

	var (
		httpOnce   sync.Once
		httpClient *http.Client
		httpErr    error

		dbOnce  sync.Once
		dbStore db.Store
		dbErr   error
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
		f.DB = func() (db.Store, error) {
			dbOnce.Do(func() {
				dbStore, dbErr = db.Open(dbPath)
			})
			return dbStore, dbErr
		}
	}

	return f
}
