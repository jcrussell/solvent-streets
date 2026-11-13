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
