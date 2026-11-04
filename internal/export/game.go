package export

import (
	"html/template"
	"io/fs"
)

// ParseGameTemplate returns the parsed template tree for the /play game page:
// templates/game.html.tmpl plus the selectors partial it shares with the index
// page (the city selector's tag filters). It does not pull in the methodology
// or theme partials, which only the index references. Shared between the
// static export and the live server so the parse path stays single-sourced.
func ParseGameTemplate() (*template.Template, error) {
	return ParseGameTemplateFS(templatesFS)
}

// ParseGameTemplateFS is the fs.FS-parametrized form of ParseGameTemplate
// (byob-interfaces.3), matching ParseIndexTemplateFS: production callers use
// ParseGameTemplate (embedded templatesFS); tests can pass a synthetic fs.FS.
func ParseGameTemplateFS(source fs.FS) (*template.Template, error) {
	return parseTemplateFiles(source, "game",
		"templates/game.html.tmpl",
		"templates/selectors.html.tmpl",
	)
}
