// Package census implements `pvmt census`: 2020 Census block ingest (from
// TIGERweb), the population compute that apportions block counts onto the
// hex grid as per-hex metrics, and status — built on the same resource
// command group as roads/parking/sidewalks/services.
package census

import (
	"context"
	"fmt"
	"io"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/population"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	"github.com/jcrussell/solvent-streets/pkg/cmd/ingest"
	"github.com/jcrussell/solvent-streets/pkg/cmd/resourcecmd"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

// blocks is the Source every census subcommand acts on.
var blocks = &resource.CensusBlocks{}

// NewCmdCensus builds the `census` group.
func NewCmdCensus(f *cmdutil.Factory) *cobra.Command {
	return resourcecmd.New(f, blocks, resourcecmd.Spec{
		Use:   "census",
		Short: "Manage 2020 Census blocks and per-hex population",
		Long: `Ingest 2020 Census blocks with their population and housing-unit counts
(from the Census Bureau's TIGERweb service), apportion those counts onto the
hex grid by area, and view status. The results land as per-hex metrics —
population, housing_units, population_per_km2 — that the dashboard offers
under "Color by", together with per-resident ratios (paved area and
year-1 need per resident) it derives from them.

US cities only: TIGERweb has no blocks elsewhere. Set census = false on a
city to keep it out of the census steps of 'pvmt all'.`,
		Example: `  # Pull Census blocks for every configured city
  pvmt census ingest

  # Apportion them onto the hex grid (after a roads compute)
  pvmt census compute

  # Show block counts and last ingest
  pvmt census status`,
		Compute: func(f *cmdutil.Factory, _ resource.Source) *cobra.Command {
			return NewCmdCompute(f, nil)
		},
	})
}

// RunIngestForCity ingests the city's Census blocks against the city-scoped
// factory cf. `pvmt all ingest` calls it in-process after the pavement
// resources; a city with census = false is skipped as an empty result.
func RunIngestForCity(ctx context.Context, cf *cmdutil.Factory) error {
	if skip, err := optedOut(cf); skip || err != nil {
		return err
	}
	return ingest.RunResourceForCity(ctx, cf, blocks)
}

// RunComputeForCity apportions the city's blocks onto grid (nil builds it).
// `pvmt all compute` calls it in-process after the pavement passes.
func RunComputeForCity(ctx context.Context, cf *cmdutil.Factory, grid []geo.Hex) error {
	if skip, err := optedOut(cf); skip || err != nil {
		return err
	}
	return runCompute(ctx, &Options{
		IO: cf.IOStreams, CityDB: cf.CityDB, Config: cf.Config, CurrentCity: cf.CurrentCity, PrebuiltGrid: grid,
	})
}

// optedOut reports a city with census = false as ErrNoResults (a quiet skip
// in `pvmt all`).
func optedOut(cf *cmdutil.Factory) (bool, error) {
	city, err := cf.CurrentCity()
	if err != nil {
		return true, fmt.Errorf("city: %w", err)
	}
	if !city.CensusEnabled() {
		fmt.Fprintf(cf.IOStreams.ErrOut, "Census disabled for %s (census = false); skipping.\n", city.Name)
		return true, cmdutil.ErrNoResults
	}
	return false, nil
}

// Options for the population compute.
type Options struct {
	IO          *iostreams.IOStreams
	CityDB      func() (db.Store, error)
	Config      func() (*config.Config, error)
	CurrentCity func() (*config.CityConfig, error)
	Exporter    cmdutil.Exporter
	// PrebuiltGrid is the clipped hex grid when `all compute` already built
	// one for the city; nil builds it here.
	PrebuiltGrid []geo.Hex
	rows         *[]computeRow
}

type computeRow struct {
	City       string  `json:"city"`
	Blocks     int     `json:"blocks"`
	Hexes      int     `json:"hexes"`
	Population float64 `json:"population"`
	SnapshotID int64   `json:"snapshotId,omitempty"`
}

var _ cmdutil.RowExporter = computeRow{}

func (r computeRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "city":
			out[f] = r.City
		case "blocks":
			out[f] = r.Blocks
		case "hexes":
			out[f] = r.Hexes
		case "population":
			out[f] = r.Population
		case "snapshotId":
			out[f] = r.SnapshotID
		}
	}
	return out
}

var computeFields = []string{"city", "blocks", "hexes", "population", "snapshotId"}

