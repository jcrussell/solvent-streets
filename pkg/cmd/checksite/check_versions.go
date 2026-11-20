package checksite

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jcrussell/solvent-streets/internal/export"
)

// checkVersions FAILs a data file whose format version is not the one this
// binary's page reads: the page refuses hexgrid.geojson and hex-metrics.json
// of another format, and falls back from compare.json to the per-city files.
// A tree exported by an older pvmt and republished with newer page assets
// would otherwise ship a site whose map never draws. A missing file is left
// to the structure check, and hex-metrics.json is optional.
func (r *runner) checkVersions(s *site) {
	for _, ex := range s.examples {
		if p := filepath.Join(ex.dir, export.CompareFileName); fileExists(p) {
			r.checkVersion(ex.slug, export.CompareFileName, p, export.CompareFileVersion)
		}
	}
	for _, d := range s.dataDirsWithLabel() {
		for _, f := range []struct {
			name string
			want int
		}{
			{export.HexGridFileName, export.HexGridFileVersion},
			{export.HexMetricsFileName, export.HexMetricsFileVersion},
		} {
			if p := filepath.Join(d.dir, f.name); fileExists(p) {
				r.checkVersion(d.label, f.name, p, f.want)
			}
		}
	}
}

// checkVersion checks one file's "v" against want.
func (r *runner) checkVersion(label, name, path string, want int) {
	v, err := readVersion(path)
	if err != nil {
		r.failf(chkVersions, "%s: cannot read %s's format version: %v", label, name, err)
		return
	}
	if v != want {
		r.failf(chkVersions, "%s: %s is format v%d, the page reads v%d — re-export with this pvmt", label, name, v, want)
		return
	}
	r.passf(chkVersions, "%s: %s is format v%d", label, name, v)
}

// readVersion reads the top-level "v" of the JSON object at path, or 0 when
// it has none, which no format uses. It streams the file and steps over
// every other value token by token: a hexgrid.geojson writes "v" after its
// features, which run to tens of megabytes, and need not be held in memory.
func readVersion(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	if t, err := dec.Token(); err != nil {
		return 0, err
	} else if t != json.Delim('{') {
		return 0, errors.New("not a JSON object")
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return 0, err
		}
		if t == "v" {
			var v int
			if err := dec.Decode(&v); err != nil {
				return 0, fmt.Errorf("v: %w", err)
			}
			return v, nil
		}
		if err := skipValue(dec); err != nil {
			return 0, err
		}
	}
	return 0, nil
}

// skipValue consumes the next JSON value from dec without keeping it.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
	}
}
