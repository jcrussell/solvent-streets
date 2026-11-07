package serve

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	exportpkg "github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/server"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO        *iostreams.IOStreams
	RootDB    func() (*db.RootStore, error)
	Config    func() (*config.Config, error)
	Cities    func() ([]config.CityConfig, error)
	Host      string
	Port      int
	ReadyFile string
	// Assets loads the site-root browser files. NewCmd sets
	// exportpkg.EmbeddedAssets, which fails with ErrNoWasm on a `go install`
	// build; tests pass fakes.
	Assets func() (*exportpkg.Assets, error)
}

func NewCmdServe(f *cmdutil.Factory, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO:     f.IOStreams,
		RootDB: f.RootDB,
		Config: f.Config,
		Cities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
		Assets: exportpkg.EmbeddedAssets,
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the web server",
		Long:  "Start the MapLibre visualization server with API endpoints.",
		Example: `  # Serve on the default :8080
  pvmt serve

  # Serve on an alternate port
  pvmt serve --port 9000`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runServe(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.Host, "host", "127.0.0.1", "Host interface to bind")
	cmd.Flags().IntVar(&opts.Port, "port", 8080, "Port to listen on")
	cmd.Flags().StringVar(&opts.ReadyFile, "ready-file", "",
		"Write the listening URL to this file once the listener is bound (for orchestration)")

	return cmd
}

// Validate runs at the Options boundary per byob-input-validation.5:
// reject obviously-wrong --port values before opening the database or
// binding a listener. Port 0 is rejected even though net/http treats it
// as "pick a free one" because callers want a predictable address.
func (opts *Options) Validate() error {
	if opts.Port < 1 || opts.Port > 65535 {
		return cmdutil.FlagErrorf("--port %d is out of range (1-65535)", opts.Port)
	}
	return nil
}

func runServe(ctx context.Context, opts *Options) error {
	if err := opts.Validate(); err != nil {
		return err
	}
	// Before any DB or network work: a `go install` build has no WASM and
	// cannot produce a working site (exportpkg.ErrNoWasm).
	assets, err := opts.Assets()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	rootDB, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	cities, err := opts.Cities()
	if err != nil {
		return fmt.Errorf("cities: %w", err)
	}

	entries, err := exportpkg.BuildCityEntries(ctx, rootDB, cfg, cities)
	if err != nil {
		return err
	}
	if err := pinEntries(ctx, entries, opts.IO.ErrOut); err != nil {
		return err
	}

	srv := server.New(entries, opts.Host, opts.Port, opts.IO, assets)
	srv.ReadyFile = opts.ReadyFile
	return srv.ListenAndServe(ctx)
}

// pinEntries pins every city to one snapshot: the newest for its config hash
// when the server starts. Each endpoint resolving "newest" on its own would
// mix snapshots after a compute commits mid-session, and responses are cached
// for the server's lifetime anyway (restart to pick up new data). ?snapshot=N
// still overrides the pin per request.
//
// A city with no snapshot under its current settings is still served (as an
// empty map, HTTP 200, like any uncomputed city), so say why once per city at
// startup — with export's error and hint — rather than leave a blank map the
// operator has to diagnose: the settings changed since its last compute, or an
// [[include]]d city resolves different data inputs than its own config.
func pinEntries(ctx context.Context, entries []exportpkg.CityEntry, errOut io.Writer) error {
	for i, entry := range entries {
		pinned, err := entry.PinCurrentSnapshot(ctx)
		if err != nil {
			return err
		}
		entries[i] = pinned
		if err := pinned.RequireMatchingSnapshot(ctx); err != nil {
			fmt.Fprintf(errOut, "warning: %v; serving it without data\n", err)
			var h *hint.ErrHint
			if errors.As(err, &h) {
				fmt.Fprintf(errOut, "  hint: %s\n", h.Hint)
			}
		}
	}
	return nil
}
