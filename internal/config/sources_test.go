package config

import (
	"slices"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

func TestPavementSources(t *testing.T) {
	for _, tc := range []struct {
		city CityConfig
		want []string
	}{
		{CityConfig{Overpass: true}, []string{resource.SourceOverpass}},
		{CityConfig{ArcGISURL: "https://example.test/arcgis"}, []string{resource.SourceArcGIS}},
		{CityConfig{Overpass: true, ArcGISURL: "https://example.test/arcgis"}, []string{resource.SourceOverpass, resource.SourceArcGIS}},
		{CityConfig{}, nil},
	} {
		if got := tc.city.PavementSources(); !slices.Equal(got, tc.want) {
			t.Errorf("PavementSources(overpass=%v, arcgis=%q) = %v, want %v", tc.city.Overpass, tc.city.ArcGISURL, got, tc.want)
		}
	}
}
