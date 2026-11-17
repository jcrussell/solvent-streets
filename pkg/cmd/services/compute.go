package services

import (
	"context"
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
	// Draft is the latest-compute update `all compute` is assembling; nil commits this
	// run's own (see compute.WithDraft).
	Draft *db.Draft
	rows  *[]computeRow
}

type computeRow struct {
	City           string `json:"city"`
	Service        string `json:"service"`
	FacilityCount  int    `json:"facility_count"`
	UnsnappedCount int    `json:"unsnapped_count"`
	HexCount       int    `json:"hex_count"`
	// UnreachableCount is the hexes on the road network that no facility
	// reaches (services.Coverage.Unreachable).
	UnreachableCount int     `json:"unreachable_count"`
	ThresholdS       float64 `json:"threshold_s"`
	// WithinThresholdShare is the fraction (0..1) of the hexes on the
	// network, reached or not, whose travel time is within ThresholdS; nil
	// (and omitted) when there are none. MedianResponseS is over the reached
	// hexes, nil when none was reached.
	WithinThresholdShare *float64 `json:"within_threshold_share,omitempty"`
	MedianResponseS      *float64 `json:"median_response_s,omitempty"`
	RunID                int64    `json:"run_id,omitempty"`
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
		case "facility_count":
			out[f] = r.FacilityCount
		case "unsnapped_count":
			out[f] = r.UnsnappedCount
		case "hex_count":
			out[f] = r.HexCount
		case "unreachable_count":
			out[f] = r.UnreachableCount
		case "threshold_s":
			out[f] = r.ThresholdS
		case "within_threshold_share":
			if r.WithinThresholdShare != nil {
				out[f] = *r.WithinThresholdShare
			}
		case "median_response_s":
			if r.MedianResponseS != nil {
				out[f] = *r.MedianResponseS
			}
		case "run_id":
			if r.RunID != 0 {
				out[f] = r.RunID
			}
		}
	}
	return out
}

var computeFields = []string{"city", "service", "facility_count", "unsnapped_count", "hex_count", "unreachable_count", "threshold_s", "within_threshold_share", "median_response_s", "run_id"}

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
		Args:  cobra.NoArgs,
		Short: "Compute response-time coverage per hex",
		Long: `Build a routable graph from the stored roads (OpenStreetMap rows when
present, joined to the routing roads fetched around the city with its
facilities; else the agency centerlines), route from every facility at once,
and store each hex's response time — turnout plus travel to the nearest
reachable point along a road from the hex centre (or, for an edge hex whose
centre the boundary clipped away, from a point inside what remains) — as a
per-hex metric.
The result updates the service's own part of the city's latest compute, so
a later roads compute leaves it in place; run 'pvmt snapshots create <name>' to
publish it, and rerun this after re-ingesting roads or facilities. Hexes off
the network carry no value; hexes on it that no facility reaches are recorded
as unreachable, with no time, and count against the within-threshold share.`,
		Example: `  # Fire response coverage for every configured city
  pvmt services compute

  # One city, as JSON
  pvmt --city oakland-ca services compute --json city,hex_count,within_threshold_share`,
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
// in-process after the pavement passes, with the city's draft (nil commits
// this run's own update).
func RunComputeForCity(ctx context.Context, cf *cmdutil.Factory, service string, grid []geo.Hex, draft *db.Draft) error {
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
		Draft:        draft,
	}
	return runCompute(ctx, opts)
}

func runComputeAllCities(ctx context.Context, f *cmdutil.Factory, opts *Options) error {
	return cmdutil.ForEachCityRows(ctx, f, opts.IO, opts.Exporter, func(cf *cmdutil.Factory, rows *[]computeRow) error {
		cityOpts := *opts
		cityOpts.CurrentCity, cityOpts.CityDB, cityOpts.rows = cf.CurrentCity, cf.CityDB, rows
		return runCompute(ctx, &cityOpts)
	})
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
	return services.Facilities(db.ResourceFeatures(rows), city.Facilities, spec.Name), nil
}

// inputs is everything the routing pass needs, gathered by loadInputs.
type inputs struct {
	store      db.Store
	city       *config.CityConfig
	cfg        *config.Config
	digests    map[string]string // the inputs read, taken before reading them
	spec       services.Spec
	roads      []resource.Feature
	source     string
	facilities []services.Facility
	proj       *geo.UTMProjector
	grid       []geo.Hex
}

