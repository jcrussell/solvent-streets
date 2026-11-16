package ingest

import (
	"context"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

// subtractArea subtracts water from boundary and returns the result with
// its area.
func subtractArea(tb testing.TB, boundary, water string) (string, float64) {
	tb.Helper()
	got, err := geo.SubtractGeoJSON(context.Background(), boundary, water)
	if err != nil {
		tb.Fatal(err)
	}
	area, err := geo.BoundaryArea(context.Background(), got)
	if err != nil {
		tb.Fatal(err)
	}
	return got, area
}

// differenceArea is the area of a minus b, 0 when b covers a.
func differenceArea(t *testing.T, a, b string) float64 {
	t.Helper()
	d, err := geo.SubtractGeoJSON(context.Background(), a, b)
	if err != nil && strings.Contains(err.Error(), "difference is empty") {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	area, err := geo.BoundaryArea(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return area
}

// densify splits each edge of ring into pieces at most 0.001° long.
func densify(ring [][2]float64) [][2]float64 {
	out := [][2]float64{ring[0]}
	for i := 1; i < len(ring); i++ {
		a, b := ring[i-1], ring[i]
		n := int(math.Ceil(math.Hypot(b[0]-a[0], b[1]-a[1]) / 0.001))
		for k := 1; k <= n; k++ {
			f := float64(k) / float64(n)
			out = append(out, [2]float64{a[0] + f*(b[0]-a[0]), a[1] + f*(b[1]-a[1])})
		}
	}
	out[len(out)-1] = ring[len(ring)-1]
	return out
}

// TestClipWaterToBBox_SubtractUnchanged: clipping the water to the padded
// query bbox leaves the stripped boundary the same. The fixture lake is
// concave and leaves and re-enters the bbox (so the clip runs along the
// bbox edge and back), and its hole, an island, straddles the bbox edge.
func TestClipWaterToBBox_SubtractUnchanged(t *testing.T) {
	bbox := [4]float64{0, 0, 1, 1}
	boundary := `{"type":"Polygon","coordinates":[[[0.05,0],[1,0.05],[0.95,1],[0,0.95],[0.05,0]]]}`
	// A U-shaped lake: two arms reaching into the bbox from the east,
	// joined outside it, and an island across the east bbox edge.
	// Edges are densified to OSM-like lengths: the subtract projects
	// vertices only, so a long lon/lat edge and its clipped part would
	// bend differently.
	lake := waterPolygon{
		outer: densify([][2]float64{{0.6, 0.1}, {3, 0.1}, {3, 0.9}, {0.6, 0.9}, {0.6, 0.7}, {2, 0.7}, {2, 0.3}, {0.6, 0.3}, {0.6, 0.1}}),
		holes: [][][2]float64{densify([][2]float64{{0.8, 0.75}, {1.2, 0.75}, {1.2, 0.85}, {0.8, 0.85}, {0.8, 0.75}})},
	}
	clipped := clipWaterToBBox(context.Background(), []waterPolygon{lake}, bbox)
	if len(clipped) != 1 || len(clipped[0].holes) != 1 {
		t.Fatalf("clipped = %+v, want the lake and its island cut at the padded bbox", clipped)
	}
	pad := geo.PadBBox(bbox, waterClipPadM)
	for _, r := range append([][][2]float64{clipped[0].outer}, clipped[0].holes...) {
		for _, p := range r {
			if p[0] > pad[3]+1e-12 {
				t.Fatalf("clipped vertex %v is past the padded bbox east edge %v", p, pad[3])
			}
		}
	}

	full, fullArea := subtractArea(t, boundary, polysToMultiPolygonGeoJSON([]waterPolygon{lake}))
	cut, cutArea := subtractArea(t, boundary, polysToMultiPolygonGeoJSON(clipped))
	if math.Abs(fullArea-cutArea) > 1e-6*fullArea {
		t.Errorf("stripped area %.3f with the clip, %.3f without", cutArea, fullArea)
	}
	onlyFull, onlyCut := differenceArea(t, full, cut), differenceArea(t, cut, full)
	if onlyFull+onlyCut > 1e-6*fullArea {
		t.Errorf("clipped and unclipped strips differ by %.3f sq m", onlyFull+onlyCut)
	}

	// A polygon inside the padded bbox is untouched.
	small := waterPolygon{outer: [][2]float64{{0.2, 0.2}, {0.4, 0.2}, {0.4, 0.4}, {0.2, 0.4}, {0.2, 0.2}}}
	if got := clipWaterToBBox(context.Background(), []waterPolygon{small}, bbox); len(got) != 1 || &got[0].outer[0] != &small.outer[0] {
		t.Errorf("an inside polygon was rewritten: %+v", got)
	}
}

// greatLake is a CW ring of n vertices about 4°×3° wide with a jittered
// shore, the size of a Great Lake relation, east of a city on its west
// shore at (-88.3, 43.0).
func greatLake(n int) waterPolygon {
	rng := rand.New(rand.NewPCG(9, 10))
	ring := make([][2]float64, 0, n+1)
	for i := range n {
		a := -2 * math.Pi * float64(i) / float64(n)
		r := 1 + 0.1*math.Sin(37*a) + 0.002*rng.Float64()
		ring = append(ring, [2]float64{-86.2 + 2.1*r*math.Cos(a), 43.0 + 1.5*r*math.Sin(a)})
	}
	return waterPolygon{outer: append(ring, ring[0])}
}

// BenchmarkSubtract_500kVertexLake times the strip of a city on the shore
// of a 500k-vertex lake, with the water clipped to the city's bbox (as
// assembleWaterGeoJSON does) and unclipped:
//
//	go test ./internal/ingest -run '^$' -bench Subtract_500kVertexLake -benchtime 1x
func BenchmarkSubtract_500kVertexLake(b *testing.B) {
	lake := greatLake(500_000)
	bbox := [4]float64{42.9, -88.4, 43.1, -88.2}
	boundary := `{"type":"Polygon","coordinates":[[[-88.4,42.9],[-88.2,42.9],[-88.2,43.1],[-88.4,43.1],[-88.4,42.9]]]}`
	for _, c := range []struct {
		name  string
		polys []waterPolygon
	}{
		{"clipped", clipWaterToBBox(context.Background(), []waterPolygon{lake}, bbox)},
		{"unclipped", []waterPolygon{lake}},
	} {
		water := polysToMultiPolygonGeoJSON(c.polys)
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				subtractArea(b, boundary, water)
			}
		})
	}
}

