package status

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"pvmt/internal/db"
	"pvmt/internal/geo"
	"pvmt/internal/resource"
	"pvmt/internal/units"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO           *iostreams.IOStreams
	CityDB       func() (db.Store, error)
	UnitSystem   func() units.System
	ResourceType resource.ResourceType // nil for global status
	Exporter     cmdutil.Exporter
}

type statusRow struct {
	ResourceType string  `json:"resourceType"`
	FeatureCount int     `json:"featureCount"`
	LastIngest   string  `json:"lastIngest,omitempty"`
	LastCompute  string  `json:"lastCompute,omitempty"`
	AreaSqM      float64 `json:"areaSqM,omitempty"`
}

var statusFields = []string{"resourceType", "featureCount", "lastIngest", "lastCompute", "areaSqM"}

func NewCmdStatus(f *cmdutil.Factory, rt resource.ResourceType, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:           f.IOStreams,
		CityDB:       f.CityDB,
		UnitSystem:   f.UnitSystem,
		ResourceType: rt,
	}

	use := "status"
	short := "Show overall status"
	if rt != nil {
		short = fmt.Sprintf("Show %s status", rt.Name())
	}

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
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

	var types []resource.ResourceType
	if opts.ResourceType != nil {
		types = []resource.ResourceType{opts.ResourceType}
	} else {
		types = resource.All
	}

	sys := opts.UnitSystem()

	var rows []statusRow
	for _, rt := range types {
		info, err := store.Stats(ctx, rt.Name())
		if err != nil {
			fmt.Fprintf(ios.ErrOut, "Warning: could not get stats for %s: %v\n", rt.Name(), err)
			continue
		}
		row := statusRow{
			ResourceType: rt.Name(),
			FeatureCount: info.FeatureCount,
			AreaSqM:      info.TotalAreaSqM,
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
		return opts.Exporter.Write(ios, rows)
	}

	// Table output
	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("Resource", "Features", "Last Ingest", "Last Compute", units.AreaLabel(sys), units.AreaLargeLabel(sys))
	for _, r := range rows {
		ingestStr := formatTimestamp(r.LastIngest, ios.IsTTY())
		computeStr := formatTimestamp(r.LastCompute, ios.IsTTY())
		tp.AddRow(
			r.ResourceType,
			strconv.Itoa(r.FeatureCount),
			ingestStr,
			computeStr,
			fmt.Sprintf("%.0f", units.AreaValue(r.AreaSqM, sys)),
			fmt.Sprintf("%.1f", units.AreaLargeValue(r.AreaSqM, sys)),
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
	cityAreaSqM, err := geo.BoundaryAreaSqM(boundaryGJSON)
	if err != nil || cityAreaSqM <= 0 {
		return
	}
	var totalPavedSqM float64
	for _, r := range rows {
		totalPavedSqM += r.AreaSqM
	}
	fmt.Fprintf(ios.ErrOut, "\n=== City Summary ===\n")
	fmt.Fprintf(ios.ErrOut, "  City Area:    %s (%s)\n", units.FormatAreaLarge(cityAreaSqM, sys), units.FormatAreaVeryLarge(cityAreaSqM, sys))
	fmt.Fprintf(ios.ErrOut, "  Paved Area:   %s (%s)\n", units.FormatAreaLarge(totalPavedSqM, sys), units.FormatAreaVeryLarge(totalPavedSqM, sys))
	if totalPavedSqM > 0 {
		fmt.Fprintf(ios.ErrOut, "  %% Paved:      %.1f%%\n", totalPavedSqM/cityAreaSqM*100)
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

func formatTimestamp(raw string, isTTY bool) string {
	if raw == "" {
		return "never"
	}
	if isTTY {
		t, _ := time.Parse(time.RFC3339, raw)
		return fmt.Sprintf("%s (%s)", raw, iostreams.RelativeTime(t))
	}
	return raw
}
