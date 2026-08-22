package export

import (
	"context"
	"encoding/json"
	"html/template"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
)

// dollarFields are the emitted keys that must scale with the cost overhead.
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

// invariantFields must be byte-identical across overheads: they describe the
// network and the model, not its price.
var invariantFields = map[string]bool{
	"area":           true,
	"decay_rate":     true,
	"year":           true,
	"strategy":       true,
	"current_budget": true, // the city's real dollars; costs changing does not change them
}

// TestOverhead_ExportScalesDollarsOnly walks the whole emitted forecast payload
// and checks the multiplier landed on every dollar and nothing else.
//
// This is the end-to-end version of the projector-level test in
// internal/forecast: it runs the real BuildForecastsForCity, so it catches a
// path that resolves tiers without the overhead (prices bare) or applies it a
// second time (prices at overhead squared) — both of which produce
// plausible-looking numbers that no amount of eyeballing an export would catch.
//
// PCI is deliberately NOT asserted invariant. Scenarios funded from a FIXED
// budget legitimately reach a different condition when the same dollars buy
// less work — that is the whole point of the loaded regime.
func TestOverhead_ExportScalesDollarsOnly(t *testing.T) {
	const factor = 2.0

	emit := func(overhead float64) map[string]any {
		entry := goldenFixtureEntry(t)
		fc := goldenForecastConfig()
		fc.CostOverhead = overhead
		got, err := BuildForecastsForCity(context.Background(), entry, &fc, ConvertCostTiers(&fc))
		if err != nil {
			t.Fatalf("BuildForecastsForCity(overhead=%v): %v", overhead, err)
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
				t.Errorf("%s: shape diverged between overheads", path)
				return
			}
			for k, v := range av {
				walk(v, bv[k], path+"/"+k, k)
			}
		case []any:
			bv, ok := b.([]any)
			if !ok || len(bv) != len(av) {
				t.Errorf("%s: length diverged between overheads", path)
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
					t.Errorf("%s: %g -> %g, want %g (exactly %gx). Off by this much means the overhead was missed on one path or applied twice on another",
						path, av, bv, want, factor)
				}
			}
		}
	}
	walk(bare, loaded, "", "")
}

// TestOverhead_SeedShipsUnscaledTiers pins the split that keeps the multiplier
// from being applied twice: forecast_seed.json carries the BARE per-m2 schedule
// plus the overhead as its own field. The browser renders those tiers into the
// editable cost inputs and sends both back to the WASM bridge, which combines
// them exactly once. Pre-scaling here would show the user loaded prices in
// boxes labelled with the tier's own cost AND price the interactive line at
// overhead squared.
func TestOverhead_SeedShipsUnscaledTiers(t *testing.T) {
	entry := goldenFixtureEntry(t)
	fc := goldenForecastConfig()
	fc.CostOverhead = 2

	seed, err := BuildForecastSeed(context.Background(), &fc, entry.Store)
	if err != nil {
		t.Fatalf("BuildForecastSeed: %v", err)
	}
	var got struct {
		CostTiers []struct {
			CostPerSqM float64 `json:"cost_per_sqm"`
			Label      string  `json:"label"`
		} `json:"cost_tiers"`
		CostOverhead float64 `json:"cost_overhead"`
	}
	if err := json.Unmarshal([]byte(seed), &got); err != nil {
		t.Fatalf("unmarshal seed: %v", err)
	}

	if got.CostOverhead != 2 {
		t.Errorf("seed cost_overhead = %v, want 2", got.CostOverhead)
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

// TestOverhead_MultiCitySeedMatchesSingleCity pins the two seed builders to one
// contract.
//
// BuildMultiCityForecastSeed had simply omitted CostOverhead from its literal.
// The field has no omitempty, so the regional site/index.html shipped
// "cost_overhead":0 inline while every cities/<slug>/data/forecast_seed.json
// carried the real multiplier — the seed is the documented contract for what
// the browser prices with, and half of it disagreed with the other half.
//
// The key-set comparison is the part that earns its keep: it catches the NEXT
// field added to ForecastSeedJSON and wired into only one of the two builders,
// which is the actual failure mode here. Area and cohort fields are compared
// too, but only because a single-entry region is by definition the same network
// as the city it contains — that equality is what makes the region's headline
// tiles trustworthy at all.
func TestOverhead_MultiCitySeedMatchesSingleCity(t *testing.T) {
	entry := goldenFixtureEntry(t)
	fc := goldenForecastConfig()
	fc.CostOverhead = 1.75

	single := buildSeedMap(t, func() (template.JS, error) {
		return BuildForecastSeed(context.Background(), &fc, entry.Store)
	})
	multi := buildSeedMap(t, func() (template.JS, error) {
		return BuildMultiCityForecastSeed(context.Background(), &fc, []CityEntry{entry})
	})

	if got := multi["cost_overhead"]; got != 1.75 {
		t.Errorf("multi-city seed cost_overhead = %v, want 1.75 — the browser would price the region bare", got)
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

// sortedCohorts orders a decoded cohort array by classification so two builders
// that visit the same cohorts in a different order still compare equal.
func sortedCohorts(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	out := slices.Clone(list)
	slices.SortFunc(out, func(a, b any) int {
		am, _ := a.(map[string]any)
		bm, _ := b.(map[string]any)
		as, _ := am["classification"].(string)
		bs, _ := bm["classification"].(string)
		return strings.Compare(as, bs)
	})
	return out
}

// TestOverhead_ResolvedTOMLReportsIt: the Config tab renders ResolvedTOML, so a
// reader can see which regime produced the dollars on the page. It must show
// the resolved value, not the raw 0.
func TestOverhead_ResolvedTOMLReportsIt(t *testing.T) {
	var fc config.ForecastConfig
	config.NormalizeForecast(&fc)
	if fc.CostOverhead != config.DefaultCostOverhead {
		t.Errorf("normalized cost_overhead = %v, want %v", fc.CostOverhead, config.DefaultCostOverhead)
	}
}
