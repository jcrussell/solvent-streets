package ingest

import (
	"math"
	"math/rand/v2"
	"testing"
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
