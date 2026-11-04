package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/services"
	"github.com/jcrussell/solvent-streets/internal/tui"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	"github.com/jcrussell/solvent-streets/pkg/cmd/ingest"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

// ingestRunResourceForCity is ingest.RunResourceForCity, indirected so tests
// can observe the `all` fan-out without a network.
var ingestRunResourceForCity = ingest.RunResourceForCity

// Options for the coverage compute.
type Options struct {
	IO          *iostreams.IOStreams
	CityDB      func() (db.Store, error)
	Config      func() (*config.Config, error)
	CurrentCity func() (*config.CityConfig, error)
	Service     string
	Exporter    cmdutil.Exporter
	// PrebuiltGrid is the clipped hex grid when `all compute` already built
	// one for the city; nil builds it here (compute.BuildCityGrid shape).
	PrebuiltGrid []geo.Hex
	rows         *[]computeRow
}

type computeRow struct {
	City          string  `json:"city"`
	Service       string  `json:"service"`
	Facilities    int     `json:"facilities"`
	Unsnapped     int     `json:"unsnapped,omitempty"`
	Hexes         int     `json:"hexes"`
	PctWithin     float64 `json:"pctWithinThreshold"`
	MedianSeconds float64 `json:"medianSeconds"`
	SnapshotID    int64   `json:"snapshotId,omitempty"`
}

var _ cmdutil.RowExporter = computeRow{}

func (r computeRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "city":
			out[f] = r.City
		case "service":
			out[f] = r.Service
		case "facilities":
			out[f] = r.Facilities
		case "unsnapped":
			out[f] = r.Unsnapped
		case "hexes":
			out[f] = r.Hexes
		case "pctWithinThreshold":
			out[f] = r.PctWithin
		case "medianSeconds":
			out[f] = r.MedianSeconds
		case "snapshotId":
			out[f] = r.SnapshotID
		}
	}
	return out
}

var computeFields = []string{"city", "service", "facilities", "unsnapped", "hexes", "pctWithinThreshold", "medianSeconds", "snapshotId"}

// NewCmdCompute builds `services compute`. service points at the group's
// --service flag value, read at run time.
func NewCmdCompute(f *cmdutil.Factory, service *string, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO:          f.IOStreams,
		CityDB:      f.CityDB,
		Config:      f.Config,
		CurrentCity: f.CurrentCity,
	}
	cmd := &cobra.Command{
		Use:   "compute",
		Short: "Compute response-time coverage per hex",
		Long: `Build a routable graph from the stored roads (OpenStreetMap rows when
present, else the agency centerlines), route from every facility at once,
and store each hex's response time — turnout plus travel to the road vertex
nearest the hex centre — as a per-hex metric attached to the city's latest
roads compute snapshot. Hexes off the network or unreachable from every
facility carry no value.`,
		Example: `  # Fire response coverage for every configured city
  pvmt services compute

  # One city, as JSON
  pvmt --city oakland services compute --json city,hexes,pctWithinThreshold`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if service != nil {
				opts.Service = *service
			}
			if opts.Service == "" {
				opts.Service = DefaultService
			}
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runComputeAllCities(cmd.Context(), f, opts)
		},
	}
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, computeFields)
	return cmd
}

// RunComputeForCity computes coverage for one city against the city-scoped
// factory cf, reusing grid (nil = build one). `pvmt all compute` calls it
// in-process after the pavement passes.
func RunComputeForCity(ctx context.Context, cf *cmdutil.Factory, service string, grid []geo.Hex) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	opts := &Options{
		IO:           cf.IOStreams,
		CityDB:       cf.CityDB,
		Config:       cf.Config,
		CurrentCity:  cf.CurrentCity,
		Service:      service,
		PrebuiltGrid: grid,
	}
	return runCompute(ctx, opts)
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

// Phase indices for the TUI checklist.
const (
	phaseLoad = iota
	phaseGraph
	phaseRoute
	phaseSave
)

func runCompute(ctx context.Context, opts *Options) error {
	if opts.Exporter != nil {
		return doCompute(ctx, io.Discard, io.Discard, opts.IO.ErrOut, tui.NoopNotifier{}, opts)
	}
	if opts.IO.IsTTY() {
		city, err := opts.CurrentCity()
		if err != nil {
			return fmt.Errorf("city: %w", err)
		}
		label := fmt.Sprintf("Routing %s · %s", opts.Service, city.Name)
		steps := []tui.Step{{Name: "Loading roads and facilities"}, {Name: "Building road graph"}, {Name: "Routing to hexes"}, {Name: "Saving metric"}}
		done := tui.DoneConfig{SuccessMsg: fmt.Sprintf("%s coverage complete for %s", opts.Service, city.Name)}
		return tui.Run(ctx, label, steps, done, func(ctx context.Context, out io.Writer, errOut io.Writer, notify tui.PhaseNotifier) error {
			return doCompute(ctx, out, out, errOut, notify, opts)
		})
	}
	return doCompute(ctx, opts.IO.Out, opts.IO.ErrOut, opts.IO.ErrOut, tui.NoopNotifier{}, opts)
}

