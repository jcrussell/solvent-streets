package export

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/export/exporttest"
)

// The export's JSON artifacts are pinned here by contract, not by bytes
// (byob-testing.8): every key the dashboard reads must exist with the right
// JSON type, and the numbers must satisfy invariants that hold for any
// correct forecast. Prose, key order, extra fields and the last digit of a
// float are free to change. The one whole-output check — a small,
// summary-shaped golden — lives in the integration tier
// (integration/export_summary_test.go).

// fixtureEntry is the deterministic city every export contract test builds
// from: exporttest's in-memory store under a fixed name and slug.
func fixtureEntry(t *testing.T) CityEntry {
	t.Helper()
	return CityEntry{
		Config: &config.Config{},
		City:   config.CityConfig{Name: exporttest.CityName},
		Store:  exporttest.Store(),
		Slug:   exporttest.Slug,
	}
}

// binding is one key path the dashboard reads and the JSON types it accepts
// there. "[]" stands for every element of an array.
// binding is one JSON path the dashboard reads and the JSON types it accepts
// there. The pseudo-type "absent" marks a key that may be omitted.
type binding struct {
	path  string
	types []string
}

// seriesBindings are the keys app.js reads off one scenario series (an entry
// of scenarios.json's city/all arrays, or forecast.json's baseline,
// all_baseline and scenarios[]).
func seriesBindings(prefix string) []binding {
	b := []binding{{prefix, []string{"object"}}}
	for _, k := range []string{"scenario.name", "scenario.label"} {
		b = append(b, binding{prefix + "." + k, []string{"string"}})
	}
	for _, k := range []string{"scenario.annual_budget", "scenario.strategy"} {
		b = append(b, binding{prefix + "." + k, []string{"number"}})
	}
	b = append(b, binding{prefix + ".years[]", []string{"object"}})
	for _, k := range []string{"year", "pci", "area", "annual_need", "annual_spend", "deferred_backlog"} {
		b = append(b, binding{prefix + ".years[]." + k, []string{"number"}})
	}
	b = append(b, binding{prefix + ".years[].cost_tier", []string{"string"}})
	b = append(b, binding{prefix + ".final_cohorts[]", []string{"object"}})
	b = append(b, binding{prefix + ".final_cohorts[].classification", []string{"string"}})
	for _, k := range []string{"area", "decay_rate", "end_pci", "total_deficit", "total_spend"} {
		b = append(b, binding{prefix + ".final_cohorts[]." + k, []string{"number"}})
	}
	return b
}

// jsonTypes flattens a decoded JSON document into path -> the set of JSON
// types seen there.
func jsonTypes(v any, path string, out map[string]map[string]bool) {
	var typ string
	switch x := v.(type) {
	case map[string]any:
		typ = "object"
		for k, child := range x {
			jsonTypes(child, path+"."+k, out)
		}
	case []any:
		typ = "array"
		for _, child := range x {
			jsonTypes(child, path+"[]", out)
		}
	case string:
		typ = "string"
	case float64:
		typ = "number"
	case bool:
		typ = "bool"
	case nil:
		typ = "null"
	}
	if out[path] == nil {
		out[path] = map[string]bool{}
	}
	out[path][typ] = true
}

// checkBindings fails t for every binding missing from doc or carrying a type
// the consumer does not accept.
func checkBindings(t *testing.T, doc []byte, bindings []binding) {
	t.Helper()
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	seen := map[string]map[string]bool{}
	jsonTypes(v, "", seen)
	for _, b := range bindings {
		got, ok := seen[b.path]
		if !ok {
			if slices.Contains(b.types, "absent") {
				continue
			}
			t.Errorf("%s: missing; the dashboard reads it", b.path)
			continue
		}
		for typ := range got {
			if !slices.Contains(b.types, typ) {
				t.Errorf("%s: JSON type %s, want one of %v", b.path, typ, b.types)
			}
		}
	}
}

