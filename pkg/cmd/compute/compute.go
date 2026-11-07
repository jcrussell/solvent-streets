package compute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/filter"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/logs"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/tui"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/peterstace/simplefeatures/geom"
	"github.com/spf13/cobra"
)

type Options struct {
	IO           *iostreams.IOStreams
	CityDB       func() (db.Store, error)
	Config       func() (*config.Config, error)
	CurrentCity  func() (*config.CityConfig, error)
	UnitSystem   func() units.System
	ResourceType resource.Source
	CityOnly     bool
	Exporter     cmdutil.Exporter
	rows         *[]computeRow // multi-city accumulator; nil for single-pass JSON
	// PrebuiltGrid, when non-nil, is a clipped hex grid built once for the city
	// and reused as-is (read-only) instead of regenerating + clipping here. Set
	// by `all compute` via RunResourceForCity so the expensive HexGrid +
	// ClipHexesToBoundary runs once per city rather than once per resource. nil
	// means build the grid in this run (the standalone `roads/parking/... compute`
	// path). The grid depends only on (boundary, hex edge), identical across
	// resources for a city.
	PrebuiltGrid []geo.Hex
	// Draft, when non-nil, is the snapshot the caller is assembling (`all
	// compute` commits one per city once all its steps are done). nil means this run
	// owns its draft and commits it as one snapshot (see WithDraft).
	Draft *db.Draft
}

type computeRow struct {
	City         string  `json:"city"`
	ResourceType string  `json:"resource_type"`
	Jurisdiction string  `json:"jurisdiction"` // "all" or "city"
	FeatureCount int     `json:"feature_count"`
	Area         float64 `json:"area"`
	// EstimatedSpaces is the parking resource's estimated vehicle capacity
	// (0 and omitted for every other resource).
	EstimatedSpaces int `json:"estimated_spaces,omitempty"`
	// RunID is the run this compute wrote. The snapshot id is not known yet
	// when the row is recorded: under `all compute` the city's snapshot is
	// committed once all of the city's steps are done.
	RunID int64 `json:"run_id,omitempty"`
}

var _ cmdutil.RowExporter = computeRow{}

func (r computeRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "city":
			out[f] = r.City
		case "resource_type":
			out[f] = r.ResourceType
		case "jurisdiction":
			out[f] = r.Jurisdiction
		case "feature_count":
			out[f] = r.FeatureCount
		case "area":
			out[f] = r.Area
		case "estimated_spaces":
			if r.EstimatedSpaces != 0 {
				out[f] = r.EstimatedSpaces
			}
		case "run_id":
			if r.RunID != 0 {
				out[f] = r.RunID
			}
		}
	}
	return out
}

// Example is the --help example text for the coverage compute subcommand of
// the command group named group (`pvmt <group> compute`).
func Example(group string) string {
	return fmt.Sprintf(`  # Compute per-hex coverage for %s across every configured city
  pvmt %s compute

  # Restrict to city-maintained features only (when the resource has cohorts)
  pvmt %s compute --city-only

  # Scope to a single city
  pvmt --city oakland %s compute`, group, group, group, group)
}

var computeFields = []string{"city", "resource_type", "jurisdiction", "feature_count", "area", "estimated_spaces", "run_id"}

func NewCmdCompute(f *cmdutil.Factory, rt resource.Source, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO:           f.IOStreams,
		CityDB:       f.CityDB,
		Config:       f.Config,
		CurrentCity:  f.CurrentCity,
		UnitSystem:   f.UnitSystem,
		ResourceType: rt,
	}

	t := rt.Type()
	cmd := &cobra.Command{
		Use:     "compute",
		Short:   fmt.Sprintf("Compute %s area statistics", t),
		Example: Example(string(t)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runComputeAllCities(cmd.Context(), f, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.CityOnly, "city-only", false, "Only show city-maintained road results")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, computeFields)

	return cmd
}

