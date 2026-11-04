// Package hexmetric is the registry of per-hex metrics: named, unit-bearing
// values that a compute pass stores per hex (db.HexMetric / hex_metrics),
// the exporter writes to hex-metrics.json, and the dashboard offers in its
// "Color by" selector. Fire response time is the first metric; population
// density and per-resident upkeep are planned. A new metric is a Spec
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
	TransformLinear = "linear"
	TransformLog    = "log"
)

// Value formats the dashboard knows how to render.
const (
	FormatInt     = "int"     // whole number with thousands separators
	FormatFloat1  = "float1"  // one decimal
	FormatMinutes = "minutes" // seconds in, "m:ss" or "N min" out
	FormatUSD     = "usd"     // dollars
)

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

// Spec describes one per-hex metric.
type Spec struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Unit   string `json:"unit"`   // SI-ish unit of the stored value ("s", "", "m2", "usd")
	Source string `json:"source"` // one line on where the numbers come from
	Format string `json:"format"` // one of the Format* constants
	Scale  Scale  `json:"scale"`
}

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
	case FormatInt, FormatFloat1, FormatMinutes, FormatUSD:
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
	for i := 1; i < len(s.Scale.Bands); i++ {
		if s.Scale.Bands[i] <= s.Scale.Bands[i-1] {
			return fmt.Errorf("hexmetric: %s band edges must be strictly ascending, got %v", s.Key, s.Scale.Bands)
		}
	}
	return nil
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
