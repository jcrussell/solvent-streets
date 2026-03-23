package compute

import (
	"crypto/sha256"
	"fmt"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/filter"
	"pvmt/internal/forecast"
	"pvmt/internal/geo"
	"pvmt/internal/resource"
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
			return runCompute(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.CityOnly, "city-only", false, "Only show city-maintained road results")

	return cmd
}

func runCompute(opts *Options) error {
	ios := opts.IO

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

	fmt.Fprintf(ios.Out, "Loading %s features from database...\n", opts.ResourceType.Name())
	dbFeatures, err := store.ListFeatures(opts.ResourceType.Name())
	if err != nil {
		return fmt.Errorf("list features: %w", err)
	}

	if len(dbFeatures) == 0 {
		fmt.Fprintf(ios.ErrOut, "No %s features in database. Run 'pvmt %s ingest' first.\n", opts.ResourceType.Name(), opts.ResourceType.Name())
		return cmdutil.ErrNoResults
	}

	fmt.Fprintf(ios.Out, "Processing %d features (UTM zone %d)...\n", len(dbFeatures), proj.Zone)

	// Convert db.Feature to resource.Feature
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

	// Partition by jurisdiction and log summary
	parts := filter.Partition(resFeatures)
	fmt.Fprintf(ios.Out, "  Total: %d features (%d city, %d county, %d state, %d federal)\n",
		len(resFeatures),
		len(parts[filter.JurisdictionCity]),
		len(parts[filter.JurisdictionCounty]),
		len(parts[filter.JurisdictionState]),
		len(parts[filter.JurisdictionFederal]),
	)

	// Process all features
	gjson, areaSqFt, err := opts.ResourceType.ProcessFeatures(resFeatures, proj)
	if err != nil {
		return fmt.Errorf("process features: %w", err)
	}

	areaAcres := geo.AreaAcres(areaSqFt)

	// Create snapshot
	configHash := fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "%v", cfg)))
	snapshot, err := store.CreateSnapshot(configHash[:16])
	if err != nil {
		fmt.Fprintf(ios.ErrOut, "Warning: failed to create snapshot: %v\n", err)
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
			fmt.Fprintf(ios.ErrOut, "Warning: failed to save cohort stats: %v\n", err)
		} else if opts.ResourceType.HasCohorts() {
			fmt.Fprintf(ios.Out, "\nCohort breakdown:\n")
			for _, cs := range cohortStats {
				pct := 0.0
				if areaSqFt > 0 {
					pct = cs.AreaSqFt / areaSqFt * 100
				}
				fmt.Fprintf(ios.Out, "  %-12s %6.1f%% (%.0f sq ft, %d features)\n",
					cs.Classification, pct, cs.AreaSqFt, cs.FeatureCount)
			}
		}
	}

	if !opts.CityOnly {
		fmt.Fprintf(ios.Out, "\n%s Results (all):\n", opts.ResourceType.Name())
		fmt.Fprintf(ios.Out, "  Features:  %d\n", len(dbFeatures))
		fmt.Fprintf(ios.Out, "  Area:      %.0f sq ft\n", areaSqFt)
		fmt.Fprintf(ios.Out, "  Area:      %.1f acres\n", areaAcres)
		fmt.Fprintf(ios.Out, "  Area:      %.2f sq mi\n", areaAcres/640)
	}

	// Process city-only features
	cityFeatures := parts[filter.JurisdictionCity]
	if len(cityFeatures) > 0 {
		cityGjson, cityAreaSqFt, err := opts.ResourceType.ProcessFeatures(cityFeatures, proj)
		if err != nil {
			fmt.Fprintf(ios.ErrOut, "Warning: failed to process city features: %v\n", err)
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
				fmt.Fprintf(ios.ErrOut, "Warning: failed to save city result: %v\n", err)
			}

			// Save city cohort stats
			cityRT := &cityResourceType{ResourceType: opts.ResourceType}
			cityCohortStats := buildCohortStats(cityRT, cityFeatures, cityAreaSqFt, snapshotID, proj)
			if len(cityCohortStats) > 0 {
				if err := store.SaveCohortStats(cityCohortStats); err != nil {
					fmt.Fprintf(ios.ErrOut, "Warning: failed to save city cohort stats: %v\n", err)
				} else if opts.ResourceType.HasCohorts() {
					fmt.Fprintf(ios.Out, "\nCity cohort breakdown:\n")
					for _, cs := range cityCohortStats {
						pct := 0.0
						if cityAreaSqFt > 0 {
							pct = cs.AreaSqFt / cityAreaSqFt * 100
						}
						fmt.Fprintf(ios.Out, "  %-12s %6.1f%% (%.0f sq ft, %d features)\n",
							cs.Classification, pct, cs.AreaSqFt, cs.FeatureCount)
					}
				}
			}

			fmt.Fprintf(ios.Out, "\n%s Results (city only):\n", opts.ResourceType.Name())
			fmt.Fprintf(ios.Out, "  Features:  %d\n", len(cityFeatures))
			fmt.Fprintf(ios.Out, "  Area:      %.0f sq ft\n", cityAreaSqFt)
			fmt.Fprintf(ios.Out, "  Area:      %.1f acres\n", cityAreaAcres)
			fmt.Fprintf(ios.Out, "  Area:      %.2f sq mi\n", cityAreaAcres/640)
		}
	}

	// Compute hex grid stats (using all features)
	hexEdge := cfg.ResolvedHexEdge(city)
	// Project bbox corners to UTM
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0]) // west, south
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2]) // east, north

	fmt.Fprintf(ios.Out, "\nComputing hex grid (edge=%.0fm)...\n", hexEdge)
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)
	fmt.Fprintf(ios.Out, "  Generated %d hexes\n", len(hexes))

	// Clip hex grid to city boundary
	boundaryGeom, err := parseGeoJSONGeometry(boundaryGJSON, proj)
	if err == nil && !boundaryGeom.IsEmpty() {
		filtered := make([]geo.Hex, 0, len(hexes))
		for _, h := range hexes {
			inter, err := geom.Intersection(h.Geom, boundaryGeom)
			if err == nil && !inter.IsEmpty() {
				h.Geom = inter
				filtered = append(filtered, h)
			}
		}
		fmt.Fprintf(ios.Out, "  Clipped to boundary: %d hexes\n", len(filtered))
		hexes = filtered
	}

	// Re-parse the union geometry for intersection
	unionGeom, err := parseGeoJSONGeometry(gjson, proj)
	if err != nil {
		return fmt.Errorf("parse union geometry for hex grid: %w", err)
	}

	geoStats := geo.ComputeHexStats(hexes, unionGeom, opts.ResourceType.Name(), proj)
	fmt.Fprintf(ios.Out, "  %d hexes with coverage\n", len(geoStats))

	// Convert to db.HexStat and save
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
		fmt.Fprintf(ios.ErrOut, "Warning: failed to save hex stats: %v\n", err)
	}

	return nil
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
