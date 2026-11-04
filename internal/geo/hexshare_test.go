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
