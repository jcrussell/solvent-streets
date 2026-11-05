package export

// DataFileNames is the single source of truth for the per-city data files a
// publish-ready export writes under data/ (single-city) or cities/<slug>/data/
// (multi-city). The exporter writes these at the call sites in export.go
// (writeCityData / exportScenariosForCity); some are written conditionally
// there (boundary only when a boundary exists, the forecast trio only when
// forecasts are present), but for a fully-computed, publish-ready city all of
// them are expected — `pvmt check-site` treats a missing one as a failure.
//
// Sorted for deterministic comparison against an on-disk directory listing.
var DataFileNames = []string{
	"boundary.geojson",
	"forecast.json",
	"forecast_seed.json",
	"hex-cost-summary.json",
	"hexgrid.geojson",
	"meta.json",
	"play-hexes.json",
	"scenarios.json",
}

// OptionalDataFileNames are per-city data files a publish-ready export MAY
// carry: written only when the city has the data (hex-metrics.json needs a
// `pvmt services compute` run), so `pvmt check-site` treats a missing one as
// fine and a present one as expected rather than extra. Sorted.
var OptionalDataFileNames = []string{
	"hex-metrics.json",
}

// AllDataFileNames is DataFileNames followed by OptionalDataFileNames: every
// filename the exporter can write into a city data directory.
func AllDataFileNames() []string {
	out := make([]string, 0, len(DataFileNames)+len(OptionalDataFileNames))
	out = append(out, DataFileNames...)
	return append(out, OptionalDataFileNames...)
}
