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
	ResourceType string  `json:"resourceType"`
	FeatureCount int     `json:"featureCount"`
	LastIngest   string  `json:"lastIngest,omitempty"`
	LastCompute  string  `json:"lastCompute,omitempty"`
	Area         float64 `json:"area,omitempty"`
}

var _ cmdutil.RowExporter = statusRow{}

func (r statusRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "resourceType":
			out[f] = r.ResourceType
		case "featureCount":
			out[f] = r.FeatureCount
		case "lastIngest":
			out[f] = r.LastIngest
		case "lastCompute":
			out[f] = r.LastCompute
		case "area":
			out[f] = r.Area
		}
	}
	return out
}

var statusFields = []string{"resourceType", "featureCount", "lastIngest", "lastCompute", "area"}

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
recent compute snapshots. Use --json to emit a single row per resource.`
	example := `  # Show feature + result counts across every resource
  pvmt status

  # Emit a single status row as JSON
  pvmt status --json`
	if rt != nil {
		short = fmt.Sprintf("Show %s status", rt.Type())
		long = fmt.Sprintf(`Report ingest and compute progress for the %s resource in the
current city: feature count, last-ingest and last-compute timestamps,
and paved area in the active unit system. Use --json for a single row.`, rt.Type())
		example = fmt.Sprintf(`  # Show feature + result counts for %s
  pvmt %s status`, rt.Type(), rt.Type())
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
			ResourceType: string(rt.Type()),
			FeatureCount: info.FeatureCount,
			Area:         info.TotalArea,
		}
		if info.LastIngestAt != nil {
			row.LastIngest = info.LastIngestAt.Format(time.RFC3339)
		}
		if info.LastComputeAt != nil {
			row.LastCompute = info.LastComputeAt.Format(time.RFC3339)
		}
		rows = append(rows, row)
	}

	// JSON output
	if opts.Exporter != nil {
		return cmdutil.WriteRows(ios, opts.Exporter, rows)
	}

	// Table output
	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("Resource", "Features", "Last Ingest", "Last Compute", units.AreaLabel(sys), units.AreaLargeLabel(sys))
	for _, r := range rows {
		ingestStr := iostreams.FormatTimestamp(r.LastIngest, ios.IsTTY())
		computeStr := iostreams.FormatTimestamp(r.LastCompute, ios.IsTTY())
		tp.AddRow(
			r.ResourceType,
			strconv.Itoa(r.FeatureCount),
			ingestStr,
			computeStr,
			fmt.Sprintf("%.0f", units.AreaValue(r.Area, sys)),
			fmt.Sprintf("%.1f", units.AreaLargeValue(r.Area, sys)),
		)
	}
	if err := tp.Render(); err != nil {
		return err
	}

	if ios.IsTTY() {
		printCitySummary(ctx, ios, store, rows, sys)
		printSnapshotHistory(ctx, ios, store)
	}

	return nil
}

func printCitySummary(ctx context.Context, ios *iostreams.IOStreams, store db.Store, rows []statusRow, sys units.System) {
	boundaryGJSON, err := store.GetBoundary(ctx)
	if err != nil || boundaryGJSON == "" {
		return
	}
	cityArea, err := geo.BoundaryArea(ctx, boundaryGJSON)
	if err != nil || cityArea <= 0 {
		return
	}
	var totalPaved float64
	for _, r := range rows {
		totalPaved += r.Area
	}
	fmt.Fprintf(ios.ErrOut, "\n=== City Summary ===\n")
	fmt.Fprintf(ios.ErrOut, "  City Area:    %s (%s)\n", units.FormatAreaLarge(cityArea, sys), units.FormatAreaVeryLarge(cityArea, sys))
	fmt.Fprintf(ios.ErrOut, "  Paved Area:   %s (%s)\n", units.FormatAreaLarge(totalPaved, sys), units.FormatAreaVeryLarge(totalPaved, sys))
	if totalPaved > 0 {
		fmt.Fprintf(ios.ErrOut, "  %% Paved:      %.1f%%\n", totalPaved/cityArea*100)
	}
}

func printSnapshotHistory(ctx context.Context, ios *iostreams.IOStreams, store db.Store) {
	snapshots, err := store.ListSnapshots(ctx)
	if err != nil || len(snapshots) == 0 {
		return
	}
	fmt.Fprintf(ios.ErrOut, "\n=== Snapshots ===\n")
	limit := min(len(snapshots), 5)
	for _, s := range snapshots[:limit] {
		fmt.Fprintf(ios.ErrOut, "  #%d  %s  (%s)\n", s.ID, s.ComputedAt.Format(time.RFC3339), iostreams.RelativeTime(s.ComputedAt))
	}
	if len(snapshots) > 5 {
		fmt.Fprintf(ios.ErrOut, "  ... and %d more\n", len(snapshots)-5)
	}
}
