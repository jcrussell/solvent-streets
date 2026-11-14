package serve

import (
	"context"
	"fmt"
	"io"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	exportpkg "github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/server"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO     *iostreams.IOStreams
	RootDB func() (*db.RootStore, error)
	Config func() (*config.Config, error)
	Cities func() ([]config.CityConfig, error)
	// UnitSystem is the display unit system the site opens in: --units, else
	// the config's (cmdutil.Factory.UnitSystem).
	UnitSystem func() units.System
	Host       string
	Port       int
	ReadyFile  string
	// Assets loads the site-root browser files. NewCmd sets
	// exportpkg.EmbeddedAssets, which fails with ErrNoWasm on a `go install`
	// build; tests pass fakes.
	Assets func() (*exportpkg.Assets, error)
}

func NewCmdServe(f *cmdutil.Factory, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO:         f.IOStreams,
		RootDB:     f.RootDB,
		Config:     f.Config,
		Cities:     func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
		UnitSystem: f.UnitSystem,
		Assets:     exportpkg.EmbeddedAssets,
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Args:  cobra.NoArgs,
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
	if opts.UnitSystem != nil {
		srv.UnitSystem = opts.UnitSystem().String()
	}
	return srv.ListenAndServe(ctx)
}

// pinEntries pins every city when the server starts: to its newest snapshot,
// whatever settings it was created under (warning when they are not the
// current ones), or, for a city with no snapshot, to its latest compute as it
// stands now (Store.WithRuns), so later computes do not mix into a running
// server's pages. Responses are cached for the server's lifetime anyway
// (restart to pick up new data). ?snapshot=N still overrides the pin per
// request, and ?snapshot=latest shows the latest compute as read here.
//
// A city with neither is still served (as an empty map, HTTP 200, like any
// uncomputed city), so say why once per city at startup rather than leave a
// blank map the operator has to diagnose.
func pinEntries(ctx context.Context, entries []exportpkg.CityEntry, errOut io.Writer) error {
	for i, entry := range entries {
		// The latest compute first, so that a compute committing between the
		// two reads cannot leave the snapshot newer than it.
		latest, err := entry.Store.LatestCompute(ctx, entry.Config.CityHash(&entry.City))
		if err != nil {
			return fmt.Errorf("latest compute for %s: %w", entry.City.Name, err)
		}
		entry.Latest = latest // ?snapshot=latest
		snap, err := entry.NewestSnapshot(ctx)
		if err != nil {
			return err
		}
		if snap != nil {
			entries[i] = entry.WithSnapshot(snap.ID)
			if w := entry.SnapshotSettingsWarning(snap); w != "" {
				cmdutil.FprintWarnf(errOut, "%s; serving it as created", w)
			}
			continue
		}
		entries[i] = entry
		if len(latest) > 0 {
			entries[i] = exportpkg.LatestComputeEntry(entry, latest)
			cmdutil.FprintWarnf(errOut, "%s has no snapshot; serving its latest compute (not snapshotted). "+
				"Run `pvmt --city %s snapshots create` to publish it", entry.City.Name, entry.Slug)
			continue
		}
		err = fmt.Errorf("%w for %s", exportpkg.ErrNoSnapshot, entry.City.Name)
		if len(entry.Config.SourceDivergence(&entry.City)) > 0 {
			err = hint.With(err, entry.Config.StaleHint(&entry.City))
		} else {
			err = hint.Withf(err, "%s has never been computed. Run `pvmt --city %s all compute` (after "+
				"`pvmt --city %s all ingest` if its data is not ingested yet), then `pvmt --city %s snapshots create`.",
				entry.City.Name, entry.Slug, entry.Slug, entry.Slug)
		}
		cmdutil.FprintWarnf(errOut, "%v; serving it without data", err)
		hint.Fprint(errOut, err)
	}
	return nil
}
