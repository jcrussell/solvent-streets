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

	// Load boundary from DB and derive bbox/center
	boundaryGJSON, err := store.GetBoundary(ctx)
	if err != nil {
		return fmt.Errorf("get boundary: %w", err)
	}
	if boundaryGJSON == "" {
		return fmt.Errorf("no boundary stored for city %s — run 'pvmt ingest' first", city.Name)
	}
	bbox, err := geo.BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return fmt.Errorf("derive bbox: %w", err)
	}
	lon, lat := geo.CenterFromBBox(bbox)
	proj := geo.NewUTMProjector(lon, lat)

	// --- Phase 0: Process features ---
	notify.PhaseStart(phaseProcess)

	fmt.Fprintf(out, "Loading %s features from database...\n", opts.ResourceType.Name())
	dbFeatures, err := store.ListFeatures(ctx, opts.ResourceType.Name())
	if err != nil {
		notify.PhaseDone(phaseProcess, err)
		return fmt.Errorf("list features: %w", err)
	}
	if len(dbFeatures) == 0 {
		fmt.Fprintf(errOut, "No %s features in database. Run 'pvmt %s ingest' first.\n", opts.ResourceType.Name(), opts.ResourceType.Name())
		notify.PhaseDone(phaseProcess, errors.New("no features"))
		return cmdutil.ErrNoResults
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

	parts := filter.Partition(resFeatures)
	fmt.Fprintf(out, "  Total: %d features (%d city, %d county, %d state, %d federal)\n",
		len(resFeatures),
		len(parts[filter.JurisdictionCity]),
		len(parts[filter.JurisdictionCounty]),
		len(parts[filter.JurisdictionState]),
		len(parts[filter.JurisdictionFederal]),
	)

	gjson, areaSqM, err := opts.ResourceType.ProcessFeatures(resFeatures, proj)
	if err != nil {
		notify.PhaseDone(phaseProcess, err)
		return fmt.Errorf("process features: %w", err)
	}
	notify.PhaseDone(phaseProcess, nil)

	sys := opts.UnitSystem()

	// Create snapshot
	configHash := fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "%v", cfg)))
	snapshot, err := store.CreateSnapshot(ctx, configHash[:16])
	if err != nil {
		fmt.Fprintf(errOut, "Warning: failed to create snapshot: %v\n", err)
	}

	var snapshotID *int64
	if snapshot != nil {
		snapshotID = &snapshot.ID
	}

	result := db.ComputeResult{
		ResourceType: opts.ResourceType.Name(),
		TotalAreaSqM: areaSqM,
		FeatureCount: len(dbFeatures),
		GeometryJSON: gjson,
		SnapshotID:   snapshotID,
	}
	if err := store.SaveComputeResult(ctx, result); err != nil {
		return fmt.Errorf("save result: %w", err)
	}

	// Save cohort stats
	cohortStats := buildCohortStats(opts.ResourceType, resFeatures, areaSqM, snapshotID, proj)
	if len(cohortStats) > 0 {
		if err := store.SaveCohortStats(ctx, cohortStats); err != nil {
			fmt.Fprintf(errOut, "Warning: failed to save cohort stats: %v\n", err)
		} else if opts.ResourceType.HasCohorts() {
			printCohortBreakdown(out, "Cohort breakdown", cohortStats, areaSqM, sys)
		}
	}

	if !opts.CityOnly {
		printResults(out, opts.ResourceType.Name()+" Results (all)", len(dbFeatures), areaSqM, sys)
	}

	processCityResults(ctx, out, errOut, opts, store, proj, parts, snapshotID, sys)

	return computeHexPipeline(ctx, out, errOut, notify, opts, cfg, city, store, bbox, proj, gjson, boundaryGJSON)
}

