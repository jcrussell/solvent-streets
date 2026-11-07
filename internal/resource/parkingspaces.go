package resource

import (
	"math"
	"strconv"
	"strings"
)

// ParkingSpaceAreaM2 is the gross area one surface parking space consumes,
// including its share of aisles and circulation: 30 m² (~325 sq ft). Planning
// references put a surface lot at 300–400 sq ft per space all-in (VTPI,
// Transportation Cost and Benefit Analysis II — Parking Costs, Litman; the
// bare 9×18 ft stall is ~15 m², the rest is drive aisle), so 30 m² sits at the
// efficient end of that range and errs toward over- rather than under-counting
// spaces in a lot. A constant rather than a config knob. Should it become one,
// add it to the compute-inputs hash (internal/config/hash.go) as an omitempty
// field whose zero value means this constant, so stored snapshots stay valid.
const ParkingSpaceAreaM2 = 30.0

// SpaceSource says how a parking-space estimate was derived.
type SpaceSource string

const (
	// SpaceSourceCapacity means the OSM capacity tag was used verbatim.
	SpaceSourceCapacity SpaceSource = "capacity"
	// SpaceSourceArea means the estimate is footprint area / ParkingSpaceAreaM2
	// (times the level count for structured parking).
	SpaceSourceArea SpaceSource = "area"
	// SpaceSourceNone means no estimate could be made (no capacity, no area).
	SpaceSourceNone SpaceSource = ""
)

// EstimateParkingSpaces estimates how many vehicles a parking feature holds.
// A parseable positive-integer `capacity` tag wins outright (that is the
// mapper's count). Otherwise the footprint area in m² is divided by
// ParkingSpaceAreaM2, and for structured parking (parking=multi-storey,
// underground or rooftop) multiplied by the level count from the first of
// parking:levels, levels, building:levels that parses to an integer above 1 —
// a garage footprint is one deck, not the whole structure. A capacity like
// "yes", "~50" or "50;60" is not a count and falls through to the area rule.
// Returns 0 and SpaceSourceNone when there is nothing to go on.
func EstimateParkingSpaces(tags map[string]string, areaM2 float64) (float64, SpaceSource) {
	if n, ok := parsePositiveInt(tags["capacity"]); ok {
		return float64(n), SpaceSourceCapacity
	}
	if areaM2 <= 0 || math.IsNaN(areaM2) || math.IsInf(areaM2, 0) {
		return 0, SpaceSourceNone
	}
	spaces := math.Floor(areaM2 / ParkingSpaceAreaM2)
	if isStructuredParking(tags["parking"]) {
		if levels, ok := parkingLevels(tags); ok && levels > 1 {
			spaces *= float64(levels)
		}
	}
	if spaces <= 0 {
		return 0, SpaceSourceNone
	}
	return spaces, SpaceSourceArea
}

// Hex-metric keys holding the per-hex parking-space estimate, one per scope.
// Compute writes them to hex_metrics against the parking compute's own
// snapshot, and export joins them back into hexgrid.geojson's per-hex
// "spaces". Deliberately NOT registered in internal/hexmetric: the dashboard's
// "parking_spaces" Color-by metric is derived from hexgrid, and a stored spec
// would duplicate it and ignore the scope toggle.
const (
	SpacesMetricAll  = "parking_spaces_all"
	SpacesMetricCity = "parking_spaces_city"
)

// SpacesMetricKey returns the hex_metrics key carrying t's per-hex parking
// spaces, or "" when t carries none (anything but parking).
func SpacesMetricKey(t Type) string {
	if t.Bare() != TypeParking {
		return ""
	}
	if t.Scope() == ScopeCity {
		return SpacesMetricCity
	}
	return SpacesMetricAll
}

// ParkingSpaceWeights returns the estimated spaces of each buffered parking
// feature, index-aligned with buffered, for apportioning onto the hex grid
// (geo.ComputeHexWeightedShares). The area used is the cleaned projected
// footprint, the same geometry the coverage pass measures.
func ParkingSpaceWeights(buffered []BufferedFeature) []float64 {
	weights := make([]float64, len(buffered))
	for i, b := range buffered {
		weights[i], _ = EstimateParkingSpaces(b.Feature.Tags, b.Geom.Area())
	}
	return weights
}

func isStructuredParking(v string) bool {
	switch v {
	case "multi-storey", "underground", "rooftop":
		return true
	}
	return false
}

func parkingLevels(tags map[string]string) (int, bool) {
	for _, key := range []string{"parking:levels", "levels", "building:levels"} {
		if n, ok := parsePositiveInt(tags[key]); ok {
			return n, true
		}
	}
	return 0, false
}

// parsePositiveInt accepts only a whole string that is a positive base-10
// integer (surrounding whitespace allowed). Anything else — ranges, lists,
// approximations, words — is not a count.
func parsePositiveInt(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
