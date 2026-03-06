package resource

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
	ProcessFeatures(features []Feature) (string, float64, error) // returns (unionGeoJSON, areaSqFt, error)
}

var All = []ResourceType{
	&Pavement{},
	&Parking{},
}

func ByName(name string) ResourceType {
	for _, r := range All {
		if r.Name() == name {
			return r
		}
	}
	return nil
}
