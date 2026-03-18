package status

import (
	"fmt"
	"time"

	"pvmt/internal/db"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO           *iostreams.IOStreams
	DB           func() (db.Store, error)
	ResourceType resource.ResourceType // nil for global status
	Exporter     cmdutil.Exporter
}

type statusRow struct {
	ResourceType string  `json:"resourceType"`
	FeatureCount int     `json:"featureCount"`
	LastIngest   string  `json:"lastIngest,omitempty"`
	LastCompute  string  `json:"lastCompute,omitempty"`
	AreaSqFt     float64 `json:"areaSqFt,omitempty"`
	AreaAcres    float64 `json:"areaAcres,omitempty"`
}

var statusFields = []string{"resourceType", "featureCount", "lastIngest", "lastCompute", "areaSqFt", "areaAcres"}

func NewCmdStatus(f *cmdutil.Factory, rt resource.ResourceType, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:           f.IOStreams,
		DB:           f.DB,
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
			return runStatus(opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, statusFields)

	return cmd
}

func runStatus(opts *Options) error {
	ios := opts.IO

	store, err := opts.DB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	var types []resource.ResourceType
	if opts.ResourceType != nil {
		types = []resource.ResourceType{opts.ResourceType}
	} else {
		types = resource.All
	}

	var rows []statusRow
	for _, rt := range types {
		info, err := store.Stats(rt.Name())
		if err != nil {
			fmt.Fprintf(ios.ErrOut, "Warning: could not get stats for %s: %v\n", rt.Name(), err)
			continue
		}
		row := statusRow{
			ResourceType: rt.Name(),
			FeatureCount: info.FeatureCount,
			AreaSqFt:     info.TotalAreaSqFt,
			AreaAcres:    info.TotalAreaAcres,
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
	tp.AddHeader("Resource", "Features", "Last Ingest", "Last Compute", "Area (sq ft)", "Area (acres)")
	for _, r := range rows {
		ingestStr := formatTimestamp(r.LastIngest, ios.IsTTY())
		computeStr := formatTimestamp(r.LastCompute, ios.IsTTY())
		tp.AddRow(
			r.ResourceType,
			fmt.Sprintf("%d", r.FeatureCount),
			ingestStr,
			computeStr,
			fmt.Sprintf("%.0f", r.AreaSqFt),
			fmt.Sprintf("%.1f", r.AreaAcres),
		)
	}
	if err := tp.Render(); err != nil {
		return err
	}

	// Show snapshot history (TTY only)
	snapshots, err := store.ListSnapshots()
	if err == nil && len(snapshots) > 0 && ios.IsTTY() {
		fmt.Fprintf(ios.Out, "\n=== Snapshots ===\n")
		limit := min(len(snapshots), 5)
		for _, s := range snapshots[:limit] {
			fmt.Fprintf(ios.Out, "  #%d  %s  (%s)\n", s.ID, s.ComputedAt.Format(time.RFC3339), iostreams.RelativeTime(s.ComputedAt))
		}
		if len(snapshots) > 5 {
			fmt.Fprintf(ios.Out, "  ... and %d more\n", len(snapshots)-5)
		}
	}

	return nil
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