// series mirrors the parts of one scenario series the invariants read.
type series struct {
	Scenario struct {
		Name string `json:"name"`
	} `json:"scenario"`
	Years []struct {
		Year            int     `json:"year"`
		PCI             float64 `json:"pci"`
		Area            float64 `json:"area"`
		AnnualNeed      float64 `json:"annual_need"`
		AnnualSpend     float64 `json:"annual_spend"`
		DeferredBacklog float64 `json:"deferred_backlog"`
	} `json:"years"`
}

// checkSeries asserts what holds for any correct series: one row per
// forecast year, numbered 1..N; PCI within [0, 100]; non-negative money and
// positive area; and a do-nothing baseline that spends nothing.
func checkSeries(t *testing.T, where string, s series, years int) {
	t.Helper()
	if len(s.Years) != years {
		t.Errorf("%s/%s: %d years, want %d", where, s.Scenario.Name, len(s.Years), years)
	}
	for i, y := range s.Years {
		at := fmt.Sprintf("%s/%s year %d", where, s.Scenario.Name, y.Year)
		if y.Year != i+1 {
			t.Errorf("%s: at index %d, want year %d", at, i, i+1)
		}
		if y.PCI < 0 || y.PCI > 100 || math.IsNaN(y.PCI) {
			t.Errorf("%s: pci %v outside [0, 100]", at, y.PCI)
		}
		if y.Area <= 0 {
			t.Errorf("%s: area %v, want > 0", at, y.Area)
		}
		if y.AnnualNeed < 0 || y.AnnualSpend < 0 || y.DeferredBacklog < 0 {
			t.Errorf("%s: negative money: need %v spend %v backlog %v", at, y.AnnualNeed, y.AnnualSpend, y.DeferredBacklog)
		}
		if s.Scenario.Name == "baseline" && y.AnnualSpend != 0 {
			t.Errorf("%s: baseline spends %v, want 0", at, y.AnnualSpend)
		}
	}
}

func finalPCI(s series) float64 {
	if len(s.Years) == 0 {
		return math.NaN()
	}
	return s.Years[len(s.Years)-1].PCI
}

func names(ss []series) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Scenario.Name
	}
	return out
}

// checkFundingLadder asserts the funded scenarios order as the dashboard
// presents them: more funding never ends at a lower PCI, and full funding
// holds the network at its starting PCI.
func checkFundingLadder(t *testing.T, where string, ss []series, initialPCI float64) {
	t.Helper()
	sorted := slices.Clone(ss)
	sort.SliceStable(sorted, func(i, j int) bool { return fundOrder(sorted[i].Scenario.Name) < fundOrder(sorted[j].Scenario.Name) })
	for i := 1; i < len(sorted); i++ {
		lo, hi := sorted[i-1], sorted[i]
		if finalPCI(hi) < finalPCI(lo) {
			t.Errorf("%s: %s ends at PCI %.2f, below %s at %.2f", where, hi.Scenario.Name, finalPCI(hi), lo.Scenario.Name, finalPCI(lo))
		}
	}
	for _, s := range ss {
		if s.Scenario.Name != "fund-100pct" {
			continue
		}
		for _, y := range s.Years {
			if math.Abs(y.PCI-initialPCI) > 1e-6 {
				t.Errorf("%s/fund-100pct year %d: pci %v, want it held at %v", where, y.Year, y.PCI, initialPCI)
			}
		}
	}
}

func fundOrder(name string) int {
	return slices.Index([]string{"baseline", "fund-25pct", "fund-50pct", "fund-100pct"}, name)
}

