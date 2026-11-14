package snapshots

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/coherence"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type CreateOptions struct {
	IO            *iostreams.IOStreams
	RootDB        func() (db.RootStorer, error)
	ResolveCities func() ([]config.CityConfig, error)
	Config        func() (*config.Config, error)
	Exporter      cmdutil.Exporter
}

// Create statuses, as --json reports them.
const (
	statusCreated   = "created"
	statusUnchanged = "unchanged"
	statusRefused   = "refused"
)

// createRow is one city's outcome.
type createRow struct {
	City       string   `json:"city"`
	Status     string   `json:"status"`
	SnapshotID int64    `json:"snapshot_id,omitempty"`
	HexEdgeM   float64  `json:"hex_edge_m,omitempty"`
	Reasons    []string `json:"reasons"`
	Warnings   []string `json:"warnings"`
	Hint       string   `json:"hint,omitempty"`
}

var _ cmdutil.RowExporter = createRow{}

var createFields = []string{"city", "status", "snapshot_id", "hex_edge_m", "reasons", "warnings", "hint"}

func (r createRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "city":
			out[f] = r.City
		case "status":
			out[f] = r.Status
		case "snapshot_id":
			if r.SnapshotID != 0 {
				out[f] = r.SnapshotID
			}
		case "hex_edge_m":
			if r.HexEdgeM != 0 {
				out[f] = r.HexEdgeM
			}
		case "reasons":
			out[f] = nonNil(r.Reasons)
		case "warnings":
			out[f] = nonNil(r.Warnings)
		case "hint":
			if r.Hint != "" {
				out[f] = r.Hint
			}
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func NewCmdCreate(f *cmdutil.Factory, runF func(context.Context, *CreateOptions) error) *cobra.Command {
	opts := &CreateOptions{
		IO:            f.IOStreams,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
		Config:        func() (*config.Config, error) { return f.Config() },
	}

	cmd := &cobra.Command{
		Use:   "create",
		Args:  cobra.NoArgs,
		Short: "Freeze each city's latest compute as a snapshot",
		Long: `Freeze each city's latest compute (the newest run of every part under the
current settings) as an immutable snapshot, with its hex edge and boundary.
Every configured city by default; restrict to one with --city. Export and
serve publish each city's newest snapshot; computes only update the latest
compute.

A snapshot must be coherent. create refuses a city when any part was
computed from features or a boundary that have been ingested again since,
when it has no pavement compute, when pavement has no combined pass, or
when a pavement resource was ingested but never computed. The refusal names
what changed and the command that fixes it. A missing optional part
(service coverage, census population) whose inputs are stored is a warning,
not a refusal.

A city whose newest snapshot already freezes exactly its latest compute is
left unchanged. create processes every city, then exits 1 if any was
refused.`,
		Example: `  # Snapshot every configured city
  pvmt snapshots create

  # One city
  pvmt --city oakland-ca snapshots create

  # Machine-readable
  pvmt snapshots create --json city,status,snapshot_id,reasons`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runCreate(cmd.Context(), opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, createFields)
	return cmd
}

func runCreate(ctx context.Context, opts *CreateOptions) error {
	cities, err := opts.ResolveCities()
	if err != nil {
		return err
	}
	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	rows := make([]createRow, 0, len(cities))
	refused := false
	for i := range cities {
		city := &cities[i]
		row, err := createForCity(ctx, root, cfg, city)
		if err != nil {
			return err
		}
		if opts.Exporter == nil {
			printCreateRow(opts.IO.Out, row)
		}
		refused = refused || row.Status == statusRefused
		rows = append(rows, row)
	}
	if opts.Exporter != nil {
		if err := cmdutil.WriteRows(opts.IO, opts.Exporter, rows); err != nil {
			return err
		}
	}
	if refused {
		return cmdutil.ErrSilent
	}
	return nil
}

// createForCity snapshots one city's latest compute. A refusal is a row,
// not an error; an error is a failure to read or write the database.
func createForCity(ctx context.Context, root db.RootStorer, cfg *config.Config, city *config.CityConfig) (createRow, error) {
	row := createRow{City: city.Slug()}
	store, err := cmdutil.EnsureCityStore(ctx, root, *city, cfg.CityConfigID(city))
	if err != nil {
		return row, err
	}
	var report coherence.Report
	check := func(parts []db.LatestPart, inputs []db.InputState) error {
		report = coherence.Check(city, parts, inputs)
		return report.Err()
	}
	snap, created, err := store.CreateSnapshot(ctx, cfg.CityHash(city), cfg.ResolvedHexEdge(city), check)
	row.Warnings = report.Warnings
	var refusedErr *coherence.RefusedError
	switch {
	case errors.As(err, &refusedErr):
		row.Status, row.Reasons, row.Hint = statusRefused, refusedErr.Report.Refusals, refusedErr.Report.Hint
		return row, nil
	case errors.Is(err, db.ErrNothingComputed):
		row.Status, row.Reasons = statusRefused, []string{err.Error()}
		row.Hint = "pvmt --city " + city.Slug() + " all compute"
		return row, nil
	case err != nil:
		return row, fmt.Errorf("create snapshot for %s: %w", city.Slug(), err)
	}
	row.Status, row.SnapshotID, row.HexEdgeM = statusUnchanged, snap.ID, snap.HexEdgeM
	if created {
		row.Status = statusCreated
	}
	return row, nil
}

// printCreateRow writes one city's outcome: "oakland-ca: created #12",
// "unchanged (#12)" or "refused: <reasons>", then its hint and warnings.
func printCreateRow(w io.Writer, r createRow) {
	switch r.Status {
	case statusCreated:
		fmt.Fprintf(w, "%s: created #%d\n", r.City, r.SnapshotID)
	case statusUnchanged:
		fmt.Fprintf(w, "%s: unchanged (#%d)\n", r.City, r.SnapshotID)
	default:
		fmt.Fprintf(w, "%s: refused: %s\n", r.City, strings.Join(r.Reasons, "; "))
	}
	if r.Hint != "" {
		fmt.Fprintf(w, "  hint: %s\n", r.Hint)
	}
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warn)
	}
}
