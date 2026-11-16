package ingest

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
)

// noisyRing is a closed CW ring of n vertices around (cx, cy): a circle of
// radius r whose radius jumps at random by up to 30% from vertex to vertex,
// so it is non-convex and its edges straddle many latitude bands.
func noisyRing(rng *rand.Rand, cx, cy, r float64, n int) [][2]float64 {
	return wobblyRing(cx, cy, r, n, func(float64) float64 { return 0.7 + 0.3*rng.Float64() })
}

// coastRing is a closed CW ring of n vertices shaped like a coastline: a
// circle of radius r with smooth bays and headlands, whose edges are short.
func coastRing(cx, cy, r float64, n int) [][2]float64 {
	return wobblyRing(cx, cy, r, n, func(a float64) float64 { return 1 + 0.15*math.Sin(9*a) + 0.05*math.Sin(41*a) })
}

func wobblyRing(cx, cy, r float64, n int, scale func(a float64) float64) [][2]float64 {
	ring := make([][2]float64, 0, n+1)
	for i := range n {
		a := -2 * math.Pi * float64(i) / float64(n) // negative: CW
		rr := r * scale(a)
		ring = append(ring, [2]float64{cx + rr*math.Cos(a), cy + rr*math.Sin(a)})
	}
	return append(ring, ring[0])
}

func randomPoints(rng *rand.Rand, n int, minX, minY, maxX, maxY float64) [][2]float64 {
	pts := make([][2]float64, n)
	for i := range pts {
		pts[i] = [2]float64{minX + rng.Float64()*(maxX-minX), minY + rng.Float64()*(maxY-minY)}
	}
	return pts
}

func bruteRingHits(probes, ring [][2]float64) int {
	hits := 0
	for _, p := range probes {
		if pointInRing(p, ring) {
			hits++
		}
	}
	return hits
}

// TestLandProbeIndex_HitsMatchPointInRing: the grid buckets and the edge
// index change only how many points and edges are read, never the count:
// RingHits and PolygonHits equal a plain pointInRing pass over every probe,
// for probe counts on both sides of indexedRingMinProbes, including probes
// on ring vertices and on the probe-bbox edges.
func TestLandProbeIndex_HitsMatchPointInRing(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{1, 3, indexedRingMinProbes - 1, indexedRingMinProbes, 500, 20000} {
		probes := randomPoints(rng, n, 0, 0, 1, 1)
		idx := newLandProbeIndex(probes)
		for trial := range 20 {
			ring := noisyRing(rng, rng.Float64(), rng.Float64(), 0.05+0.5*rng.Float64(), 3+rng.IntN(400))
			if trial == 0 && n > 2 {
				// Probes sitting exactly on ring vertices.
				ring = append(append([][2]float64{}, probes[:3]...), [2]float64{2, 2}, probes[0])
			}
			if got, want := idx.RingHits(ring), bruteRingHits(probes, ring); got != want {
				t.Fatalf("n=%d trial=%d: RingHits = %d, pointInRing count = %d", n, trial, got, want)
			}
			hole := noisyRing(rng, ring[0][0], ring[0][1], 0.1, 50)
			poly := waterPolygon{outer: ring, holes: [][][2]float64{hole}}
			want := 0
			for _, p := range probes {
				if pointInRing(p, ring) && !pointInRing(p, hole) {
					want++
				}
			}
			if got := idx.PolygonHits(poly); got != want {
				t.Fatalf("n=%d trial=%d: PolygonHits = %d, want %d", n, trial, got, want)
			}
		}
	}
}

// TestIndexedRing_ContainsMatchesPointInRing checks the edge index alone,
// with points on band boundaries and outside the ring's latitude range.
func TestIndexedRing_ContainsMatchesPointInRing(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for _, ring := range [][][2]float64{noisyRing(rng, 0, 0, 1, 1000), coastRing(0, 0, 1, 1000)} {
		ir := newIndexedRing(ring)
		pts := randomPoints(rng, 20000, -1.2, -1.2, 1.2, 1.2)
		for b := range len(ir.bands) + 1 {
			pts = append(pts, [2]float64{0, ir.minY + float64(b)*ir.step})
		}
		pts = append(pts, ring[:50]...)
		for _, p := range pts {
			if got, want := ir.contains(p), pointInRing(p, ring); got != want {
				t.Fatalf("contains(%v) = %v, pointInRing = %v", p, got, want)
			}
		}
	}
}

