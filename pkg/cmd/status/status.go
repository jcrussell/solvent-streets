package status

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO           *iostreams.IOStreams
	CityDB       func() (db.Store, error)
	UnitSystem   func() units.System
	ResourceType resource.Source // nil for global status
	Exporter     cmdutil.Exporter
}

type statusRow struct {
	ResourceType  string  `json:"resource_type"`
	FeatureCount  int     `json:"feature_count"`
	LastIngestAt  string  `json:"last_ingest_at,omitempty"`
	LastComputeAt string  `json:"last_compute_at,omitempty"`
	Area          float64 `json:"area,omitempty"`
	// EstimatedSpaces is the parking resource's estimated vehicle capacity
	// from its last compute; 0 and omitted for other resources.
	EstimatedSpaces int `json:"estimated_spaces,omitempty"`
}

var _ cmdutil.RowExporter = statusRow{}

func (r statusRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "resource_type":
			out[f] = r.ResourceType
		case "feature_count":
			out[f] = r.FeatureCount
		case "last_ingest_at":
			if r.LastIngestAt != "" {
				out[f] = r.LastIngestAt
			}
		case "last_compute_at":
			if r.LastComputeAt != "" {
				out[f] = r.LastComputeAt
			}
		case "area":
			// Area is a compute result: a computed 0 is a value, but a
			// never-computed row has none.
			if r.LastComputeAt != "" {
				out[f] = r.Area
			}
		case "estimated_spaces":
			if r.EstimatedSpaces != 0 {
				out[f] = r.EstimatedSpaces
			}
		}
	}
	return out
}

// Example is the --help example text for the status subcommand of the
// command group named group (`pvmt <group> status`). The group name is not
// derivable from the resource type (census blocks are stored as
// "layer-census-blocks" but their command is `pvmt census`).
func Example(group string) string {
	return fmt.Sprintf(`  # Show feature + result counts for %s
  pvmt %s status

  # Emit the row as JSON
  pvmt %s status --json resource_type,feature_count,area`, group, group, group)
}

var statusFields = []string{"resource_type", "feature_count", "last_ingest_at", "last_compute_at", "area", "estimated_spaces"}

func NewCmdStatus(f *cmdutil.Factory, rt resource.Source, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO:           f.IOStreams,
		CityDB:       f.CityDB,
		UnitSystem:   f.UnitSystem,
		ResourceType: rt,
	}

	use := "status"
	short := "Show overall status"
	long := `Report ingest and compute progress for the current city across every
resource type: feature counts, last-ingest and last-compute timestamps,
and paved area totals in the active unit system. On a TTY the output is
followed by a city-area summary (paved-vs-total %) and the five most
recent compute snapshots. Use --json with a comma-separated field list
(resource_type, feature_count, last_ingest_at, last_compute_at, area,
estimated_spaces) to emit one row per resource.`
	example := `  # Show feature + result counts across every resource
  pvmt status

  # Emit one row per resource as JSON
  pvmt status --json resource_type,feature_count,area`
	if rt != nil {
		short = fmt.Sprintf("Show %s status", rt.Type())
		long = fmt.Sprintf(`Report ingest and compute progress for the %s resource in the
current city: feature count, last-ingest and last-compute timestamps,
and paved area in the active unit system. Use --json with a
comma-separated field list (resource_type, feature_count, last_ingest_at,
last_compute_at, area, estimated_spaces) for a single row.`, rt.Type())
		example = Example(string(rt.Type()))
	}

	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Long:    long,
		Example: example,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runStatus(cmd.Context(), opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, statusFields)

	return cmd
}

