package resource

// Metric keys of the pavement results, as stored in city_metrics and
// hex_metrics (internal/db, 001_init.sql): what is measured, snake_case with
// its unit. A row's subject (see Subject) says what it is measured over.
//
// internal/hexmetric registers paved_area_m2 and parking_spaces only as
// DERIVED specs (Spec.Derive): the dashboard computes those colorings from
// hexgrid.geojson so they follow the resource and scope toggles, and never
// reads the stored rows through the metric registry.
const (
	// MetricPavedArea is paved area in square meters: a resource's city total,
	// one highway class's share of it, or the area inside one hex.
	MetricPavedArea = "paved_area_m2"
	// MetricCoveragePct is the paved share of one hex, in percent.
	MetricCoveragePct = "coverage_pct"
	// MetricFeatureCount counts the features a total or class covers.
	MetricFeatureCount = "feature_count"
	// MetricParkingSpaces is estimated parking capacity (see
	// EstimateParkingSpaces); written for parking subjects only.
	MetricParkingSpaces = "parking_spaces"
	// MetricDefaultDecayRate is a hex's road decay rate, per year, at no
	// decay_rate override: the area-weighted blend of its road classes'
	// default rates (DefaultDecayBlends). Written per hex for roads:city only;
	// export scales it by the override for the Play board's k.
	MetricDefaultDecayRate = "default_decay_rate"
)

// Subject returns the result-row subject t's measures are stored under: t
// itself, scope included ("roads", "roads:city", "combined:city"), for the
// pavement resources and the combined pass, and "" for anything else.
// Producers that are not about one resource (fire response, census) write
// subject "".
func Subject(t Type) string {
	switch t.Bare() {
	case TypeRoads, TypeParking, TypeSidewalks, TypeCombined:
		return string(t)
	}
	return ""
}
