package export

import (
	"html/template"
	"io/fs"
	"slices"

	"github.com/jcrussell/solvent-streets/internal/config"
)

// gameHorizonAlternatives are the horizons the Play start screen offers
// besides the forecast's own: a shorter and a longer game.
var gameHorizonAlternatives = []int{20, 50}

// GameHorizons lists the horizons, in years, the Play start screen offers,
// ascending: the page's forecast horizon (which the game opens on), the
// default horizon, and gameHorizonAlternatives.
func (d TemplateData) GameHorizons() []int {
	out := append([]int{d.ForecastDefaults().HorizonYears, config.DefaultForecastYears}, gameHorizonAlternatives...)
	slices.Sort(out)
	return slices.Compact(out)
}

// ParseGameTemplate returns the parsed template tree for the /play game page:
// templates/game.html.tmpl plus the partials it shares with the index page
// (selectors: the city selector's tag filters; common: the error banner). It does not pull in the methodology
// or theme partials, which only the index references. Shared between the
// static export and the live server so the parse path stays single-sourced.
func ParseGameTemplate() (*template.Template, error) {
	return ParseGameTemplateFS(templatesFS)
}

// ParseGameTemplateFS is the fs.FS-parametrized form of ParseGameTemplate,
// matching ParseIndexTemplateFS: production callers use
// ParseGameTemplate (embedded templatesFS); tests can pass a synthetic fs.FS.
func ParseGameTemplateFS(source fs.FS) (*template.Template, error) {
	return parseTemplateFiles(source, "game",
		"templates/game.html.tmpl",
		"templates/selectors.html.tmpl",
		"templates/common.html.tmpl",
	)
}