// RunResourceForCity computes stats for a single resource type against the
// already city-scoped factory f, reusing the caller-supplied clipped hex grid
// (nil = build one for this run). `all compute` calls it in-process — instead
// of executing a nested cobra command — so one clipped grid can be shared
// across the per-resource passes. It mirrors the Options NewCmdCompute builds and
// runs the same single-city path (TTY/JSON routing included).
//
// draft is the snapshot the caller is assembling; nil commits this run's own.
func RunResourceForCity(ctx context.Context, f *cmdutil.Factory, rt resource.Source, grid []geo.Hex, draft *db.Draft) error {
	// Pre-check cancellation before doing any work. The `all` fan-out calls
	// this in-process, bypassing cmdutil.ForEachCity's pre-check, so without it
	// a mid-fan-out cancellation aborts only because the store or HTTP layer happens to
	// surface context.Canceled. Real sqlite does; a ctx-ignoring store —
	// dbtest.MockStore — would run every remaining resource after cancellation.
	if err := ctx.Err(); err != nil {
		return err
	}
	opts := &Options{
		IO:           f.IOStreams,
		CityDB:       f.CityDB,
		Config:       f.Config,
		CurrentCity:  f.CurrentCity,
		UnitSystem:   f.UnitSystem,
		ResourceType: rt,
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

// Phase indices for the TUI step checklist.
const (
	phaseProcess = iota
	phaseHexGrid
	phaseClip
	phaseStats
	phaseSave
)

func runCompute(ctx context.Context, opts *Options) error {
	// JSON mode (--json set) routes prose to discard and disables TUI so
	// only the final JSON payload reaches stdout (byob-iostreams.3: data
	// to Out, chatter to ErrOut).
	if opts.Exporter != nil {
		// JSON mode: the final payload is written separately by WriteRows, so
		// result DATA is discarded here and human progress is discarded too —
		// only warnings (errOut) reach the user.
		return doCompute(ctx, io.Discard, io.Discard, opts.IO.ErrOut, tui.NoopNotifier{}, opts)
	}
	if opts.IO.IsTTY() {
		return runComputeTUI(ctx, opts)
	}
	// Plain (non-TTY, non-JSON): result DATA goes to Out; human progress
	// chatter and warnings go to ErrOut so a redirected stdout captures only
	// the command's data (byob-iostreams.3).
	return doCompute(ctx, opts.IO.Out, opts.IO.ErrOut, opts.IO.ErrOut, tui.NoopNotifier{}, opts)
}

func runComputeTUI(ctx context.Context, opts *Options) error {
	city, err := opts.CurrentCity()
	if err != nil {
		return fmt.Errorf("city: %w", err)
	}
	label := fmt.Sprintf("Computing %s · %s", opts.ResourceType.Type(), city.Name)
	steps := []tui.Step{
		{Name: "Processing features"},
		{Name: "Generating hex grid"},
		{Name: "Clipping hexes to boundary"},
		{Name: "Computing hex stats"},
		{Name: "Saving results"},
	}
	done := tui.DoneConfig{
		SuccessMsg: fmt.Sprintf("%s compute complete for %s", opts.ResourceType.Type(), city.Name),
	}
	return tui.Run(ctx, label, steps, done, func(ctx context.Context, out io.Writer, errOut io.Writer, notify tui.PhaseNotifier) error {
		// In the TUI both result DATA and progress chatter render through the
		// progress writer (out); warnings render through the warn writer
		// (errOut). Preserves the pre-split behavior where chatter appeared as
		// progress detail rather than as warnings.
		return doCompute(ctx, out, out, errOut, notify, opts)
	})
}

// computer bundles the per-run environment shared by the hex-pipeline methods
// built once in doCompute, then runAllFeaturesPass / processCityResults /
// computeHexPipeline are invoked as methods with only the varying payload
// (resFeatures, clippedHexes, buffered) as explicit args.
type computer struct {
	opts     *Options
	cfg      *config.Config
	city     *config.CityConfig
	store    db.Store
	proj     *geo.UTMProjector
	bbox     [4]float64
	runID    int64 // the run every row of this compute is tagged with
	sys      units.System
	notify   tui.PhaseNotifier
	out      io.Writer // result DATA (printResults / printCohortBreakdown)
	progress io.Writer // human progress chatter ("Loading...", "Generated N hexes")
	errOut   io.Writer // warnings
}

func doCompute(ctx context.Context, out, progress, errOut io.Writer, notify tui.PhaseNotifier, opts *Options) error {
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

	boundaryGJSON, bbox, proj, err := LoadBoundary(ctx, store, city)
	if err != nil {
		return err
	}

	c := &computer{
		opts:     opts,
		cfg:      cfg,
		city:     city,
		store:    store,
		proj:     proj,
		bbox:     bbox,
		sys:      opts.UnitSystem(),
		notify:   notify,
		out:      out,
		progress: progress,
		errOut:   errOut,
	}

	producer := string(opts.ResourceType.Type())
	// No features is an empty result (ErrNoResults) that removes the
	// resource's part, so coverage computed from features
	// that are gone (a source removed and swept) stops being published. A
	// city that never had the part is unchanged: dropping an absent part is a
	// no-op commit.
	resFeatures, err := c.loadResourceFeatures(ctx)
	if errors.Is(err, cmdutil.ErrNoResults) {
		return WithDraft(ctx, opts.Draft, store, cfg.CityHash(city), func(draft *db.Draft) error {
			draft.Drop(producer)
			return err
		})
	}
	if err != nil {
		return err
	}
	return WithDraft(ctx, opts.Draft, store, cfg.CityHash(city), func(draft *db.Draft) error {
		runID, err := draft.NewRun(ctx, producer)
		if err != nil {
			return err
		}
		c.runID = runID
		if err := c.computeAndSave(ctx, resFeatures, boundaryGJSON); err != nil {
			return err
		}
		draft.Put(producer, runID)
		return nil
	})
}

// computeAndSave runs the coverage passes over resFeatures and saves every
// result row under c.runID.
func (c *computer) computeAndSave(ctx context.Context, resFeatures []resource.Feature, boundaryGJSON string) error {
	opts, notify, progress, proj := c.opts, c.notify, c.progress, c.proj
	jurisdictionParts := filter.Partition(resFeatures)
	fmt.Fprintf(progress, "  Total: %d features (%d city, %d county, %d state, %d federal)\n",
		len(resFeatures),
		len(jurisdictionParts[filter.JurisdictionCity]),
		len(jurisdictionParts[filter.JurisdictionCounty]),
		len(jurisdictionParts[filter.JurisdictionState]),
		len(jurisdictionParts[filter.JurisdictionFederal]),
	)

	// Buffer every feature exactly once. The "all" and "city" passes share
	// this slice; the city pass filters on jurisdiction without invoking
	// BufferFeatures again, and cohort stats re-use the same polygons.
	allBuffered := opts.ResourceType.BufferFeaturesPaired(ctx, resFeatures, proj)
	// BufferFeaturesPaired is ParallelMap-backed and returns an empty (partial)
	// slice with no error on cancellation. Distinguish a real "no valid
	// geometries" from a SIGINT so a cancelled run reports context.Canceled
	// rather than a misleading buffer error — and so callers up the chain
	// (forEachResource, all compute) can special-case cancellation.
	if err := ctx.Err(); err != nil {
		notify.PhaseDone(phaseProcess, err)
		return fmt.Errorf("compute cancelled: %w", err)
	}
	if len(allBuffered) == 0 {
		err := errors.New("buffer features: no valid geometries to process")
		notify.PhaseDone(phaseProcess, err)
		return err
	}
	notify.PhaseDone(phaseProcess, nil)
	cityBuffered := filterBufferedByJurisdiction(allBuffered, filter.JurisdictionCity)

	hexStats, clippedHexes := c.runAllFeaturesPass(ctx, allBuffered, boundaryGJSON)
	// Parking only: apportion each lot's estimated spaces onto the same hexes
	// (a no-op slice for other resources, see spaceWeights).
	hexStats = c.mergeSpaces(ctx, hexStats, allBuffered, clippedHexes)
	// ParallelMap returns partial results (no error) on cancellation, so a
	// SIGINT'd run would otherwise persist truncated hex stats. Guard between
	// compute and persistence: bail on cancellation without saving anything
	// (the run is then discarded, never committed).
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("compute cancelled: %w", err)
	}
	if err := c.saveAllJurisdictionsResult(ctx, allBuffered, len(resFeatures), hexStats, clippedHexes); err != nil {
		return err
	}

	if err := c.processCityResults(ctx, cityBuffered, len(jurisdictionParts[filter.JurisdictionCity]), clippedHexes); err != nil {
		return err
	}

	return c.saveHexStats(ctx, hexStats)
}

// spaceWeights returns the per-feature estimated parking spaces for the
// parking resource, index-aligned with buffered, and nil for every other
// resource — the estimate is a parking concept (lots and garages), so roads
// and sidewalks carry no spaces.
func (c *computer) spaceWeights(buffered []resource.BufferedFeature) []float64 {
	if c.opts.ResourceType.Type() != resource.TypeParking {
		return nil
	}
	return resource.ParkingSpaceWeights(buffered)
}

// mergeSpaces apportions the parking-space estimate onto the hex grid and
// folds it into stats by hex id. A hex that holds spaces but no coverage row
// cannot occur (a lot with area always has coverage), so only existing rows
// are updated and a stray share is dropped. Returns stats unchanged for
// resources without weights.
func (c *computer) mergeSpaces(ctx context.Context, stats []geo.HexStat, buffered []resource.BufferedFeature, hexes []geo.Hex) []geo.HexStat {
	weights := c.spaceWeights(buffered)
	if weights == nil {
		return stats
	}
	shares := geo.ComputeHexWeightedShares(ctx, hexes, resource.Geoms(buffered), weights, nil)
	if len(shares) == 0 {
		return stats
	}
	byHex := make(map[string]float64, len(shares))
	for _, sh := range shares {
		byHex[sh.HexID] += sh.Value
	}
	for i := range stats {
		stats[i].EstimatedSpaces = byHex[stats[i].HexID]
	}
	return stats
}

// sumSpaces totals the apportioned spaces over a hex-stat slice, rounded to
// a whole count: the city-level figure is the sum of the hex-clipped shares,
// so it stays consistent with total_area (also a sum over clipped hexes).
func sumSpaces(stats []geo.HexStat) int {
	var s float64
	for _, st := range stats {
		s += st.EstimatedSpaces
	}
	return int(math.Round(s))
}

// filterBufferedByJurisdiction returns the subset of buffered features whose
// source feature tags classify as j. Avoids re-buffering by sharing the same
// geom values.
func filterBufferedByJurisdiction(bufs []resource.BufferedFeature, j filter.Jurisdiction) []resource.BufferedFeature {
	out := make([]resource.BufferedFeature, 0, len(bufs))
	for _, b := range bufs {
		if filter.ClassifyJurisdiction(b.Feature.Tags) == j {
			out = append(out, b)
		}
	}
	return out
}

// LoadBoundary reads the city's stored boundary and derives its bbox and UTM
// projector, warning about an oversized bbox or dropped sub-polygons. It is
// the single source of the projection every per-city pass (coverage, the
// combined pass, service routing) must share so their hex ids and metres
// agree.
func LoadBoundary(ctx context.Context, store db.Store, city *config.CityConfig) (string, [4]float64, *geo.UTMProjector, error) {
	boundaryGJSON, err := store.GetBoundary(ctx)
	if err != nil {
		return "", [4]float64{}, nil, fmt.Errorf("get boundary: %w", err)
	}
	if boundaryGJSON == "" {
		return "", [4]float64{}, nil, fmt.Errorf("no boundary stored for city %s — run 'pvmt all ingest' first", city.Name)
	}
	bbox, err := geo.BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return "", [4]float64{}, nil, fmt.Errorf("derive bbox: %w", err)
	}
	lon, lat := geo.CenterFromBBox(bbox)
	proj := geo.NewUTMProjector(lon, lat)
	// A single UTM zone is picked from the bbox center; a bbox much wider than
	// one 6° zone gets distorted metric areas/lengths near its edges. Warn so an
	// oversized city isn't silently mis-measured (bbox is [south,west,north,east],
	// so the lon span is east-west = bbox[3]-bbox[1]).
	if geo.UTMLonSpanExceeds(bbox[1], bbox[3], 6) {
		logs.From(ctx).Warn("boundary: bbox longitude span exceeds one UTM zone; metric areas near the edges may be distorted",
			"city", city.Name, "lon_span_deg", bbox[3]-bbox[1], "utm_zone", proj.Zone)
	}
	// Surface partial boundary loss: a degenerate sub-polygon that fails
	// cleaning silently drops a whole landmass from the boundary, shrinking
	// computed area and hex coverage. Warn so operators can fix the source.
	if _, _, dropped, gErr := geo.GeoJSONToProjectedGeometryDropped(boundaryGJSON, proj); gErr == nil && dropped > 0 {
		logs.From(ctx).Warn("boundary: dropped degenerate sub-polygons",
			"city", city.Name, "dropped", dropped)
	}
	return boundaryGJSON, bbox, proj, nil
}