// islandInLake is a water relation whose outer and inner (an island) both
// enclose the [0,1]² bbox: a city wholly on a river or lake island.
func islandInLake() string {
	return `{"elements":[{"type":"relation","id":88,"tags":{"natural":"water","type":"multipolygon"},"members":[` +
		`{"type":"way","ref":1,"role":"outer","geometry":[{"lat":-5,"lon":-5},{"lat":-5,"lon":6},{"lat":6,"lon":6},{"lat":6,"lon":-5},{"lat":-5,"lon":-5}]},` +
		`{"type":"way","ref":2,"role":"inner","geometry":[{"lat":-2,"lon":-2},{"lat":-2,"lon":3},{"lat":3,"lon":3},{"lat":3,"lon":-2},{"lat":-2,"lon":-2}]}]}]}`
}

// TestAssembleWater_CityOnAnIslandOfALakeHasNoWater: the island (the
// relation's inner) holds the whole bbox, so once clipped the outer and its
// hole are the same rectangle and no water is left: the assembly answers no
// water rather than an empty polygon the subtract would fail on.
func TestAssembleWater_CityOnAnIslandOfALakeHasNoWater(t *testing.T) {
	water, err := parseWaterResponse(context.Background(), []byte(islandInLake()), [4]float64{0, 0, 1, 1},
		denseProbes(gridProbes(10, 0.05, 0.05, 0.95, 0.95)))
	if err != nil || water != "" {
		t.Errorf("water = %q, err = %v; want no water", water, err)
	}
	if !waterless(waterPolygon{outer: [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 0}}, holes: [][][2]float64{{{0, 0}, {1, 0}, {1, 1}, {0, 0}}}}) {
		t.Error("a polygon whose hole is its outer has water")
	}
}

// TestClipWaterToBBox_FarIslandsDoNotEmptyTheLake: a lake's islands wholly
// outside the padded bbox are dropped, not kept unclipped: their area would
// otherwise outweigh the clipped outer and drop the lake as waterless (Lake
// Champlain's big islands, miles north of Burlington, solvent-streets-z3si).
func TestClipWaterToBBox_FarIslandsDoNotEmptyTheLake(t *testing.T) {
	lake := waterPolygon{
		outer: [][2]float64{{-1, -1}, {-1, 20}, {20, 20}, {20, -1}, {-1, -1}},
		holes: [][][2]float64{
			{{5, 5}, {5, 15}, {15, 15}, {15, 5}, {5, 5}},                 // far island, larger than the clipped lake
			{{0.4, 0.4}, {0.4, 0.6}, {0.6, 0.6}, {0.6, 0.4}, {0.4, 0.4}}, // island in the bbox
		},
	}
	got := clipWaterToBBox(context.Background(), []waterPolygon{lake}, [4]float64{0, 0, 1, 1})
	if len(got) != 1 || len(got[0].holes) != 1 || got[0].holes[0][0] != [2]float64{0.4, 0.4} {
		t.Fatalf("clipped = %+v; want the lake kept with only the island inside the bbox", got)
	}
}