// TestScenariosJSON_Contract pins scenarios.json, which app.js loads per city
// and reads per scope (data.city[...] / data.all[...]).
func TestScenariosJSON_Contract(t *testing.T) {
	fc := exporttest.ForecastConfig()
	got, err := BuildScenariosData(context.Background(), fixtureEntry(t), &fc)
	if err != nil {
		t.Fatalf("BuildScenariosData: %v", err)
	}
	doc, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var bindings []binding
	for _, scope := range []string{"city", "all"} {
		bindings = append(bindings, binding{"." + scope, []string{"array"}})
		bindings = append(bindings, seriesBindings("."+scope+"[]")...)
	}
	checkBindings(t, doc, bindings)

	var data struct {
		City []series `json:"city"`
		All  []series `json:"all"`
	}
	if err := json.Unmarshal(doc, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{"baseline", "fund-25pct", "fund-50pct", "fund-100pct"}
	for scope, ss := range map[string][]series{"city": data.City, "all": data.All} {
		if got := names(ss); !slices.Equal(got, want) {
			t.Errorf("%s scenarios = %v, want %v (app.js indexes them by name and order)", scope, got, want)
		}
		for _, s := range ss {
			checkSeries(t, scope, s, fc.Years)
		}
		checkFundingLadder(t, scope, ss, fc.InitialPCI)
	}
	// The fixture's city scope is a strict subset of its all.
	if len(data.City) > 0 && len(data.All) > 0 && len(data.City[0].Years) > 0 && len(data.All[0].Years) > 0 {
		if c, b := data.City[0].Years[0].Area, data.All[0].Years[0].Area; c >= b {
			t.Errorf("city-scope area %v >= all-scope area %v; the scopes are swapped or merged", c, b)
		}
	}
}

// TestForecastJSON_Contract pins forecast.json: per-resource baselines and
// funded scenarios, plus the roads-only solvency metrics behind the
// Financials headline and the cross-city leaderboard.
func TestForecastJSON_Contract(t *testing.T) {
	fc := exporttest.ForecastConfig()
	got, err := BuildForecastsForCity(context.Background(), fixtureEntry(t), &fc, ConvertCostTiers(&fc))
	if err != nil {
		t.Fatalf("BuildForecastsForCity: %v", err)
	}
	doc, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	bindings := []binding{
		{"", []string{"array"}},
		{"[].resource_type", []string{"string"}},
		{"[].scenarios", []string{"array"}},
		// Omitted when the budget never runs out / when there is no budget.
		{"[].insolvency_year", []string{"number", "absent"}},
		{"[].funding_gap", []string{"number", "absent"}},
		{"[].break_even_budget", []string{"number"}},
		{"[].current_budget", []string{"number"}},
	}
	bindings = append(bindings, seriesBindings("[].baseline")...)
	bindings = append(bindings, seriesBindings("[].all_baseline")...)
	bindings = append(bindings, seriesBindings("[].scenarios[]")...)
	checkBindings(t, doc, bindings)

	var entries []struct {
		ResourceType    string   `json:"resource_type"`
		Baseline        series   `json:"baseline"`
		AllBaseline     series   `json:"all_baseline"`
		Scenarios       []series `json:"scenarios"`
		InsolvencyYear  *int     `json:"insolvency_year"`
		FundingGap      *float64 `json:"funding_gap"`
		BreakEvenBudget *float64 `json:"break_even_budget"`
		CurrentBudget   *float64 `json:"current_budget"`
	}
	if err := json.Unmarshal(doc, &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var types []string
	for _, e := range entries {
		types = append(types, e.ResourceType)
		checkSeries(t, e.ResourceType+"/baseline", e.Baseline, fc.Years)
		checkSeries(t, e.ResourceType+"/all_baseline", e.AllBaseline, fc.Years)
		for _, s := range e.Scenarios {
			checkSeries(t, e.ResourceType, s, fc.Years)
		}
		checkFundingLadder(t, e.ResourceType, append([]series{e.Baseline}, e.Scenarios...), fc.InitialPCI)
		if got, want := names(e.Scenarios), []string{"fund-25pct", "fund-50pct", "fund-100pct"}; !slices.Equal(got, want) {
			t.Errorf("%s scenarios = %v, want %v", e.ResourceType, got, want)
		}

		if e.ResourceType != "roads" {
			// Solvency is a roads-budget concept; other resources carry none.
			if e.BreakEvenBudget != nil || e.CurrentBudget != nil || e.FundingGap != nil || e.InsolvencyYear != nil {
				t.Errorf("%s: carries roads-only solvency metrics", e.ResourceType)
			}
			continue
		}
		if e.CurrentBudget == nil || *e.CurrentBudget != fc.CurrentBudget {
			t.Errorf("roads current_budget = %v, want the configured %v", e.CurrentBudget, fc.CurrentBudget)
			continue
		}
		if e.BreakEvenBudget == nil || *e.BreakEvenBudget <= 0 || e.FundingGap == nil {
			t.Errorf("roads break_even_budget = %v, funding_gap = %v; want a positive break-even and a gap", e.BreakEvenBudget, e.FundingGap)
			continue
		}
		// The gap is the shortfall relative to the current budget.
		if want := (*e.BreakEvenBudget - *e.CurrentBudget) / *e.CurrentBudget; math.Abs(*e.FundingGap-want) > 1e-9 {
			t.Errorf("roads funding_gap = %v, want (break_even - current) / current = %v", *e.FundingGap, want)
		}
		// The fixture budget exceeds break-even, so it never runs out. Assert
		// the premise too: a fixture change that drops the budget below
		// break-even must fail here, not quietly skip the insolvency check.
		if *e.FundingGap >= 0 {
			t.Errorf("roads funding_gap = %v; exporttest.ForecastConfig's budget must exceed break-even", *e.FundingGap)
		} else if e.InsolvencyYear != nil {
			t.Errorf("roads insolvency_year = %d with a budget above break-even", *e.InsolvencyYear)
		}
	}
	if want := []string{"roads", "parking", "sidewalks"}; !slices.Equal(types, want) {
		t.Errorf("resource types = %v, want %v", types, want)
	}
}

// TestResolvedTOML_StripsConfigID guards against config_id leaking into
// the published static site. ResolvedTOML powers the Config tab visible
// to anyone who loads the rendered index.html, so an auto-computed
// host-path-derived ConfigID hash (or even a user-set value treated as
// internal plumbing) must not appear there.
func TestResolvedTOML_StripsConfigID(t *testing.T) {
	cfg := &config.Config{
		ConfigID: "secret-host-hash",
		Cities:   []config.CityConfig{{Name: "Test"}},
	}
	out := ResolvedTOML(cfg)
	if strings.Contains(out, "config_id") {
		t.Errorf("ResolvedTOML output contains config_id; must be stripped.\nOutput:\n%s", out)
	}
	if strings.Contains(out, "secret-host-hash") {
		t.Errorf("ResolvedTOML output leaked ConfigID value.\nOutput:\n%s", out)
	}
}

// TestResolvedTOML_StripsZeroCurrentBudget guards the Config tab against a
// fabricated `current_budget = 0.0` for uncalibrated cities. current_budget
// uses 0 as a "not provided" sentinel and BurntSushi emits zero floats
// (its isEmpty has no float case), so the encoded TOML must be stripped.
// A configured budget must still survive.
func TestResolvedTOML_StripsZeroCurrentBudget(t *testing.T) {
	uncalibrated := &config.Config{
		Cities: []config.CityConfig{{Name: "Nowhere", Forecast: &config.ForecastConfig{}}},
	}
	if out := ResolvedTOML(uncalibrated); strings.Contains(out, "current_budget") {
		t.Errorf("ResolvedTOML leaked a zero current_budget for an uncalibrated config:\n%s", out)
	}

	calibrated := &config.Config{
		Forecast: config.ForecastConfig{CurrentBudget: 4_850_000},
		Cities:   []config.CityConfig{{Name: "Somewhere"}},
	}
	out := ResolvedTOML(calibrated)
	if !strings.Contains(out, "current_budget") {
		t.Errorf("ResolvedTOML dropped a configured current_budget:\n%s", out)
	}
}

// TestResolvedTOML_ShowsPerCityMinHexArea pins l51o's Config-tab half: the
// per-city min_hex_area must be published as its *resolved* value. CityConfig
// emits the field unconditionally, so an unresolved city would print
// `min_hex_area = 0.0` right below a top level showing the resolved default —
// a misleading published artifact.
func TestResolvedTOML_ShowsPerCityMinHexArea(t *testing.T) {
	cfg := &config.Config{
		Display: config.DisplayConfig{MinHexArea: 40},
		Cities: []config.CityConfig{
			{Name: "Inherits"},
			{Name: "Fine City", HexEdgeM: 25, MinHexArea: 5},
		},
	}
	out := ResolvedTOML(cfg)
	if strings.Contains(out, "min_hex_area = 0.0") {
		t.Errorf("Config tab published an unresolved min_hex_area = 0.0:\n%s", out)
	}
	// The inheriting city resolves to the top-level 40 (so 40 appears twice:
	// top-level [display] plus that city), and the override keeps its 5.
	if got := strings.Count(out, "min_hex_area = 40.0"); got != 2 {
		t.Errorf("expected top-level and inherited min_hex_area = 40.0 (2 occurrences), got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "min_hex_area = 5.0") {
		t.Errorf("per-city min_hex_area override (5) not shown in Config tab:\n%s", out)
	}
}

// TestResolvedTOML_MinHexAreaFallsBackToDefault: a config that sets no
// threshold anywhere publishes the applied default for both the top level and
// every city, rather than a zero that reads as "slivers are never dropped".
func TestResolvedTOML_MinHexAreaFallsBackToDefault(t *testing.T) {
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Nowhere"}}}
	out := ResolvedTOML(cfg)
	want := fmt.Sprintf("min_hex_area = %.1f", config.DefaultMinHexArea)
	if got := strings.Count(out, want); got != 2 {
		t.Errorf("expected %q twice (top level + city), got %d:\n%s", want, got, out)
	}
}

// TestResolvedTOML_MaterializesExportDefaults pins that the [export] block
// reports the values the pipeline actually uses. Both fields resolve lazily
// through Config accessors and neither has omitempty, so without
// materialization an unset block would publish sentinel zeros, contradicting
// docs/configuration.md (5 and 10) and every other section, which
// materializes its defaults.
func TestResolvedTOML_MaterializesExportDefaults(t *testing.T) {
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Nowhere"}}}
	out := ResolvedTOML(cfg)
	for _, want := range []string{
		fmt.Sprintf("coordinate_decimals = %d", config.DefaultCoordinateDecimals),
		fmt.Sprintf("boundary_simplify_m = %.1f", config.DefaultBoundarySimplifyM),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in resolved TOML:\n%s", want, out)
		}
	}
}

// TestResolvedTOML_PreservesBoundarySimplifyOptOut guards the asymmetry in
// Config.BoundarySimplifyM: it keys on zero-vs-nonzero, not positive, because a
// NEGATIVE tolerance is the documented byte-exact opt-out. Materializing the
// default with a `<= 0` guard would silently convert that opt-out into 10 m.
func TestResolvedTOML_PreservesBoundarySimplifyOptOut(t *testing.T) {
	cfg := &config.Config{
		Export: config.ExportConfig{BoundarySimplifyM: -1, CoordinateDecimals: 7},
		Cities: []config.CityConfig{{Name: "Nowhere"}},
	}
	out := ResolvedTOML(cfg)
	if !strings.Contains(out, "boundary_simplify_m = -1.0") {
		t.Errorf("the negative opt-out must survive resolution:\n%s", out)
	}
	if !strings.Contains(out, "coordinate_decimals = 7") {
		t.Errorf("an explicit coordinate_decimals must survive resolution:\n%s", out)
	}
}

// TestResolvedTOML_ShowsPerCityResolvedCalibration pins solvent-streets-xl1t:
// the Config tab must reflect each city's effective, resolved calibration
// (the per-metro hex_edge/forecast an [[include]] flattens onto the city),
// not a parent-only view that hides those overrides.
func TestResolvedTOML_ShowsPerCityResolvedCalibration(t *testing.T) {
	cfg := &config.Config{
		Grid:     config.GridConfig{HexEdgeM: 100},
		Forecast: config.ForecastConfig{DecayRate: 0.088},
		Cities: []config.CityConfig{
			{Name: "Default City"},
			{Name: "Fine City", HexEdgeM: 250},
		},
	}
	out := ResolvedTOML(cfg)
	if !strings.Contains(out, "250") {
		t.Errorf("per-city hex_edge_m override (250) not shown in Config tab:\n%s", out)
	}
	// Every city now carries its resolved forecast, so decay_rate appears
	// per-city (2 cities) on top of the top-level occurrence.
	if got := strings.Count(out, "decay_rate"); got < 2 {
		t.Errorf("expected per-city resolved forecast (decay_rate ≥2 occurrences), got %d:\n%s", got, out)
	}
}
