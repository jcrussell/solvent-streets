// Package hexmetric is the registry of per-hex metrics: named, unit-bearing
// values that a compute pass stores per hex (db.HexMetric / hex_metrics),
// the exporter writes to hex-metrics.json, and the dashboard offers in its
// "Color by" selector. Fire response time is the first stored metric; the
// pavement colorings (coverage, area, spaces, est. cost) are derived metrics
// registered in builtin.go, which the dashboard computes from hexgrid.geojson
// so every hex coloring goes through this one registry. A new metric is a Spec
// registered from its producing package's init(), and nothing on the export
// or client side changes.
//
// Keys are lowercase snake_case with a unit suffix when the value carries one
// ("fire_response_s", "road_m2_per_resident"); the dashboard reads Format and
// Scale to render a value and to paint hexes, so a Spec fully describes how
// its numbers should look without any metric-specific client code.
package hexmetric

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Scale kinds the dashboard paints (Kind picks the colour ramp) and
// transforms. Transform is ADVISORY: the client paints the explicit Bands
// edges as discrete steps and never rescales values, so a metric that wants
// a logarithmic legend supplies log-spaced edges and labels itself "log" for
// readers of the file.
const (
	KindSequential  = "sequential" // low → high, one hue ramp
	KindDiverging   = "diverging"  // two hues around a midpoint
	KindCoverage    = "coverage"   // the blue pavement-coverage ramp (--legend-*)
	TransformLinear = "linear"
	TransformLog    = "log"
)

// Value formats the dashboard knows how to render.
const (
	FormatInt     = "int"     // whole number with thousands separators
	FormatFloat1  = "float1"  // one decimal
	FormatMinutes = "minutes" // seconds in, "m:ss" or "N min" out
	FormatUSD     = "usd"     // dollars
	FormatPercent = "percent" // 0–100, rendered with a % sign
	FormatArea    = "area"    // square metres in, the viewer's area unit out
)

// Scale modes. Bands (the default, and what an empty Mode means so files
// written before Mode existed read unchanged) paints one discrete colour per
// band; Ramp interpolates continuously between the edges, which are then
// the colour stops.
const (
	ModeBands = "bands"
	ModeRamp  = "ramp"
)

// Derivation ops. A derived metric is computed by the dashboard from data it
// already holds instead of being stored per hex, so it follows the viewer's
// resource toggles and scope.
const (
	// OpSum sums Prop over the toggled resources, capped at Max when Max > 0.
	OpSum = "sum"
	// OpRatio divides the OpSum numerator by the stored metric Den, leaving
	// the hex empty when Den is below MinDen.
	OpRatio = "ratio"
)

// Per-resource hex properties a Derivation may combine: the coverage
// percentage and area from hexgrid.geojson, the apportioned parking spaces,
// and the client's year-1 cost estimate (the resource's year-1 need from
// hex-cost-summary.json split by the hex's share of that resource's area).
const (
	PropPct     = "pct"
	PropArea    = "area"
	PropSpaces  = "spaces"
	PropEstCost = "est_cost"
)

// Derivation describes how the dashboard computes a derived metric. See the
// Op* constants.
type Derivation struct {
	Op     string  `json:"op"`
	Prop   string  `json:"prop"`
	Max    float64 `json:"max,omitempty"`
	Den    string  `json:"den,omitempty"`
	MinDen float64 `json:"min_den,omitempty"`
}

func (d Derivation) validate(key string) error {
	switch d.Prop {
	case PropPct, PropArea, PropSpaces, PropEstCost:
	default:
		return fmt.Errorf("hexmetric: %s derives from unknown property %q", key, d.Prop)
	}
	if d.Max < 0 || d.MinDen < 0 {
		return fmt.Errorf("hexmetric: %s has a negative max or min_den", key)
	}
	switch d.Op {
	case OpSum:
		if d.Den != "" {
			return fmt.Errorf("hexmetric: %s is a sum but names a denominator", key)
		}
	case OpRatio:
		if !keyRe.MatchString(d.Den) {
			return fmt.Errorf("hexmetric: %s ratio denominator %q must be a metric key", key, d.Den)
		}
	default:
		return fmt.Errorf("hexmetric: %s has unknown derivation op %q", key, d.Op)
	}
	return nil
}

// Scale describes how a metric's values map to colours: band edges in
// ascending order (the client paints one colour per band, values at or below
// an edge fall in that band, values above the last edge in the final band).
// At least one edge is required — a bandless scale would be a single colour
// and produces an invalid map paint expression.
//
// Mode and Ticks are omitempty so a bands scale serializes exactly as it did
// before they existed (hex-metrics.json stays at v1).
type Scale struct {
	Kind      string    `json:"kind"`
	Transform string    `json:"transform"`
	Bands     []float64 `json:"bands"`
	// Mode is ModeBands (or empty) or ModeRamp; a ramp needs two or more
	// edges, which are its colour stops.
	Mode string `json:"mode,omitempty"`
	// Ticks are the legend labels for a ramp; empty labels the edges.
	Ticks []float64 `json:"ticks,omitempty"`
}

