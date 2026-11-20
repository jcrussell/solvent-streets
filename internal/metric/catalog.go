package metric

// The catalog: every metric key, declared once. A row's subject says what a
// stored value is measured over (resource.Subject, a service producer, or ""
// for a city-wide census figure).
const (
	// PavedArea is paved area: a resource's city total, one highway class's
	// part of it, or the area inside one hex.
	PavedArea = "paved_area"
	// PavedShare is the paved proportion of one hex's area.
	PavedShare = "paved_share"
	// FeatureCount counts the features a city total or class covers.
	FeatureCount = "feature_count"
	// EstimatedSpaces is estimated parking capacity, for parking subjects.
	EstimatedSpaces = "estimated_spaces"
	// DefaultDecayRate is a hex's road decay rate at no decay_rate override.
	DefaultDecayRate = "default_decay_rate"

	// TurnoutS is the turnout a service run's stored times include.
	TurnoutS = "turnout_s"
	// FireResponseS is a hex's fire response time.
	FireResponseS = "fire_response_s"

	// Population, HousingUnits and PopulationDensity are the census
	// apportionment's per-hex figures.
	Population        = "population"
	HousingUnits      = "housing_units"
	PopulationDensity = "population_density"

	// Year1Need, MaintainedAreaPerResident and Year1NeedPerResident are
	// derived by the dashboard over the toggled resources.
	Year1Need                 = "year1_need"
	MaintainedAreaPerResident = "maintained_area_per_resident"
	Year1NeedPerResident      = "year1_need_per_resident"
)

func init() {
	for _, d := range []Def{
		{PavedArea, UnitSquareMeters, Both,
			"Paved area: a resource's city total (class '' or one highway class) or the area inside one hex"},
		{PavedShare, UnitShare, Hex,
			"Paved proportion of the hex's clipped area, 0..1"},
		{FeatureCount, UnitCount, City,
			"Features a resource's city total or highway class covers"},
		{EstimatedSpaces, UnitCount, Both,
			"Estimated parking capacity (OSM capacity tags, else lot area over a per-space constant); parking subjects only"},
		{DefaultDecayRate, UnitPerYear, Hex,
			"The hex's road decay rate per year at no decay_rate override: its road classes' default rates blended by area; roads:city only"},
		{TurnoutS, UnitSeconds, City,
			"The turnout time a service run's stored response times include; subject is the service producer"},
		{FireResponseS, UnitSeconds, Hex,
			"Turnout plus travel time from the nearest fire station over the road network"},
		{Population, UnitCount, Hex,
			"2020 Census population apportioned to the hex by block area overlap"},
		{HousingUnits, UnitCount, Hex,
			"2020 Census housing units apportioned to the hex by block area overlap"},
		{PopulationDensity, UnitPerSquareMeter, Hex,
			"Residents per square meter of the hex's clipped area"},
		{Year1Need, UnitDollars, Derived,
			"Each toggled resource's year-1 need split by the hex's part of its area"},
		{MaintainedAreaPerResident, UnitSquareMetersPerResident, Derived,
			"Maintained area of the toggled resources per resident of the hex"},
		{Year1NeedPerResident, UnitDollarsPerResident, Derived,
			"Year-1 need of the toggled resources per resident of the hex"},
	} {
		Register(d)
	}
}
