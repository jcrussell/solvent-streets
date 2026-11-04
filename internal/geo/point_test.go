package geo

import (
	"math"
	"testing"
)

// TestGeoJSONToProjectedGeometry_Point pins Point/MultiPoint support, which
// facility features (fire stations) rely on: the projected point lands where
// the projector puts the lon/lat, and a MultiPoint keeps every member.
func TestGeoJSONToProjectedGeometry_Point(t *testing.T) {
	proj := NewUTMProjector(-121.77, 37.68)
	g, gtype, err := GeoJSONToProjectedGeometry(`{"type":"Point","coordinates":[-121.77,37.68]}`, proj)
	if err != nil {
		t.Fatalf("Point: %v", err)
	}
	if gtype != "Point" || !g.IsPoint() {
		t.Fatalf("got type %q (IsPoint=%v)", gtype, g.IsPoint())
	}
	xy, ok := g.MustAsPoint().XY()
	if !ok {
		t.Fatal("empty point")
	}
	wx, wy := proj.ToProjected(-121.77, 37.68)
	if math.Abs(xy.X-wx) > 1e-6 || math.Abs(xy.Y-wy) > 1e-6 {
		t.Errorf("projected point = (%v, %v), want (%v, %v)", xy.X, xy.Y, wx, wy)
	}

	mg, mtype, err := GeoJSONToProjectedGeometry(`{"type":"MultiPoint","coordinates":[[-121.77,37.68],[-121.76,37.69]]}`, proj)
	if err != nil {
		t.Fatalf("MultiPoint: %v", err)
	}
	if mtype != "MultiPoint" || mg.MustAsMultiPoint().NumPoints() != 2 {
		t.Errorf("MultiPoint: type %q, %d points", mtype, mg.MustAsMultiPoint().NumPoints())
	}
}
