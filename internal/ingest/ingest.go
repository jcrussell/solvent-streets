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

func AllSources() []Source {
	return []Source{
		&OverpassSource{},
		&ArcGISSource{},
	}
}

func SourceByName(name string) (Source, error) {
	for _, s := range AllSources() {
		if s.Name() == name {
			return s, nil
		}
	}
	return nil, fmt.Errorf("unknown source: %s", name)
}
