package ingest

import (
	"fmt"
	"net/http"

	"pvmt/internal/db"
	"pvmt/internal/resource"
)

type Source interface {
	Name() string
	Fetch(client *http.Client, rt resource.ResourceType) ([]db.Feature, error)
}

func AllSources(bbox [4]float64, arcgisURL string) []Source {
	return []Source{
		&OverpassSource{BBox: bbox},
		&ArcGISSource{BBox: bbox, URL: arcgisURL},
	}
}

func SourceByName(name string, bbox [4]float64, arcgisURL string) (Source, error) {
	for _, s := range AllSources(bbox, arcgisURL) {
		if s.Name() == name {
			return s, nil
		}
	}
	return nil, fmt.Errorf("unknown source: %s", name)
}
