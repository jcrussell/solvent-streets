package resource

import (
	"context"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

// projectedSquare returns a unit-edge square polygon centered at (x, y)
// in projected coordinates. Each cohort feature uses one so the cohort
// classifier has something with positive area to index against the hex
// grid below.
func projectedSquare(t *testing.T, x, y float64) geom.Geometry {
	t.Helper()
	wkt := func() string {
		return `POLYGON((` +
			ftoa(x-1) + ` ` + ftoa(y-1) + `,` +
			ftoa(x+1) + ` ` + ftoa(y-1) + `,` +
			ftoa(x+1) + ` ` + ftoa(y+1) + `,` +
			ftoa(x-1) + ` ` + ftoa(y+1) + `,` +
			ftoa(x-1) + ` ` + ftoa(y-1) + `))`
	}()
	g, err := geom.UnmarshalWKT(wkt)
	if err != nil {
		t.Fatalf("UnmarshalWKT: %v", err)
	}
	return g
}

func ftoa(f float64) string {
	// Tiny float-to-string to keep test geometry literals out of fmt.Sprintf
	// (geom.UnmarshalWKT parses fixed numerics fine).
	const epsilon = 1e9
	whole := int64(f * epsilon)
	return strconvI64(whole, epsilon)
}

func strconvI64(n int64, divisor int64) string {
	whole := n / divisor
	frac := n % divisor
	if frac < 0 {
		frac = -frac
	}
	return itoa(whole) + "." + leftPad(itoa(frac), 9)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func leftPad(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}
	return s
}

// TestComputeRoadCohortAreas_AllHighwayTypes confirms that each canonical
// highway class produces a positive coverage area when its features
// overlap the hex grid. Pins the classifier output shape for the
// forecast cohort breakdown.
func TestComputeRoadCohortAreas_AllHighwayTypes(t *testing.T) {
	hexes := geo.HexGrid(0, 0, 100, 100, 20)
	if len(hexes) == 0 {
		t.Fatal("hex grid is empty")
	}

	classes := []string{"motorway", "trunk", "primary", "secondary", "tertiary", forecast.ClassResidential, "service"}
	var buffered []BufferedFeature
	for i, c := range classes {
		buffered = append(buffered, BufferedFeature{
			Feature: Feature{
				ID:   c,
				Tags: map[string]string{"highway": c},
			},
			Geom: projectedSquare(t, float64(10+10*i), 50),
		})
	}

	areas := ComputeRoadCohortAreas(t.Context(), buffered, hexes)
	for _, c := range classes {
		if areas[c] <= 0 {
			t.Errorf("class %q: expected positive area, got %f", c, areas[c])
		}
	}
}

// TestComputeRoadCohortAreas_Empty pins the empty-input contract: no
// features in means no classifications out, not a panic or an artifact
// entry under the default class.
func TestComputeRoadCohortAreas_Empty(t *testing.T) {
	hexes := geo.HexGrid(0, 0, 100, 100, 20)
	areas := ComputeRoadCohortAreas(context.Background(), nil, hexes)
	if len(areas) != 0 {
		t.Errorf("expected empty result, got %v", areas)
	}
}

// TestComputeRoadCohortAreas_UnknownClassFallsToResidential pins the
// NormalizeClass fallback used by the forecast cohort breakdown: any
// highway tag outside the canonical set is bucketed into residential,
// not silently dropped or assigned to a phantom class.
func TestComputeRoadCohortAreas_UnknownClassFallsToResidential(t *testing.T) {
	hexes := geo.HexGrid(0, 0, 100, 100, 20)
	buffered := []BufferedFeature{
		{
			Feature: Feature{ID: "odd", Tags: map[string]string{"highway": "rocket_road"}},
			Geom:    projectedSquare(t, 30, 50),
		},
	}
	areas := ComputeRoadCohortAreas(context.Background(), buffered, hexes)
	if got := areas[forecast.ClassResidential]; got <= 0 {
		t.Errorf("expected positive area under %q (default bucket), got %f", forecast.ClassResidential, got)
	}
	for class := range areas {
		if class != forecast.ClassResidential {
			t.Errorf("unexpected class %q in result; want only %q", class, forecast.ClassResidential)
		}
	}
}
