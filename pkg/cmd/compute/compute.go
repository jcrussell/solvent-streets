package compute

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/filter"
	"pvmt/internal/forecast"
	"pvmt/internal/geo"
	"pvmt/internal/resource"
	"pvmt/internal/tui"
	"pvmt/internal/units"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/peterstace/simplefeatures/geom"
	"github.com/spf13/cobra"
)

type Options struct {
	IO           *iostreams.IOStreams
	CityDB       func() (db.Store, error)
	Config       func() (*config.Config, error)
	CurrentCity  func() (*config.CityConfig, error)
	UnitSystem   func() units.System
	ResourceType resource.ResourceType
	CityOnly     bool
}

func NewCmdCompute(f *cmdutil.Factory, rt resource.ResourceType, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:           f.IOStreams,
		CityDB:       f.CityDB,
		Config:       f.Config,
		CurrentCity:  f.CurrentCity,
		UnitSystem:   f.UnitSystem,
		ResourceType: rt,
	}

	cmd := &cobra.Command{
		Use:   "compute",
		Short: fmt.Sprintf("Compute %s area statistics", rt.Name()),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}
			return runComputeAllCities(cmd.Context(), f, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.CityOnly, "city-only", false, "Only show city-maintained road results")

	return cmd
}

func runComputeAllCities(ctx context.Context, f *cmdutil.Factory, opts *Options) error {
	return cmdutil.ForEachCity(ctx, f, func(cf *cmdutil.Factory, _ *config.CityConfig) error {
		cityOpts := *opts
		cityOpts.CurrentCity = cf.CurrentCity
		cityOpts.CityDB = cf.CityDB
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
	label := fmt.Sprintf("Computing %s · %s", opts.ResourceType.Name(), city.Name)
	steps := []tui.Step{
		{Name: "Processing features"},
		{Name: "Generating hex grid"},
		{Name: "Clipping hexes to boundary"},
		{Name: "Computing hex stats"},
		{Name: "Saving results"},
	}
	done := tui.DoneConfig{
		SuccessMsg: fmt.Sprintf("%s compute complete for %s", opts.ResourceType.Name(), city.Name),
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

	resFeatures, err := loadResourceFeatures(ctx, out, errOut, notify, opts, store, proj)
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

	// runAllFeaturesPass keeps the buffered slice in its own scope so it's
	// collected before processCityResults buffers the city subset.
	hexStats, clippedHexes, err := c.runAllFeaturesPass(ctx, resFeatures, boundaryGJSON)
	if err != nil {
		return err
	}

	c.processCityResults(ctx, jurisdictionParts, clippedHexes)

	return saveHexStats(ctx, errOut, notify, store, hexStats)
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
	return boundaryGJSON, bbox, geo.NewUTMProjector(lon, lat), nil
}

func loadResourceFeatures(ctx context.Context, out, errOut io.Writer, notify tui.PhaseNotifier, opts *Options, store db.Store, proj *geo.UTMProjector) ([]resource.Feature, error) {
	notify.PhaseStart(phaseProcess)
	fmt.Fprintf(out, "Loading %s features from database...\n", opts.ResourceType.Name())
	dbFeatures, err := store.ListFeatures(ctx, opts.ResourceType.Name())
	if err != nil {
		notify.PhaseDone(phaseProcess, err)
		return nil, fmt.Errorf("list features: %w", err)
	}
	if len(dbFeatures) == 0 {
		fmt.Fprintf(errOut, "No %s features in database. Run 'pvmt %s ingest' first.\n", opts.ResourceType.Name(), opts.ResourceType.Name())
		notify.PhaseDone(phaseProcess, errors.New("no features"))
		return nil, cmdutil.ErrNoResults
	}
	fmt.Fprintf(out, "Processing %d features (UTM zone %d)...\n", len(dbFeatures), proj.Zone)
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
	configHash := fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "%v", cfg)))
	snapshot, err := store.CreateSnapshot(ctx, configHash[:16])
	if err != nil {
		fmt.Fprintf(errOut, "Warning: failed to create snapshot: %v\n", err)
	}
	if snapshot != nil {
		return &snapshot.ID
	}
	return nil
}

