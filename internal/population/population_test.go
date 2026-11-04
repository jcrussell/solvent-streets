package population

import (
	"context"
	"math"
	"strconv"
	"testing"

	"github.com/peterstace/simplefeatures/geom"

	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/hexmetric"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

func square(cx, cy, s float64) geom.Geometry {
	h := s / 2
	ring := geom.NewLineString(geom.NewSequence([]float64{cx - h, cy - h, cx + h, cy - h, cx + h, cy + h, cx - h, cy + h, cx - h, cy - h}, geom.DimXY))
	return geom.NewPolygon([]geom.LineString{ring}).AsGeometry()
}

func block(g geom.Geometry, pop, hu int) resource.BufferedFeature {
	return resource.BufferedFeature{
		Feature: resource.Feature{Tags: map[string]string{resource.TagPOP100: strconv.Itoa(pop), resource.TagHU100: strconv.Itoa(hu)}},
		Geom:    g,
	}
}

// TestApportion_ConservesTotalsAndDensity: blocks inside the grid hand their
// whole population and housing back across the hexes, and each hex's density
// is its population over its own area.
func TestApportion_ConservesTotalsAndDensity(t *testing.T) {
	grid := geo.HexGrid(0, 0, 2000, 2000, 100)
	blocks := []resource.BufferedFeature{
		block(square(700, 700, 180), 120, 50),
		block(square(900, 700, 220), 80, 30),
		block(square(800, 950, 150), 0, 4), // homes, no people
	}
	m := Apportion(context.Background(), grid, blocks, nil)
	sum := func(xs []float64) float64 {
		var s float64
		for _, x := range xs {
			s += x
		}
		return s
	}
	var pops, hus []float64
	for _, r := range m.Population {
		pops = append(pops, r.Value)
	}
	for _, r := range m.HousingUnits {
		hus = append(hus, r.Value)
	}
	if got := sum(pops); math.Abs(got-200) > 1e-6 || math.Abs(m.Total-200) > 1e-6 {
		t.Errorf("population total = %v (Total %v), want 200", got, m.Total)
	}
	if got := sum(hus); math.Abs(got-84) > 1e-6 {
		t.Errorf("housing total = %v, want 84", got)
	}
	area := map[string]float64{}
	for _, h := range grid {
		area[h.ID] = h.Geom.Area()
	}
	popBy := map[string]float64{}
	for _, r := range m.Population {
		popBy[r.HexID] = r.Value
	}
	if len(m.Density) != len(m.Population) {
		t.Fatalf("density rows %d != population rows %d", len(m.Density), len(m.Population))
	}
	for _, d := range m.Density {
		if want := popBy[d.HexID] / (area[d.HexID] / 1e6); math.Abs(d.Value-want) > 1e-6 {
			t.Errorf("hex %s density %v, want %v", d.HexID, d.Value, want)
		}
	}
}

// TestSpecs: the stored and derived population metrics are registered, and
// the ratios divide by the stored population above MinResidents.
func TestSpecs(t *testing.T) {
	for _, k := range []string{KeyPopulation, KeyHousingUnits, KeyDensity} {
		s, ok := hexmetric.Lookup(k)
		if !ok || s.Derived() {
			t.Errorf("%s must be a registered stored metric", k)
		}
	}
	for _, k := range []string{KeyRoadAreaPerResident, KeyNeedPerResident} {
		s, ok := hexmetric.Lookup(k)
		if !ok || !s.Derived() || s.Derive.Op != hexmetric.OpRatio || s.Derive.Den != KeyPopulation || s.Derive.MinDen != MinResidents {
			t.Errorf("%s must be a ratio over %s, got %+v", k, KeyPopulation, s.Derive)
		}
	}
}
