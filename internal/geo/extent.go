package geo

// ProjectedBBoxExtent returns the projected envelope (minX, minY, maxX, maxY) of
// a geographic bounding box, in projected units (meters for UTM).
//
// bbox is [minLat, minLon, maxLat, maxLon] (see bbox.go). Projecting only the
// SW and NE corners is wrong: in transverse Mercator the image of a geographic
// rectangle is not axis-aligned. Easting offset grows with cosφ, peaking at the
// equator (φ = 0), so on a box straddling the equator the east/west extremes lie
// on the edges at φ = 0 — interior to the latitude span and beyond every corner.
// We therefore also sample {minLon, 0} and {maxLon, 0} when the box spans the
// equator. Northing along a parallel reaches its extreme at the central meridian
// (A = 0), which is interior to the box when the box straddles the meridian — so
// the true minY/maxY can lie below/above every corner. We therefore also sample
// the central meridian at both latitudes when the box spans it. Sampling both
// (rather than case-splitting on hemisphere) keeps this correct in either
// hemisphere.
//
// Projection errors are ignored (matching the existing call-site style); the
// projector is total over valid lon/lat.
func ProjectedBBoxExtent(proj *UTMProjector, bbox [4]float64) (minX, minY, maxX, maxY float64) {
	minLat, minLon, maxLat, maxLon := bbox[0], bbox[1], bbox[2], bbox[3]

	// lon/lat candidates: the four corners, plus the central-meridian samples
	// when the box straddles the zone's central meridian.
	pts := [][2]float64{
		{minLon, minLat}, // SW
		{maxLon, minLat}, // SE
		{minLon, maxLat}, // NW
		{maxLon, maxLat}, // NE
	}
	cmLon := float64(proj.Zone*6 - 183) // central meridian, degrees
	if minLon <= cmLon && cmLon <= maxLon {
		pts = append(pts, [2]float64{cmLon, minLat}, [2]float64{cmLon, maxLat})
	}
	// The easting extremes sit at the equator on the east/west edges when the
	// box straddles it (cosφ peaks at φ = 0), beyond every corner.
	if minLat <= 0 && 0 <= maxLat {
		pts = append(pts, [2]float64{minLon, 0}, [2]float64{maxLon, 0})
	}

	for i, p := range pts {
		x, y := proj.ToProjected(p[0], p[1])
		if i == 0 {
			minX, maxX, minY, maxY = x, x, y, y
			continue
		}
		minX = min(minX, x)
		maxX = max(maxX, x)
		minY = min(minY, y)
		maxY = max(maxY, y)
	}
	return minX, minY, maxX, maxY
}