// Spec describes one per-hex metric.
type Spec struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Unit   string `json:"unit"`   // SI-ish unit of the stored value ("s", "", "m2", "usd")
	Source string `json:"source"` // one line on where the numbers come from
	Format string `json:"format"` // one of the Format* constants
	Scale  Scale  `json:"scale"`
	// Additive marks a count whose per-hex values sum to a meaningful city
	// total (population, housing units). The stats card then leads with
	// that total; for a time, rate or density a sum means nothing.
	Additive bool `json:"additive,omitempty"`
	// Derive, when set, makes this a derived metric: the dashboard computes
	// it (see Derivation) and nothing is stored or exported per hex.
	Derive *Derivation `json:"derive,omitempty"`
}

// Derived reports whether s is computed by the dashboard rather than stored.
func (s Spec) Derived() bool { return s.Derive != nil }

var keyRe = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// Validate rejects a Spec the dashboard could not render: a malformed key, no
// label, an unknown format or scale kind/transform, no band edges, or band
// edges that are not strictly ascending.
func (s Spec) Validate() error {
	if !keyRe.MatchString(s.Key) {
		return fmt.Errorf("hexmetric: key %q must be lowercase snake_case", s.Key)
	}
	if s.Label == "" {
		return fmt.Errorf("hexmetric: %s has no label", s.Key)
	}
	switch s.Format {
	case FormatInt, FormatFloat1, FormatMinutes, FormatUSD, FormatPercent, FormatArea:
	default:
		return fmt.Errorf("hexmetric: %s has unknown format %q", s.Key, s.Format)
	}
	switch s.Scale.Kind {
	case KindSequential, KindDiverging, KindCoverage:
	default:
		return fmt.Errorf("hexmetric: %s has unknown scale kind %q", s.Key, s.Scale.Kind)
	}
	switch s.Scale.Transform {
	case TransformLinear, TransformLog:
	default:
		return fmt.Errorf("hexmetric: %s has unknown scale transform %q", s.Key, s.Scale.Transform)
	}
	if len(s.Scale.Bands) == 0 {
		return fmt.Errorf("hexmetric: %s needs at least one band edge", s.Key)
	}
	if !ascending(s.Scale.Bands) {
		return fmt.Errorf("hexmetric: %s band edges must be strictly ascending, got %v", s.Key, s.Scale.Bands)
	}
	switch s.Scale.Mode {
	case "", ModeBands:
		if len(s.Scale.Ticks) > 0 {
			return fmt.Errorf("hexmetric: %s has ticks but is not a ramp", s.Key)
		}
	case ModeRamp:
		if len(s.Scale.Bands) < 2 {
			return fmt.Errorf("hexmetric: %s ramp needs at least two stops", s.Key)
		}
		if !ascending(s.Scale.Ticks) {
			return fmt.Errorf("hexmetric: %s ticks must be strictly ascending, got %v", s.Key, s.Scale.Ticks)
		}
	default:
		return fmt.Errorf("hexmetric: %s has unknown scale mode %q", s.Key, s.Scale.Mode)
	}
	if s.Derive != nil {
		return s.Derive.validate(s.Key)
	}
	return nil
}

func ascending(xs []float64) bool {
	for i := 1; i < len(xs); i++ {
		if xs[i] <= xs[i-1] {
			return false
		}
	}
	return true
}

var (
	mu       sync.RWMutex
	registry = map[string]Spec{}
)

// ErrDuplicate is returned by TryRegister when the key is already taken.
var ErrDuplicate = errors.New("hexmetric: key already registered")

// Register adds s to the registry. It panics on an invalid Spec or a
// duplicate key: both are programmer errors in a package init(), where a
// returned error would have nowhere to go.
func Register(s Spec) {
	if err := TryRegister(s); err != nil {
		panic(err)
	}
}

// TryRegister is Register with an error return, for callers (and tests) that
// want to handle a bad Spec instead of panicking.
func TryRegister(s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[s.Key]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicate, s.Key)
	}
	registry[s.Key] = s
	return nil
}

// Lookup returns the Spec registered under key.
func Lookup(key string) (Spec, bool) {
	mu.RLock()
	defer mu.RUnlock()
	s, ok := registry[key]
	return s, ok
}

// All returns every registered Spec sorted by key, so output built from it
// is deterministic.
func All() []Spec {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Spec, 0, len(registry))
	for _, s := range registry {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// StoredSpecs returns the registered Specs whose values a compute pass stores
// per hex (hex_metrics), sorted by key. These are what the exporter writes to
// hex-metrics.json.
func StoredSpecs() []Spec { return filterSpecs(false) }

// DerivedSpecs returns the registered Specs the dashboard computes itself,
// sorted by key. The exporter ships them in the page config so they exist for
// every city, with or without hex-metrics.json.
func DerivedSpecs() []Spec { return filterSpecs(true) }

func filterSpecs(derived bool) []Spec {
	var out []Spec
	for _, s := range All() {
		if s.Derived() == derived {
			out = append(out, s)
		}
	}
	return out
}
