package snapshots

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/textutil"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type ShowOptions struct {
	IO            *iostreams.IOStreams
	RootDB        func() (db.RootStorer, error)
	ResolveCities func() ([]config.CityConfig, error)
	CityFlagSet   func() bool
	Exporter      cmdutil.Exporter
	Name          string
}

// showRow is one city of a snapshot.
type showRow struct {
	Snapshot   string    `json:"snapshot"`
	City       string    `json:"city"`
	ConfigHash string    `json:"config_hash"`
	HexEdgeM   float64   `json:"hex_edge_m"`
	Parts      []partRow `json:"parts"`
}

// partRow is one entry of a city's manifest: the run filling a part.
type partRow struct {
	Part       string `json:"part"`
	RunID      int64  `json:"run_id"`
	ComputedAt string `json:"computed_at"`
}

var _ cmdutil.RowExporter = showRow{}

var showFields = []string{"snapshot", fieldCity, "config_hash", "hex_edge_m", "parts"}

func (r showRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "snapshot":
			out[f] = r.Snapshot
		case fieldCity:
			out[f] = r.City
		case "config_hash":
			out[f] = r.ConfigHash
		case "hex_edge_m":
			out[f] = r.HexEdgeM
		case "parts":
			out[f] = r.Parts
		}
	}
	return out
}

func NewCmdShow(f *cmdutil.Factory, runF func(context.Context, *ShowOptions) error) *cobra.Command {
	opts := &ShowOptions{
		IO:            f.IOStreams,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
		CityFlagSet:   cityFlagSet(f),
	}

	cmd := &cobra.Command{
		Use:               "show <name>",
		Args:              snapshotNameArgs(1),
		ValidArgsFunction: cmdutil.SnapshotNameCompletion(f, 1),
		Short:             "Show each city in a snapshot",
		Long: `Show a snapshot: when it was created and by which compute epoch, then one
row per city it covers, with the config hash the city was computed under,
its hex edge, and the run filling each part ("roads#12" is run 12). Use
--city to show one city.`,
		Example: `  # Every city in a snapshot
  pvmt snapshots show 2026-11

  # One city
  pvmt --city oakland-ca snapshots show 2026-11

  # Machine-readable
  pvmt snapshots show 2026-11 --json city,hex_edge_m,parts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Name = args[0]
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runShow(cmd.Context(), opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, showFields)
	return cmd
}

func runShow(ctx context.Context, opts *ShowOptions) error {
	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	snap, err := lookupSnapshot(ctx, root, opts.Name)
	if err != nil {
		return err
	}
	cities := snap.Cities
	if opts.CityFlagSet != nil && opts.CityFlagSet() {
		selected, err := opts.ResolveCities()
		if err != nil {
			return err
		}
		cities = filterCities(cities, selected)
	}
	rows := make([]showRow, len(cities))
	for i, c := range cities {
		parts := make([]partRow, len(c.Parts))
		for j, p := range c.Parts {
			parts[j] = partRow{Part: p.Part, RunID: p.RunID, ComputedAt: p.ComputedAt.UTC().Format(time.RFC3339)}
		}
		rows[i] = showRow{Snapshot: snap.Name, City: c.CitySlug, ConfigHash: c.ConfigHash, HexEdgeM: c.HexEdgeM, Parts: parts}
	}
	if opts.Exporter != nil {
		return cmdutil.WriteRows(opts.IO, opts.Exporter, rows)
	}

	ios := opts.IO
	fmt.Fprintf(ios.ErrOut, "Snapshot %s: created %s, compute epoch %d, %d %s\n", snap.Name,
		iostreams.FormatTimestamp(snap.CreatedAt.UTC().Format(time.RFC3339), ios.IsTTY()),
		snap.ComputeEpoch, snap.CityCount, textutil.Plural(snap.CityCount, "city", "cities"))
	if len(rows) == 0 {
		fmt.Fprintf(ios.ErrOut, "It does not cover the selected city.\n")
		return nil
	}
	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("City", "Config Hash", "Hex Edge", "Parts")
	for _, r := range rows {
		parts := make([]string, len(r.Parts))
		for i, p := range r.Parts {
			parts[i] = p.Part + "#" + strconv.FormatInt(p.RunID, 10)
		}
		tp.AddRow(r.City, r.ConfigHash, strconv.FormatFloat(r.HexEdgeM, 'f', -1, 64)+" m", strings.Join(parts, " "))
	}
	return tp.Render()
}

// filterCities keeps the snapshot cities whose slug is one of selected's.
func filterCities(cities []db.SnapshotCity, selected []config.CityConfig) []db.SnapshotCity {
	want := make(map[string]bool, len(selected))
	for _, c := range selected {
		want[c.Slug()] = true
	}
	var out []db.SnapshotCity
	for _, c := range cities {
		if want[c.CitySlug] {
			out = append(out, c)
		}
	}
	return out
}
