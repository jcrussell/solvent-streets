package snapshots

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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
	Config        func() (*config.Config, error)
	Exporter      cmdutil.Exporter
}

type snapshotRow struct {
	City       string    `json:"city"`
	ID         int64     `json:"id"`
	CreatedAt  string    `json:"created_at"`
	ConfigHash string    `json:"config_hash"`
	Parts      []partRow `json:"parts"`
}

// partRow is one entry of a snapshot's manifest: the run filling a part.
type partRow struct {
	Part       string `json:"part"`
	RunID      int64  `json:"run_id"`
	ComputedAt string `json:"computed_at"`
}

var _ cmdutil.RowExporter = snapshotRow{}

var snapshotFields = []string{"city", "id", "created_at", "config_hash", "parts"}

func (r snapshotRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "city":
			out[f] = r.City
		case "id":
			out[f] = r.ID
		case "created_at":
			out[f] = r.CreatedAt
		case "config_hash":
			out[f] = r.ConfigHash
		case "parts":
			out[f] = r.Parts
		}
	}
	return out
}

func NewCmdLs(f *cmdutil.Factory, runF func(context.Context, *LsOptions) error) *cobra.Command {
	opts := &LsOptions{
		IO:            f.IOStreams,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
		Config:        func() (*config.Config, error) { return f.Config() },
	}

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List snapshots",
		Long: `List snapshots across configured cities, newest first. Use --city to
scope to one city.

A snapshot is a city's complete computed state: one run per part (roads,
parking, sidewalks, combined, each resource's forecast, service coverage,
census). A recompute commits a new snapshot that swaps in only the parts
it produced, so the Parts column shows which run each part comes from:
"roads#12" is run 12. Export and serve read the newest snapshot for the
current config.`,
		Example: `  # All snapshots across every city
  pvmt snapshots ls

  # Restrict to one city
  pvmt --city oakland snapshots ls

  # Machine-readable
  pvmt snapshots ls --json city,id,created_at,parts`,
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
	rows, err := collectSnapshotRows(ctx, opts.RootDB, opts.ResolveCities, opts.Config)
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
	tp.AddHeader("City", "ID", "Created", "Config Hash", "Parts")
	for _, r := range rows {
		parts := make([]string, len(r.Parts))
		for i, p := range r.Parts {
			parts[i] = p.Part + "#" + strconv.FormatInt(p.RunID, 10)
		}
		tp.AddRow(
			r.City,
			strconv.FormatInt(r.ID, 10),
			iostreams.FormatTimestamp(r.CreatedAt, ios.IsTTY()),
			r.ConfigHash,
			strings.Join(parts, " "),
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
	resolveConfig func() (*config.Config, error),
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
		store, err := cmdutil.EnsureCityStore(ctx, root, city, cmdutil.ResolveConfigID(resolveConfig, &city))
		if err != nil {
			return nil, err
		}
		snaps, err := store.ListSnapshots(ctx)
		if err != nil {
			return nil, fmt.Errorf("list snapshots for %s: %w", city.Slug(), err)
		}
		for _, s := range snaps {
			parts := make([]partRow, len(s.Parts))
			for i, p := range s.Parts {
				parts[i] = partRow{Part: p.Part, RunID: p.RunID, ComputedAt: p.ComputedAt.UTC().Format(time.RFC3339)}
			}
			rows = append(rows, snapshotRow{
				City:       city.Slug(),
				ID:         s.ID,
				CreatedAt:  s.CreatedAt.UTC().Format(time.RFC3339),
				ConfigHash: s.ConfigHash,
				Parts:      parts,
			})
		}
	}
	return rows, nil
}