// loadResourceFeatures reads features for opts.ResourceType from store and
// returns them in resource.Feature form. out/errOut come from the computer
// so prose honors the JSON/TTY routing chosen by doCompute's caller.
func (c *computer) loadResourceFeatures(ctx context.Context) ([]resource.Feature, error) {
	c.notify.PhaseStart(phaseProcess)
	t := c.opts.ResourceType.Type()
	fmt.Fprintf(c.progress, "Loading %s features from database...\n", t)
	dbFeatures, err := c.store.ListFeatures(ctx, t)
	if err != nil {
		c.notify.PhaseDone(phaseProcess, err)
		return nil, fmt.Errorf("list features: %w", err)
	}
	if len(dbFeatures) == 0 {
		fmt.Fprintf(c.errOut, "No %s features in database. Run 'pvmt %s ingest' first.\n", t, t)
		c.notify.PhaseDone(phaseProcess, errors.New("no features"))
		return nil, cmdutil.ErrNoResults
	}
	fmt.Fprintf(c.progress, "Processing %d features (UTM zone %d)...\n", len(dbFeatures), c.proj.Zone)
	return db.ResourceFeatures(dbFeatures), nil
}

// runAllFeaturesPass runs the hex pipeline against every-jurisdiction
// buffered features and returns the per-hex coverage plus the clipped hex
// grid for downstream passes to share.
func (c *computer) runAllFeaturesPass(ctx context.Context, allBuffered []resource.BufferedFeature, boundaryGJSON string) ([]geo.HexStat, []geo.Hex) {
	return c.computeHexPipeline(ctx, resource.Geoms(allBuffered), boundaryGJSON)
}

