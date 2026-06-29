package export

import (
	"fmt"
	"html/template"
	"io/fs"

	"github.com/jcrussell/solvent-streets/internal/units"
)

// ParseGameTemplate returns the parsed template tree for the /play game page.
// It mirrors ParseIndexTemplate (same indexFuncMap so the page can use the
// shared area helpers and the parse can't drift between the two pages), but
// parses only templates/game.html.tmpl: the stub doesn't pull in the
// methodology/theme partials the index references via {{template ...}}, so
// parsing them here would be dead weight. Shared between any future static
// export and the live server so the parse path stays single-sourced.
func ParseGameTemplate(sys units.System) (*template.Template, error) {
	return ParseGameTemplateFS(templatesFS, sys)
}

// ParseGameTemplateFS is the fs.FS-parametrized form of ParseGameTemplate
// (byob-interfaces.3), matching ParseIndexTemplateFS: production callers use
// ParseGameTemplate (embedded templatesFS); tests can pass a synthetic fs.FS.
func ParseGameTemplateFS(source fs.FS, sys units.System) (*template.Template, error) {
	const name = "templates/game.html.tmpl"
	data, err := fs.ReadFile(source, name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	tmpl, err := template.New("game").Funcs(indexFuncMap(sys)).Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	return tmpl, nil
}