// BenchmarkRingHits_150kRoads_10kRing is the dense-probe worst case the
// water strip meets: a large city's road probes against a long coastline
// or lake ring that covers much of them.
func BenchmarkRingHits_150kRoads_10kRing(b *testing.B) {
	rng := rand.New(rand.NewPCG(5, 6))
	idx := newLandProbeIndex(randomPoints(rng, 150_000, 0, 0, 1, 1))
	ring := coastRing(0.5, 0.5, 0.38, 10_000)
	b.ResetTimer()
	for b.Loop() {
		_ = idx.RingHits(ring)
	}
}

func roadFeature(id string, tags map[string]string, coords ...[2]float64) db.Feature {
	pts := make([]string, len(coords))
	for i, c := range coords {
		pts[i] = fmt.Sprintf("[%v,%v]", c[0], c[1])
	}
	if tags == nil {
		tags = map[string]string{"highway": "residential"}
	}
	return db.Feature{ID: id, Tags: tags,
		GeometryJSON: `{"type":"LineString","coordinates":[` + strings.Join(pts, ",") + `]}`}
}

// TestFeaturesTouchingBBox: a feature is kept when any segment touches the
// bbox, so a road that crosses it with no vertex inside is kept, as an
// Overpass way(bbox) query returns it.
func TestFeaturesTouchingBBox(t *testing.T) {
	bbox := [4]float64{0, 0, 1, 1} // south, west, north, east
	features := []db.Feature{
		roadFeature("inside", nil, [2]float64{0.2, 0.2}, [2]float64{0.3, 0.3}),
		roadFeature("crossing", nil, [2]float64{-0.5, 0.5}, [2]float64{1.5, 0.5}),
		roadFeature("leaving", nil, [2]float64{0.9, 0.9}, [2]float64{2, 2}),
		roadFeature("outside", nil, [2]float64{2, 2}, [2]float64{3, 3}),
		roadFeature("corner-miss", nil, [2]float64{1.6, 0.5}, [2]float64{0.5, 1.6}),
		{ID: "multi", GeometryJSON: `{"type":"MultiLineString","coordinates":[[[5,5],[6,6]],[[0.5,-1],[0.5,2]]]}`},
		{ID: "point", GeometryJSON: `{"type":"Point","coordinates":[0.5,0.5]}`},
		{ID: "garbage", GeometryJSON: `not json`},
	}
	var got []string
	for _, f := range FeaturesTouchingBBox(features, bbox) {
		got = append(got, f.ID)
	}
	want := []string{"inside", "crossing", "leaving", "multi", "point"}
	if !slices.Equal(got, want) {
		t.Errorf("FeaturesTouchingBBox = %q, want %q", got, want)
	}
}

