package export

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/export/exporttest"
)

// dollarFields are the emitted keys that must scale with the cost multiplier.
// Everything else — areas, PCI, decay rates, years, counts — must not.
var dollarFields = map[string]bool{
	"annual_need":       true,
	"annual_spend":      true,
	"deferred_backlog":  true,
	"total_spend":       true,
	"total_deficit":     true,
	"break_even_budget": true,
	"annual_budget":     true,
}

// invariantFields must be byte-identical across multipliers: they describe the
// network and the model, not its price.
var invariantFields = map[string]bool{
	"area":           true,
	"decay_rate":     true,
	"year":           true,
	"strategy":       true,
	"current_budget": true, // the city's real dollars; costs changing does not change them
}

// TestCostMultiplier_ExportScalesDollarsOnly walks the whole emitted forecast payload
// and checks the multiplier landed on every dollar and nothing else.
//
// This is the end-to-end version of the projector-level test in
// internal/forecast: it runs the real BuildForecastsForCity, so it catches a
// path that resolves tiers without the multiplier (prices bare) or applies it a
// second time (prices at multiplier squared) — both of which produce
// plausible-looking numbers that no amount of eyeballing an export would catch.
//
// PCI is deliberately NOT asserted invariant. Scenarios funded from a FIXED
// budget legitimately reach a different condition when the same dollars buy
// less work — that is the whole point of the loaded regime.
func TestCostMultiplier_ExportScalesDollarsOnly(t *testing.T) {
	const factor = 2.0

	emit := func(multiplier float64) map[string]any {
		entry := fixtureEntry(t)
		fc := exporttest.ForecastConfig()
		fc.CostMultiplier = multiplier
		got, err := BuildForecastsForCity(context.Background(), entry, &fc, ConvertCostTiers(&fc))
		if err != nil {
			t.Fatalf("BuildForecastsForCity(multiplier=%v): %v", multiplier, err)
		}
		b, err := json.Marshal(map[string]any{"forecasts": got})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out
	}

	bare := emit(1)
	loaded := emit(factor)

	var walk func(a, b any, path, key string)
	walk = func(a, b any, path, key string) {
		switch av := a.(type) {
		case map[string]any:
			bv, ok := b.(map[string]any)
			if !ok {
				t.Errorf("%s: shape diverged between multipliers", path)
				return
			}
			for k, v := range av {
				walk(v, bv[k], path+"/"+k, k)
			}
		case []any:
			bv, ok := b.([]any)
			if !ok || len(bv) != len(av) {
				t.Errorf("%s: length diverged between multipliers", path)
				return
			}
			for i := range av {
				walk(av[i], bv[i], path+"[]", key)
			}
		case float64:
			bv, ok := b.(float64)
			if !ok {
				return
			}
			switch {
			case invariantFields[key]:
				if av != bv {
					t.Errorf("%s: %g -> %g, want unchanged (not a priced quantity)", path, av, bv)
				}
			case dollarFields[key]:
				if av == 0 && bv == 0 {
					return
				}
				want := av * factor
				if math.Abs(bv-want) > 1e-6*math.Abs(want) {
					t.Errorf("%s: %g -> %g, want %g (exactly %gx). Off by this much means the multiplier was missed on one path or applied twice on another",
						path, av, bv, want, factor)
				}
			}
		}
	}
	walk(bare, loaded, "", "")
}

// TestCostMultiplier_SeedShipsUnscaledTiers pins the split that keeps the multiplier
// from being applied twice: forecast_seed.json carries the BARE per-m2 schedule
// plus the multiplier as its own field. The browser renders those tiers into the
// editable cost inputs and sends both back to the WASM bridge, which combines
// them exactly once. Pre-scaling here would show the user loaded prices in
// boxes labelled with the tier's own cost AND price the interactive line at
// multiplier squared.
func TestCostMultiplier_SeedShipsUnscaledTiers(t *testing.T) {
	entry := fixtureEntry(t)
	fc := exporttest.ForecastConfig()
	fc.CostMultiplier = 2

	seed, err := BuildForecastSeed(context.Background(), &fc, entry.Store)
	if err != nil {
		t.Fatalf("BuildForecastSeed: %v", err)
	}
	var got struct {
		CostTiers []struct {
			CostPerSqM float64 `json:"cost_per_sqm"`
			Label      string  `json:"label"`
		} `json:"cost_tiers"`
		CostMultiplier float64 `json:"cost_multiplier"`
	}
	if err := json.Unmarshal([]byte(seed), &got); err != nil {
		t.Fatalf("unmarshal seed: %v", err)
	}

	if got.CostMultiplier != 2 {
		t.Errorf("seed cost_multiplier = %v, want 2", got.CostMultiplier)
	}
	want := ConvertCostTiers(&fc)
	if len(got.CostTiers) != len(want) {
		t.Fatalf("seed has %d tiers, want %d", len(got.CostTiers), len(want))
	}
	for i, tier := range got.CostTiers {
		if tier.CostPerSqM != want[i].CostPerSqM {
			t.Errorf("seed tier %q cost_per_sqm = %g, want %g (unscaled) — pre-scaling here doubles the load",
				tier.Label, tier.CostPerSqM, want[i].CostPerSqM)
		}
	}
}