// loadFacilities gathers the service's facilities: the ingested OSM points
// plus the city's hand-placed [[cities.facilities]] for this service.
func loadFacilities(ctx context.Context, store db.Store, city *config.CityConfig, spec services.Spec) ([]services.Facility, error) {
	rows, err := store.ListFeatures(ctx, resource.FacilityType(spec.Name))
	if err != nil {
		return nil, fmt.Errorf("list %s facilities: %w", spec.Name, err)
	}
	var out []services.Facility
	for _, r := range rows {
		var pt struct {
			Type        string     `json:"type"`
			Coordinates [2]float64 `json:"coordinates"`
		}
		if err := json.Unmarshal([]byte(r.GeometryJSON), &pt); err != nil || pt.Type != "Point" {
			continue
		}
		out = append(out, services.Facility{ID: r.ID, Name: r.Name, Lon: pt.Coordinates[0], Lat: pt.Coordinates[1], Source: "osm"})
	}
	for i, fc := range city.Facilities {
		if fc.Service != spec.Name {
			continue
		}
		out = append(out, services.Facility{ID: fmt.Sprintf("config:%s:%d", spec.Name, i), Name: fc.Name, Lon: fc.Lon, Lat: fc.Lat, Source: "config"})
	}
	return out, nil
}

// inputs is everything the routing pass needs, gathered by loadInputs.
type inputs struct {
	store      db.Store
	city       *config.CityConfig
	spec       services.Spec
	snapshotID *int64
	roads      []resource.Feature
	source     string
	facilities []services.Facility
	proj       *geo.UTMProjector
	grid       []geo.Hex
}

// loadInputs resolves the service, the roads snapshot the metric attaches
// to, the road rows, the facilities and the clipped grid. It returns
// cmdutil.ErrNoResults (after a note on errOut) when the city has no roads or
// no facilities — an empty result, not a failure.
func loadInputs(ctx context.Context, errOut io.Writer, opts *Options) (*inputs, error) {
	cfg, err := opts.Config()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	city, err := opts.CurrentCity()
	if err != nil {
		return nil, fmt.Errorf("city: %w", err)
	}
	store, err := opts.CityDB()
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	spec, err := lookupService(opts.Service)
	if err != nil {
		return nil, err
	}
	// A [[services]] entry naming an unregistered service is a config error,
	// not something to skip silently (the docs promise rejection).
	if err := services.ValidateOverrides(cfg.Services); err != nil {
		return nil, errors.Join(config.ErrInvalidConfig, err)
	}
	in := &inputs{store: store, city: city, spec: services.Resolve(spec, cfg.Services)}

	// Empty inputs first (ErrNoResults = a quiet skip in `pvmt all`), then
	// the snapshot precondition (a real, hinted error): a city with no roads
	// or no stations is not a failure, a city that skipped `roads compute` is.
	roadRows, err := store.ListFeatures(ctx, resource.TypeRoads)
	if err != nil {
		return nil, fmt.Errorf("list roads: %w", err)
	}
	if len(roadRows) == 0 {
		fmt.Fprintf(errOut, "No roads features in database for %s. Run 'pvmt roads ingest' first.\n", city.Name)
		return nil, cmdutil.ErrNoResults
	}
	in.facilities, err = loadFacilities(ctx, store, city, in.spec)
	if err != nil {
		return nil, err
	}
	if len(in.facilities) == 0 {
		fmt.Fprintf(errOut, "No %s facilities for %s (run 'pvmt services ingest' or add [[cities.facilities]]).\n", in.spec.Name, city.Name)
		return nil, cmdutil.ErrNoResults
	}
	// The metric attaches to the latest roads compute snapshot (the forecast
	// pattern): without one there is no grid the dashboard would join it to.
	roadsResult, err := store.LatestComputeResult(ctx, resource.TypeRoads)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, cmdutil.Hintf(fmt.Errorf("no roads compute snapshot for %s", city.Name),
				"run `pvmt roads compute` (or `pvmt all compute`) first; service coverage attaches to the roads snapshot")
		}
		return nil, fmt.Errorf("latest roads compute result: %w", err)
	}
	in.snapshotID = roadsResult.SnapshotID
	boundaryGJSON, bbox, proj, err := compute.LoadBoundary(ctx, store, city)
	if err != nil {
		return nil, err
	}
	in.proj = proj
	in.grid = opts.PrebuiltGrid
	if in.grid == nil {
		// Same grid the pavement passes use, so hex ids line up.
		in.grid = compute.BuildClippedHexGrid(ctx, cfg, city, proj, bbox, boundaryGJSON, errOut)
	}
	roads := make([]resource.Feature, len(roadRows))
	for i, r := range roadRows {
		roads[i] = resource.Feature{ID: r.ID, Name: r.Name, Tags: r.Tags, GeometryJSON: r.GeometryJSON, SourceAPI: r.SourceAPI}
	}
	in.roads, in.source = services.PreferredRoads(roads)
	return in, nil
}

