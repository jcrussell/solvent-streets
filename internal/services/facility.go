package services

import (
	"encoding/json"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// ConfigFacilityID is the stable id of a hand-placed [[cities.facilities]]
// entry: its service and position, not its index in the list, so inserting,
// removing or reordering entries between a compute and an export never makes
// a stored hex reference resolve to a different station. Two entries at the
// same position share an id; Facilities keeps the first.
func ConfigFacilityID(service string, lon, lat float64) string {
	return fmt.Sprintf("config:%s:%.6f,%.6f", service, lon, lat)
}

// Facilities gathers a service's points of service: the ingested rows (their
// stored Point geometry; any other geometry is skipped) followed by the
// configured entries for that service. Compute routes from this list and
// export resolves stored refs against it, so both see the same ids.
func Facilities(rows []resource.Feature, configured []config.FacilityConfig, service string) []Facility {
	var out []Facility
	for _, r := range rows {
		if lon, lat, ok := pointLonLat(r.GeometryJSON); ok {
			out = append(out, Facility{ID: r.ID, Name: r.Name, Lon: lon, Lat: lat, Source: "osm"})
		}
	}
	seen := map[string]bool{}
	for _, fc := range configured {
		id := ConfigFacilityID(service, fc.Lon, fc.Lat)
		if fc.Service != service || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, Facility{ID: id, Name: fc.Name, Lon: fc.Lon, Lat: fc.Lat, Source: "config"})
	}
	return out
}

// pointLonLat parses a GeoJSON Point geometry.
func pointLonLat(gjson string) (lon, lat float64, ok bool) {
	var pt struct {
		Type        string     `json:"type"`
		Coordinates [2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(gjson), &pt); err != nil || pt.Type != "Point" {
		return 0, 0, false
	}
	return pt.Coordinates[0], pt.Coordinates[1], true
}
