package compute

import (
	"crypto/sha256"
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
	ResourceType resource.ResourceType
	CityOnly     bool
}

func NewCmdCompute(f *cmdutil.Factory, rt resource.ResourceType, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:           f.IOStreams,
		CityDB:       f.CityDB,
		Config:       f.Config,
		CurrentCity:  f.CurrentCity,
		ResourceType: rt,
	}

	cmd := &cobra.Command{
		Use:   "compute",
		Short: fmt.Sprintf("Compute %s area statistics", rt.Name()),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}
			return runComputeAllCities(f, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.CityOnly, "city-only", false, "Only show city-maintained road results")

	return cmd
}

func runComputeAllCities(f *cmdutil.Factory, opts *Options) error {
	return cmdutil.ForEachCity(f, func(cf *cmdutil.Factory, _ *config.CityConfig) error {
		cityOpts := *opts
		cityOpts.CurrentCity = cf.CurrentCity
		cityOpts.CityDB = cf.CityDB
		return runCompute(&cityOpts)
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

func runCompute(opts *Options) error {
	if opts.IO.IsTTY() {
		return runComputeTUI(opts)
	}
	return doCompute(opts.IO.Out, opts.IO.ErrOut, tui.NoopNotifier{}, opts)
}

func runComputeTUI(opts *Options) error {
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
		return doCompute(out, errOut, notify, opts)
	})
}

func doCompute(out, errOut io.Writer, notify tui.PhaseNotifier, opts *Options) error {
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
	boundaryGJSON, err := store.GetBoundary()
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
	dbFeatures, err := store.ListFeatures(opts.ResourceType.Name())
	if err != nil {
		notify.PhaseDone(phaseProcess, err)
		return fmt.Errorf("list features: %w", err)
	}
	if len(dbFeatures) == 0 {
		fmt.Fprintf(errOut, "No %s features in database. Run 'pvmt %s ingest' first.\n", opts.ResourceType.Name(), opts.ResourceType.Name())
		notify.PhaseDone(phaseProcess, fmt.Errorf("no features"))
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

	gjson, areaSqFt, err := opts.ResourceType.ProcessFeatures(resFeatures, proj)
	if err != nil {
		notify.PhaseDone(phaseProcess, err)
		return fmt.Errorf("process features: %w", err)
	}
	notify.PhaseDone(phaseProcess, nil)

	areaAcres := geo.AreaAcres(areaSqFt)

	// Create snapshot
	configHash := fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "%v", cfg)))
	snapshot, err := store.CreateSnapshot(configHash[:16])
	if err != nil {
		fmt.Fprintf(errOut, "Warning: failed to create snapshot: %v\n", err)
	}

	var snapshotID *int64
	if snapshot != nil {
		snapshotID = &snapshot.ID
	}

	result := db.ComputeResult{
		ResourceType:   opts.ResourceType.Name(),
		TotalAreaSqFt:  areaSqFt,
		TotalAreaAcres: areaAcres,
		FeatureCount:   len(dbFeatures),
		GeometryJSON:   gjson,
		SnapshotID:     snapshotID,
	}
	if err := store.SaveComputeResult(result); err != nil {
		return fmt.Errorf("save result: %w", err)
	}

	// Save cohort stats
	cohortStats := buildCohortStats(opts.ResourceType, resFeatures, areaSqFt, snapshotID, proj)
	if len(cohortStats) > 0 {
		if err := store.SaveCohortStats(cohortStats); err != nil {
			fmt.Fprintf(errOut, "Warning: failed to save cohort stats: %v\n", err)
		} else if opts.ResourceType.HasCohorts() {
			fmt.Fprintf(out, "\nCohort breakdown:\n")
			for _, cs := range cohortStats {
				pct := 0.0
				if areaSqFt > 0 {
					pct = cs.AreaSqFt / areaSqFt * 100
				}
				fmt.Fprintf(out, "  %-12s %6.1f%% (%.0f sq ft, %d features)\n",
					cs.Classification, pct, cs.AreaSqFt, cs.FeatureCount)
			}
		}
	}

	if !opts.CityOnly {
		fmt.Fprintf(out, "\n%s Results (all):\n", opts.ResourceType.Name())
		fmt.Fprintf(out, "  Features:  %d\n", len(dbFeatures))
		fmt.Fprintf(out, "  Area:      %.0f sq ft\n", areaSqFt)
		fmt.Fprintf(out, "  Area:      %.1f acres\n", areaAcres)
		fmt.Fprintf(out, "  Area:      %.2f sq mi\n", areaAcres/640)
	}

	// Process city-only features
	cityFeatures := parts[filter.JurisdictionCity]
	if len(cityFeatures) > 0 {
		cityGjson, cityAreaSqFt, err := opts.ResourceType.ProcessFeatures(cityFeatures, proj)
		if err != nil {
			fmt.Fprintf(errOut, "Warning: failed to process city features: %v\n", err)
		} else {
			cityAreaAcres := geo.AreaAcres(cityAreaSqFt)
			cityResult := db.ComputeResult{
				ResourceType:   opts.ResourceType.Name() + ":city",
				TotalAreaSqFt:  cityAreaSqFt,
				TotalAreaAcres: cityAreaAcres,
				FeatureCount:   len(cityFeatures),
				GeometryJSON:   cityGjson,
				SnapshotID:     snapshotID,
			}
			if err := store.SaveComputeResult(cityResult); err != nil {
				fmt.Fprintf(errOut, "Warning: failed to save city result: %v\n", err)
			}

			cityRT := &cityResourceType{ResourceType: opts.ResourceType}
			cityCohortStats := buildCohortStats(cityRT, cityFeatures, cityAreaSqFt, snapshotID, proj)
			if len(cityCohortStats) > 0 {
				if err := store.SaveCohortStats(cityCohortStats); err != nil {
					fmt.Fprintf(errOut, "Warning: failed to save city cohort stats: %v\n", err)
				} else if opts.ResourceType.HasCohorts() {
					fmt.Fprintf(out, "\nCity cohort breakdown:\n")
					for _, cs := range cityCohortStats {
						pct := 0.0
						if cityAreaSqFt > 0 {
							pct = cs.AreaSqFt / cityAreaSqFt * 100
						}
						fmt.Fprintf(out, "  %-12s %6.1f%% (%.0f sq ft, %d features)\n",
							cs.Classification, pct, cs.AreaSqFt, cs.FeatureCount)
					}
				}
			}

			fmt.Fprintf(out, "\n%s Results (city only):\n", opts.ResourceType.Name())
			fmt.Fprintf(out, "  Features:  %d\n", len(cityFeatures))
			fmt.Fprintf(out, "  Area:      %.0f sq ft\n", cityAreaSqFt)
			fmt.Fprintf(out, "  Area:      %.1f acres\n", cityAreaAcres)
			fmt.Fprintf(out, "  Area:      %.2f sq mi\n", cityAreaAcres/640)
		}
	}

	// --- Phase 1: Generate hex grid ---
	notify.PhaseStart(phaseHexGrid)

	hexEdge := cfg.ResolvedHexEdge(city)
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0]) // west, south
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2]) // east, north

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
	geoStats := geo.ComputeHexStats(hexes, unionGeom, opts.ResourceType.Name(), proj, &statsCounter)
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
			AreaSqFt:     s.AreaSqFt,
			PctCovered:   s.PctCovered,
		}
	}

	if err := store.SaveHexStats(dbStats); err != nil {
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
func buildCohortStats(rt resource.ResourceType, features []resource.Feature, totalAreaSqFt float64, snapshotID *int64, proj geo.Projector) []db.CohortStat {
	if !rt.HasCohorts() {
		return []db.CohortStat{{
			ResourceType:   rt.Name(),
			Classification: rt.Name(),
			AreaSqFt:       totalAreaSqFt,
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
			AreaSqFt:       totalAreaSqFt * proportion,
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
