package compute

import (
	"context"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestRunCombined_DedupesCrossResourceOverlap exercises the cx2 fix end-to-end:
// when a road buffer and a parking polygon overlap, the "combined" row's area
// is strictly less than the sum of per-resource areas computed against the same
// hex grid. Pre-fix, BuildMeta summed per-resource rows directly and inflated
// pct_paved by the overlap.
func TestRunCombined_DedupesCrossResourceOverlap(t *testing.T) {
	ctx := context.Background()

	// ~600m boundary at lat 38°, lon -120°. Picked so the road and parking
	// fit inside the bbox-derived hex grid with margin.
	const boundary = `{"type":"Polygon","coordinates":[[[-120.003,37.998],[-119.997,37.998],[-119.997,38.002],[-120.003,38.002],[-120.003,37.998]]]}`

	// Horizontal road, ~440m long; explicit width=20m so the buffer is a
	// 440m × 20m rectangle (~8800 sqm before clipping).
	rtRoads := resource.TypeRoads
	rtParking := resource.TypeParking
	roadFeature := db.Feature{
		ID:           "road1",
		ResourceType: rtRoads,
		Tags:         map[string]string{"highway": "residential", "width": "20"},
		GeometryJSON: `{"type":"LineString","coordinates":[[-120.0025,38.0],[-119.9975,38.0]]}`,
	}

	// Parking polygon: ~50m × 10m, centered on the road and entirely inside
	// the road's 20m-wide buffer. The combined union therefore equals the
	// road buffer alone — parking adds zero net area, so combined < sum.
	parkingFeature := db.Feature{
		ID:           "park1",
		ResourceType: rtParking,
		Tags:         map[string]string{"amenity": "parking"},
		GeometryJSON: `{"type":"Polygon","coordinates":[[[-120.000285,37.99996],[-119.999715,37.99996],[-119.999715,38.00004],[-120.000285,38.00004],[-120.000285,37.99996]]]}`,
	}

	saved := map[resource.Type]db.ComputeResult{}
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return boundary, nil },
		ListFeaturesFunc: func(_ context.Context, rt resource.Type) ([]db.Feature, error) {
			switch rt { //nolint:exhaustive // test fixture: other resource types are intentionally empty
			case rtRoads:
				return []db.Feature{roadFeature}, nil
			case rtParking:
				return []db.Feature{parkingFeature}, nil
			default:
				return nil, nil
			}
		},
		SaveComputeResultFunc: func(_ context.Context, r db.ComputeResult) error {
			saved[r.ResourceType] = r
			return nil
		},
	}

	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return &cfg.Cities[0], nil },
		Config:      func() (*config.Config, error) { return cfg, nil },
	}

	if err := RunCombined(ctx, f); err != nil {
		t.Fatalf("RunCombined: %v", err)
	}

	combined, ok := saved[resource.CombinedAll]
	if !ok {
		t.Fatalf("no %q ComputeResult was saved (got: %v)", resource.CombinedAll, saved)
	}
	if combined.TotalArea <= 0 {
		t.Fatalf("combined.TotalArea = %v; want > 0", combined.TotalArea)
	}

	// Compute per-resource areas against the same projection and hex grid
	// RunCombined uses internally. Anything else would compare apples to
	// oranges (different hex tiling clips features differently).
	bbox, err := geo.BBoxFromGeoJSON(boundary)
	if err != nil {
		t.Fatalf("BBoxFromGeoJSON: %v", err)
	}
	lon, lat := geo.CenterFromBBox(bbox)
	proj := geo.NewUTMProjector(lon, lat)
	hexEdge := cfg.ResolvedHexEdge(&cfg.Cities[0])
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0])
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2])
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)
	if bg, _, err := geo.GeoJSONToProjectedGeometry(boundary, proj); err == nil && !bg.IsEmpty() {
		hexes = geo.ClipHexesToBoundary(t.Context(), hexes, bg, nil)
	}

	areaForResource := func(t *testing.T, rt resource.Source, feat db.Feature) float64 {
		t.Helper()
		bufs, err := rt.BufferFeatures([]resource.Feature{{
			ID:           feat.ID,
			Tags:         feat.Tags,
			GeometryJSON: feat.GeometryJSON,
		}}, proj)
		if err != nil {
			t.Fatalf("buffer %s: %v", rt.Type(), err)
		}
		stats := geo.ComputeHexStats(t.Context(), hexes, geo.NewGeomIndexFromGeoms(bufs), string(rt.Type()), nil)
		var sum float64
		for _, s := range stats {
			sum += s.Area
		}
		return sum
	}

	roadsArea := areaForResource(t, &resource.Pavement{}, roadFeature)
	parkingArea := areaForResource(t, &resource.Parking{}, parkingFeature)

	if roadsArea <= 0 || parkingArea <= 0 {
		t.Fatalf("expected positive per-resource areas; got roads=%v parking=%v", roadsArea, parkingArea)
	}

	sum := roadsArea + parkingArea
	if combined.TotalArea >= sum {
		t.Errorf("combined area %v >= per-resource sum %v (roads=%v, parking=%v) — overlap was not deduped",
			combined.TotalArea, sum, roadsArea, parkingArea)
	}
}

// TestRunCombined_NoFeaturesSkipsSave verifies the early-return path when no
// resource has any features: RunCombined logs and skips without writing a
// "combined" row that would later be misread as zero-paved.
func TestRunCombined_NoFeaturesSkipsSave(t *testing.T) {
	ctx := context.Background()

	const boundary = `{"type":"Polygon","coordinates":[[[-120.003,37.998],[-119.997,37.998],[-119.997,38.002],[-120.003,38.002],[-120.003,37.998]]]}`

	saveCalled := false
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return boundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) { return nil, nil },
		SaveComputeResultFunc: func(_ context.Context, _ db.ComputeResult) error {
			saveCalled = true
			return nil
		},
	}

	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return &cfg.Cities[0], nil },
		Config:      func() (*config.Config, error) { return cfg, nil },
	}

	if err := RunCombined(ctx, f); err != nil {
		t.Fatalf("RunCombined: %v", err)
	}
	if saveCalled {
		t.Errorf("SaveComputeResult was called on the no-features path; expected early return")
	}
}
