package compute

import (
	"context"
	"errors"
	"fmt"
	"io"
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
}

type computeRow struct {
	City         string  `json:"city"`
	ResourceType string  `json:"resourceType"`
	Jurisdiction string  `json:"jurisdiction"` // "all" or "city"
	FeatureCount int     `json:"featureCount"`
	Area         float64 `json:"area"`
	SnapshotID   int64   `json:"snapshotId,omitempty"`
}

var _ cmdutil.RowExporter = computeRow{}

func (r computeRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "city":
			out[f] = r.City
		case "resourceType":
			out[f] = r.ResourceType
		case "jurisdiction":
			out[f] = r.Jurisdiction
		case "featureCount":
			out[f] = r.FeatureCount
		case "area":
			out[f] = r.Area
		case "snapshotId":
			out[f] = r.SnapshotID
		}
	}
	return out
}

var computeFields = []string{"city", "resourceType", "jurisdiction", "featureCount", "area", "snapshotId"}

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
		Use:   "compute",
		Short: fmt.Sprintf("Compute %s area statistics", t),
		Example: fmt.Sprintf(`  # Compute per-hex coverage for %s across every configured city
  pvmt %s compute

  # Restrict to city-maintained features only (when the resource has cohorts)
  pvmt %s compute --city-only

  # Scope to a single city
  pvmt --city oakland %s compute`, t, t, t, t),
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
		return doCompute(ctx, io.Discard, opts.IO.ErrOut, tui.NoopNotifier{}, opts)
	}
	if opts.IO.IsTTY() {
		return runComputeTUI(ctx, opts)
	}
	return doCompute(ctx, opts.IO.Out, opts.IO.ErrOut, tui.NoopNotifier{}, opts)
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
	return tui.Run(label, steps, done, func(out io.Writer, errOut io.Writer, notify tui.PhaseNotifier) error {
		return doCompute(ctx, out, errOut, notify, opts)
	})
}

// computer bundles the per-run environment shared by the hex-pipeline methods
// built once in doCompute, then runAllFeaturesPass / processCityResults /
// computeHexPipeline are invoked as methods with only the varying payload
// (resFeatures, clippedHexes, buffered) as explicit args.
type computer struct {
	opts       *Options
	cfg        *config.Config
	city       *config.CityConfig
	store      db.Store
	proj       *geo.UTMProjector
	bbox       [4]float64
	snapshotID *int64
	sys        units.System
	notify     tui.PhaseNotifier
	out        io.Writer
	errOut     io.Writer
}

func doCompute(ctx context.Context, out, errOut io.Writer, notify tui.PhaseNotifier, opts *Options) error {
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

	boundaryGJSON, bbox, proj, err := loadBoundary(ctx, store, city)
	if err != nil {
		return err
	}

	c := &computer{
		opts:       opts,
		cfg:        cfg,
		city:       city,
		store:      store,
		proj:       proj,
		bbox:       bbox,
		snapshotID: createSnapshot(ctx, errOut, store, cfg),
		sys:        opts.UnitSystem(),
		notify:     notify,
		out:        out,
		errOut:     errOut,
	}

	resFeatures, err := c.loadResourceFeatures(ctx)
	if err != nil {
		return err
	}
	jurisdictionParts := filter.Partition(resFeatures)
	fmt.Fprintf(out, "  Total: %d features (%d city, %d county, %d state, %d federal)\n",
		len(resFeatures),
		len(jurisdictionParts[filter.JurisdictionCity]),
		len(jurisdictionParts[filter.JurisdictionCounty]),
		len(jurisdictionParts[filter.JurisdictionState]),
		len(jurisdictionParts[filter.JurisdictionFederal]),
	)

	// Buffer every feature exactly once. The "all" and "city" passes share
	// this slice; the city pass filters on jurisdiction without invoking
	// BufferFeatures again, and cohort stats re-use the same polygons.
	allBuffered := opts.ResourceType.BufferFeaturesPaired(resFeatures, proj)
	if len(allBuffered) == 0 {
		err := errors.New("buffer features: no valid geometries to process")
		notify.PhaseDone(phaseProcess, err)
		return err
	}
	notify.PhaseDone(phaseProcess, nil)
	cityBuffered := filterBufferedByJurisdiction(allBuffered, filter.JurisdictionCity)

	hexStats, clippedHexes := c.runAllFeaturesPass(ctx, allBuffered, boundaryGJSON)
	if err := c.saveAllJurisdictionsResult(ctx, allBuffered, len(resFeatures), hexStats, clippedHexes); err != nil {
		return err
	}

	c.processCityResults(ctx, cityBuffered, len(jurisdictionParts[filter.JurisdictionCity]), clippedHexes)

	return c.saveHexStats(ctx, hexStats)
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

func loadBoundary(ctx context.Context, store db.Store, city *config.CityConfig) (string, [4]float64, *geo.UTMProjector, error) {
	boundaryGJSON, err := store.GetBoundary(ctx)
	if err != nil {
		return "", [4]float64{}, nil, fmt.Errorf("get boundary: %w", err)
	}
	if boundaryGJSON == "" {
		return "", [4]float64{}, nil, fmt.Errorf("no boundary stored for city %s — run 'pvmt ingest' first", city.Name)
	}
	bbox, err := geo.BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return "", [4]float64{}, nil, fmt.Errorf("derive bbox: %w", err)
	}
	lon, lat := geo.CenterFromBBox(bbox)
	proj := geo.NewUTMProjector(lon, lat)
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
	fmt.Fprintf(c.out, "Loading %s features from database...\n", t)
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
	fmt.Fprintf(c.out, "Processing %d features (UTM zone %d)...\n", len(dbFeatures), c.proj.Zone)
	resFeatures := make([]resource.Feature, len(dbFeatures))
	for i, f := range dbFeatures {
		resFeatures[i] = resource.Feature{
			ID:           f.ID,
			Name:         f.Name,
			Tags:         f.Tags,
			GeometryJSON: f.GeometryJSON,
			SourceAPI:    f.SourceAPI,
		}
	}
	return resFeatures, nil
}

