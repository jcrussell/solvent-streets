package cities

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO         *iostreams.IOStreams
	RootDB     func() (db.RootStorer, error)
	UnitSystem func() units.System
	Exporter   cmdutil.Exporter
}

type cityRow struct {
	Slug          string         `json:"slug"`
	Name          string         `json:"name"`
	FeatureCounts map[string]int `json:"feature_counts"`
	TotalPaved    float64        `json:"total_paved"`
	LastIngestAt  string         `json:"last_ingest_at,omitempty"`
	LastComputeAt string         `json:"last_compute_at,omitempty"`
}

var _ cmdutil.RowExporter = cityRow{}

func (r cityRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "slug":
			out[f] = r.Slug
		case "name":
			out[f] = r.Name
		case "feature_counts":
			out[f] = r.FeatureCounts
		case "total_paved":
			out[f] = r.TotalPaved
		case "last_ingest_at":
			if r.LastIngestAt != "" {
				out[f] = r.LastIngestAt
			}
		case "last_compute_at":
			if r.LastComputeAt != "" {
				out[f] = r.LastComputeAt
			}
		}
	}
	return out
}

var citiesFields = []string{"slug", "name", "feature_counts", "total_paved", "last_ingest_at", "last_compute_at"}

func NewCmdCities(f *cmdutil.Factory, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO: f.IOStreams,
		RootDB: func() (db.RootStorer, error) {
			return f.RootDB()
		},
		UnitSystem: f.UnitSystem,
	}

	cmd := &cobra.Command{
		Use:   "cities",
		Short: "List cities in the database",
		Long:  "List all cities stored in the shared pvmt database with per-resource feature counts.",
		Example: `  # Table of cities with per-resource feature counts
  pvmt cities

  # Same data as JSON
  pvmt cities --json slug,name,feature_counts,total_paved`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runCities(cmd.Context(), opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, citiesFields)

	return cmd
}

func runCities(ctx context.Context, opts *Options) error {
	ios := opts.IO

	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	list, err := root.ListCities(ctx)
	if err != nil {
		return fmt.Errorf("list cities: %w", err)
	}

	rows := make([]cityRow, 0, len(list))
	for _, c := range list {
		rows = append(rows, buildCityRow(ctx, root.ForCity(c.ID), c, ios))
	}

	if opts.Exporter != nil {
		return cmdutil.WriteRows(ios, opts.Exporter, rows)
	}

	if len(rows) == 0 {
		// Empty-state message is chatter, not data: route to ErrOut so
		// `pvmt cities | wc -l` is 0 when there are no cities to list
		// (byob-iostreams.3).
		fmt.Fprintln(ios.ErrOut, "No cities in database. Run 'pvmt all ingest' to populate.")
		return nil
	}

	sys := opts.UnitSystem()
	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("Slug", "Name", "Roads", "Parking", "Sidewalks", units.AreaLargeLabel(sys), "Last Ingest", "Last Compute")
	for _, r := range rows {
		tp.AddRow(
			r.Slug,
			r.Name,
			strconv.Itoa(r.FeatureCounts["roads"]),
			strconv.Itoa(r.FeatureCounts["parking"]),
			strconv.Itoa(r.FeatureCounts["sidewalks"]),
			fmt.Sprintf("%.1f", units.AreaLargeValue(r.TotalPaved, sys)),
			iostreams.FormatTimestamp(r.LastIngestAt, ios.IsTTY()),
			iostreams.FormatTimestamp(r.LastComputeAt, ios.IsTTY()),
		)
	}
	return tp.Render()
}

func buildCityRow(ctx context.Context, store db.Store, c db.City, ios *iostreams.IOStreams) cityRow {
	row := cityRow{
		Slug:          c.Slug,
		Name:          c.Name,
		FeatureCounts: make(map[string]int, len(resource.All)),
	}
	var latestIngest, latestCompute time.Time
	var perResourceSum float64
	for _, rt := range resource.All {
		t := rt.Type()
		info, err := store.Stats(ctx, t)
		if err != nil {
			fmt.Fprintf(ios.ErrOut, "Warning: %s/%s: %v\n", c.Slug, t, err)
			continue
		}
		row.FeatureCounts[string(t)] = info.FeatureCount
		perResourceSum += info.TotalArea
		if info.LastIngestAt != nil && info.LastIngestAt.After(latestIngest) {
			latestIngest = *info.LastIngestAt
		}
		if info.LastComputeAt != nil && info.LastComputeAt.After(latestCompute) {
			latestCompute = *info.LastComputeAt
		}
	}
	// Prefer the combined (de-duplicated) compute row from `pvmt all compute`
	// for TotalPaved; fall back to the per-resource sum when it's absent. The
	// sum double-counts road/parking/sidewalk buffer overlap — mirrors
	// export.totalPavedFromStore so `pvmt cities` agrees with meta.json.
	row.TotalPaved = perResourceSum
	if r, err := store.LatestComputeResult(ctx, resource.CombinedAll); err == nil && r != nil {
		row.TotalPaved = r.TotalArea
	}
	if !latestIngest.IsZero() {
		row.LastIngestAt = latestIngest.UTC().Format(time.RFC3339)
	}
	if !latestCompute.IsZero() {
		row.LastComputeAt = latestCompute.UTC().Format(time.RFC3339)
	}
	return row
}
