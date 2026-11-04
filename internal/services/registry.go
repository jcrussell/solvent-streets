// Package services computes service-coverage metrics: how long it takes a
// facility of some service (a fire station, first) to reach each hex over the
// stored road network. Facilities are ingested separately
// (resource.FacilityType) and the coverage result is stored as a generic
// per-hex metric (internal/hexmetric, db.HexMetric), so adding a service is a
// Registry entry: the OSM selector that finds its facilities, its fixed
// overhead and "covered" threshold, the speed table its vehicles move at,
// and the legend bands for the dashboard.
package services

import (
	"sort"

	"github.com/jcrussell/solvent-streets/internal/hexmetric"
)

// Spec describes one service.
type Spec struct {
	// Name is the service key used in resource types ("facility-fire"),
	// CLI flags (--service fire) and config ([[services]] name = "fire").
	Name string
	// Label is the human name ("Fire response").
	Label string
	// OSMSelector lists Overpass tag filters, each one a complete bracketed
	// filter; the facility query ORs node/way/relation over all of them.
	OSMSelector []string
	// TurnoutS is the fixed overhead added to every travel time: the seconds
	// between the alarm and the vehicle moving.
	TurnoutS float64
	// TravelThresholdS is the travel time (excluding turnout) within which a
	// hex counts as covered in summaries.
	TravelThresholdS float64
	// Speeds is the road-class speed table the vehicle travels at.
	Speeds SpeedTable
	// Metric is the hexmetric key the coverage is stored under.
	Metric string
	// Bands are the legend band edges in seconds (total time incl. turnout).
	Bands []float64
}

// MetricFire is the hexmetric key for fire response time.
const MetricFire = "fire_response_s"

// Registry holds every known service. Fire is the only shipped service; the
// values follow NFPA 1710 (career fire departments): 80 s turnout for fire
// and special operations, 240 s travel for the first-arriving engine. The
// legend bands are total time, so the first edge is turnout + the 240 s
// travel threshold, then two-minute steps.
var Registry = map[string]Spec{
	"fire": {
		Name:             "fire",
		Label:            "Fire response",
		OSMSelector:      []string{`["amenity"="fire_station"]`},
		TurnoutS:         80,
		TravelThresholdS: 240,
		Speeds:           DefaultRoadSpeeds,
		Metric:           MetricFire,
		Bands:            []float64{320, 440, 560, 680},
	},
}

// Lookup returns the Spec for a service name.
func Lookup(name string) (Spec, bool) {
	s, ok := Registry[name]
	return s, ok
}

// Names returns the registered service names sorted, for flag validation
// messages and deterministic iteration.
func Names() []string {
	out := make([]string, 0, len(Registry))
	for n := range Registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func init() {
	hexmetric.Register(hexmetric.Spec{
		Key:    MetricFire,
		Label:  "Fire response",
		Unit:   "s",
		Source: "Nearest OSM fire station over the stored road network: NFPA 1710 80 s turnout plus travel at posted or class-default speeds",
		Format: hexmetric.FormatMinutes,
		Scale: hexmetric.Scale{
			Kind:      hexmetric.KindSequential,
			Transform: hexmetric.TransformLinear,
			Bands:     Registry["fire"].Bands,
		},
	})
}