// runAllFeaturesPass buffers every feature (all jurisdictions), runs the hex
// pipeline, and persists the full-resource ComputeResult + cohort stats. The
// buffered slice is scoped to this method so it can be GC'd before the
// city pass allocates its own.
func (c *computer) runAllFeaturesPass(ctx context.Context, resFeatures []resource.Feature, boundaryGJSON string) ([]geo.HexStat, []geo.Hex, error) {
	buffered, err := c.opts.ResourceType.BufferFeatures(resFeatures, c.proj)
	if err != nil {
		c.notify.PhaseDone(phaseProcess, err)
		return nil, nil, fmt.Errorf("buffer features: %w", err)
	}
	c.notify.PhaseDone(phaseProcess, nil)

	hexStats, clippedHexes := c.computeHexPipeline(buffered, boundaryGJSON)

	// Per-hex coverage is dedup'd by the local union inside ComputeHexStats,
	// so summing does not double-count crossing-road overlaps.
	var areaSqM float64
	for _, s := range hexStats {
		areaSqM += s.AreaSqM
	}

	result := db.ComputeResult{
		ResourceType: c.opts.ResourceType.Name(),
		TotalAreaSqM: areaSqM,
		FeatureCount: len(resFeatures),
		SnapshotID:   c.snapshotID,
	}
	if err := c.store.SaveComputeResult(ctx, result); err != nil {
		return nil, nil, fmt.Errorf("save result: %w", err)
	}

	cohortStats := buildCohortStats(c.opts.ResourceType, resFeatures, areaSqM, c.snapshotID, c.proj, clippedHexes)
	if len(cohortStats) > 0 {
		if err := c.store.SaveCohortStats(ctx, cohortStats); err != nil {
			fmt.Fprintf(c.errOut, "Warning: failed to save cohort stats: %v\n", err)
		} else if c.opts.ResourceType.HasCohorts() {
			printCohortBreakdown(c.out, "Cohort breakdown", cohortStats, areaSqM, c.sys)
		}
	}
	if !c.opts.CityOnly {
		printResults(c.out, c.opts.ResourceType.Name()+" Results (all)", len(resFeatures), areaSqM, c.sys)
	}
	return hexStats, clippedHexes, nil
}

func saveHexStats(ctx context.Context, errOut io.Writer, notify tui.PhaseNotifier, store db.Store, hexStats []geo.HexStat) error {
	notify.PhaseStart(phaseSave)
	defer notify.PhaseDone(phaseSave, nil)
	dbStats := make([]db.HexStat, len(hexStats))
	for i, s := range hexStats {
		dbStats[i] = db.HexStat{
			HexID:        s.HexID,
			ResourceType: s.ResourceType,
			AreaSqM:      s.AreaSqM,
			PctCovered:   s.PctCovered,
		}
	}
	if err := store.SaveHexStats(ctx, dbStats); err != nil {
		fmt.Fprintf(errOut, "Warning: failed to save hex stats: %v\n", err)
	}
	return nil
}

func (c *computer) processCityResults(ctx context.Context, parts map[filter.Jurisdiction][]resource.Feature, clippedHexes []geo.Hex) {
	cityFeatures := parts[filter.JurisdictionCity]
	if len(cityFeatures) == 0 {
		return
	}

	cityBuffered, err := c.opts.ResourceType.BufferFeatures(cityFeatures, c.proj)
	if err != nil {
		fmt.Fprintf(c.errOut, "Warning: failed to buffer city features: %v\n", err)
		return
	}

	cityIdx := geo.NewGeomIndexFromGeoms(cityBuffered)
	cityStats := geo.ComputeHexStats(clippedHexes, cityIdx, c.opts.ResourceType.Name()+":city", nil)

	var cityAreaSqM float64
	for _, s := range cityStats {
		cityAreaSqM += s.AreaSqM
	}

	cityResult := db.ComputeResult{
		ResourceType: c.opts.ResourceType.Name() + ":city",
		TotalAreaSqM: cityAreaSqM,
		FeatureCount: len(cityFeatures),
		SnapshotID:   c.snapshotID,
	}
	if err := c.store.SaveComputeResult(ctx, cityResult); err != nil {
		fmt.Fprintf(c.errOut, "Warning: failed to save city result: %v\n", err)
	}

	cityRT := &cityResourceType{ResourceType: c.opts.ResourceType}
	cityCohortStats := buildCohortStats(cityRT, cityFeatures, cityAreaSqM, c.snapshotID, c.proj, clippedHexes)
	if len(cityCohortStats) > 0 {
		if err := c.store.SaveCohortStats(ctx, cityCohortStats); err != nil {
			fmt.Fprintf(c.errOut, "Warning: failed to save city cohort stats: %v\n", err)
		} else if c.opts.ResourceType.HasCohorts() {
			printCohortBreakdown(c.out, "City cohort breakdown", cityCohortStats, cityAreaSqM, c.sys)
		}
	}

	printResults(c.out, c.opts.ResourceType.Name()+" Results (city only)", len(cityFeatures), cityAreaSqM, c.sys)
}

func printResults(out io.Writer, label string, featureCount int, areaSqM float64, sys units.System) {
	fmt.Fprintf(out, "\n%s:\n", label)
	fmt.Fprintf(out, "  Features:  %d\n", featureCount)
	fmt.Fprintf(out, "  Area:      %s\n", units.FormatArea(areaSqM, sys))
	fmt.Fprintf(out, "  Area:      %s\n", units.FormatAreaLarge(areaSqM, sys))
	fmt.Fprintf(out, "  Area:      %s\n", units.FormatAreaVeryLarge(areaSqM, sys))
}

