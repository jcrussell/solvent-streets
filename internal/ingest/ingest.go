package ingest

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

type Source interface {
	Name() string
	Fetch(ctx context.Context, client *http.Client, rt resource.Source) ([]db.Feature, error)
}

// Options configures optional behavior applied to the sources returned by
// AllSources / SourceByName. Zero value is valid — progress writes are
// discarded when Progress is nil.
type Options struct {
	Progress io.Writer

	// AllowPrivateArcGIS opts the ArcGIS source out of the SSRF guard
	// that rejects loopback / link-local / private destinations. Set
	// only for staging or self-hosted endpoints on internal networks.
	AllowPrivateArcGIS bool
}

// AllSources assembles the enabled sources for a city. OverpassSource is
// included only when overpass is true (the city's CityConfig.Overpass flag);
// ArcGISSource only when arcgisURL is non-empty. A city with overpass=false
// and no arcgis_url therefore yields zero sources — config validation rejects
// that combination up front (see internal/config validateCityFields).
func AllSources(bbox [4]float64, overpass bool, arcgisURL string, opts Options) []Source {
	var sources []Source
	if overpass {
		sources = append(sources, &OverpassSource{BBox: bbox})
	}
	if arcgisURL != "" {
		sources = append(sources, &ArcGISSource{
			BBox:         bbox,
			URL:          arcgisURL,
			Progress:     opts.Progress,
			AllowPrivate: opts.AllowPrivateArcGIS,
		})
	}
	return sources
}

func SourceByName(name string, bbox [4]float64, overpass bool, arcgisURL string, opts Options) (Source, error) {
	for _, s := range AllSources(bbox, overpass, arcgisURL, opts) {
		if s.Name() == name {
			return s, nil
		}
	}
	return nil, fmt.Errorf("unknown source: %s", name)
}
