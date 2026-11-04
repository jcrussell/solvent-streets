package geo

import (
	"context"
	"math"
	"testing"

	"github.com/peterstace/simplefeatures/geom"
)

// squareAt builds an axis-aligned square of side s centred on (cx, cy).
func squareAt(cx, cy, s float64) geom.Geometry {
	h := s / 2
	coords := []float64{cx - h, cy - h, cx + h, cy - h, cx + h, cy + h, cx - h, cy + h, cx - h, cy - h}
	ring := geom.NewLineString(geom.NewSequence(coords, geom.DimXY))
	return geom.NewPolygon([]geom.LineString{ring}).AsGeometry()
}

func sumShares(shares []HexShare) float64 {
	var s float64
	for _, sh := range shares {
		s += sh.Value
	}
	return s
}

// TestComputeHexWeightedShares_FullyInsideOneHex: a square well inside a hex
// (half-side 0.25·edge is inside the 0.866·edge inradius) hands that hex the
// whole weight and nothing to its neighbours.
func TestComputeHexWeightedShares_FullyInsideOneHex(t *testing.T) {
	const edge = 100.0
	hexes := HexGrid(0, 0, 1000, 1000, edge)
	target := hexes[len(hexes)/2]
	sq := squareAt(target.CenterX, target.CenterY, edge/2)

	shares := ComputeHexWeightedShares(context.Background(), hexes, []geom.Geometry{sq}, []float64{40}, nil)
	if len(shares) != 1 {
		t.Fatalf("expected exactly one hex to receive the weight, got %d: %+v", len(shares), shares)
	}
	if shares[0].HexID != target.ID || math.Abs(shares[0].Value-40) > 1e-6 {
		t.Errorf("share = %+v, want {%s 40}", shares[0], target.ID)
	}
}

// TestComputeHexWeightedShares_StraddlingSumsToWeight: a square that straddles
// several hexes is split by area fraction, so the pieces sum back to the
// weight (the feature lies entirely within the grid) and more than one hex
// receives a strictly partial share.
func TestComputeHexWeightedShares_StraddlingSumsToWeight(t *testing.T) {
	const edge = 100.0
	hexes := HexGrid(0, 0, 1000, 1000, edge)
	target := hexes[len(hexes)/2]
	// Centred on a vertex region between hexes: offset from the centre by the
	// hex circumradius so the square certainly crosses an edge.
	sq := squareAt(target.CenterX+edge, target.CenterY, 1.5*edge)

	shares := ComputeHexWeightedShares(context.Background(), hexes, []geom.Geometry{sq}, []float64{90}, nil)
	if len(shares) < 2 {
		t.Fatalf("expected the square to straddle at least two hexes, got %d shares", len(shares))
	}
	if got := sumShares(shares); math.Abs(got-90) > 1e-6 {
		t.Errorf("shares sum to %v, want 90 (weight preserved)", got)
	}
	for _, sh := range shares {
		if sh.Value <= 0 || sh.Value >= 90 {
			t.Errorf("share %+v should be a strictly partial, positive slice of the weight", sh)
		}
	}
}

// TestComputeHexWeightedShares_OverlapIsNotDeduped pins the documented
// difference from ComputeHexStats: two identical lots each contribute their
// full weight (the weight belongs to the feature, not the paved surface).
func TestComputeHexWeightedShares_OverlapIsNotDeduped(t *testing.T) {
	const edge = 100.0
	hexes := HexGrid(0, 0, 500, 500, edge)
	target := hexes[len(hexes)/2]
	sq := squareAt(target.CenterX, target.CenterY, edge/2)

	shares := ComputeHexWeightedShares(context.Background(), hexes, []geom.Geometry{sq, sq}, []float64{10, 5}, nil)
	if got := sumShares(shares); math.Abs(got-15) > 1e-6 {
		t.Errorf("overlapping lots sum to %v, want 15 (no dedup)", got)
	}
}

// TestComputeHexWeightedShares_Degenerate: zero weights and zero-area or
// mismatched inputs contribute nothing rather than NaN or a panic.
func TestComputeHexWeightedShares_Degenerate(t *testing.T) {
	const edge = 100.0
	hexes := HexGrid(0, 0, 500, 500, edge)
	target := hexes[len(hexes)/2]
	sq := squareAt(target.CenterX, target.CenterY, edge/2)
	degenerate := squareAt(target.CenterX, target.CenterY, 0)

	if got := ComputeHexWeightedShares(context.Background(), hexes, []geom.Geometry{sq}, []float64{0}, nil); len(got) != 0 {
		t.Errorf("zero weight produced shares: %+v", got)
	}
	if got := ComputeHexWeightedShares(context.Background(), hexes, []geom.Geometry{degenerate}, []float64{10}, nil); len(got) != 0 {
		t.Errorf("zero-area geom produced shares: %+v", got)
	}
	if got := ComputeHexWeightedShares(context.Background(), hexes, []geom.Geometry{sq}, []float64{1, 2}, nil); got != nil {
		t.Errorf("mismatched slices should return nil, got %+v", got)
	}
}

