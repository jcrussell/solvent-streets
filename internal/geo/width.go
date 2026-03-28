package geo

import "strconv"

// Default road widths by highway classification (meters)
var defaultWidths = map[string]float64{
	"motorway":       14.0,
	"motorway_link":  7.0,
	"trunk":          12.0,
	"trunk_link":     6.0,
	"primary":        10.0,
	"primary_link":   5.0,
	"secondary":      8.0,
	"secondary_link": 4.5,
	"tertiary":       7.0,
	"tertiary_link":  4.0,
	"residential":    5.5,
	"unclassified":   5.0,
	"service":        3.5,
	"living_street":  4.0,
}

const laneWidth = 3.7 // meters per lane

// InferWidth returns the estimated road width in meters based on OSM tags.
// Priority: explicit width tag > lanes-based > highway classification fallback.
func InferWidth(tags map[string]string) float64 {
	// 1. Explicit width tag — use as-is (surveyed width includes or
	// intentionally excludes parking; adding parkingAddon would double-count).
	if w, ok := tags["width"]; ok {
		if v, err := strconv.ParseFloat(w, 64); err == nil && v > 0 {
			return v
		}
	}

	// 2. Lanes-based
	if l, ok := tags["lanes"]; ok {
		if v, err := strconv.ParseFloat(l, 64); err == nil && v > 0 {
			return v*laneWidth + parkingAddon(tags)
		}
	}

	// 3. Highway classification fallback
	if hw, ok := tags["highway"]; ok {
		if w, ok := defaultWidths[hw]; ok {
			return w + parkingAddon(tags)
		}
	}

	return 5.5 // ultimate fallback: residential width
}

// Default sidewalk widths by highway classification (meters)
var defaultSidewalkWidths = map[string]float64{
	"footway":    1.5,
	"pedestrian": 3.0,
	"corridor":   2.0,
}

// InferSidewalkWidth returns the estimated sidewalk width in meters based on OSM tags.
// Priority: explicit width tag > highway classification fallback.
func InferSidewalkWidth(tags map[string]string) float64 {
	if w, ok := tags["width"]; ok {
		if v, err := strconv.ParseFloat(w, 64); err == nil && v > 0 {
			return v
		}
	}
	if hw, ok := tags["highway"]; ok {
		if w, ok := defaultSidewalkWidths[hw]; ok {
			return w
		}
	}
	return 1.5 // fallback: standard sidewalk width
}

func parkingAddon(tags map[string]string) float64 {
	addon := 0.0
	if tags["parking:left"] == "lane" || tags["parking:left"] == "parallel" {
		addon += 2.4
	}
	if tags["parking:right"] == "lane" || tags["parking:right"] == "parallel" {
		addon += 2.4
	}
	return addon
}
