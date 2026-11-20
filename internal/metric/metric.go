// Package metric is the catalog of metric keys: every measure a producer
// stores in city_metrics or hex_metrics (internal/db), and every measure the
// dashboard derives per hex, is declared here exactly once with its unit,
// the level it is stored at and what it means. The key is the name the
// measure has on every surface: the database, hex-metrics.json, meta.json's
// metrics, the page config's hex_metrics, `?metric=` and `pvmt snapshots
// diff`.
//
// Registration enforces the JSON naming rule (docs/architecture.md, "JSON
// conventions"), so a key cannot carry a unit the convention leaves out:
//
//   - keys are lowercase snake_case;
//   - a key carries no unit suffix (areas are m², money dollars), except a
//     duration, which ends in _s and has unit "s";
//   - a proportion in 0..1 ends in _share and has unit "share";
//   - a rate named X_per_Y has a unit with a "/"; its Y may be the area unit
//     sqm (cost_per_sqm), the one unit a key spells out.
//
// The package imports nothing outside the standard library, so the WASM
// build and every producer can depend on it.
package metric

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Level says where a measure lives.
type Level int

const (
	// City is a city-wide measure (city_metrics).
	City Level = iota + 1
	// Hex is a per-hex measure (hex_metrics).
	Hex
	// Both is stored city-wide and per hex.
	Both
	// Derived is computed by the dashboard per hex and never stored.
	Derived
)

func (l Level) String() string {
	switch l {
	case City:
		return "city"
	case Hex:
		return "hex"
	case Both:
		return "both"
	case Derived:
		return "derived"
	}
	return fmt.Sprintf("Level(%d)", int(l))
}

// AtCity reports whether a measure of level l is stored in city_metrics.
func (l Level) AtCity() bool { return l == City || l == Both }

// AtHex reports whether a measure of level l is stored in hex_metrics.
func (l Level) AtHex() bool { return l == Hex || l == Both }

// Units of a measure's value. A count has UnitCount; the rest name the
// canonical unit the value is stored and exported in.
const (
	UnitCount                   = "count"
	UnitShare                   = "share" // a proportion in 0..1
	UnitSquareMeters            = "m2"
	UnitSeconds                 = "s"
	UnitDollars                 = "usd"
	UnitPerSquareMeter          = "1/m2"
	UnitPerYear                 = "1/yr"
	UnitSquareMetersPerResident = "m2/person"
	UnitDollarsPerResident      = "usd/person"
	UnitDollarsPerSquareMeter   = "usd/m2"
)

// units is every unit a Def may declare.
var units = []string{
	UnitCount, UnitShare, UnitSquareMeters, UnitSeconds, UnitDollars,
	UnitPerSquareMeter, UnitPerYear, UnitSquareMetersPerResident,
	UnitDollarsPerResident, UnitDollarsPerSquareMeter,
}

// Def declares one metric key.
type Def struct {
	Key         string
	Unit        string
	Level       Level
	Description string
}

var keyRe = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// unitWords are key segments that name a unit, which the convention keeps out
// of keys: the unit lives in Def.Unit and the value is canonical.
var unitWords = []string{
	"m", "m2", "km", "km2", "sqm", "sqkm", "ha", "hectares", "acres", "ft", "ft2", "sqft", "mi", "mi2", "sqmi",
	"usd", "dollars", "cents", "pct", "percent", "kg", "tonnes",
	"ms", "seconds", "minutes", "hours", "days", "yr", "years", "mph", "kmh",
}

// Validate checks d against the naming rule and reports the first violation.
func (d Def) Validate() error {
	if !keyRe.MatchString(d.Key) {
		return fmt.Errorf("metric: key %q must be lowercase snake_case", d.Key)
	}
	switch d.Level {
	case City, Hex, Both, Derived:
	default:
		return fmt.Errorf("metric: %s has no level", d.Key)
	}
	if !slices.Contains(units, d.Unit) {
		return fmt.Errorf("metric: %s has unit %q, which is not one of the Unit constants", d.Key, d.Unit)
	}
	if d.Description == "" {
		return fmt.Errorf("metric: %s has no description", d.Key)
	}
	segs := strings.Split(d.Key, "_")
	for i, s := range segs {
		if s == "s" && i != len(segs)-1 {
			return fmt.Errorf("metric: %s: %q is a unit and only ends a duration's key", d.Key, s)
		}
		if s == "sqm" && i > 0 && segs[i-1] == "per" {
			continue
		}
		if slices.Contains(unitWords, s) {
			return fmt.Errorf("metric: %s carries the unit %q in its key; the unit is %q and belongs in Unit", d.Key, s, d.Unit)
		}
	}
	last := segs[len(segs)-1]
	if (last == "s") != (d.Unit == UnitSeconds) {
		return fmt.Errorf("metric: %s: a duration (unit %q) ends in _s, and only a duration does", d.Key, UnitSeconds)
	}
	if (last == "share") != (d.Unit == UnitShare) {
		return fmt.Errorf("metric: %s: a proportion (unit %q) ends in _share, and only a proportion does", d.Key, UnitShare)
	}
	if slices.Contains(segs, "per") && !strings.Contains(d.Unit, "/") {
		return fmt.Errorf("metric: %s is named as a rate but its unit %q is not one", d.Key, d.Unit)
	}
	return nil
}

var (
	mu      sync.RWMutex
	catalog = map[string]Def{}
)

// ErrDuplicate is returned by TryRegister when the key is already declared.
var ErrDuplicate = errors.New("metric: key already declared")

// Register declares d. It panics on a key that breaks the naming rule or is
// already declared. The catalog's own init (catalog.go) is the only
// production caller; tests declare scratch keys through TryRegister.
func Register(d Def) {
	if err := TryRegister(d); err != nil {
		panic(err)
	}
}

// TryRegister is Register with an error return.
func TryRegister(d Def) error {
	if err := d.Validate(); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := catalog[d.Key]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicate, d.Key)
	}
	catalog[d.Key] = d
	return nil
}

// Lookup returns the Def declared under key.
func Lookup(key string) (Def, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := catalog[key]
	return d, ok
}

// All returns every declared Def sorted by key.
func All() []Def {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Def, 0, len(catalog))
	for _, d := range catalog {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// CheckCity returns an error unless key is declared and stored city-wide.
func CheckCity(key string) error { return check(key, City, Level.AtCity) }

// CheckHex returns an error unless key is declared and stored per hex.
func CheckHex(key string) error { return check(key, Hex, Level.AtHex) }

func check(key string, want Level, ok func(Level) bool) error {
	d, found := Lookup(key)
	if !found {
		return fmt.Errorf("metric %q is not in the metric catalog (internal/metric)", key)
	}
	if !ok(d.Level) {
		return fmt.Errorf("metric %q is a %s metric, not stored at the %s level", key, d.Level, want)
	}
	return nil
}
