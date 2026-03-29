package resource

import "pvmt/internal/geo"

type Feature struct {
	ID           string
	Name         string
	Tags         map[string]string
	GeometryJSON string // GeoJSON geometry string
	SourceAPI    string
}

type ResourceType interface {
	Name() string
	OverpassQuery(bbox [4]float64) string
	ProcessFeatures(features []Feature, proj geo.Projector) (string, float64, error) // returns (unionGeoJSON, areaSqM, error)
	HasCohorts() bool                                                                // whether this resource type supports per-classification cohort stats
}

var All = []ResourceType{
	&Pavement{},
	&Parking{},
	&Sidewalk{},
}

func ByName(name string) ResourceType {
	for _, r := range All {
		if r.Name() == name {
			return r
		}
	}
	return nil
}