// TestCostMultiplier_MultiCitySeedMatchesSingleCity pins the two seed builders to one
// contract.
//
// BuildMultiCityForecastSeed had simply omitted CostMultiplier from its literal.
// The field has no omitempty, so the regional site/index.html shipped
// "cost_multiplier":0 inline while every cities/<slug>/data/forecast_seed.json
// carried the real multiplier — the seed is the documented contract for what
// the browser prices with, and half of it disagreed with the other half.
//
// The key-set comparison is the part that earns its keep: it catches the NEXT
// field added to ForecastSeedJSON and wired into only one of the two builders,
// which is the actual failure mode here. Area and cohort fields are compared
// too, but only because a single-entry region is by definition the same network
// as the city it contains — that equality is what makes the region's headline
// tiles trustworthy at all.
func TestCostMultiplier_MultiCitySeedMatchesSingleCity(t *testing.T) {
	entry := fixtureEntry(t)
	fc := exporttest.ForecastConfig()
	fc.CostMultiplier = 1.75
	// The region reads each city's cohort decay rate from that city's own
	// resolved forecast, so the entry's config must
	// state the same forecast the single-city builder is handed.
	entry.Config = &config.Config{Forecast: fc}

	single := buildSeedMap(t, func() (template.JS, error) {
		return BuildForecastSeed(context.Background(), &fc, entry.Store)
	})
	multi := buildSeedMap(t, func() (template.JS, error) {
		return BuildMultiCityForecastSeed(context.Background(), &fc, []CityEntry{entry})
	})

	if got := multi["cost_multiplier"]; got != 1.75 {
		t.Errorf("multi-city seed cost_multiplier = %v, want 1.75 — the browser would price the region bare", got)
	}

	singleKeys := slices.Sorted(maps.Keys(single))
	multiKeys := slices.Sorted(maps.Keys(multi))
	if !slices.Equal(singleKeys, multiKeys) {
		t.Errorf("seed key sets diverge:\n single: %v\n  multi: %v\nevery ForecastSeedJSON field must be populated by BOTH builders",
			singleKeys, multiKeys)
	}

	// One entry means one network, so the two builders must agree on all of it.
	// Cohort order is not part of the contract; compare them order-insensitively.
	for _, key := range singleKeys {
		want, got := single[key], multi[key]
		if key == "cohorts" || key == "city_cohorts" {
			want, got = sortedCohorts(want), sortedCohorts(got)
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("seed %q: single-city %v, multi-city %v", key, want, got)
		}
	}
}

func buildSeedMap(t *testing.T, build func() (template.JS, error)) map[string]any {
	t.Helper()
	seed, err := build()
	if err != nil {
		t.Fatalf("build seed: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(seed), &out); err != nil {
		t.Fatalf("unmarshal seed: %v", err)
	}
	return out
}

// sortedCohorts orders a decoded cohort array so two builders that visit the
// same cohorts in a different order still compare equal.
//
// Classification alone is NOT a unique key: mergeCohortSeeds buckets on
// (resource_type, classification) and keeps cross-resource collisions as
// separate cohorts, so two entries can share a classification. Sorting on it
// alone with an unstable sort would let tied entries swap and fail the test for
// no reason, so break ties on the remaining fields.
func sortedCohorts(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	field := func(x any, name string) string {
		m, _ := x.(map[string]any)
		return fmt.Sprint(m[name])
	}
	out := slices.Clone(list)
	slices.SortStableFunc(out, func(a, b any) int {
		return cmp.Or(
			strings.Compare(field(a, "classification"), field(b, "classification")),
			strings.Compare(field(a, "area"), field(b, "area")),
			strings.Compare(field(a, "decay_rate"), field(b, "decay_rate")),
		)
	})
	return out
}

// TestCostMultiplier_ResolvedTOMLReportsIt: the Config tab renders ResolvedTOML, so a
// reader can see which regime produced the dollars on the page. An unset
// cost_multiplier must show the resolved default, at the top level and for the
// city, not the raw 0.
func TestCostMultiplier_ResolvedTOMLReportsIt(t *testing.T) {
	out := ResolvedTOML(&config.Config{Cities: []config.CityConfig{{Name: "Nowhere"}}})
	want := fmt.Sprintf("cost_multiplier = %.1f", config.DefaultCostMultiplier)
	if got := strings.Count(out, want); got != 2 {
		t.Errorf("%q appears %d times, want 2 (top level + city):\n%s", want, got, out)
	}
	if strings.Contains(out, "cost_multiplier = 0") {
		t.Errorf("ResolvedTOML shows the raw unset cost_multiplier:\n%s", out)
	}
}
