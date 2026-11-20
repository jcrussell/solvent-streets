package cities

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
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
		Args:  cobra.NoArgs,
		Short: "List cities in the database",
		Long:  "List all cities stored in the shared pvmt database with per-resource ingested feature counts\n(everything the bounding-box fetch returned, before the boundary clip).",
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
		// `pvmt cities | wc -l` is 0 when there are no cities to list.
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
			strconv.Itoa(r.FeatureCounts[string(resource.TypeRoads)]),
			strconv.Itoa(r.FeatureCounts[string(resource.TypeParking)]),
			strconv.Itoa(r.FeatureCounts[string(resource.TypeSidewalks)]),
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
	for _, rt := range resource.All {
		t := rt.Type()
		info, err := store.Stats(ctx, t)
		if err != nil {
			cmdutil.Warnf(ios, "%s/%s: %v", c.Slug, t, err)
			continue
		}
		row.FeatureCounts[string(t)] = info.FeatureCount
		if info.LastIngestAt != nil && info.LastIngestAt.After(latestIngest) {
			latestIngest = *info.LastIngestAt
		}
	}
	if totals, err := results.LoadTotals(ctx, store, resource.AllTypes(resource.ScopeAll)); err != nil {
		cmdutil.Warnf(ios, "%s: compute results: %v", c.Slug, err)
	} else {
		for _, tot := range totals {
			if tot.ComputedAt.After(latestCompute) {
				latestCompute = tot.ComputedAt
			}
		}
	}
	// The figure meta.json publishes as total_paved (results.PavedArea).
	if area, _, err := results.PavedArea(ctx, store, resource.ScopeAll); err != nil {
		cmdutil.Warnf(ios, "%s: paved area: %v", c.Slug, err)
	} else {
		row.TotalPaved = area
	}
	if !latestIngest.IsZero() {
		row.LastIngestAt = latestIngest.UTC().Format(time.RFC3339)
	}
	if !latestCompute.IsZero() {
		row.LastComputeAt = latestCompute.UTC().Format(time.RFC3339)
	}
	return row
}