// TestRoadLandProbes covers how roads become land probes: the middle
// vertex (or a two-vertex road's midpoint), roads over water excluded by
// their tags, only points inside the boundary, one per grid cell, and the
// hit allowance.
func TestRoadLandProbes(t *testing.T) {
	// A [0,1]² boundary with a hole at [0.4,0.6]².
	boundary := `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]],[[0.4,0.4],[0.6,0.4],[0.6,0.6],[0.4,0.6],[0.4,0.4]]]}`
	roads := []db.Feature{
		// Middle vertex (0.25,0.25), though the first vertex is outside.
		roadFeature("three", nil, [2]float64{-1, -1}, [2]float64{0.25, 0.25}, [2]float64{3, 3}),
		// Two vertices: the segment midpoint (0.75, 0.25).
		roadFeature("two", nil, [2]float64{0.7, 0.2}, [2]float64{0.8, 0.3}),
		// Same 0.001° cell as "two": deduplicated.
		roadFeature("twin", nil, [2]float64{0.7504, 0.2504}, [2]float64{0.7504, 0.2504}),
		// In the hole and outside the boundary: not land.
		roadFeature("hole", nil, [2]float64{0.5, 0.5}, [2]float64{0.5, 0.5}),
		roadFeature("outside", nil, [2]float64{2, 2}, [2]float64{2, 2}),
		// Structures over or through water.
		roadFeature("bridge", map[string]string{"highway": "primary", "bridge": "yes"}, [2]float64{0.1, 0.9}, [2]float64{0.2, 0.9}),
		roadFeature("tunnel", map[string]string{"highway": "primary", "tunnel": "culvert"}, [2]float64{0.1, 0.8}, [2]float64{0.2, 0.8}),
		roadFeature("pier", map[string]string{"highway": "footway", "man_made": "pier"}, [2]float64{0.1, 0.7}, [2]float64{0.2, 0.7}),
		roadFeature("ford", map[string]string{"highway": "track", "ford": "yes"}, [2]float64{0.1, 0.6}, [2]float64{0.2, 0.6}),
		roadFeature("embankment", map[string]string{"highway": "primary", "embankment": "yes"}, [2]float64{0.1, 0.5}, [2]float64{0.2, 0.5}),
		roadFeature("causeway", map[string]string{"highway": "primary", "causeway": "yes"}, [2]float64{0.1, 0.4}, [2]float64{0.2, 0.4}),
		roadFeature("ferry", map[string]string{"route": "ferry"}, [2]float64{0.1, 0.3}, [2]float64{0.2, 0.3}),
		// bridge=no is an ordinary road.
		roadFeature("not-a-bridge", map[string]string{"highway": "primary", "bridge": "no"}, [2]float64{0.85, 0.85}, [2]float64{0.95, 0.95}),
	}
	got := RoadLandProbes(roads, boundary)
	want := [][2]float64{{0.25, 0.25}, {0.75, 0.25}, {0.9, 0.9}}
	if len(got.Points) != len(want) {
		t.Fatalf("probes = %v, want %v", got.Points, want)
	}
	for i := range want {
		if math.Abs(got.Points[i][0]-want[i][0]) > 1e-9 || math.Abs(got.Points[i][1]-want[i][1]) > 1e-9 {
			t.Errorf("probe %d = %v, want %v", i, got.Points[i], want[i])
		}
	}
	if !got.Dense || got.MaxHits != waterLandHitFloor {
		t.Errorf("Dense = %v, MaxHits = %d; want dense with the floor allowance %d", got.Dense, got.MaxHits, waterLandHitFloor)
	}
	if empty := RoadLandProbes(nil, boundary); len(empty.Points) != 0 || empty.Dense {
		t.Errorf("no roads gave %+v, want no probes", empty)
	}
	if bad := RoadLandProbes(roads, `{"type":"Point","coordinates":[0,0]}`); len(bad.Points) != 0 {
		t.Errorf("a non-polygon boundary gave %+v, want no probes", bad)
	}
}

func TestWaterLandHitAllowance(t *testing.T) {
	for _, c := range []struct{ n, want int }{{1, 3}, {3000, 3}, {3001, 4}, {150_000, 150}} {
		if got := waterLandHitAllowance(c.n); got != c.want {
			t.Errorf("waterLandHitAllowance(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

// TestAssembleWater_DenseProbesAllowAFewHits: with road probes, a lake that
// holds up to MaxHits of them (a bridge's ArcGIS centerline, a dam-crest
// road) is still water; one that holds more is land-side and dropped.
func TestAssembleWater_DenseProbesAllowAFewHits(t *testing.T) {
	body := closedWayBody(7, [][2]float64{{0.2, 0.2}, {0.2, 0.4}, {0.4, 0.4}, {0.4, 0.2}, {0.2, 0.2}})
	bbox := [4]float64{0, 0, 1, 1}
	outside := randomPoints(rand.New(rand.NewPCG(7, 8)), 200, 0.5, 0.5, 1, 1)
	inLake := [][2]float64{{0.25, 0.25}, {0.3, 0.3}, {0.35, 0.35}, {0.3, 0.25}}
	for _, c := range []struct {
		name   string
		inside int
		dense  bool
		kept   bool
	}{
		{"dense, within the allowance", 3, true, true},
		{"dense, over the allowance", 4, true, false},
		{"sparse, one hit", 1, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			pts := append(slices.Clone(outside), inLake[:c.inside]...)
			probes := LandProbes{Points: pts, Dense: c.dense}
			if c.dense {
				probes.MaxHits = waterLandHitAllowance(len(pts))
			}
			got, err := parseWaterResponse(context.Background(), []byte(body), bbox, probes)
			if err != nil {
				t.Fatal(err)
			}
			if kept := got != ""; kept != c.kept {
				t.Errorf("lake kept = %v, want %v (result %q)", kept, c.kept, got)
			}
		})
	}
}