// saveAllJurisdictionsResult persists the every-jurisdiction ComputeResult,
// cohort stats, and prose summary derived from the pre-buffered features.
// Splits out from runAllFeaturesPass so the hex pipeline output is reusable
// for the city-only pass before any per-jurisdiction persistence happens.
// featureCount is the count of input features, including any dropped by the
// buffer step.
func (c *computer) saveAllJurisdictionsResult(ctx context.Context, allBuffered []resource.BufferedFeature, featureCount int, hexStats []geo.HexStat, clippedHexes []geo.Hex) error {
	// Per-hex coverage is dedup'd by the local union inside ComputeHexStats,
	// so summing does not double-count crossing-road overlaps.
	var area float64
	for _, s := range hexStats {
		area += s.Area
	}

	rtAll := c.opts.ResourceType.Type()
	spaces := sumSpaces(hexStats)
	result := db.ComputeResult{
		ResourceType:    rtAll,
		TotalArea:       area,
		FeatureCount:    featureCount,
		EstimatedSpaces: spaces,
		RunID:           c.runID,
	}
	if err := c.store.SaveComputeResult(ctx, result); err != nil {
		return fmt.Errorf("save result: %w", err)
	}
	c.recordRow("all", featureCount, area, spaces)

	cohortStats := c.buildCohortStats(ctx, rtAll, allBuffered, area, clippedHexes)
	if len(cohortStats) > 0 {
		if err := c.store.SaveCohortStats(ctx, cohortStats); err != nil {
			return fmt.Errorf("save cohort stats: %w", err)
		}
		if !c.opts.CityOnly && c.opts.ResourceType.HasCohorts() {
			printCohortBreakdown(c.out, "Cohort breakdown", cohortStats, area, c.sys)
		}
	}
	if !c.opts.CityOnly {
		printResults(c.out, fmt.Sprintf("%s Results (all)", rtAll.Bare()), featureCount, area, spaces, c.sys)
	}
	return nil
}

