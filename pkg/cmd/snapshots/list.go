package snapshots

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type ListOptions struct {
	IO            *iostreams.IOStreams
	RootDB        func() (db.RootStorer, error)
	ResolveCities func() ([]config.CityConfig, error)
	Config        func() (*config.Config, error)
	CityFlagSet   func() bool
	Exporter      cmdutil.Exporter
}

// snapshotRow is one snapshot (one tag) in `snapshots list`.
type snapshotRow struct {
	Name         string `json:"name"`
	CreatedAt    string `json:"created_at"`
	CityCount    int    `json:"city_count"`
	ComputeEpoch int    `json:"compute_epoch"`
}

var _ cmdutil.RowExporter = snapshotRow{}

var snapshotFields = []string{"name", "created_at", "city_count", "compute_epoch"}

func (r snapshotRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "name":
			out[f] = r.Name
		case "created_at":
			out[f] = r.CreatedAt
		case "city_count":
			out[f] = r.CityCount
		case "compute_epoch":
			out[f] = r.ComputeEpoch
		}
	}
	return out
}

func NewCmdList(f *cmdutil.Factory, runF func(context.Context, *ListOptions) error) *cobra.Command {
	opts := &ListOptions{
		IO:            f.IOStreams,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
		Config:        func() (*config.Config, error) { return f.Config() },
		CityFlagSet:   cityFlagSet(f),
	}

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		Short:   "List snapshots",
		Long: `List snapshots, newest first: each one's name, when it was created, how
many cities it covers, and the compute epoch of the pvmt that created it.
With --city, only the snapshots that cover that city.

A snapshot is a named tag over every configured city: each city's complete
computed state, one run per part. Use 'pvmt snapshots show <name>' for each
city's settings and runs. Export and serve publish the newest snapshot.`,
		Example: `  # Every snapshot
  pvmt snapshots list

  # The snapshots that cover one city
  pvmt --city oakland-ca snapshots list

  # Machine-readable
  pvmt snapshots list --json name,created_at,city_count`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runList(cmd.Context(), opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, snapshotFields)
	return cmd
}

func runList(ctx context.Context, opts *ListOptions) error {
	rows, err := collectSnapshotRows(ctx, opts)
	if err != nil {
		return err
	}

	if opts.Exporter != nil {
		return cmdutil.WriteRows(opts.IO, opts.Exporter, rows)
	}

	ios := opts.IO
	if len(rows) == 0 {
		fmt.Fprintln(ios.ErrOut, "No snapshots. Run 'pvmt all compute', then 'pvmt snapshots create <name>'.")
		return nil
	}

	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("Name", "Created", "Cities", "Epoch")
	for _, r := range rows {
		tp.AddRow(
			r.Name,
			iostreams.FormatTimestamp(r.CreatedAt, ios.IsTTY()),
			strconv.Itoa(r.CityCount),
			strconv.Itoa(r.ComputeEpoch),
		)
	}
	return tp.Render()
}

// collectSnapshotRows returns every snapshot newest first or, with --city,
// those covering the selected city.
func collectSnapshotRows(ctx context.Context, opts *ListOptions) ([]snapshotRow, error) {
	root, err := opts.RootDB()
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	snaps, err := root.ListSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	var covers map[int64]bool
	if opts.CityFlagSet != nil && opts.CityFlagSet() {
		if covers, err = coveringSnapshots(ctx, opts, root); err != nil {
			return nil, err
		}
	}
	rows := make([]snapshotRow, 0, len(snaps))
	for _, s := range snaps {
		if covers != nil && !covers[s.ID] {
			continue
		}
		rows = append(rows, snapshotRow{
			Name:         s.Name,
			CreatedAt:    s.CreatedAt.UTC().Format(time.RFC3339),
			CityCount:    s.CityCount,
			ComputeEpoch: s.ComputeEpoch,
		})
	}
	return rows, nil
}

// coveringSnapshots returns the ids of the snapshots that cover the cities
// --city selects.
func coveringSnapshots(ctx context.Context, opts *ListOptions, root db.RootStorer) (map[int64]bool, error) {
	cities, err := opts.ResolveCities()
	if err != nil {
		return nil, err
	}
	covers := map[int64]bool{}
	for _, city := range cities {
		store, err := cmdutil.EnsureCityStore(ctx, root, city, cmdutil.ResolveConfigID(opts.Config, &city))
		if err != nil {
			return nil, err
		}
		own, err := store.ListSnapshots(ctx)
		if err != nil {
			return nil, fmt.Errorf("list snapshots for %s: %w", city.Slug(), err)
		}
		for _, s := range own {
			covers[s.ID] = true
		}
	}
	return covers, nil
}
