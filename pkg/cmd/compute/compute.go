package compute

import (
	"crypto/sha256"
	"fmt"

	"pvmt/internal/db"
	"pvmt/internal/filter"
	"pvmt/internal/geo"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"

	"github.com/peterstace/simplefeatures/geom"
	"github.com/spf13/cobra"
)

type Options struct {
	Factory      *cmdutil.Factory
	ResourceType resource.ResourceType
	CityOnly     bool
}

func NewCmdCompute(f *cmdutil.Factory, rt resource.ResourceType, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		Factory:      f,
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
	ios := opts.Factory.IOStreams

	cfg, err := opts.Factory.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	store, err := opts.Factory.DB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	// Create projector from config bbox center
	lon, lat := cfg.Center()
	proj := geo.NewUTMProjector(lon, lat)

	fmt.Fprintf(ios.Out, "Loading %s features from database...\n", opts.ResourceType.Name())
	dbFeatures, err := store.ListFeatures(opts.ResourceType.Name())
	if err != nil {
		return fmt.Errorf("list features: %w", err)
	}

	if len(dbFeatures) == 0 {
		return fmt.Errorf("no %s features in database. Run 'pvmt %s ingest' first", opts.ResourceType.Name(), cmdName(opts.ResourceType))
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

			fmt.Fprintf(ios.Out, "\n%s Results (city only):\n", opts.ResourceType.Name())
			fmt.Fprintf(ios.Out, "  Features:  %d\n", len(cityFeatures))
			fmt.Fprintf(ios.Out, "  Area:      %.0f sq ft\n", cityAreaSqFt)
			fmt.Fprintf(ios.Out, "  Area:      %.1f acres\n", cityAreaAcres)
			fmt.Fprintf(ios.Out, "  Area:      %.2f sq mi\n", cityAreaAcres/640)
		}
	}

	// Compute hex grid stats (using all features)
	hexEdge := cfg.HexEdge()
	bbox := cfg.Area.BBox
	// Project bbox corners to UTM
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0]) // west, south
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2]) // east, north

	fmt.Fprintf(ios.Out, "\nComputing hex grid (edge=%.0fm)...\n", hexEdge)
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)
	fmt.Fprintf(ios.Out, "  Generated %d hexes\n", len(hexes))

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

func cmdName(rt resource.ResourceType) string {
	switch rt.Name() {
	case "roads":
		return "roads"
	case "parking":
		return "parking"
	default:
		return "all"
	}
}
