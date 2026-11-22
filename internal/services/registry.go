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
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/hexmetric"
	"github.com/jcrussell/solvent-streets/internal/metric"
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
	// Metric is the catalog key (internal/metric) the coverage is stored under.
	Metric string
	// FacilityNoun names one facility in the dashboard ("station"): the
	// stats panel's "Stations serving the city" row and its completeness
	// caveat. Presentation only, read by export.
	FacilityNoun string
	// SparsePer100k is the completeness floor export flags a city's facility
	// list against: fewer facilities inside the city boundary serving it
	// than this per 100,000 residents, or none at all, and the list is
	// probably missing some (OSM's coverage is uneven). Presentation only,
	// read by export; 0 applies only the none-inside test.
	SparsePer100k float64
	// SourceFormat is the metric's methodology line with one %s for the
	// turnout phrase (SourceText), so the published line states the turnout
	// the times include. Presentation only, read by export.
	SourceFormat string
}

// SourceText is the metric's methodology line for this spec: the turnout it
// states is s.TurnoutS, credited to NFPA 1710 only while it is the
// registry's default (a [[services]] turnout_s override is not NFPA's). A
// spec without a SourceFormat has no methodology line and returns "".
func (s Spec) SourceText() string {
	if s.SourceFormat == "" {
		return ""
	}
	turnout := fmt.Sprintf("%g s turnout", s.TurnoutS)
	if def, ok := Registry[s.Name]; ok && s.TurnoutS == def.TurnoutS {
		turnout = "NFPA 1710 " + turnout
	}
	return fmt.Sprintf(s.SourceFormat, turnout)
}

// legendBandStepS is the width of each legend band after the first edge.
const legendBandStepS = 120

// CoveredS is the total response time, in seconds, within which a hex counts
// as covered: turnout plus the travel threshold.
func (s Spec) CoveredS() float64 { return s.TurnoutS + s.TravelThresholdS }

// LegendBands returns the legend band edges in total seconds for this spec:
// the first edge is turnout + travel threshold (the "covered" line), then
// three two-minute steps. Derived rather than stored so a [[services]]
// override of turnout_s or travel_threshold_s moves the legend with it.
func (s Spec) LegendBands() []float64 {
	first := s.CoveredS()
	return []float64{first, first + legendBandStepS, first + 2*legendBandStepS, first + 3*legendBandStepS}
}

// Resolve applies the config's [[services]] override for s, if any: each
// field the override sets replaces the registry default, an explicit 0
// included (turnout_s = 0 times travel alone).
func Resolve(s Spec, overrides []config.ServiceOverride) Spec {
	for _, o := range overrides {
		if o.Name != s.Name {
			continue
		}
		if o.TurnoutS != nil {
			s.TurnoutS = *o.TurnoutS
		}
		if o.TravelThresholdS != nil {
			s.TravelThresholdS = *o.TravelThresholdS
		}
	}
	return s
}

// ValidateConfig rejects a config whose [[services]] names or
// [[cities.facilities]] service values are not registered services: a typo
// would otherwise leave the registry defaults silently in force, or drop a
// hand-placed station without a word. internal/config cannot check this
// itself without importing the registry, so the factory runs it as part of
// loading the config for every command. Errors are joined under
// config.ErrInvalidConfig, the invalid-config exit code.
func ValidateConfig(cfg *config.Config) error {
	for _, o := range cfg.Services {
		if _, ok := Registry[o.Name]; !ok {
			return errors.Join(config.ErrInvalidConfig, fmt.Errorf(
				"[[services]] name %q is not a registered service (registered: %s)", o.Name, strings.Join(Names(), ", ")))
		}
	}
	for i := range cfg.Cities {
		city := &cfg.Cities[i]
		if city.SourceConfigID == "" {
			continue
		}
		// A city reached through [[include]] runs under its declaring file's
		// [[services]] (config.ResolvedServices), which the loop above never
		// sees.
		for _, o := range cfg.ResolvedServices(city) {
			if _, ok := Registry[o.Name]; !ok {
				return errors.Join(config.ErrInvalidConfig, fmt.Errorf(
					"%s: [[services]] name %q in its own config is not a registered service (registered: %s)",
					config.CityPath(i, city), o.Name, strings.Join(Names(), ", ")))
			}
		}
	}
	for i, city := range cfg.Cities {
		for j, f := range city.Facilities {
			if _, ok := Registry[f.Service]; !ok {
				return errors.Join(config.ErrInvalidConfig, fmt.Errorf(
					"%s.facilities[%d].service %q is not a registered service (registered: %s)",
					config.CityPath(i, &city), j, f.Service, strings.Join(Names(), ", ")))
			}
		}
	}
	return nil
}

// ForMetric returns the service whose coverage is stored under the metric key.
func ForMetric(key string) (Spec, bool) {
	for _, s := range Registry {
		if s.Metric == key {
			return s, true
		}
	}
	return Spec{}, false
}

// SubjectUnreachable is the hex_metrics subject a service run records its
// unreachable hexes under (Coverage.Unreachable), beside the metric's timed
// rows (subject ""): one row per hex on the road network that no facility
// reaches. The row's value carries nothing (it is stored as 0); the subject
// is the record. Readers of the times list subject "" and never see these.
const SubjectUnreachable = "unreachable"

// Registry holds every known service. Fire is the only shipped service; the
// values follow NFPA 1710 (career fire departments): 80 s turnout for fire
// and special operations, 240 s travel for the first-arriving engine. The
// legend bands (LegendBands) are total time: turnout + the travel threshold,
// then two-minute steps.
var Registry = map[string]Spec{
	"fire": {
		Name:             "fire",
		Label:            "Fire response",
		OSMSelector:      []string{`["amenity"="fire_station"]`},
		TurnoutS:         80,
		TravelThresholdS: 240,
		Speeds:           DefaultRoadSpeeds,
		Metric:           metric.FireResponseS,
		FacilityNoun:     "station",
		// About the 5th percentile of the v0.5.0 showcase (2.05 per 100k
		// over the 379 cities with census data and a station inside), where
		// the cities known to be missing stations in OSM sit: Bismarck 1.4,
		// Montgomery 1.5, Stockton 1.9.
		SparsePer100k: 2,
		SourceFormat:  "Estimated time from the nearest fire station in OpenStreetMap: %s plus driving over the road network at posted or typical speeds.",
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
	// A service's coverage is stored per hex under its Metric, so the key
	// must be a per-hex catalog key.
	for _, name := range Names() {
		if err := metric.CheckHex(Registry[name].Metric); err != nil {
			panic(fmt.Sprintf("services: %s: %v", name, err))
		}
	}
	hexmetric.Register(hexmetric.Spec{
		Key:    metric.FireResponseS,
		Label:  "Fire response",
		Source: Registry["fire"].SourceText(),
		Format: hexmetric.FormatMinutes,
		Scale: hexmetric.Scale{
			Kind:      hexmetric.KindSequential,
			Transform: hexmetric.TransformLinear,
			Bands:     Registry["fire"].LegendBands(),
		},
	})
}