func processCityResults(ctx context.Context, out, errOut io.Writer, opts *Options,
	store db.Store, proj geo.Projector, parts map[filter.Jurisdiction][]resource.Feature,
	snapshotID *int64, sys units.System) {
	cityFeatures := parts[filter.JurisdictionCity]
	if len(cityFeatures) == 0 {
		return
	}

	cityGjson, cityAreaSqM, err := opts.ResourceType.ProcessFeatures(cityFeatures, proj)
	if err != nil {
		fmt.Fprintf(errOut, "Warning: failed to process city features: %v\n", err)
		return
	}

	cityResult := db.ComputeResult{
		ResourceType: opts.ResourceType.Name() + ":city",
		TotalAreaSqM: cityAreaSqM,
		FeatureCount: len(cityFeatures),
		GeometryJSON: cityGjson,
		SnapshotID:   snapshotID,
	}
	if err := store.SaveComputeResult(ctx, cityResult); err != nil {
		fmt.Fprintf(errOut, "Warning: failed to save city result: %v\n", err)
	}

	cityRT := &cityResourceType{ResourceType: opts.ResourceType}
	cityCohortStats := buildCohortStats(cityRT, cityFeatures, cityAreaSqM, snapshotID, proj)
	if len(cityCohortStats) > 0 {
		if err := store.SaveCohortStats(ctx, cityCohortStats); err != nil {
			fmt.Fprintf(errOut, "Warning: failed to save city cohort stats: %v\n", err)
		} else if opts.ResourceType.HasCohorts() {
			printCohortBreakdown(out, "City cohort breakdown", cityCohortStats, cityAreaSqM, sys)
		}
	}

	printResults(out, opts.ResourceType.Name()+" Results (city only)", len(cityFeatures), cityAreaSqM, sys)
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

func computeHexPipeline(ctx context.Context, out, errOut io.Writer, notify tui.PhaseNotifier,
	opts *Options, cfg *config.Config, city *config.CityConfig, store db.Store,
	bbox [4]float64, proj *geo.UTMProjector, gjson, boundaryGJSON string) error {
	// --- Phase 1: Generate hex grid ---
	notify.PhaseStart(phaseHexGrid)

	hexEdge := cfg.ResolvedHexEdge(city)
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0])
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2])

	fmt.Fprintf(out, "\nComputing hex grid (edge=%.0fm)...\n", hexEdge)
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)
	fmt.Fprintf(out, "  Generated %d hexes\n", len(hexes))
	notify.PhaseDone(phaseHexGrid, nil)

	// --- Phase 2: Clip hexes to boundary ---
	notify.PhaseStart(phaseClip)

	boundaryGeom, err := parseGeoJSONGeometry(boundaryGJSON, proj)
	if err == nil && !boundaryGeom.IsEmpty() {
		var clipCounter atomic.Int64
		stopClipProgress := startProgressTicker(notify, phaseClip, len(hexes), &clipCounter)
		hexes = geo.ClipHexesToBoundary(hexes, boundaryGeom, &clipCounter)
		stopClipProgress()
		fmt.Fprintf(out, "  Clipped to boundary: %d hexes\n", len(hexes))
	}
	notify.PhaseDone(phaseClip, nil)

	// --- Phase 3: Compute hex stats ---
	notify.PhaseStart(phaseStats)

	unionGeom, err := parseGeoJSONGeometry(gjson, proj)
	if err != nil {
		notify.PhaseDone(phaseStats, err)
		return fmt.Errorf("parse union geometry for hex grid: %w", err)
	}

	var statsCounter atomic.Int64
	stopStatsProgress := startProgressTicker(notify, phaseStats, len(hexes), &statsCounter)
	geoStats := geo.ComputeHexStats(hexes, unionGeom, opts.ResourceType.Name(), &statsCounter)
	stopStatsProgress()
	fmt.Fprintf(out, "  %d hexes with coverage\n", len(geoStats))
	notify.PhaseDone(phaseStats, nil)

	// --- Phase 4: Save results ---
	notify.PhaseStart(phaseSave)

	dbStats := make([]db.HexStat, len(geoStats))
	for i, s := range geoStats {
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
	notify.PhaseDone(phaseSave, nil)

	return nil
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
// HasCohorts()=true (e.g. roads), it computes per-classification areas.
// Otherwise it creates a single cohort stat.
func buildCohortStats(rt resource.ResourceType, features []resource.Feature, totalAreaSqM float64, snapshotID *int64, proj geo.Projector) []db.CohortStat {
	if !rt.HasCohorts() {
		return []db.CohortStat{{
			ResourceType:   rt.Name(),
			Classification: rt.Name(),
			AreaSqM:        totalAreaSqM,
			FeatureCount:   len(features),
			SnapshotID:     snapshotID,
		}}
	}

	rawAreas := resource.ComputeRoadCohortAreas(features, proj)
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
