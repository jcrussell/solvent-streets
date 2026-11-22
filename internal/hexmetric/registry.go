// Package hexmetric is the presentation registry of per-hex metrics: how the
// dashboard's "Color by" selector labels, formats and paints each metric of
// the catalog (internal/metric) it offers. A Spec names a catalog key and
// adds a label, a format, a colour scale and, for a metric the dashboard
// computes itself, a derivation. Stored metrics (fire response, the census
// figures) are written per hex by their producers and exported to
// hex-metrics.json; derived metrics (the pavement colorings and the
// per-resident ratios) are computed by the dashboard from hexgrid.geojson
// under the same key the stored value has. A new metric is a catalog entry
// plus a Spec registered from its producing package's init(), and nothing on
// the export or client side changes.
package hexmetric

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/jcrussell/solvent-streets/internal/metric"
)

// Scale kinds the dashboard paints (Kind picks the colour ramp) and
// transforms. Transform is ADVISORY: the client paints the explicit Bands
// edges as discrete steps and never rescales values, so a metric that wants
// a logarithmic legend supplies log-spaced edges and labels itself "log" for
// readers of the file.
const (
	KindSequential  = "sequential" // low → high, one hue ramp
	KindDiverging   = "diverging"  // two hues around a midpoint
	TransformLinear = "linear"
	TransformLog    = "log"
)

// Groups the Color by menu, the city cards and the region card show the
// metrics under, in this order.
const (
	GroupPavement = "pavement"
	GroupPeople   = "people"
	GroupServices = "services"
)

// groupRank orders the groups; a group not listed here is invalid.
var groupRank = map[string]int{GroupPavement: 0, GroupPeople: 1, GroupServices: 2}

// Value formats the dashboard knows how to render.
const (
	FormatInt     = "int"     // whole number with thousands separators
	FormatFloat1  = "float1"  // one decimal
	FormatMinutes = "minutes" // seconds in, "m:ss" or "N min" out
	FormatUSD     = "usd"     // dollars
	FormatPercent = "percent" // a 0..1 share in, "N%" out
	FormatArea    = "area"    // square metres in, the viewer's area unit out
	FormatDensity = "density" // per m² in, per the viewer's large area unit (km² or sq mi) out
)

// Derivation ops. A derived metric is computed by the dashboard from data it
// already holds instead of being stored per hex, so it follows the viewer's
// resource toggles and scope.
const (
	// OpSum sums Prop over the toggled resources, capped at Max when Max > 0.
	OpSum = "sum"
)

// Per-resource hex properties a Derivation may combine: the covered share and
// area from hexgrid.geojson, the apportioned parking spaces,
// and the client's year-1 need (the resource's year-1 need from
// hex-cost-summary.json split by the hex's part of that resource's area).
const (
	PropShare     = "share"
	PropArea      = "area"
	PropSpaces    = "spaces"
	PropYear1Need = "year1_need"
)

// Derivation describes how the dashboard computes a derived metric. See the
// Op* constants.
type Derivation struct {
	Op   string  `json:"op"`
	Prop string  `json:"prop"`
	Max  float64 `json:"max,omitempty"`
}

func (d Derivation) validate(key string) error {
	switch d.Prop {
	case PropShare, PropArea, PropSpaces, PropYear1Need:
	default:
		return fmt.Errorf("hexmetric: %s derives from unknown property %q", key, d.Prop)
	}
	if d.Max < 0 {
		return fmt.Errorf("hexmetric: %s has a negative max", key)
	}
	switch d.Op {
	case OpSum:
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
type Scale struct {
	Kind      string    `json:"kind"`
	Transform string    `json:"transform"`
	Bands     []float64 `json:"bands"`
}

// Spec describes how the dashboard presents one catalog metric per hex.
type Spec struct {
	// Key is the metric's catalog key (internal/metric).
	Key   string `json:"key"`
	Label string `json:"label"`
	// Unit is the catalog's unit for Key; Register fills it.
	Unit   string `json:"unit"`
	Source string `json:"source"` // one line on where the numbers come from
	Format string `json:"format"` // one of the Format* constants
	Scale  Scale  `json:"scale"`
	// Group is one of the Group* constants and Order the metric's place
	// within it; the dashboard lists metrics by group, then Order.
	Group string `json:"group"`
	Order int    `json:"order"`
	// Additive marks a count whose per-hex values sum to a meaningful city
	// total (population, housing units): the exporter then gives its summary
	// a total, which the stats card leads with; for a time, rate or density a
	// sum means nothing. Not emitted: the page reads the total, not the flag.
	Additive bool `json:"-"`
	// Derive, when set, makes this a derived metric: the dashboard computes
	// it (see Derivation) and nothing is stored or exported per hex.
	Derive *Derivation `json:"derive,omitempty"`
}

// Derived reports whether s is computed by the dashboard rather than stored.
func (s Spec) Derived() bool { return s.Derive != nil }

// Validate rejects a Spec the dashboard could not render: a key the catalog
// does not hold per hex (a stored Spec) or at all (a derived one), no label,
// an unknown format or scale kind/transform, no band edges, or band edges
// that are not strictly ascending.
func (s Spec) Validate() error {
	def, ok := metric.Lookup(s.Key)
	switch {
	case !ok:
		return fmt.Errorf("hexmetric: %q is not in the metric catalog (internal/metric)", s.Key)
	case s.Derive == nil && !def.Level.AtHex():
		return fmt.Errorf("hexmetric: %s is a %s metric, not stored per hex, so it needs a derivation", s.Key, def.Level)
	case s.Derive != nil && def.Level == metric.City:
		return fmt.Errorf("hexmetric: %s is a city-wide metric and has no per-hex value", s.Key)
	case s.Unit != "" && s.Unit != def.Unit:
		return fmt.Errorf("hexmetric: %s unit %q differs from the catalog's %q", s.Key, s.Unit, def.Unit)
	}
	if s.Label == "" {
		return fmt.Errorf("hexmetric: %s has no label", s.Key)
	}
	if _, ok := groupRank[s.Group]; !ok {
		return fmt.Errorf("hexmetric: %s has unknown group %q", s.Key, s.Group)
	}
	switch s.Format {
	case FormatInt, FormatFloat1, FormatMinutes, FormatUSD, FormatPercent, FormatArea, FormatDensity:
	default:
		return fmt.Errorf("hexmetric: %s has unknown format %q", s.Key, s.Format)
	}
	switch s.Scale.Kind {
	case KindSequential, KindDiverging:
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
	def, _ := metric.Lookup(s.Key)
	s.Unit = def.Unit
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

// All returns every registered Spec in display order: by group, then
// Order, then key, so output built from it is deterministic.
func All() []Spec {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Spec, 0, len(registry))
	for _, s := range registry {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// less orders a before b for display: by group, then Order, then key.
func less(a, b Spec) bool {
	if ga, gb := groupRank[a.Group], groupRank[b.Group]; ga != gb {
		return ga < gb
	}
	if a.Order != b.Order {
		return a.Order < b.Order
	}
	return a.Key < b.Key
}

// StoredSpecs returns the registered Specs whose values a compute pass stores
// per hex (hex_metrics), in display order. These are what the exporter writes to
// hex-metrics.json.
func StoredSpecs() []Spec { return filterSpecs(false) }

// DerivedSpecs returns the registered Specs the dashboard computes itself,
// in display order. The exporter ships them in the page config so they exist for
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