// NewCmdCompute builds `census compute`.
func NewCmdCompute(f *cmdutil.Factory, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{IO: f.IOStreams, CityDB: f.CityDB, Config: f.Config, CurrentCity: f.CurrentCity}
	cmd := &cobra.Command{
		Use:   "compute",
		Short: "Apportion Census block counts onto the hex grid",
		Long: `Spread each stored Census block's population and housing units onto the
hexes it overlaps, in proportion to the overlapping area, and store per hex:
population, housing_units and population_per_km2 (population over the hex's
own boundary-clipped area). The metrics attach to the city's latest roads
compute snapshot, like service coverage, so run a roads compute first; export
publishes them only while that snapshot is still the latest, so rerun this
after a roads compute.`,
		Example: `  # Every configured city
  pvmt census compute

  # One city, as JSON
  pvmt --city oakland census compute --json city,hexes,population`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runComputeAllCities(cmd.Context(), f, opts)
		},
	}
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, computeFields)
	return cmd
}

func runComputeAllCities(ctx context.Context, f *cmdutil.Factory, opts *Options) error {
	var accumulator []computeRow
	if opts.Exporter != nil {
		opts.rows = &accumulator
	}
	err := cmdutil.ForEachCity(ctx, f, func(cf *cmdutil.Factory, _ *config.CityConfig) error {
		cityOpts := *opts
		cityOpts.CurrentCity = cf.CurrentCity
		cityOpts.CityDB = cf.CityDB
		return runCompute(ctx, &cityOpts)
	})
	if err != nil {
		return err
	}
	if opts.Exporter != nil {
		return cmdutil.WriteRows(opts.IO, opts.Exporter, accumulator)
	}
	return nil
}

func runCompute(ctx context.Context, opts *Options) error {
	out, errOut := opts.IO.Out, opts.IO.ErrOut
	if opts.Exporter != nil {
		out = io.Discard
	}
	cfg, err := opts.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	city, err := opts.CurrentCity()
	if err != nil {
		return fmt.Errorf("city: %w", err)
	}
	store, err := opts.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	// No blocks is an empty result (ErrNoResults: a quiet skip in `pvmt
	// all`), checked before the snapshot precondition, which is a real error.
	rows, err := store.ListFeatures(ctx, resource.TypeCensusBlocks)
	if err != nil {
		return fmt.Errorf("list census blocks: %w", err)
	}
	if len(rows) == 0 {
		fmt.Fprintf(errOut, "No Census blocks in database for %s. Run 'pvmt census ingest' first.\n", city.Name)
		return cmdutil.ErrNoResults
	}
	target, err := compute.LoadMetricTarget(ctx, store, cfg, city, opts.PrebuiltGrid, errOut, "population")
	if err != nil {
		return err
	}
	feats := make([]resource.Feature, len(rows))
	for i, r := range rows {
		feats[i] = resource.Feature{ID: r.ID, Name: r.Name, Tags: r.Tags, GeometryJSON: r.GeometryJSON, SourceAPI: r.SourceAPI}
	}
	fmt.Fprintf(errOut, "Apportioning %d Census blocks onto %d hexes for %s...\n", len(feats), len(target.Grid), city.Name)
	projected := blocks.BufferFeaturesPaired(ctx, feats, target.Proj)
	m := population.Apportion(ctx, target.Grid, projected, nil)
	// Partial results come back without an error on cancellation; never save
	// a truncated set under a complete-looking snapshot.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("compute cancelled: %w", err)
	}
	// All three keys in one transaction, so a failure never leaves them from
	// different runs; each is saved even when empty, so a rerun that
	// apportions nothing clears the previous rows instead of leaving them on
	// the map.
	if err := store.SaveHexMetricSet(ctx, target.SnapshotID, map[string][]db.HexMetric{
		population.KeyPopulation:   m.Population,
		population.KeyHousingUnits: m.HousingUnits,
		population.KeyDensity:      m.Density,
	}); err != nil {
		return fmt.Errorf("save population metrics: %w", err)
	}

	row := computeRow{City: city.Name, Blocks: len(projected), Hexes: len(m.Population), Population: m.Total}
	if target.SnapshotID != nil {
		row.SnapshotID = *target.SnapshotID
	}
	if opts.rows != nil {
		*opts.rows = append(*opts.rows, row)
	}
	fmt.Fprintf(out, "\nPopulation Results for %s:\n", city.Name)
	fmt.Fprintf(out, "  Blocks:            %d\n", row.Blocks)
	fmt.Fprintf(out, "  Hexes populated:   %d\n", row.Hexes)
	fmt.Fprintf(out, "  Residents in grid: %.0f\n", row.Population)
	return nil
}
