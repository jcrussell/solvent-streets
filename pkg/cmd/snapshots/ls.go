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

type LsOptions struct {
	IO            *iostreams.IOStreams
	RootDB        func() (db.RootStorer, error)
	ResolveCities func() ([]config.CityConfig, error)
	Exporter      cmdutil.Exporter
}

type snapshotRow struct {
	City       string `json:"city"`
	ID         int64  `json:"id"`
	ComputedAt string `json:"computedAt"`
	ConfigHash string `json:"configHash"`
}

var _ cmdutil.RowExporter = snapshotRow{}

var snapshotFields = []string{"city", "id", "computedAt", "configHash"}

func (r snapshotRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "city":
			out[f] = r.City
		case "id":
			out[f] = r.ID
		case "computedAt":
			out[f] = r.ComputedAt
		case "configHash":
			out[f] = r.ConfigHash
		}
	}
	return out
}

func NewCmdLs(f *cmdutil.Factory, runF func(context.Context, *LsOptions) error) *cobra.Command {
	opts := &LsOptions{
		IO:            f.IOStreams,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
	}

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List snapshots",
		Long:  "List snapshots across configured cities. Use --city to scope to one city.",
		Example: `  # All snapshots across every city
  pvmt snapshots ls

  # Restrict to one city
  pvmt --city oakland snapshots ls

  # Machine-readable
  pvmt snapshots ls --json city,id,computedAt`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runLs(cmd.Context(), opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, snapshotFields)
	return cmd
}

func runLs(ctx context.Context, opts *LsOptions) error {
	rows, err := collectSnapshotRows(ctx, opts.RootDB, opts.ResolveCities)
	if err != nil {
		return err
	}

	if opts.Exporter != nil {
		return cmdutil.WriteRows(opts.IO, opts.Exporter, rows)
	}

	ios := opts.IO
	if len(rows) == 0 {
		fmt.Fprintln(ios.ErrOut, "No snapshots in database. Run 'pvmt all compute' to create one.")
		return nil
	}

	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("City", "ID", "Computed", "Config Hash")
	for _, r := range rows {
		tp.AddRow(
			r.City,
			strconv.FormatInt(r.ID, 10),
			iostreams.FormatTimestamp(r.ComputedAt, ios.IsTTY()),
			r.ConfigHash,
		)
	}
	return tp.Render()
}

// collectSnapshotRows resolves cities from --city / config and returns
// every snapshot tagged with its owning city slug, newest-first per
// city (matching ListSnapshots' ORDER BY).
func collectSnapshotRows(
	ctx context.Context,
	rootDB func() (db.RootStorer, error),
	resolveCities func() ([]config.CityConfig, error),
) ([]snapshotRow, error) {
	cities, err := resolveCities()
	if err != nil {
		return nil, err
	}
	root, err := rootDB()
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}

	var rows []snapshotRow
	for _, city := range cities {
		id, err := root.EnsureCity(ctx, city.Slug(), city.Name)
		if err != nil {
			return nil, fmt.Errorf("ensure city %s: %w", city.Slug(), err)
		}
		store := root.ForCity(id)
		snaps, err := store.ListSnapshots(ctx)
		if err != nil {
			return nil, fmt.Errorf("list snapshots for %s: %w", city.Slug(), err)
		}
		for _, s := range snaps {
			rows = append(rows, snapshotRow{
				City:       city.Slug(),
				ID:         s.ID,
				ComputedAt: s.ComputedAt.UTC().Format(time.RFC3339),
				ConfigHash: s.ConfigHash,
			})
		}
	}
	return rows, nil
}