func doCompute(ctx context.Context, out, progress, errOut io.Writer, notify tui.PhaseNotifier, opts *Options) error {
	notify.PhaseStart(phaseLoad)
	in, err := loadInputs(ctx, errOut, opts)
	if err != nil {
		notify.PhaseDone(phaseLoad, err)
		return err
	}
	spec, city, facilities, grid, proj, store := in.spec, in.city, in.facilities, in.grid, in.proj, in.store
	fmt.Fprintf(progress, "Loaded %d road features (%s), %d %s facilities, %d hexes\n", len(in.roads), in.source, len(facilities), spec.Name, len(grid))
	notify.PhaseDone(phaseLoad, nil)

	notify.PhaseStart(phaseGraph)
	graph := services.BuildGraph(ctx, in.roads, spec.Speeds, proj)
	if err := ctx.Err(); err != nil {
		notify.PhaseDone(phaseGraph, err)
		return fmt.Errorf("compute cancelled: %w", err)
	}
	fmt.Fprintf(progress, "Road graph: %d vertices\n", graph.NodeCount())
	notify.PhaseDone(phaseGraph, nil)

	notify.PhaseStart(phaseRoute)
	cov := services.ComputeCoverage(ctx, spec, grid, graph, facilities)
	// ParallelMap yields partial results with no error on cancellation; never
	// persist a truncated coverage set under a complete-looking snapshot.
	if err := ctx.Err(); err != nil {
		notify.PhaseDone(phaseRoute, err)
		return fmt.Errorf("compute cancelled: %w", err)
	}
	// Warnings go through errOut, the TUI's warn writer, not straight to
	// ErrOut under the alt-screen program.
	for _, i := range cov.Unsnapped {
		fmt.Fprintf(errOut, "warning: %s facility %s (%s) is more than %.0f m from any road vertex and was ignored\n", spec.Name, facilities[i].ID, facilities[i].Name, services.FacilitySnapM)
	}
	for _, i := range cov.Isolated {
		fmt.Fprintf(errOut, "warning: %s facility %s (%s) snapped to a road fragment of fewer than %d vertices (an apron or driveway OSM does not join to the street); it reaches almost nothing\n", spec.Name, facilities[i].ID, facilities[i].Name, services.MinFacilityComponent)
	}
	notify.PhaseDone(phaseRoute, nil)

	notify.PhaseStart(phaseSave)
	// Always saved, even empty: a run that reached nothing must clear the
	// previous run's rows rather than leave stale coverage on the map.
	metrics := make([]db.HexMetric, len(cov.Hexes))
	for i, h := range cov.Hexes {
		metrics[i] = db.HexMetric{HexID: h.HexID, Value: h.Seconds, Ref: facilities[h.Facility].ID}
	}
	if err := store.SaveHexMetrics(ctx, spec.Metric, in.snapshotID, metrics); err != nil {
		notify.PhaseDone(phaseSave, err)
		return fmt.Errorf("save hex metrics: %w", err)
	}
	notify.PhaseDone(phaseSave, nil)

	row := summarize(city.Name, spec, cov, len(facilities), in.snapshotID)
	if opts.rows != nil {
		*opts.rows = append(*opts.rows, row)
	}
	fmt.Fprintf(out, "\n%s Results:\n", spec.Label)
	fmt.Fprintf(out, "  Facilities:        %d (%d unsnapped)\n", row.Facilities, row.Unsnapped)
	fmt.Fprintf(out, "  Hexes reached:     %d\n", row.Hexes)
	fmt.Fprintf(out, "  Within %.0f s travel: %.1f%%\n", spec.TravelThresholdS, row.PctWithin)
	fmt.Fprintf(out, "  Median response:   %.0f s (incl. %.0f s turnout)\n", row.MedianSeconds, spec.TurnoutS)
	return nil
}

// summarize reduces a coverage result to the JSON row / prose figures.
func summarize(cityName string, spec services.Spec, cov services.Coverage, facilities int, snapshotID *int64) computeRow {
	row := computeRow{City: cityName, Service: spec.Name, Facilities: facilities, Unsnapped: len(cov.Unsnapped), Hexes: len(cov.Hexes)}
	if snapshotID != nil {
		row.SnapshotID = *snapshotID
	}
	if len(cov.Hexes) == 0 {
		return row
	}
	secs := make([]float64, len(cov.Hexes))
	within := 0
	for i, h := range cov.Hexes {
		secs[i] = h.Seconds
		if h.Seconds-spec.TurnoutS <= spec.TravelThresholdS {
			within++
		}
	}
	sort.Float64s(secs)
	row.PctWithin = float64(within) / float64(len(secs)) * 100
	row.MedianSeconds = secs[len(secs)/2]
	return row
}
