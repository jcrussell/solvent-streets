package export

import "path/filepath"

// The files and folders of a generated site. `pvmt export` writes this
// layout, `pvmt serve` routes it, and `pvmt check-site` audits it, all by
// these names.
const (
	// IndexFileName and PlayFileName are the two pages.
	IndexFileName = "index.html"
	PlayFileName  = "play.html"
	// NojekyllFileName is the empty marker that stops GitHub Pages running
	// the site through Jekyll.
	NojekyllFileName = ".nojekyll"
	// CitiesFileName lists a multi-city site's cities (CityInfos).
	CitiesFileName = "cities.json"
	// DataDirName holds one city's data files: at the site root for a
	// single-city site, under CitiesDirName/<slug>/ for a multi-city one.
	DataDirName   = "data"
	CitiesDirName = "cities"
)

// SiteRootFiles are the files every site root carries: the pages, the
// .nojekyll marker and the browser assets (Assets.Names).
func SiteRootFiles() []string {
	return append([]string{IndexFileName, PlayFileName, NojekyllFileName}, assetNames...)
}

// CityDataDir is where a multi-city site under root keeps slug's data files.
func CityDataDir(root, slug string) string {
	return filepath.Join(root, CitiesDirName, slug, DataDirName)
}

// CityDataPrefix is the URL prefix a page fetches slug's data files under,
// ahead of DataDirName: "cities/<slug>/".
func CityDataPrefix(slug string) string {
	return CitiesDirName + "/" + slug + "/"
}

// A static export leaves a data file out when the city has nothing to put in
// it, and the page treats the missing file as empty. The live server answers
// every data file, so for those cities it serves the documents below: what
// each builder's output looks like with nothing in it.

// EmptyHexGrid is hexgrid.geojson for a city with no per-hex results. It
// carries the file version: the page refuses a hex grid whose version it does
// not recognise, which would turn "no hex stats yet" into a failed city load.
func EmptyHexGrid() map[string]any {
	return map[string]any{"type": "FeatureCollection", "v": HexGridFileVersion, "features": []any{}}
}

// EmptyBoundary is boundary.geojson for a city with no boundary stored.
func EmptyBoundary() map[string]any {
	return map[string]any{"type": "FeatureCollection", "features": []any{}}
}

// EmptyHexMetrics is hex-metrics.json for a city with no metric rows.
func EmptyHexMetrics() HexMetricsJSON {
	return HexMetricsJSON{V: HexMetricsFileVersion, Metrics: map[string]MetricJSON{}}
}
