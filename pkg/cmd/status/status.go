package status

import (
	"fmt"
	"time"

	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"

	"github.com/spf13/cobra"
)

type Options struct {
	Factory      *cmdutil.Factory
	ResourceType resource.ResourceType // nil for global status
}

func NewCmdStatus(f *cmdutil.Factory, rt resource.ResourceType, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		Factory:      f,
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

	return cmd
}

func runStatus(opts *Options) error {
	ios := opts.Factory.IOStreams

	store, err := opts.Factory.DB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	var types []resource.ResourceType
	if opts.ResourceType != nil {
		types = []resource.ResourceType{opts.ResourceType}
	} else {
		types = resource.All
	}

	isTTY := ios.IsTTY()

	for _, rt := range types {
		info, err := store.Stats(rt.Name())
		if err != nil {
			fmt.Fprintf(ios.ErrOut, "Warning: could not get stats for %s: %v\n", rt.Name(), err)
			continue
		}

		if isTTY {
			fmt.Fprintf(ios.Out, "\n=== %s ===\n", rt.Name())
			fmt.Fprintf(ios.Out, "  Features:      %d\n", info.FeatureCount)
			if info.LastIngestAt != nil {
				fmt.Fprintf(ios.Out, "  Last Ingest:   %s (%s)\n", info.LastIngestAt.Format(time.RFC3339), relativeTime(*info.LastIngestAt))
			} else {
				fmt.Fprintf(ios.Out, "  Last Ingest:   never\n")
			}
			if info.LastComputeAt != nil {
				fmt.Fprintf(ios.Out, "  Last Compute:  %s (%s)\n", info.LastComputeAt.Format(time.RFC3339), relativeTime(*info.LastComputeAt))
				fmt.Fprintf(ios.Out, "  Area:          %.0f sq ft (%.1f acres)\n", info.TotalAreaSqFt, info.TotalAreaAcres)
			} else {
				fmt.Fprintf(ios.Out, "  Last Compute:  never\n")
			}
		} else {
			fmt.Fprintf(ios.Out, "resource_type\t%s\n", rt.Name())
			fmt.Fprintf(ios.Out, "feature_count\t%d\n", info.FeatureCount)
			if info.LastIngestAt != nil {
				fmt.Fprintf(ios.Out, "last_ingest\t%s\n", info.LastIngestAt.Format(time.RFC3339))
			}
			if info.LastComputeAt != nil {
				fmt.Fprintf(ios.Out, "last_compute\t%s\n", info.LastComputeAt.Format(time.RFC3339))
				fmt.Fprintf(ios.Out, "total_area_sqft\t%.0f\n", info.TotalAreaSqFt)
				fmt.Fprintf(ios.Out, "total_area_acres\t%.1f\n", info.TotalAreaAcres)
			}
		}
	}

	// Show snapshot history
	snapshots, err := store.ListSnapshots()
	if err == nil && len(snapshots) > 0 {
		if isTTY {
			fmt.Fprintf(ios.Out, "\n=== Snapshots ===\n")
			limit := len(snapshots)
			if limit > 5 {
				limit = 5
			}
			for _, s := range snapshots[:limit] {
				fmt.Fprintf(ios.Out, "  #%d  %s  (%s)\n", s.ID, s.ComputedAt.Format(time.RFC3339), relativeTime(s.ComputedAt))
			}
			if len(snapshots) > 5 {
				fmt.Fprintf(ios.Out, "  ... and %d more\n", len(snapshots)-5)
			}
		}
	}

	return nil
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
