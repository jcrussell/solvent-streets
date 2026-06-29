package export

import (
	"context"
	"strconv"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// lineFeature builds a roads db.Feature from a two-point GeoJSON LineString and
// a highway class. Both endpoints sit well inside boundaryGeoJSON so the
// buffered footprint clips into the grid rather than the boundary band.
func lineFeature(id, highway string, lon1, lat1, lon2, lat2 float64) db.Feature {
	gj := `{"type":"LineString","coordinates":[[` +
		ftoa(lon1) + `,` + ftoa(lat1) + `],[` + ftoa(lon2) + `,` + ftoa(lat2) + `]]}`
	return db.Feature{
		ID:           id,
		ResourceType: resource.TypeRoads,
		Tags:         map[string]string{"highway": highway},
		GeometryJSON: gj,
	}
}

func ftoa(f float64) string {
	// Six decimals is ~0.1 m at these latitudes — enough fidelity for fixtures.
	return strconv.FormatFloat(f, 'f', 6, 64)
}

// playHexEntry wires a MockStore returning the given road features and the
// shared square boundary, with a 200 m hex edge.
func playHexEntry(roads []db.Feature) CityEntry {
	store := &dbtest.MockStore{
		ListFeaturesFunc: func(_ context.Context, rt resource.Type) ([]db.Feature, error) {
			if rt == resource.TypeRoads {
				return roads, nil
			}
			return nil, nil
		},
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			return boundaryGeoJSON, nil
		},
	}
	return CityEntry{
		Config: &config.Config{Grid: config.GridConfig{HexEdgeM: 200}},
		City:   config.CityConfig{Name: "Test City"},
		Store:  store,
		Slug:   "test-city",
	}
}

// TestBuildPlayHexes_BlendsRealClasses pins the core contract: per-hex k is a
// real area-weighted blend of the actual road classes' decay rates — a hex over
// a primary arterial (k=0.025) holds while a hex over a residential street
// (k=0.040) rots faster — and is NOT uniformly the 0.035 default. It also
// asserts every emitted id belongs to the served hex grid and carries positive
// road area.
func TestBuildPlayHexes_BlendsRealClasses(t *testing.T) {
	ctx := context.Background()
	// Two city-jurisdiction roads of different classes, ~6 km apart so they
	// land in distinct hexes: a primary in the southwest, residential in the
	// northeast. Both are well inside (-122.5..-122.4, 37.5..37.6).
	roads := []db.Feature{
		lineFeature("primary-1", "primary", -122.485, 37.515, -122.470, 37.515),
		lineFeature("res-1", "residential", -122.420, 37.585, -122.405, 37.585),
	}
	entry := playHexEntry(roads)

	_, lon0, lat0, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	proj := geo.NewUTMProjector(lon0, lat0)

	hexes, err := BuildPlayHexes(ctx, entry, proj)
	if err != nil {
		t.Fatalf("BuildPlayHexes: %v", err)
	}
	if len(hexes) == 0 {
		t.Fatal("BuildPlayHexes returned no hexes; expected road-bearing hexes")
	}

	// (a) Every emitted id must belong to the served hex grid (same grid the
	// hexgrid.geojson layer is built from).
	grid, err := cityHexGrid(ctx, entry, proj)
	if err != nil {
		t.Fatalf("cityHexGrid: %v", err)
	}
	gridIDs := make(map[string]bool, len(grid))
	for _, h := range grid {
		gridIDs[h.ID] = true
	}

	primaryK := forecast.DecayRateForClass("primary")         // 0.025
	residentialK := forecast.DecayRateForClass("residential") // 0.040
	defaultK := forecast.DefaultDecayRates["default"]         // 0.035

	var minK, maxK float64
	sawDifferent := false
	for i, ph := range hexes {
		// (a) id joins the grid.
		if !gridIDs[ph.ID] {
			t.Errorf("play hex id %q is not in the served hex grid", ph.ID)
		}
		// (b) positive road area.
		if ph.RoadArea <= 0 {
			t.Errorf("hex %q road_area = %v; want > 0", ph.ID, ph.RoadArea)
		}
		// (c) k is a real per-class rate, not a guessed/zero value.
		if ph.K <= 0 {
			t.Errorf("hex %q k = %v; want > 0", ph.ID, ph.K)
		}
		if i == 0 {
			minK, maxK = ph.K, ph.K
		}
		if ph.K < minK {
			minK = ph.K
		}
		if ph.K > maxK {
			maxK = ph.K
		}
	}
	if maxK > minK {
		sawDifferent = true
	}

	// (c) The blend produces a real spread, not a uniform default.
	if !sawDifferent {
		t.Errorf("all hexes share k=%v; expected a class-driven spread (primary vs residential)", minK)
	}
	// A pure-primary hex must hold slower than a pure-residential hex, and
	// neither pure hex should read as the 0.035 default.
	if minK > defaultK {
		t.Errorf("min k = %v; expected a primary-driven hex at/below %v", minK, primaryK)
	}
	if maxK < residentialK-1e-9 {
		t.Errorf("max k = %v; expected a residential-driven hex near %v", maxK, residentialK)
	}
	// Sanity: the spread brackets the per-class rates we fed in.
	if minK < primaryK-1e-9 || maxK > residentialK+1e-9 {
		t.Errorf("k spread [%v, %v] escaped the input class rates [%v, %v]", minK, maxK, primaryK, residentialK)
	}
}

// TestBuildPlayHexes_NoFeatures returns no hexes (and no error) when the city
// has no road features — a legitimate empty the server turns into [].
func TestBuildPlayHexes_NoFeatures(t *testing.T) {
	ctx := context.Background()
	entry := playHexEntry(nil)
	_, lon0, lat0, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	hexes, err := BuildPlayHexes(ctx, entry, geo.NewUTMProjector(lon0, lat0))
	if err != nil {
		t.Fatalf("BuildPlayHexes: %v", err)
	}
	if len(hexes) != 0 {
		t.Errorf("expected no hexes for a city with no roads, got %d", len(hexes))
	}
}