func createSnapshot(ctx context.Context, errOut io.Writer, store db.Store, cfg *config.Config) *int64 {
	snapshot, err := store.CreateSnapshot(ctx, cfg.Hash())
	if err != nil {
		fmt.Fprintf(errOut, "Warning: failed to create snapshot: %v\n", err)
	}
	if snapshot != nil {
		return &snapshot.ID
	}
	return nil
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
// featureCount is the count of input features (including any dropped by the
// buffer step), preserved so DB rows match the pre-refactor semantics.
func (c *computer) saveAllJurisdictionsResult(ctx context.Context, allBuffered []resource.BufferedFeature, featureCount int, hexStats []geo.HexStat, clippedHexes []geo.Hex) error {
	// Per-hex coverage is dedup'd by the local union inside ComputeHexStats,
	// so summing does not double-count crossing-road overlaps.
	var area float64
	for _, s := range hexStats {
		area += s.Area
	}

	rtAll := c.opts.ResourceType.Type()
	result := db.ComputeResult{
		ResourceType: rtAll,
		TotalArea:    area,
		FeatureCount: featureCount,
		SnapshotID:   c.snapshotID,
	}
	if err := c.store.SaveComputeResult(ctx, result); err != nil {
		return fmt.Errorf("save result: %w", err)
	}
	c.recordRow("all", featureCount, area)

	cohortStats := c.buildCohortStats(ctx, rtAll, allBuffered, area, clippedHexes)
	if len(cohortStats) > 0 {
		if err := c.store.SaveCohortStats(ctx, cohortStats); err != nil {
			fmt.Fprintf(c.errOut, "Warning: failed to save cohort stats: %v\n", err)
		} else if c.opts.ResourceType.HasCohorts() {
			printCohortBreakdown(c.out, "Cohort breakdown", cohortStats, area, c.sys)
		}
	}
	if !c.opts.CityOnly {
		printResults(c.out, fmt.Sprintf("%s Results (all)", rtAll.Bare()), featureCount, area, c.sys)
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
			SnapshotID:   c.snapshotID,
		}
	}
	if err := c.store.SaveHexStats(ctx, dbStats); err != nil {
		fmt.Fprintf(c.errOut, "Warning: failed to save hex stats: %v\n", err)
	}
	return nil
}

