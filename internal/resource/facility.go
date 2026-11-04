package resource

import (
	"context"
	"fmt"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

// facilityPrefix namespaces facility resource types. It uses a hyphen, not
// a colon, because ":" is the scope separator in Type (With/Bare/Scope) and a
// colon here would read as a jurisdiction scope.
const facilityPrefix = "facility-"

// FacilityType returns the resource type facility features of a service are
// stored under: "facility-fire" for fire stations. Facilities are points of
// service (internal/services) rather than paved surface, so these types are
// deliberately NOT in All: every compute/export loop over All ignores them.
func FacilityType(service string) Type {
	return Type(facilityPrefix + service)
}

// IsFacility reports whether t is a facility type.
func (t Type) IsFacility() bool {
	return strings.HasPrefix(string(t), facilityPrefix)
}

// FacilityService returns the service name of a facility type ("fire" for
// "facility-fire"), or "" when t is not a facility type.
func (t Type) FacilityService() string {
	if !t.IsFacility() {
		return ""
	}
	return strings.TrimPrefix(string(t), facilityPrefix)
}

// Facility is the Source for one service's facilities (e.g. fire stations).
// It exists so facility ingest can reuse the Overpass client, the ingest
// command and the status command unchanged; it has no paved surface, so
// BufferFeaturesPaired yields nothing and it never takes part in coverage.
type Facility struct {
	// Service is the service name ("fire"); see FacilityType.
	Service string
	// Selectors are complete bracketed Overpass tag filters, e.g.
	// `["amenity"="fire_station"]`; the query ORs node/way/relation over all
	// of them. The parser turns each match into a Point feature.
	Selectors []string
}

var _ Source = (*Facility)(nil)

func (f *Facility) Type() Type            { return FacilityType(f.Service) }
func (f *Facility) HasCohorts() bool      { return false }
func (f *Facility) AsphaltSurfaced() bool { return false }

// OverpassQuery requests nodes, ways and relations matching any selector,
// with inline geometry so ways and relations can be collapsed to a point.
func (f *Facility) OverpassQuery(bbox [4]float64) string {
	var b strings.Builder
	b.WriteString("[out:json][timeout:120];\n(\n")
	for _, sel := range f.Selectors {
		for _, kind := range []string{"node", "way", "relation"} {
			fmt.Fprintf(&b, "  %s%s(%f,%f,%f,%f);\n", kind, sel, bbox[0], bbox[1], bbox[2], bbox[3])
		}
	}
	b.WriteString(");\nout geom;")
	return b.String()
}

// BufferFeaturesPaired returns nil: a facility has no paved surface to
// measure. Coverage of the service is computed by internal/services from the
// stored points instead.
func (f *Facility) BufferFeaturesPaired(_ context.Context, _ []Feature, _ geo.Projector) []BufferedFeature {
	return nil
}