func (c *computer) saveHexStats(ctx context.Context, hexStats []geo.HexStat) error {
	c.notify.PhaseStart(phaseSave)
	defer c.notify.PhaseDone(phaseSave, nil)
	rt := c.opts.ResourceType.Type()
	dbStats := make([]db.HexStat, len(hexStats))
	for i, s := range hexStats {
		dbStats[i] = db.HexStat{
			HexID:        s.HexID,
			ResourceType: rt,
			Area:         s.Area,
			PctCovered:   s.PctCovered,
			RunID:        c.runID,
		}
	}
	if err := c.store.SaveHexStats(ctx, dbStats); err != nil {
		return fmt.Errorf("save hex stats: %w", err)
	}
	return c.saveSpaceMetrics(ctx, rt, hexStats)
}

// saveSpaceMetrics persists the per-hex parking spaces of rt's stats to
// hex_metrics (resource.SpacesMetricKey) under this compute's run, the same
// run its hex_stats rows carry, so the snapshot always pairs them.
// Hexes with no spaces are not stored. A no-op for resources without spaces.
func (c *computer) saveSpaceMetrics(ctx context.Context, rt resource.Type, stats []geo.HexStat) error {
	key := resource.SpacesMetricKey(rt)
	if key == "" {
		return nil
	}
	var rows []db.HexMetric
	for _, s := range stats {
		if s.EstimatedSpaces > 0 {
			rows = append(rows, db.HexMetric{HexID: s.HexID, Value: s.EstimatedSpaces})
		}
	}
	if err := c.store.SaveHexMetrics(ctx, key, c.runID, rows); err != nil {
		return fmt.Errorf("save %s parking space metrics: %w", rt, err)
	}
	return nil
}