// processCityResults runs the city-jurisdiction hex pipeline over the
// pre-buffered city subset and persists the :city ComputeResult, cohort
// stats, and prose summary. featureCount is the input city-feature count
// (including any later dropped by the buffer step).
func (c *computer) processCityResults(ctx context.Context, cityBuffered []resource.BufferedFeature, featureCount int, clippedHexes []geo.Hex) {
	if len(cityBuffered) == 0 {
		return
	}

	rtCity := c.opts.ResourceType.Type().With(resource.ScopeCity)
	cityIdx := geo.NewGeomIndexFromGeoms(resource.Geoms(cityBuffered))
	cityStats := geo.ComputeHexStats(ctx, clippedHexes, cityIdx, string(rtCity), nil)

	var cityArea float64
	cityDBStats := make([]db.HexStat, len(cityStats))
	for i, s := range cityStats {
		cityArea += s.Area
		cityDBStats[i] = db.HexStat{
			HexID:        s.HexID,
			ResourceType: rtCity,
			Area:         s.Area,
			PctCovered:   s.PctCovered,
			SnapshotID:   c.snapshotID,
		}
	}
	if err := c.store.SaveHexStats(ctx, cityDBStats); err != nil {
		fmt.Fprintf(c.errOut, "Warning: failed to save city hex stats: %v\n", err)
	}

	cityResult := db.ComputeResult{
		ResourceType: rtCity,
		TotalArea:    cityArea,
		FeatureCount: featureCount,
		SnapshotID:   c.snapshotID,
	}
	if err := c.store.SaveComputeResult(ctx, cityResult); err != nil {
		fmt.Fprintf(c.errOut, "Warning: failed to save city result: %v\n", err)
	}
	c.recordRow("city", featureCount, cityArea)

	cityCohortStats := c.buildCohortStats(ctx, rtCity, cityBuffered, cityArea, clippedHexes)
	if len(cityCohortStats) > 0 {
		if err := c.store.SaveCohortStats(ctx, cityCohortStats); err != nil {
			fmt.Fprintf(c.errOut, "Warning: failed to save city cohort stats: %v\n", err)
		} else if c.opts.ResourceType.HasCohorts() {
			printCohortBreakdown(c.out, "City cohort breakdown", cityCohortStats, cityArea, c.sys)
		}
	}

	printResults(c.out, fmt.Sprintf("%s Results (city only)", c.opts.ResourceType.Type()), featureCount, cityArea, c.sys)
}

// recordRow appends a computeRow to the multi-city accumulator if the
// caller set one up. No-op when --json is not active.
func (c *computer) recordRow(jurisdiction string, featureCount int, area float64) {
	if c.opts.rows == nil {
		return
	}
	row := computeRow{
		City:         c.city.Name,
		ResourceType: string(c.opts.ResourceType.Type()),
		Jurisdiction: jurisdiction,
		FeatureCount: featureCount,
		Area:         area,
	}
	if c.snapshotID != nil {
		row.SnapshotID = *c.snapshotID
	}
	*c.opts.rows = append(*c.opts.rows, row)
}

func printResults(out io.Writer, label string, featureCount int, area float64, sys units.System) {
	fmt.Fprintf(out, "\n%s:\n", label)
	fmt.Fprintf(out, "  Features:  %d\n", featureCount)
	fmt.Fprintf(out, "  Area:      %s\n", units.FormatArea(area, sys))
	fmt.Fprintf(out, "  Area:      %s\n", units.FormatAreaLarge(area, sys))
	fmt.Fprintf(out, "  Area:      %s\n", units.FormatAreaVeryLarge(area, sys))
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
	// --- Phase 1: Generate hex grid ---
	c.notify.PhaseStart(phaseHexGrid)

	hexEdge := c.cfg.ResolvedHexEdge(c.city)
	minX, minY, maxX, maxY := geo.ProjectedBBoxExtent(c.proj, c.bbox)

	fmt.Fprintf(c.out, "\nComputing hex grid (edge=%.0fm)...\n", hexEdge)
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)
	fmt.Fprintf(c.out, "  Generated %d hexes\n", len(hexes))
	c.notify.PhaseDone(phaseHexGrid, nil)

	// --- Phase 2: Clip hexes to boundary ---
	c.notify.PhaseStart(phaseClip)

	boundaryGeom, err := parseGeoJSONGeometry(boundaryGJSON, c.proj)
	if err == nil && !boundaryGeom.IsEmpty() {
		var clipCounter atomic.Int64
		stopClipProgress := startProgressTicker(ctx, c.notify, phaseClip, len(hexes), &clipCounter)
		hexes = geo.ClipHexesToBoundary(ctx, hexes, boundaryGeom, &clipCounter)
		stopClipProgress()
		fmt.Fprintf(c.out, "  Clipped to boundary: %d hexes\n", len(hexes))
	}
	c.notify.PhaseDone(phaseClip, nil)

	// --- Phase 3: Compute hex stats ---
	c.notify.PhaseStart(phaseStats)

	idx := geo.NewGeomIndexFromGeoms(buffered)

	var statsCounter atomic.Int64
	stopStatsProgress := startProgressTicker(ctx, c.notify, phaseStats, len(hexes), &statsCounter)
	geoStats := geo.ComputeHexStats(ctx, hexes, idx, string(c.opts.ResourceType.Type()), &statsCounter)
	stopStatsProgress()
	fmt.Fprintf(c.out, "  %d hexes with coverage\n", len(geoStats))
	c.notify.PhaseDone(phaseStats, nil)

	return geoStats, hexes
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
			SnapshotID:     c.snapshotID,
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
			SnapshotID:     c.snapshotID,
		})
	}
	return stats
}