func printCohortBreakdown(out io.Writer, title string, stats []db.CohortStat, totalAreaSqM float64, sys units.System) {
	fmt.Fprintf(out, "\n%s:\n", title)
	for _, cs := range stats {
		pct := 0.0
		if totalAreaSqM > 0 {
			pct = cs.AreaSqM / totalAreaSqM * 100
		}
		fmt.Fprintf(out, "  %-12s %6.1f%% (%s, %d features)\n",
			cs.Classification, pct, units.FormatArea(cs.AreaSqM, sys), cs.FeatureCount)
	}
}

// computeHexPipeline runs the hex-grid stages (generate, clip, stats) and
// returns the per-hex stats plus the clipped hex slice. The caller owns
// persistence and can reuse clippedHexes for a second pass (e.g. city-only).
func (c *computer) computeHexPipeline(buffered []geom.Geometry, boundaryGJSON string) ([]geo.HexStat, []geo.Hex) {
	// --- Phase 1: Generate hex grid ---
	c.notify.PhaseStart(phaseHexGrid)

	hexEdge := c.cfg.ResolvedHexEdge(c.city)
	minX, minY, _ := c.proj.ToProjected(c.bbox[1], c.bbox[0])
	maxX, maxY, _ := c.proj.ToProjected(c.bbox[3], c.bbox[2])

	fmt.Fprintf(c.out, "\nComputing hex grid (edge=%.0fm)...\n", hexEdge)
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)
	fmt.Fprintf(c.out, "  Generated %d hexes\n", len(hexes))
	c.notify.PhaseDone(phaseHexGrid, nil)

	// --- Phase 2: Clip hexes to boundary ---
	c.notify.PhaseStart(phaseClip)

	boundaryGeom, err := parseGeoJSONGeometry(boundaryGJSON, c.proj)
	if err == nil && !boundaryGeom.IsEmpty() {
		var clipCounter atomic.Int64
		stopClipProgress := startProgressTicker(c.notify, phaseClip, len(hexes), &clipCounter)
		hexes = geo.ClipHexesToBoundary(hexes, boundaryGeom, &clipCounter)
		stopClipProgress()
		fmt.Fprintf(c.out, "  Clipped to boundary: %d hexes\n", len(hexes))
	}
	c.notify.PhaseDone(phaseClip, nil)

	// --- Phase 3: Compute hex stats ---
	c.notify.PhaseStart(phaseStats)

	idx := geo.NewGeomIndexFromGeoms(buffered)

	var statsCounter atomic.Int64
	stopStatsProgress := startProgressTicker(c.notify, phaseStats, len(hexes), &statsCounter)
	geoStats := geo.ComputeHexStats(hexes, idx, c.opts.ResourceType.Name(), &statsCounter)
	stopStatsProgress()
	fmt.Fprintf(c.out, "  %d hexes with coverage\n", len(geoStats))
	c.notify.PhaseDone(phaseStats, nil)

	return geoStats, hexes
}

// startProgressTicker launches a goroutine that sends PhaseProgress updates
// every 200ms based on counter / total. Returns a stop function that must be
// called when the phase is done.
func startProgressTicker(notify tui.PhaseNotifier, phase, total int, counter *atomic.Int64) func() {
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
			}
		}
	}()
	return func() {
		close(done)
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

// buildCohortStats builds cohort stats for a resource type. For types with
// HasCohorts()=true (e.g. roads), it computes per-classification areas via
// the same hex-clipped pipeline used for the resource total.
// Otherwise it creates a single cohort stat.
func buildCohortStats(rt resource.ResourceType, features []resource.Feature, totalAreaSqM float64, snapshotID *int64, proj geo.Projector, hexes []geo.Hex) []db.CohortStat {
	if !rt.HasCohorts() {
		return []db.CohortStat{{
			ResourceType:   rt.Name(),
			Classification: rt.Name(),
			AreaSqM:        totalAreaSqM,
			FeatureCount:   len(features),
			SnapshotID:     snapshotID,
		}}
	}

	rawAreas := resource.ComputeRoadCohortAreas(features, proj, hexes)
	var rawTotal float64
	for _, a := range rawAreas {
		rawTotal += a
	}
	counts := make(map[string]int)
	for _, f := range features {
		class := forecast.NormalizeClass(f.Tags["highway"])
		counts[class]++
	}
	var stats []db.CohortStat
	for class, rawArea := range rawAreas {
		proportion := 0.0
		if rawTotal > 0 {
			proportion = rawArea / rawTotal
		}
		stats = append(stats, db.CohortStat{
			ResourceType:   rt.Name(),
			Classification: class,
			AreaSqM:        totalAreaSqM * proportion,
			FeatureCount:   counts[class],
			SnapshotID:     snapshotID,
		})
	}
	return stats
}

// cityResourceType wraps a ResourceType to produce ":city" suffixed names.
type cityResourceType struct {
	resource.ResourceType
}

func (c *cityResourceType) Name() string {
	return c.ResourceType.Name() + ":city"
}