// processCityResults runs the city-jurisdiction hex pipeline over the
// pre-buffered city subset and persists the :city ComputeResult, cohort
// stats, and prose summary. featureCount is the input city-feature count
// (including any later dropped by the buffer step).
func (c *computer) processCityResults(ctx context.Context, cityBuffered []resource.BufferedFeature, featureCount int, clippedHexes []geo.Hex) error {
	if len(cityBuffered) == 0 {
		return nil
	}

	rtCity := c.opts.ResourceType.Type().With(resource.ScopeCity)
	cityIdx := geo.NewGeomIndexFromGeoms(resource.Geoms(cityBuffered))
	cityStats := geo.ComputeHexStats(ctx, clippedHexes, cityIdx, string(rtCity), nil)
	cityStats = c.mergeSpaces(ctx, cityStats, cityBuffered, clippedHexes)
	// ComputeHexStats (ParallelMap-backed) yields partial results on cancel;
	// bail before persisting truncated city stats.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("compute cancelled: %w", err)
	}

	var cityArea float64
	cityDBStats := make([]db.HexStat, len(cityStats))
	for i, s := range cityStats {
		cityArea += s.Area
		cityDBStats[i] = db.HexStat{
			HexID:        s.HexID,
			ResourceType: rtCity,
			Area:         s.Area,
			PctCovered:   s.PctCovered,
			RunID:        c.runID,
		}
	}
	if err := c.store.SaveHexStats(ctx, cityDBStats); err != nil {
		return fmt.Errorf("save city hex stats: %w", err)
	}
	if err := c.saveSpaceMetrics(ctx, rtCity, cityStats); err != nil {
		return err
	}

	citySpaces := sumSpaces(cityStats)
	cityResult := db.ComputeResult{
		ResourceType:    rtCity,
		TotalArea:       cityArea,
		FeatureCount:    featureCount,
		EstimatedSpaces: citySpaces,
		RunID:           c.runID,
	}
	if err := c.store.SaveComputeResult(ctx, cityResult); err != nil {
		return fmt.Errorf("save city result: %w", err)
	}
	c.recordRow("city", featureCount, cityArea, citySpaces)

	cityCohortStats := c.buildCohortStats(ctx, rtCity, cityBuffered, cityArea, clippedHexes)
	if len(cityCohortStats) > 0 {
		if err := c.store.SaveCohortStats(ctx, cityCohortStats); err != nil {
			return fmt.Errorf("save city cohort stats: %w", err)
		}
		if c.opts.ResourceType.HasCohorts() {
			printCohortBreakdown(c.out, "City cohort breakdown", cityCohortStats, cityArea, c.sys)
		}
	}

	printResults(c.out, fmt.Sprintf("%s Results (city only)", c.opts.ResourceType.Type()), featureCount, cityArea, citySpaces, c.sys)
	return nil
}

