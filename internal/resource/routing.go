package resource

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

// ServiceAreaPadM is how far beyond the city's boundary bbox, in metres,
// service facilities and the roads they are routed over are fetched, so a
// neighbouring city's station just across the line can serve the edge hexes
// it is closest to. 5 km is the NFPA 1710 240 s first-engine travel time at
// 75 km/h: a station farther out than that could reach an edge hex within
// the threshold only by freeway. Facilities are never filtered back to the
// boundary; the hexes are, so which stations count follows from distance
// rather than from where the bbox happens to fall.
const ServiceAreaPadM = 5000.0

// FetchPadder is implemented by a Source fetched over the city's boundary
// bbox grown by FetchPadM metres on every side (geo.PadBBox) rather than over
// the bbox itself. Pavement is fetched unpadded: its stored results are
// clipped to the boundary, but its feature counts and the inverted-boundary
// gate are not.
type FetchPadder interface {
	FetchPadM() float64
}

// ExcludingQuerier is implemented by a Source that can leave out of its
// Overpass fetch what lies in an inner bbox, because another resource
// already holds it: the routing roads around a city skip the city's own
// roads, fetched over the city's boundary bbox.
type ExcludingQuerier interface {
	// OverpassQueryExcluding is OverpassQuery(bbox) without the elements
	// the same query over exclude would return.
	OverpassQueryExcluding(bbox, exclude [4]float64) string
}

// Companioned is implemented by a Source whose ingest also ingests other
// sources, with the same options, after its own: a service's facilities
// bring the routing roads they are routed over (RoutingRoads).
type Companioned interface {
	Companions() []Source
}

// FetchPadM implements FetchPadder: facilities are fetched ServiceAreaPadM
// beyond the boundary bbox.
func (f *Facility) FetchPadM() float64 { return ServiceAreaPadM }

// Companions implements Companioned: ingesting a service's facilities also
// ingests the routing roads that reach them.
func (f *Facility) Companions() []Source { return []Source{&RoutingRoads{}} }

// TypeRoutingRoads is the resource type of the roads a service is routed
// over beyond the city's own: like facilities, deliberately NOT in All.
var TypeRoutingRoads = Type("routing-roads")

// RoutingRoads is the Source for the public road network around a city,
// ServiceAreaPadM beyond its boundary bbox, from OpenStreetMap. The services
// road graph joins it to the city's own OSM roads (which share its node
// coordinates) so a station outside the city is routed in over real roads.
// A city whose roads come from Overpass fetches only the ring outside its
// boundary bbox (OverpassQueryExcluding); the rest is its own roads.
// It is never measured as pavement: it has no paved surface and is not in
// All, so neither the pavement compute nor the export reads it. Service roads
// (driveways, parking aisles) are left out to keep the fetch small: a station
// on one is placed on the street within FacilitySnapM, and inside the city
// the roads resource supplies them.
type RoutingRoads struct{}

var (
	_ Source           = (*RoutingRoads)(nil)
	_ OwnSourced       = (*RoutingRoads)(nil)
	_ FetchPadder      = (*RoutingRoads)(nil)
	_ ExcludingQuerier = (*RoutingRoads)(nil)
)

func (*RoutingRoads) Type() Type            { return TypeRoutingRoads }
func (*RoutingRoads) HasCohorts() bool      { return false }
func (*RoutingRoads) AsphaltSurfaced() bool { return false }
func (*RoutingRoads) OwnSource() string     { return SourceOverpass }
func (*RoutingRoads) FetchPadM() float64    { return ServiceAreaPadM }

// routingRoadsFilter selects the drivable public road classes.
const routingRoadsFilter = `way["highway"~"^(motorway|trunk|primary|secondary|tertiary)(_link)?$|^(unclassified|residential|living_street|road)$"]`

// OverpassQuery requests the drivable public road classes in bbox.
func (*RoutingRoads) OverpassQuery(bbox [4]float64) string {
	return fmt.Sprintf(`[out:json][timeout:%d];
(
  %s(%f,%f,%f,%f);
);
out geom;`, OverpassTimeoutS, routingRoadsFilter, bbox[0], bbox[1], bbox[2], bbox[3])
}

// OverpassQueryExcluding requests the drivable public road classes in bbox
// that do not touch exclude, as one Overpass difference. Every way that
// touches the city's boundary bbox is in the city's own roads, which the
// pavement filter fetches over that bbox with every class this one selects,
// so the ring is all the services graph needs from here: without the
// difference about half of Livermore's routing-roads download repeated its
// roads (solvent-streets-530m).
func (*RoutingRoads) OverpassQueryExcluding(bbox, exclude [4]float64) string {
	return fmt.Sprintf(`[out:json][timeout:%d];
(
  %s(%f,%f,%f,%f);
  - %s(%f,%f,%f,%f);
);
out geom;`, OverpassTimeoutS,
		routingRoadsFilter, bbox[0], bbox[1], bbox[2], bbox[3],
		routingRoadsFilter, exclude[0], exclude[1], exclude[2], exclude[3])
}

// BufferFeaturesPaired returns nil: routing roads are not measured as
// pavement.
func (*RoutingRoads) BufferFeaturesPaired(context.Context, []Feature, geo.Projector) []BufferedFeature {
	return nil
}
