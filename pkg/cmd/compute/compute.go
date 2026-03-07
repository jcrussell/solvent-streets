package compute

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"pvmt/internal/db"
	"pvmt/internal/geo"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"

	"github.com/peterstace/simplefeatures/geom"
	"github.com/spf13/cobra"
)

type Options struct {
	Factory      *cmdutil.Factory
	ResourceType resource.ResourceType
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

	gjson, areaSqFt, err := opts.ResourceType.ProcessFeatures(resFeatures, proj)
	if err != nil {
		return fmt.Errorf("process features: %w", err)
	}

	areaAcres := geo.AreaAcres(areaSqFt)

	// Create snapshot
	configHash := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%v", cfg))))
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

	fmt.Fprintf(ios.Out, "\n%s Results:\n", opts.ResourceType.Name())
	fmt.Fprintf(ios.Out, "  Features:  %d\n", len(dbFeatures))
	fmt.Fprintf(ios.Out, "  Area:      %.0f sq ft\n", areaSqFt)
	fmt.Fprintf(ios.Out, "  Area:      %.1f acres\n", areaAcres)
	fmt.Fprintf(ios.Out, "  Area:      %.2f sq mi\n", areaAcres/640)

	// Compute hex grid stats
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
		return fmt.Errorf("hex grid computation failed (compute result saved, but hex stats not): %w", err)
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
	// The GeoJSON is in WGS84, we need to project it back to UTM
	var obj struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(gjson), &obj); err != nil {
		return geom.Geometry{}, fmt.Errorf("parse geojson type: %w", err)
	}

	g, _, err := geo.GeoJSONToProjectedGeometry(gjson, proj)
	if err != nil {
		return geom.Geometry{}, err
	}
	return g, nil
}

func cmdName(rt resource.ResourceType) string {
	switch rt.Name() {
	case "pavements":
		return "roads"
	case "parking":
		return "parking"
	default:
		return "all"
	}
}