// recordRow appends a computeRow to the multi-city accumulator if the
// caller set one up. No-op when --json is not active.
func (c *computer) recordRow(jurisdiction string, featureCount int, area float64, spaces int) {
	if c.opts.rows == nil {
		return
	}
	row := computeRow{
		City:            c.city.Slug(),
		ResourceType:    string(c.opts.ResourceType.Type()),
		Jurisdiction:    jurisdiction,
		FeatureCount:    featureCount,
		Area:            area,
		EstimatedSpaces: spaces,
	}
	row.RunID = c.runID
	*c.opts.rows = append(*c.opts.rows, row)
}

func printResults(out io.Writer, label string, featureCount int, area float64, spaces int, sys units.System) {
	fmt.Fprintf(out, "\n%s:\n", label)
	fmt.Fprintf(out, "  Features:  %d\n", featureCount)
	fmt.Fprintf(out, "  Area:      %s\n", units.FormatArea(area, sys))
	fmt.Fprintf(out, "  Area:      %s\n", units.FormatAreaLarge(area, sys))
	fmt.Fprintf(out, "  Area:      %s\n", units.FormatAreaVeryLarge(area, sys))
	if spaces > 0 {
		fmt.Fprintf(out, "  Spaces:    ~%d (estimated)\n", spaces)
	}
}

func printCohortBreakdown(out io.Writer, title string, stats []db.CohortStat, totalArea float64, sys units.System) {
	fmt.Fprintf(out, "\n%s:\n", title)
	for _, cs := range stats {
		pct := 0.0
		if totalArea > 0 {
			pct = cs.Area / totalArea * 100
		}
		fmt.Fprintf(out, "  %-12s %6.1f%% (%s, %d features)\n",
			cs.Classification, pct, units.FormatArea(cs.Area, sys), cs.FeatureCount)
	}
}

// computeHexPipeline runs the hex-grid stages (generate, clip, stats) and
// returns the per-hex stats plus the clipped hex slice. The caller owns
// persistence and can reuse clippedHexes for a second pass (e.g. city-only).
func (c *computer) computeHexPipeline(ctx context.Context, buffered []geom.Geometry, boundaryGJSON string) ([]geo.HexStat, []geo.Hex) {
	hexes := c.hexGrid(ctx, boundaryGJSON)

	// --- Phase 3: Compute hex stats ---
	c.notify.PhaseStart(phaseStats)

	idx := geo.NewGeomIndexFromGeoms(buffered)

	var statsCounter atomic.Int64
	stopStatsProgress := startProgressTicker(ctx, c.notify, phaseStats, len(hexes), &statsCounter)
	geoStats := geo.ComputeHexStats(ctx, hexes, idx, string(c.opts.ResourceType.Type()), &statsCounter)
	stopStatsProgress()
	fmt.Fprintf(c.progress, "  %d hexes with coverage\n", len(geoStats))
	c.notify.PhaseDone(phaseStats, nil)

	return geoStats, hexes
}