func runStatus(ctx context.Context, opts *Options) error {
	ios := opts.IO

	store, err := opts.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	var sources []resource.Source
	if opts.ResourceType != nil {
		sources = []resource.Source{opts.ResourceType}
	} else {
		sources = resource.All
	}

	sys := opts.UnitSystem()

	var rows []statusRow
	for _, rt := range sources {
		rtVal := rt.Type()
		info, err := store.Stats(ctx, rtVal)
		if err != nil {
			fmt.Fprintf(ios.ErrOut, "Warning: could not get stats for %s: %v\n", rt.Type(), err)
			continue
		}
		row := statusRow{
			ResourceType:    string(rt.Type()),
			FeatureCount:    info.FeatureCount,
			Area:            info.TotalArea,
			EstimatedSpaces: info.EstimatedSpaces,
		}
		if info.LastIngestAt != nil {
			row.LastIngestAt = info.LastIngestAt.UTC().Format(time.RFC3339)
		}
		if info.LastComputeAt != nil {
			row.LastComputeAt = info.LastComputeAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}

	// JSON output
	if opts.Exporter != nil {
		return cmdutil.WriteRows(ios, opts.Exporter, rows)
	}

	// Table output
	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("Resource", "Features", "Last Ingest", "Last Compute", units.AreaLabel(sys), units.AreaLargeLabel(sys), "Est. Spaces")
	for _, r := range rows {
		ingestStr := iostreams.FormatTimestamp(r.LastIngestAt, ios.IsTTY())
		computeStr := iostreams.FormatTimestamp(r.LastComputeAt, ios.IsTTY())
		spacesStr := "-"
		if r.EstimatedSpaces > 0 {
			spacesStr = strconv.Itoa(r.EstimatedSpaces)
		}
		tp.AddRow(
			r.ResourceType,
			strconv.Itoa(r.FeatureCount),
			ingestStr,
			computeStr,
			fmt.Sprintf("%.0f", units.AreaValue(r.Area, sys)),
			fmt.Sprintf("%.1f", units.AreaLargeValue(r.Area, sys)),
			spacesStr,
		)
	}
	if err := tp.Render(); err != nil {
		return err
	}

	if ios.IsTTY() {
		printCitySummary(ctx, ios, store, rows, sys, opts.ResourceType == nil)
		printSnapshotHistory(ctx, ios, store)
	}

	return nil
}

func printCitySummary(ctx context.Context, ios *iostreams.IOStreams, store db.Store, rows []statusRow, sys units.System, allResources bool) {
	boundaryGJSON, err := store.GetBoundary(ctx)
	if err != nil || boundaryGJSON == "" {
		return
	}
	cityArea, err := geo.BoundaryArea(ctx, boundaryGJSON)
	if err != nil || cityArea <= 0 {
		return
	}
	totalPaved, accurate := totalPavedArea(ctx, store, rows, allResources)
	fmt.Fprintf(ios.ErrOut, "\n=== City Summary ===\n")
	fmt.Fprintf(ios.ErrOut, "  City Area:    %s (%s)\n", units.FormatAreaLarge(cityArea, sys), units.FormatAreaVeryLarge(cityArea, sys))
	pavedLabel := "  Paved Area:   "
	if !accurate {
		// All-resources view with no combined (de-duplicated) compute row —
		// this is the per-resource sum, which double-counts road/parking/
		// sidewalk buffer overlap. Flag it so the number isn't mistaken for
		// the true union.
		pavedLabel = "  Paved Area*:  "
	}
	fmt.Fprintf(ios.ErrOut, "%s%s (%s)\n", pavedLabel, units.FormatAreaLarge(totalPaved, sys), units.FormatAreaVeryLarge(totalPaved, sys))
	if totalPaved > 0 {
		if accurate {
			fmt.Fprintf(ios.ErrOut, "  %% Paved:      %.1f%%\n", totalPaved/cityArea*100)
		} else {
			fmt.Fprintf(ios.ErrOut, "  %% Paved*:     %.1f%%\n", totalPaved/cityArea*100)
		}
	}
	if !accurate {
		fmt.Fprintf(ios.ErrOut, "  * sum of per-resource areas (double-counts overlap); run 'pvmt all compute' for the de-duplicated total.\n")
	}
}

// totalPavedArea returns the paved area for the City Summary and whether that
// figure is free of cross-resource double-counting.
//
// For an all-resources status it prefers the "combined" ComputeResult written
// by `pvmt all compute` (the de-duplicated union, mirroring
// export.totalPavedFromStore so `pvmt status` agrees with meta.json); when that
// row is absent it falls back to summing per-resource areas, which
// intentionally double-counts where road/parking/sidewalk buffers overlap
// (accurate=false → footnoted).
//
// For a single-resource status (`pvmt <res> status`) the sum is just that one
// resource — no overlap to de-duplicate — so it is returned as-is and reported
// as accurate; pulling the combined union here would mismatch the single-row
// table above it.
func totalPavedArea(ctx context.Context, store db.Store, rows []statusRow, allResources bool) (float64, bool) {
	if allResources {
		if r, err := store.LatestComputeResult(ctx, resource.CombinedAll); err == nil && r != nil {
			return r.TotalArea, true
		}
	}
	var sum float64
	for _, r := range rows {
		sum += r.Area
	}
	// A single-resource sum has no cross-resource overlap, so it is accurate;
	// an all-resources sum reached here only as a fallback and double-counts.
	return sum, !allResources
}

func printSnapshotHistory(ctx context.Context, ios *iostreams.IOStreams, store db.Store) {
	snapshots, err := store.ListSnapshots(ctx)
	if err != nil || len(snapshots) == 0 {
		return
	}
	fmt.Fprintf(ios.ErrOut, "\n=== Snapshots ===\n")
	limit := min(len(snapshots), 5)
	for _, s := range snapshots[:limit] {
		fmt.Fprintf(ios.ErrOut, "  #%d  %s  (%s)  %d parts\n", s.ID, s.CreatedAt.Format(time.RFC3339), iostreams.RelativeTime(s.CreatedAt), len(s.Parts))
	}
	if len(snapshots) > 5 {
		fmt.Fprintf(ios.ErrOut, "  ... and %d more\n", len(snapshots)-5)
	}
}
