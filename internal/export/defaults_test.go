package export

import (
	"encoding/json"
	"html/template"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/forecast"
)

func TestForecastDefaults_GoConstantsWithoutASeed(t *testing.T) {
	got := TemplateData{}.ForecastDefaults()
	want := ForecastDefaults{
		InitialPCI:          config.DefaultInitialPCI,
		HorizonYears:        config.DefaultForecastYears,
		TreatmentCycleYears: forecast.DefaultTreatmentCycleYears,
		BarrelsPerTonBinder: forecast.BarrelsPerTonBinder,
	}
	if got != want {
		t.Errorf("ForecastDefaults() = %+v; want the Go defaults %+v", got, want)
	}
}

func TestForecastDefaults_FollowTheSeed(t *testing.T) {
	seed := template.JS(`{"initial_pci":72.5,"horizon_years":25,"treatment_cycle_years":15,"barrels_per_ton_binder":6}`)
	got := TemplateData{ForecastSeed: seed}.ForecastDefaults()
	want := ForecastDefaults{InitialPCI: 72.5, HorizonYears: 25, TreatmentCycleYears: 15, BarrelsPerTonBinder: 6}
	if got != want {
		t.Errorf("ForecastDefaults() = %+v; want the seed's %+v", got, want)
	}
	if got.PCISlider() != 73 {
		t.Errorf("PCISlider() = %d; want 72.5 rounded half up", got.PCISlider())
	}

	// A value the seed leaves unset keeps its Go default.
	partial := TemplateData{ForecastSeed: `{"horizon_years":40}`}.ForecastDefaults()
	if partial.HorizonYears != 40 || partial.InitialPCI != config.DefaultInitialPCI ||
		partial.TreatmentCycleYears != forecast.DefaultTreatmentCycleYears {
		t.Errorf("partial seed resolved to %+v", partial)
	}
}

func TestGameHorizons(t *testing.T) {
	def := config.DefaultForecastYears
	if got := (TemplateData{}).GameHorizons(); !slices.Contains(got, def) || !slices.IsSorted(got) || len(got) != len(gameHorizonAlternatives)+1 {
		t.Errorf("GameHorizons() = %v; want the default horizon %d among the alternatives %v", got, def, gameHorizonAlternatives)
	}
	got := TemplateData{ForecastSeed: `{"horizon_years":25}`}.GameHorizons()
	if !slices.Contains(got, 25) || !slices.Contains(got, def) || !slices.IsSorted(got) {
		t.Errorf("GameHorizons() = %v; want the configured horizon 25 offered, ascending", got)
	}
	// A configured horizon that is already an alternative is offered once.
	alt := gameHorizonAlternatives[0]
	seed, _ := json.Marshal(map[string]int{"horizon_years": alt})
	got = TemplateData{ForecastSeed: template.JS(seed)}.GameHorizons()
	if n := len(got); n != len(slices.Compact(slices.Clone(got))) || !slices.Contains(got, alt) {
		t.Errorf("GameHorizons() = %v; want %d listed once", got, alt)
	}
}

// TestPageConfigCarriesGoDefaults pins that both pages' PVMT_CONFIG carry the
// forecast defaults and the hexgrid version they read. The play page's
// defaults are the package ones, not the page seed's: the board reads the
// city's own seed, and on a multi-city page the page seed is the region's.
// The board's PCI jitter is the engine's default, so the play page carries
// none.
func TestPageConfigCarriesGoDefaults(t *testing.T) {
	td := TemplateData{ForecastSeed: `{"horizon_years":25}`}
	for name, payload := range map[string]template.JS{"index": td.IndexConfigJSON(), "play": td.GameConfigJSON()} {
		var cfg struct {
			Defaults  ForecastDefaults `json:"forecast_defaults"`
			PCIJitter *float64         `json:"pci_jitter"`
			HexGridV  int              `json:"hexgrid_version"`
		}
		if err := json.Unmarshal([]byte(payload), &cfg); err != nil {
			t.Fatalf("%s config: %v", name, err)
		}
		want := td.ForecastDefaults()
		if name == "play" {
			want = TemplateData{}.ForecastDefaults()
		}
		if cfg.Defaults != want {
			t.Errorf("%s forecast_defaults = %+v; want %+v", name, cfg.Defaults, want)
		}
		if cfg.HexGridV != HexGridFileVersion {
			t.Errorf("%s hexgrid_version = %d; want %d", name, cfg.HexGridV, HexGridFileVersion)
		}
		if cfg.PCIJitter != nil {
			t.Errorf("%s pci_jitter = %v; the engine owns the jitter", name, *cfg.PCIJitter)
		}
	}
}

// TestStaticMarkupFollowsForecastDefaults renders the pages with and without
// a seed and checks the horizon and initial PCI written into the markup.
func TestStaticMarkupFollowsForecastDefaults(t *testing.T) {
	render := func(parse func() (*template.Template, error), td TemplateData) string {
		t.Helper()
		html, err := RenderPage(parse, td)
		if err != nil {
			t.Fatal(err)
		}
		return string(html)
	}
	cities := []CityInfo{{Slug: "a", Name: "A"}}
	plain := TemplateData{Cities: cities, TagDimensions: GroupCitiesByDimension(cities)}
	seeded := plain
	seeded.ForecastSeed = `{"initial_pci":62,"horizon_years":25}`

	for _, tc := range []struct {
		name  string
		td    TemplateData
		index []string
		play  []string
	}{
		{"go defaults", plain,
			[]string{`id="hl-spend-label">30-Year Spend<`, `id="cohort-deficit-th">30yr Deficit<`, `id="compare-need-title">Year 1 vs Year 30 Maintenance Need<`, `id="pci-slider" min="50" max="100" value="85"`, `aria-valuenow="85"`, `id="pci-value">85<`},
			[]string{`data-horizon="20">20y<`, `data-horizon="30">30y<`, `data-horizon="50">50y<`, `data-horizon="endless"`}},
		{"seeded", seeded,
			[]string{`id="hl-spend-label">25-Year Spend<`, `id="cohort-spend-th">25yr Spend<`, `id="compare-cost-title">25-Year Cost Trajectory<`, `id="pci-slider" min="50" max="100" value="62"`, `id="pci-value">62<`},
			[]string{`data-horizon="20">20y<`, `data-horizon="25">25y<`, `data-horizon="30">30y<`, `data-horizon="50">50y<`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index, play := render(ParseIndexTemplate, tc.td), render(ParseGameTemplate, tc.td)
			for _, want := range tc.index {
				if !strings.Contains(index, want) {
					t.Errorf("index.html lacks %s", want)
				}
			}
			for _, want := range tc.play {
				if !strings.Contains(play, want) {
					t.Errorf("play.html lacks %s", want)
				}
			}
		})
	}
}
