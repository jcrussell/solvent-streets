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
	Fetch(ctx context.Context, client *http.Client, rt resource.ResourceType) ([]db.Feature, error)
}

// Options configures optional behavior applied to the sources returned by
// AllSources / SourceByName. Zero value is valid — progress writes are
// discarded when Progress is nil.
type Options struct {
	Progress io.Writer
}

func AllSources(bbox [4]float64, arcgisURL string, opts Options) []Source {
	sources := []Source{
		&OverpassSource{BBox: bbox},
	}
	if arcgisURL != "" {
		sources = append(sources, &ArcGISSource{BBox: bbox, URL: arcgisURL, Progress: opts.Progress})
	}
	return sources
}

func SourceByName(name string, bbox [4]float64, arcgisURL string, opts Options) (Source, error) {
	for _, s := range AllSources(bbox, arcgisURL, opts) {
		if s.Name() == name {
			return s, nil
		}
	}
	return nil, fmt.Errorf("unknown source: %s", name)
}