// TestComputeHexWeightedShares_CancelledIsPartial: a cancelled context yields
// a partial (possibly empty) result with no error — the same contract as
// ComputeHexStats — so callers must check ctx.Err() before persisting.
func TestComputeHexWeightedShares_CancelledIsPartial(t *testing.T) {
	const edge = 100.0
	hexes := HexGrid(0, 0, 2000, 2000, edge)
	sq := squareAt(1000, 1000, 1500)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	full := ComputeHexWeightedShares(context.Background(), hexes, []geom.Geometry{sq}, []float64{100}, nil)
	partial := ComputeHexWeightedShares(ctx, hexes, []geom.Geometry{sq}, []float64{100}, nil)
	if len(partial) > len(full) {
		t.Errorf("cancelled run produced %d shares, more than the full run's %d", len(partial), len(full))
	}
}

// TestComputeHexWeightedSharesN_ConservesTotalsOverTiling: blocks that tile a
// region inside the grid (as census blocks tile a city) hand every column its
// full total back, however the blocks straddle hex edges, and each column is
// apportioned independently by the same area fractions.
func TestComputeHexWeightedSharesN_ConservesTotalsOverTiling(t *testing.T) {
	const edge = 100.0
	hexes := HexGrid(0, 0, 2000, 2000, edge)
	var geoms []geom.Geometry
	var pop, hu []float64
	var wantPop, wantHU float64
	// A 6x6 tiling of 130 m squares (deliberately not commensurate with the
	// hex edge) starting well inside the grid.
	for r := range 6 {
		for c := range 6 {
			geoms = append(geoms, squareAt(500+65+float64(c)*130, 500+65+float64(r)*130, 130))
			p := float64(10 + r*6 + c)
			pop = append(pop, p)
			hu = append(hu, p/2)
			wantPop += p
			wantHU += p / 2
		}
	}
	shares := ComputeHexWeightedSharesN(context.Background(), hexes, geoms, [][]float64{pop, hu}, nil)
	if len(shares) < 2 {
		t.Fatalf("expected the tiling to span several hexes, got %d", len(shares))
	}
	var gotPop, gotHU float64
	for _, sh := range shares {
		if len(sh.Values) != 2 {
			t.Fatalf("share %+v: want 2 columns", sh)
		}
		gotPop += sh.Values[0]
		gotHU += sh.Values[1]
		if math.Abs(sh.Values[1]-sh.Values[0]/2) > 1e-6 {
			t.Errorf("hex %s: columns not apportioned by the same fractions: %v", sh.HexID, sh.Values)
		}
	}
	if math.Abs(gotPop-wantPop) > 1e-6 || math.Abs(gotHU-wantHU) > 1e-6 {
		t.Errorf("totals = (%v, %v), want (%v, %v)", gotPop, gotHU, wantPop, wantHU)
	}
}

// TestComputeHexWeightedSharesN_BoundaryClippedHex: when the grid is clipped
// to a boundary, a block straddling the boundary only hands over the share
// that lands inside the clipped hexes — the rest is outside the city.
func TestComputeHexWeightedSharesN_BoundaryClippedHex(t *testing.T) {
	const edge = 100.0
	hexes := HexGrid(0, 0, 1000, 1000, edge)
	boundary := squareAt(500, 500, 400) // covers [300,700]²
	clipped := ClipHexesToBoundary(context.Background(), hexes, boundary, nil)
	// A block half inside the boundary: [600,800] x [400,600].
	blk := squareAt(700, 500, 200)
	shares := ComputeHexWeightedSharesN(context.Background(), clipped, []geom.Geometry{blk}, [][]float64{{100}}, nil)
	var got float64
	for _, sh := range shares {
		got += sh.Values[0]
	}
	if math.Abs(got-50) > 1e-6 {
		t.Errorf("clipped share = %v, want 50 (half the block is inside the boundary)", got)
	}
}

// TestComputeHexWeightedSharesN_Mismatched: any column of the wrong length,
// or no columns at all, is refused with nil rather than an index panic.
func TestComputeHexWeightedSharesN_Mismatched(t *testing.T) {
	hexes := HexGrid(0, 0, 500, 500, 100)
	sq := squareAt(250, 250, 50)
	if got := ComputeHexWeightedSharesN(context.Background(), hexes, []geom.Geometry{sq}, [][]float64{{1}, {1, 2}}, nil); got != nil {
		t.Errorf("mismatched column should return nil, got %+v", got)
	}
	if got := ComputeHexWeightedSharesN(context.Background(), hexes, []geom.Geometry{sq}, nil, nil); got != nil {
		t.Errorf("no columns should return nil, got %+v", got)
	}
}
