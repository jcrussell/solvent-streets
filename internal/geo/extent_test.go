package geo

import (
	"math"
	"testing"
)

// TestProjectedBBoxExtent_BoundsAllCorners verifies the envelope contains every
// geographic corner — not just SW/NE. For an LA-shaped box (UTM zone 11, west of
// the central meridian) the SE/NW corners project beyond the SW/NE-only
// rectangle, so a two-corner extent would clip in-boundary hexes.
func TestProjectedBBoxExtent_BoundsAllCorners(t *testing.T) {
	// LA-ish: [minLat, minLon, maxLat, maxLon]
	bbox := [4]float64{33.70, -118.67, 34.34, -118.16}
	proj := NewUTMProjector((bbox[1]+bbox[3])/2, (bbox[0]+bbox[2])/2)

	minX, minY, maxX, maxY := ProjectedBBoxExtent(proj, bbox)

	corners := [][2]float64{
		{bbox[1], bbox[0]}, {bbox[3], bbox[0]},
		{bbox[1], bbox[2]}, {bbox[3], bbox[2]},
	}
	for _, c := range corners {
		x, y := proj.ToProjected(c[0], c[1])
		if x < minX-1e-6 || x > maxX+1e-6 || y < minY-1e-6 || y > maxY+1e-6 {
			t.Errorf("corner (%.4f,%.4f)->(%.2f,%.2f) outside envelope x[%.2f,%.2f] y[%.2f,%.2f]",
				c[0], c[1], x, y, minX, maxX, minY, maxY)
		}
	}

	// The full envelope must be strictly taller than the SW/NE-only rectangle:
	// the SE corner sits below the SW corner's northing here.
	swX, swY := proj.ToProjected(bbox[1], bbox[0])
	neX, neY := proj.ToProjected(bbox[3], bbox[2])
	twoCornerMinY := min(swY, neY)
	twoCornerMaxY := max(swY, neY)
	if minY >= twoCornerMinY {
		t.Errorf("minY %.2f not below two-corner minY %.2f (should capture SE/NW)", minY, twoCornerMinY)
	}
	if maxY <= twoCornerMaxY {
		t.Errorf("maxY %.2f not above two-corner maxY %.2f", maxY, twoCornerMaxY)
	}
	_ = swX
	_ = neX
}

// TestProjectedBBoxExtent_StraddlesCentralMeridian exercises the CM samples:
// when the box spans the zone's central meridian, the northing extreme along a
// parallel is interior (at the CM), below every corner.
func TestProjectedBBoxExtent_StraddlesCentralMeridian(t *testing.T) {
	// Denver-ish, straddling CM of zone 13 (central meridian -105°).
	bbox := [4]float64{39.50, -105.30, 39.94, -104.60}
	proj := NewUTMProjector((bbox[1]+bbox[3])/2, (bbox[0]+bbox[2])/2)
	cmLon := float64(proj.Zone*6 - 183)
	if cmLon < bbox[1] || cmLon > bbox[3] {
		t.Fatalf("test precondition: CM %.1f not within bbox lon range", cmLon)
	}

	_, minY, _, _ := ProjectedBBoxExtent(proj, bbox)

	// The CM-at-min-lat sample should be the southern extreme, below all corners.
	_, cmMinY := proj.ToProjected(cmLon, bbox[0])
	if cmMinY > minY+1e-6 {
		t.Errorf("expected CM sample to set minY; cmMinY=%.2f minY=%.2f", cmMinY, minY)
	}
	for _, c := range [][2]float64{{bbox[1], bbox[0]}, {bbox[3], bbox[0]}} {
		_, y := proj.ToProjected(c[0], c[1])
		if y <= cmMinY {
			t.Errorf("corner northing %.2f should be above CM sample %.2f", y, cmMinY)
		}
	}
}

// TestProjectedBBoxExtent_StraddlesEquator exercises the equator samples: for a
// box spanning the equator, easting offset (∝ cosφ) peaks at φ = 0, so the true
// east/west extremes lie on the edges at the equator, beyond every corner.
func TestProjectedBBoxExtent_StraddlesEquator(t *testing.T) {
	// Equator-straddling box with a wide-ish lon span west of the CM, so the
	// equator-edge easting exceeds the corner eastings.
	bbox := [4]float64{-2.0, 108.0, 8.0, 114.0}
	proj := NewUTMProjector((bbox[1]+bbox[3])/2, (bbox[0]+bbox[2])/2)
	if bbox[0] > 0 || bbox[2] < 0 {
		t.Fatalf("test precondition: bbox must straddle the equator, got lat [%f, %f]", bbox[0], bbox[2])
	}

	minX, _, maxX, _ := ProjectedBBoxExtent(proj, bbox)

	// The west edge at the equator sets the easting minimum, below all corners.
	westEqX, _ := proj.ToProjected(bbox[1], 0)
	if westEqX > minX+1e-6 {
		t.Errorf("got minX %.2f, want <= west-equator easting %.2f (equator-edge extreme)", minX, westEqX)
	}
	// The east edge at the equator sets the easting maximum, above all corners.
	eastEqX, _ := proj.ToProjected(bbox[3], 0)
	if eastEqX < maxX-1e-6 {
		t.Errorf("got maxX %.2f, want >= east-equator easting %.2f (equator-edge extreme)", maxX, eastEqX)
	}

	// The equator-edge eastings must lie strictly outside the corner-only extent.
	corners := [][2]float64{
		{bbox[1], bbox[0]}, {bbox[3], bbox[0]},
		{bbox[1], bbox[2]}, {bbox[3], bbox[2]},
	}
	cornerMinX, cornerMaxX := math.Inf(1), math.Inf(-1)
	for _, c := range corners {
		x, _ := proj.ToProjected(c[0], c[1])
		cornerMinX = min(cornerMinX, x)
		cornerMaxX = max(cornerMaxX, x)
	}
	if westEqX >= cornerMinX {
		t.Errorf("west-equator easting %.2f not below corner-min easting %.2f", westEqX, cornerMinX)
	}
	if eastEqX <= cornerMaxX {
		t.Errorf("east-equator easting %.2f not above corner-max easting %.2f", eastEqX, cornerMaxX)
	}
}
