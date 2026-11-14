package export

// The per-city data files, by the names the exporter writes them under, the
// server routes them at, the pages fetch and check-site budgets.
const (
	BoundaryFileName       = "boundary.geojson"
	ForecastFileName       = "forecast.json"
	ForecastSeedFileName   = "forecast_seed.json"
	HexCostSummaryFileName = "hex-cost-summary.json"
	HexGridFileName        = "hexgrid.geojson"
	MetaFileName           = "meta.json"
	ScenariosFileName      = "scenarios.json"
	HexMetricsFileName     = "hex-metrics.json"
)

// DataFileNames is the single source of truth for the per-city data files a
// publish-ready export writes into a city's data directory (DataDirName,
// CityDataDir). Some are written conditionally (the boundary only when one
// is stored, the forecast trio only when forecasts are present), but for a
// fully-computed, publish-ready city all of them are expected: `pvmt
// check-site` treats a missing one as a failure.
//
// Sorted for deterministic comparison against an on-disk directory listing.
var DataFileNames = []string{
	BoundaryFileName,
	ForecastFileName,
	ForecastSeedFileName,
	HexCostSummaryFileName,
	HexGridFileName,
	MetaFileName,
	ScenariosFileName,
}

// OptionalDataFileNames are per-city data files a publish-ready export MAY
// carry: written only when the city has the data (hex-metrics.json needs a
// `pvmt services compute` run), so `pvmt check-site` treats a missing one as
// fine and a present one as expected rather than extra. Sorted.
var OptionalDataFileNames = []string{
	HexMetricsFileName,
}

// AllDataFileNames is DataFileNames followed by OptionalDataFileNames: every
// filename the exporter can write into a city data directory.
func AllDataFileNames() []string {
	out := make([]string, 0, len(DataFileNames)+len(OptionalDataFileNames))
	out = append(out, DataFileNames...)
	return append(out, OptionalDataFileNames...)
}
