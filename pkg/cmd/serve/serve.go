package serve

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/coherence"
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
	// Snapshot is --snapshot: the snapshot to serve; "" serves each city's
	// newest.
	Snapshot  string
	ReadyFile string
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
		Long: `Start the MapLibre visualization server with API endpoints. Each city is
served from its newest snapshot, or from the snapshot --snapshot names, which
must cover every configured city; the page's snapshot picker shows any other,
and ?snapshot=latest shows the latest compute. A city with no snapshot is
served from its latest compute, with a warning, so a draft can be previewed
before 'pvmt snapshots create'. A city with neither is served empty, with a
warning, but when no city has a snapshot or a latest compute, or none has a
boundary stored, serve refuses to start and names the command to run.`,
		Example: `  # Serve on the default :8080
  pvmt serve

  # Serve one snapshot
  pvmt serve --snapshot 2026-11

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
	cmd.Flags().StringVar(&opts.Snapshot, "snapshot", "", "Publish the snapshot with this name instead of each city's newest (pvmt snapshots list)")
	_ = cmd.RegisterFlagCompletionFunc("snapshot", cmdutil.SnapshotNameCompletion(f, -1))
	cmd.Flags().StringVar(&opts.ReadyFile, "ready-file", "",
		"Write the listening URL to this file once the listener is bound (for orchestration)")

	return cmd
}

// Validate runs at the Options boundary:
// reject obviously-wrong --port values before opening the database or
// binding a listener. Port 0 is rejected even though net/http treats it
// as "pick a free one" because callers want a predictable address.
func (opts *Options) Validate() error {
	if opts.Port < 1 || opts.Port > 65535 {
		return cmdutil.FlagErrorf("--port %d is out of range (1-65535)", opts.Port)
	}
	if opts.Snapshot != "" {
		if err := db.CheckSnapshotName(opts.Snapshot); err != nil {
			return cmdutil.FlagErrorf("--snapshot: %v", err)
		}
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
		return err
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
	if opts.Snapshot != "" {
		if _, err := cmdutil.LookupSnapshot(ctx, rootDB, opts.Snapshot); err != nil {
			return err
		}
		err = pinNamed(ctx, entries, opts.Snapshot, opts.IO.ErrOut)
	} else {
		err = pinEntries(ctx, entries, opts.IO.ErrOut)
	}
	if err != nil {
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
// A city with neither is still served (as an empty map, like any uncomputed
// city) when another city has data, so say why once per city at startup
// rather than leave a blank map the operator has to diagnose. When no city
// has either, or none has a boundary stored, there is nothing to serve, and
// pinEntries refuses (exit 3) as export does, naming the command to run.
func pinEntries(ctx context.Context, entries []exportpkg.CityEntry, errOut io.Writer) error {
	var empty []error // the cities with no data, warned about once some city has some
	served, bounded := 0, 0
	for i, entry := range entries {
		gj, err := entry.Store.GetBoundary(ctx)
		if err != nil {
			return fmt.Errorf("boundary for %s: %w", entry.City.Name, err)
		}
		if gj != "" {
			bounded++
		}
		pinned, noData, err := pinEntry(ctx, entry, errOut)
		if err != nil {
			return err
		}
		entries[i] = pinned
		if noData != nil {
			empty = append(empty, noData)
		} else {
			served++
		}
	}
	switch {
	case bounded == 0:
		return hint.NeedsFirst(fmt.Errorf("nothing to serve: %s", noCity(entries, "boundary stored")), "pvmt all ingest")
	case served == 0:
		return hint.NeedsFirst(fmt.Errorf("nothing to serve: %s", noCity(entries, "snapshot or latest compute")), "pvmt all compute")
	}
	for _, err := range empty {
		cmdutil.FprintWarnf(errOut, "%v; serving it without data", err)
		hint.Fprint(errOut, err)
	}
	return nil
}

// pinEntry pins one city for pinEntries, warning on errOut about a snapshot
// created under other settings, a stale or empty latest compute and a city
// served from its latest compute. A city with neither a snapshot nor a latest
// compute is returned unpinned with noData, the hinted reason, for the caller
// to warn about or refuse with.
func pinEntry(ctx context.Context, entry exportpkg.CityEntry, errOut io.Writer) (pinned exportpkg.CityEntry, noData, err error) {
	// The latest compute first, so that a compute committing between the
	// two reads cannot leave the snapshot newer than it.
	latest, err := entry.Store.LatestCompute(ctx, entry.Config.CityHash(&entry.City))
	if err != nil {
		return entry, nil, fmt.Errorf("latest compute for %s: %w", entry.City.Name, err)
	}
	entry.Latest = latest // ?snapshot=latest
	snaps, err := entry.Store.ListSnapshots(ctx)
	if err != nil {
		return entry, nil, fmt.Errorf("list snapshots for %s: %w", entry.City.Name, err)
	}
	entry.Snapshots = append([]db.SnapshotCity{}, snaps...) // non-nil: read
	if err := warnStaleLatest(ctx, entry, errOut); err != nil {
		return entry, nil, err
	}
	if len(snaps) > 0 {
		snap := &snaps[0] // newest first
		if w := entry.SnapshotSettingsWarning(snap); w != "" {
			cmdutil.FprintWarnf(errOut, "%s; serving it as created", w)
		}
		if len(latest) == 0 {
			cmdutil.FprintWarnf(errOut, "%s has nothing computed under the current settings, so its "+
				"Latest compute view is empty; run 'pvmt --city %s all compute' to fill it", entry.City.Name, entry.Slug)
		}
		return entry.WithSnapshot(snap.ID), nil, nil
	}
	if len(latest) > 0 {
		cmdutil.FprintWarnf(errOut, "%s has no snapshot; serving its latest compute (not snapshotted); "+
			"run 'pvmt snapshots create <name>' to publish it", entry.City.Name)
		return exportpkg.LatestComputeEntry(entry, latest), nil, nil
	}
	noData = fmt.Errorf("%w for %s", exportpkg.ErrNoSnapshot, entry.City.Name)
	if len(entry.Config.SourceDivergence(&entry.City)) > 0 {
		noData = hint.With(noData, entry.Config.StaleHint(&entry.City))
	} else {
		noData = hint.Withf(noData, "%s has never been computed: run 'pvmt --city %s all compute' (after "+
			"'pvmt --city %s all ingest' if its data is not ingested yet), then 'pvmt snapshots create <name>'",
			entry.City.Name, entry.Slug, entry.Slug)
	}
	return entry, noData, nil
}

// noCity says that no configured city has what ("boundary stored").
func noCity(entries []exportpkg.CityEntry, what string) string {
	if len(entries) == 1 {
		return fmt.Sprintf("%s has no %s", entries[0].City.Name, what)
	}
	return fmt.Sprintf("none of the %d cities has a %s", len(entries), what)
}

// pinNamed pins every city to the snapshot named name (export.PinSnapshot),
// refusing to start when it does not cover every configured city. Each
// entry keeps its latest compute (?snapshot=latest) and snapshot list (the
// picker), read before the pin.
func pinNamed(ctx context.Context, entries []exportpkg.CityEntry, name string, errOut io.Writer) error {
	for i, entry := range entries {
		latest, err := entry.Store.LatestCompute(ctx, entry.Config.CityHash(&entry.City))
		if err != nil {
			return fmt.Errorf("latest compute for %s: %w", entry.City.Name, err)
		}
		snaps, err := entry.Store.ListSnapshots(ctx)
		if err != nil {
			return fmt.Errorf("list snapshots for %s: %w", entry.City.Name, err)
		}
		entries[i].Latest = latest
		entries[i].Snapshots = append([]db.SnapshotCity{}, snaps...) // non-nil: read
	}
	if err := exportpkg.PinSnapshot(ctx, entries, name, false, io.Discard); err != nil {
		return err
	}
	for _, entry := range entries {
		snap, err := entry.Store.ViewSnapshot(ctx)
		if err != nil {
			return fmt.Errorf("snapshot %q for %s: %w", name, entry.City.Name, err)
		}
		if w := entry.SnapshotSettingsWarning(snap); w != "" {
			cmdutil.FprintWarnf(errOut, "%s; serving it as created", w)
		}
	}
	return nil
}

// warnStaleLatest warns when a part of entry's latest compute read inputs
// ingested again since. Its view is drawn on the current boundary, so after a
// boundary re-ingest its map and its totals disagree until a recompute.
func warnStaleLatest(ctx context.Context, entry exportpkg.CityEntry, errOut io.Writer) error {
	if len(entry.Latest) == 0 {
		return nil
	}
	inputs, err := entry.Store.InputStates(ctx)
	if err != nil {
		return fmt.Errorf("inputs for %s: %w", entry.City.Name, err)
	}
	parts := make([]string, len(entry.Latest))
	for i, p := range entry.Latest {
		parts[i] = p.Part
	}
	var stale []string
	for _, st := range coherence.PartStates(parts, entry.Latest, inputs) {
		if st.State == coherence.StateStale {
			stale = append(stale, st.Part+" ("+strings.Join(st.Stale, ", ")+")")
		}
	}
	if len(stale) == 0 {
		return nil
	}
	cmdutil.FprintWarnf(errOut, "%s's latest compute is stale: %s. Its Latest compute view is drawn on the "+
		"current boundary and will not match its totals; run 'pvmt --city %s all compute'",
		entry.City.Name, strings.Join(stale, "; "), entry.Slug)
	return nil
}
