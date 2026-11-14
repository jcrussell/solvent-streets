package status

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO     *iostreams.IOStreams
	CityDB func() (db.Store, error)
	// Config and CurrentCity resolve the city's snapshot key, so status can
	// say when the city's snapshots were computed under other settings. Either
	// may be nil (no config): status then reports what it can without it.
	Config       func() (*config.Config, error)
	CurrentCity  func() (*config.CityConfig, error)
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

// Short is the one-line help of the status subcommand of the command group
// named group (`pvmt <group> status`).
func Short(group string) string {
	return fmt.Sprintf("Show %s ingest and compute status", group)
}

// Long is the --help description of the status subcommand of the command
// group named group. Only a paved resource reports an area.
func Long(group string, rt resource.Source) string {
	area := ""
	if t := rt.Type(); !t.IsLayer() && !t.IsFacility() {
		area = ", and paved area in the active unit system"
	}
	return fmt.Sprintf(`Report ingest and compute progress for %s in the current city:
ingested feature count (everything the bounding-box fetch returned, before
the boundary clip), last-ingest and last-compute timestamps%s. Use --json
with a comma-separated field list (resource_type, feature_count,
last_ingest_at, last_compute_at, area, estimated_spaces) for a single row.`, group, area)
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
		Config:       f.Config,
		CurrentCity:  f.CurrentCity,
		UnitSystem:   f.UnitSystem,
		ResourceType: rt,
	}

	use := "status"
	short := "Show overall status"
	long := `Report ingest and compute progress for the current city across every
resource type: ingested feature counts (everything the bounding-box fetch
returned, before the boundary clip), last-ingest and last-compute
timestamps, and paved area totals in the active unit system. On a TTY the output is
followed by a city-area summary (paved-vs-total %) and the five most
recent compute snapshots with their config hash, marking those computed
under the current settings; when none was, a note says so and how to
recompute. Use --json with a comma-separated field list
(resource_type, feature_count, last_ingest_at, last_compute_at, area,
estimated_spaces) to emit one row per resource.`
	example := `  # Show feature + result counts across every resource
  pvmt status

  # Emit one row per resource as JSON
  pvmt status --json resource_type,feature_count,area`
	if rt != nil {
		short, long = Short(string(rt.Type())), Long(string(rt.Type()), rt)
		example = Example(string(rt.Type()))
	}

	cmd := &cobra.Command{
		Use:     use,
		Args:    cobra.NoArgs,
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

// resolveCityFirst resolves the config and city before the database opens, as
// the other commands do, so a missing pvmt.toml or an unknown --city says so
// instead of reading as a database error (solvent-streets-4ftl). Either may be
// nil (no config).
func resolveCityFirst(opts *Options) error {
	if opts.Config != nil {
		if _, err := opts.Config(); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	if opts.CurrentCity != nil {
		if _, err := opts.CurrentCity(); err != nil {
			return fmt.Errorf("city: %w", err)
		}
	}
	return nil
}

func runStatus(ctx context.Context, opts *Options) error {
	ios := opts.IO

	if err := resolveCityFirst(opts); err != nil {
		return err
	}
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
			cmdutil.Warnf(ios, "could not get stats for %s: %v", rt.Type(), err)
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

	snaps, snapsErr := store.ListSnapshots(ctx)
	hash := opts.noteStaleSnapshots(ctx, ios, store, snaps, snapsErr)

	if ios.IsTTY() {
		printCitySummary(ctx, ios, store, rows, sys, opts.ResourceType == nil)
		if snapsErr == nil {
			printSnapshotHistory(ios, snaps, hash)
		}
	}

	return nil
}

// noteStaleSnapshots prints the shared stale-settings hint when the city has
// snapshots but nothing computed under its current settings, and returns the
// current hash ("" without a config). "Last Compute" reads through the latest
// compute under the current settings, so such a city shows it blank; say
// why rather than leave it looking never-computed.
func (opts *Options) noteStaleSnapshots(ctx context.Context, ios *iostreams.IOStreams, store db.Store, snaps []db.Snapshot, snapsErr error) string {
	cfg, city := opts.currentCity()
	if cfg == nil {
		return ""
	}
	hash := cfg.CityHash(city)
	latest, latestErr := store.LatestCompute(ctx, hash)
	if snapsErr == nil && latestErr == nil && export.StaleSettings(cfg, city, snaps, latest) {
		fmt.Fprintf(ios.ErrOut, "\nNote: %s\n", cfg.StaleHint(city))
	}
	return hash
}

// currentCity returns the config and the current city, or nils when either
// does not resolve (then nothing is marked or hinted).
func (opts *Options) currentCity() (*config.Config, *config.CityConfig) {
	if opts.Config == nil || opts.CurrentCity == nil {
		return nil, nil
	}
	cfg, err := opts.Config()
	if err != nil || cfg == nil {
		return nil, nil
	}
	city, err := opts.CurrentCity()
	if err != nil || city == nil {
		return nil, nil
	}
	return cfg, city
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
// An all-resources status reports the de-duplicated combined total
// (results.PavedArea, the figure meta.json publishes) and, until `pvmt all
// compute` has stored one, the sum of the rows in the table above it
// (accurate=false → footnoted).
//
// For a single-resource status (`pvmt <res> status`) the sum is just that one
// resource — no overlap to de-duplicate — so it is returned as-is and reported
// as accurate; pulling the combined union here would mismatch the single-row
// table above it.
func totalPavedArea(ctx context.Context, store db.Store, rows []statusRow, allResources bool) (float64, bool) {
	if allResources {
		if area, deduped, err := results.PavedArea(ctx, store, resource.ScopeAll); err == nil && deduped {
			return area, true
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

// printSnapshotHistory lists the newest snapshots with their config hash,
// marking the ones computed under the current settings (hash; "" marks none).
func printSnapshotHistory(ios *iostreams.IOStreams, snapshots []db.Snapshot, hash string) {
	if len(snapshots) == 0 {
		return
	}
	fmt.Fprintf(ios.ErrOut, "\n=== Snapshots ===\n")
	limit := min(len(snapshots), 5)
	for _, s := range snapshots[:limit] {
		mark := ""
		if hash != "" && s.ConfigHash == hash {
			mark = "  (current settings)"
		}
		fmt.Fprintf(ios.ErrOut, "  #%d  %s  (%s)  %d parts  %s%s\n", s.ID, s.CreatedAt.Format(time.RFC3339),
			iostreams.RelativeTime(s.CreatedAt), len(s.Parts), s.ConfigHash, mark)
	}
	if len(snapshots) > 5 {
		fmt.Fprintf(ios.ErrOut, "  ... and %d more\n", len(snapshots)-5)
	}
}