// loadInputs resolves the service, the road rows, the facilities and the
// clipped grid. It returns cmdutil.ErrNoResults (after a note on errOut) when
// the city has no roads or no facilities — an empty result, not a failure —
// together with the partly filled inputs (store, hash, spec), so the caller
// can drop the service's part from the latest compute.
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
	in := &inputs{store: store, city: city, cfg: cfg, spec: services.Resolve(spec, cfg.ResolvedServices(city))}
	// Digests first: an ingest landing after this makes the run stale, never
	// falsely current.
	if in.digests, err = db.ReadProducerInputs(ctx, store, Producer(in.spec.Name)); err != nil {
		return nil, err
	}

	// Empty inputs are ErrNoResults (a quiet skip in `pvmt all`): a city with
	// no roads or no stations is not a failure. Coverage routes over the road
	// FEATURES, so no roads compute is required.
	roadRows, err := store.ListFeatures(ctx, resource.TypeRoads)
	if err != nil {
		return nil, fmt.Errorf("list roads: %w", err)
	}
	if len(roadRows) == 0 {
		fmt.Fprintf(errOut, "No roads features in database for %s. Run 'pvmt roads ingest' first.\n", city.Name)
		return in, cmdutil.ErrNoResults
	}
	in.facilities, err = loadFacilities(ctx, store, city, in.spec)
	if err != nil {
		return nil, err
	}
	if len(in.facilities) == 0 {
		fmt.Fprintf(errOut, "No %s facilities for %s (run 'pvmt services ingest' or add [[cities.facilities]]).\n", in.spec.Name, city.Name)
		return in, cmdutil.ErrNoResults
	}
	target, err := compute.LoadMetricTarget(ctx, store, cfg, city, opts.PrebuiltGrid, errOut)
	if err != nil {
		return nil, err
	}
	in.proj, in.grid = target.Proj, target.Grid
	in.roads, in.source = services.PreferredRoads(db.ResourceFeatures(roadRows))
	routingRows, err := store.ListFeatures(ctx, resource.TypeRoutingRoads)
	if err != nil {
		return nil, fmt.Errorf("list routing roads: %w", err)
	}
	in.roads = services.WithRoutingRoads(in.roads, in.source, db.ResourceFeatures(routingRows))
	return in, nil
}

func doCompute(ctx context.Context, out, progress, errOut io.Writer, notify tui.PhaseNotifier, opts *Options) error {
	notify.PhaseStart(phaseLoad)
	in, err := loadInputs(ctx, errOut, opts)
	if errors.Is(err, cmdutil.ErrNoResults) && in != nil {
		// Nothing to route from: drop the service's part, so a city whose
		// stations were removed stops publishing its old coverage.
		notify.PhaseDone(phaseLoad, err)
		return compute.WithDraft(ctx, opts.Draft, in.store, in.cfg, in.city, func(draft *db.Draft) error {
			draft.Drop(Producer(in.spec.Name))
			return err
		})
	}
	if err != nil {
		notify.PhaseDone(phaseLoad, err)
		return err
	}
	spec, facilities, grid := in.spec, in.facilities, in.grid
	fmt.Fprintf(progress, "Loaded %d road features (%s), %d %s facilities, %d hexes\n", len(in.roads), in.source, len(facilities), spec.Name, len(grid))
	notify.PhaseDone(phaseLoad, nil)
	return compute.WithDraft(ctx, opts.Draft, in.store, in.cfg, in.city, func(draft *db.Draft) error {
		return route(ctx, out, progress, errOut, notify, opts, in, draft)
	})
}

// Producer is the snapshot part a service's coverage fills.
func Producer(service string) string { return db.ServiceProducer(service) }