// hexGrid returns the clipped hex grid for this run. When the caller injected a
// PrebuiltGrid (all compute sharing one grid across the per-resource passes),
// it is reused as-is and the generate + clip phases are skipped — the grid is
// treated read-only, matching how clippedHexes is already shared across the
// all/city/cohort passes within one run. Otherwise the grid is generated and
// clipped to the boundary here (the standalone `<resource> compute` path).
func (c *computer) hexGrid(ctx context.Context, boundaryGJSON string) []geo.Hex {
	if c.opts.PrebuiltGrid != nil {
		// Fire the phase notifications so the TUI checklist still advances past
		// the (skipped) grid + clip steps.
		c.notify.PhaseStart(phaseHexGrid)
		c.notify.PhaseDone(phaseHexGrid, nil)
		c.notify.PhaseStart(phaseClip)
		c.notify.PhaseDone(phaseClip, nil)
		fmt.Fprintf(c.progress, "\nReusing shared clipped hex grid: %d hexes\n", len(c.opts.PrebuiltGrid))
		return c.opts.PrebuiltGrid
	}

	// --- Phase 1: Generate hex grid ---
	c.notify.PhaseStart(phaseHexGrid)

	hexEdge := c.cfg.ResolvedHexEdge(c.city)
	minX, minY, maxX, maxY := geo.ProjectedBBoxExtent(c.proj, c.bbox)

	fmt.Fprintf(c.progress, "\nComputing hex grid (edge=%.0fm)...\n", hexEdge)
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)
	fmt.Fprintf(c.progress, "  Generated %d hexes\n", len(hexes))
	c.notify.PhaseDone(phaseHexGrid, nil)

	// --- Phase 2: Clip hexes to boundary ---
	c.notify.PhaseStart(phaseClip)

	boundaryGeom, err := parseGeoJSONGeometry(boundaryGJSON, c.proj)
	switch {
	case err != nil:
		// Skipping the clip runs stats over the full rectangular bbox grid,
		// inflating coverage. Warn (as loadBoundary does) so it isn't silent.
		fmt.Fprintf(c.errOut, "Warning: boundary parse failed for city %q, computing over full bbox: %v\n", c.city.Name, err)
	case boundaryGeom.IsEmpty():
		fmt.Fprintf(c.errOut, "Warning: boundary empty after cleaning for city %q, computing over full bbox\n", c.city.Name)
	default:
		var clipCounter atomic.Int64
		stopClipProgress := startProgressTicker(ctx, c.notify, phaseClip, len(hexes), &clipCounter)
		hexes = geo.ClipHexesToBoundary(ctx, hexes, boundaryGeom, &clipCounter)
		stopClipProgress()
		fmt.Fprintf(c.progress, "  Clipped to boundary: %d hexes\n", len(hexes))
	}
	c.notify.PhaseDone(phaseClip, nil)

	return hexes
}

// startProgressTicker launches a goroutine that sends PhaseProgress updates
// every 200ms based on counter / total. Returns a stop function that must be
// called when the phase is done. The goroutine also exits when ctx is cancelled,
// so a parent context teardown won't leak the goroutine even if the caller's
// stop is skipped (panic, early return).
func startProgressTicker(ctx context.Context, notify tui.PhaseNotifier, phase, total int, counter *atomic.Int64) func() {
	if total == 0 {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n := int(counter.Load())
				notify.PhaseProgress(phase, float64(n)/float64(total),
					fmt.Sprintf("%d / %d hexes", n, total))
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		notify.PhaseProgress(phase, 1.0, fmt.Sprintf("%d / %d hexes", total, total))
	}
}

func parseGeoJSONGeometry(gjson string, proj *geo.UTMProjector) (geom.Geometry, error) {
	g, _, err := geo.GeoJSONToProjectedGeometry(gjson, proj)
	if err != nil {
		return geom.Geometry{}, err
	}
	return g, nil
}

// buildCohortStats builds cohort stats for the resource type variant rt
// (e.g. "roads" or "roads:city"). When the resource has cohorts it computes
// per-classification areas via the same hex-clipped pipeline used for the
// resource total; otherwise it returns a single cohort stat. buffered carries
// each feature paired with its pre-buffered polygon so cohort computation
// reuses those polygons rather than re-buffering.
func (c *computer) buildCohortStats(ctx context.Context, rt resource.Type, buffered []resource.BufferedFeature, totalArea float64, hexes []geo.Hex) []db.CohortStat {
	if !c.opts.ResourceType.HasCohorts() {
		return []db.CohortStat{{
			ResourceType:   rt,
			Classification: string(rt.Bare()),
			Area:           totalArea,
			FeatureCount:   len(buffered),
			RunID:          c.runID,
		}}
	}

	rawAreas := resource.ComputeRoadCohortAreas(ctx, buffered, hexes)
	var rawTotal float64
	for _, a := range rawAreas {
		rawTotal += a
	}
	counts := make(map[string]int)
	for _, bf := range buffered {
		class := forecast.NormalizeClass(bf.Feature.Tags["highway"])
		counts[class]++
	}
	var stats []db.CohortStat
	for class, rawArea := range rawAreas {
		proportion := 0.0
		if rawTotal > 0 {
			proportion = rawArea / rawTotal
		}
		stats = append(stats, db.CohortStat{
			ResourceType:   rt,
			Classification: class,
			Area:           totalArea * proportion,
			FeatureCount:   counts[class],
			RunID:          c.runID,
		})
	}
	return stats
}