// route builds the road graph, routes every hex to its nearest facility and
// saves the result as the draft's run for the service.
func route(ctx context.Context, out, progress, errOut io.Writer, notify tui.PhaseNotifier, opts *Options, in *inputs, draft *db.Draft) error {
	spec, city, facilities, grid, proj, store := in.spec, in.city, in.facilities, in.grid, in.proj, in.store

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
	// persist a truncated coverage set.
	if err := ctx.Err(); err != nil {
		notify.PhaseDone(phaseRoute, err)
		return fmt.Errorf("compute cancelled: %w", err)
	}
	// Warnings go through errOut, the TUI's warn writer, not straight to
	// ErrOut under the alt-screen program.
	for _, i := range cov.Unsnapped {
		cmdutil.FprintWarnf(errOut, "%s facility %s (%s) is more than %.0f m from any road and was ignored", spec.Name, facilities[i].ID, facilities[i].Name, services.FacilitySnapM)
	}
	for _, i := range cov.Isolated {
		cmdutil.FprintWarnf(errOut, "%s facility %s (%s) snapped to a road fragment of fewer than %d vertices (an apron or driveway OSM does not join to the street); it reaches almost nothing", spec.Name, facilities[i].ID, facilities[i].Name, services.MinFacilityComponent)
	}
	notify.PhaseDone(phaseRoute, nil)

	notify.PhaseStart(phaseSave)
	// The run is committed even when it reached nothing: an empty run hides
	// the metric instead of leaving the previous run's coverage on the map.
	runID, err := draft.NewRun(ctx, Producer(spec.Name), in.digests)
	if err != nil {
		notify.PhaseDone(phaseSave, err)
		return err
	}
	metrics := make([]db.HexMetric, 0, len(cov.Hexes)+len(cov.Unreachable))
	for _, h := range cov.Hexes {
		metrics = append(metrics, db.HexMetric{HexID: h.HexID, Metric: spec.Metric, Value: h.Seconds, Ref: facilities[h.Facility].ID})
	}
	for _, id := range cov.Unreachable {
		metrics = append(metrics, db.HexMetric{HexID: id, Metric: spec.Metric, Subject: services.SubjectUnreachable})
	}
	if err := store.SaveHexMetrics(ctx, runID, metrics); err != nil {
		notify.PhaseDone(phaseSave, err)
		return fmt.Errorf("save hex metrics: %w", err)
	}
	if err := saveRunContext(ctx, store, runID, spec, facilities); err != nil {
		notify.PhaseDone(phaseSave, err)
		return err
	}
	draft.Put(Producer(spec.Name), runID)
	notify.PhaseDone(phaseSave, nil)

	row := summarize(city.Slug(), spec, cov, len(facilities), runID)
	if opts.rows != nil {
		*opts.rows = append(*opts.rows, row)
	}
	fmt.Fprintf(out, "\n%s Results:\n", spec.Label)
	fmt.Fprintf(out, "  Facilities:        %d (%d unsnapped)\n", row.FacilityCount, row.UnsnappedCount)
	fmt.Fprintf(out, "  Hexes reached:     %d\n", row.HexCount)
	if row.UnreachableCount > 0 {
		fmt.Fprintf(out, "  Hexes unreachable: %d (on the road network, but no facility reaches them)\n", row.UnreachableCount)
	}
	if row.WithinThresholdShare != nil {
		fmt.Fprintf(out, "  Within %.0f s travel: %.1f%%\n", row.ThresholdS, *row.WithinThresholdShare*100)
	}
	if row.MedianResponseS != nil {
		fmt.Fprintf(out, "  Median response:   %.0f s (incl. %.0f s turnout)\n", *row.MedianResponseS, spec.TurnoutS)
	}
	return nil
}

// saveRunContext freezes with the run what export draws its coverage with:
// the facilities it routed from (the station markers) and the turnout its
// times include (the legend bands and the covered line).
func saveRunContext(ctx context.Context, store db.Store, runID int64, spec services.Spec, facilities []services.Facility) error {
	frozen := make([]db.RunFacility, len(facilities))
	for i, f := range facilities {
		frozen[i] = db.RunFacility{ID: f.ID, Name: f.Name, Source: f.Source, Lon: f.Lon, Lat: f.Lat}
	}
	if err := store.SaveRunFacilities(ctx, runID, frozen); err != nil {
		return fmt.Errorf("save facilities: %w", err)
	}
	if err := store.SaveCityMetrics(ctx, runID, []db.CityMetric{
		{Metric: services.MetricTurnoutS, Subject: Producer(spec.Name), Value: spec.TurnoutS},
	}); err != nil {
		return fmt.Errorf("save turnout: %w", err)
	}
	return nil
}

// summarize reduces a coverage result to the JSON row / prose figures.
func summarize(citySlug string, spec services.Spec, cov services.Coverage, facilities int, runID int64) computeRow {
	row := computeRow{
		City:             citySlug,
		Service:          spec.Name,
		FacilityCount:    facilities,
		UnsnappedCount:   len(cov.Unsnapped),
		HexCount:         len(cov.Hexes),
		UnreachableCount: len(cov.Unreachable),
		ThresholdS:       spec.TravelThresholdS,
		RunID:            runID,
	}
	// The share's denominator is every hex on the network: an unreachable
	// hex is not within the threshold, and leaving it out would bias the
	// share upward.
	onNetwork := len(cov.Hexes) + len(cov.Unreachable)
	if onNetwork == 0 {
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
	share := float64(within) / float64(onNetwork)
	row.WithinThresholdShare = &share
	if len(secs) > 0 {
		sort.Float64s(secs)
		median := secs[len(secs)/2]
		row.MedianResponseS = &median
	}
	return row
}
