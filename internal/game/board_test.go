package game

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/forecast"
)

// boardClass is one road class a synthetic board is built from: its decay
// rate and its relative share of the hexes.
type boardClass struct {
	name   string
	k      float64
	weight int
}

// syntheticBoard builds a board of n hexes over classes, and the per-class
// cohorts that hold exactly the board's road area. Areas, classes and blends
// come from a fixed LCG, so a case is the same board on every run. Every
// blendEvery-th hex mixes two classes, its k their area-weighted blend, as the
// export blends a hex's decay rate; the rest hold one class.
func syntheticBoard(n, blendEvery int, classes []boardClass, seed uint64) ([]HexConfig, []CohortConfig) {
	state := seed
	next := func() float64 {
		state = state*6364136223846793005 + 1442695040888963407
		return float64(state>>11) / float64(1<<53)
	}
	var totalWeight int
	for _, c := range classes {
		totalWeight += c.weight
	}
	pick := func() int {
		w := int(next() * float64(totalWeight))
		for i, c := range classes {
			if w < c.weight {
				return i
			}
			w -= c.weight
		}
		return len(classes) - 1
	}

	classArea := make([]float64, len(classes))
	hexes := make([]HexConfig, n)
	side := int(math.Ceil(math.Sqrt(float64(n))))
	for i := range hexes {
		area := 50 + next()*6000
		ci := pick()
		k := classes[ci].k
		if blendEvery > 0 && i%blendEvery == 0 {
			cj := pick()
			share := next()
			classArea[ci] += area * share
			classArea[cj] += area * (1 - share)
			k = share*classes[ci].k + (1-share)*classes[cj].k
		} else {
			classArea[ci] += area
		}
		hexes[i] = HexConfig{ID: fmt.Sprintf("hex:%d:%d", i%side, i/side), RoadsArea: area, DecayRate: k}
	}
	var cohorts []CohortConfig
	for i, c := range classes {
		if classArea[i] > 0 {
			cohorts = append(cohorts, CohortConfig{Classification: c.name, Area: classArea[i], DecayRate: c.k})
		}
	}
	return hexes, cohorts
}

// forecastBaseline is forecast.json's baseline for cfg's cohorts: the spread
// cohorts, simulated do-nothing over years.
func forecastBaseline(cfg Config, years int) forecast.ScenarioResult {
	in := cfg.forecastInputs(0)
	cohorts := forecast.SpreadCohorts(in.Cohorts, in.InitialPCI)
	params := forecast.NewParams(in.GrowthRate, in.CostTiers, forecast.ResolveCycleYears(in.CycleYears), in.CostMultiplier)
	return forecast.SimulateBaseline(cohorts, years, params)
}

// forecastYear1Need is forecast.json's baseline years[0].annual_need.
func forecastYear1Need(cfg Config) float64 {
	return forecastBaseline(cfg, 1).Years[0].AnnualNeed
}

// boardCase is one synthetic city the parity test plays.
type boardCase struct {
	name       string
	initialPCI float64
	multiplier float64
	cycle      float64
	growth     float64
	n          int
	blendEvery int
	classes    []boardClass
}

func (c boardCase) config() Config {
	hexes, cohorts := syntheticBoard(c.n, c.blendEvery, c.classes, uint64(c.n)*7919+uint64(c.initialPCI))
	cfg := baseConfig()
	cfg.Hexes = hexes
	cfg.Cohorts = cohorts
	cfg.InitialPCI = c.initialPCI
	cfg.CostMultiplier = c.multiplier
	cfg.TreatmentCycleYears = c.cycle
	cfg.GrowthRate = c.growth
	return cfg
}

var boardCases = []boardCase{
	{
		name: "mid-range residential city", initialPCI: 65, multiplier: 1, cycle: 12, n: 2000, blendEvery: 3,
		classes: []boardClass{{"residential", 0.040, 6}, {"tertiary", 0.035, 2}, {"secondary", 0.030, 1}, {"service", 0.045, 2}},
	},
	{
		name: "worn arterial city", initialPCI: 45, multiplier: 1.4, cycle: 10, growth: 0.015, n: 1200, blendEvery: 2,
		classes: []boardClass{{"primary", 0.025, 3}, {"secondary", 0.030, 3}, {"residential", 0.040, 4}, {"motorway", 0.015, 1}},
	},
	{
		name: "good small town", initialPCI: 82, multiplier: 1, cycle: 14, n: 400, blendEvery: 0,
		classes: []boardClass{{"residential", 0.040, 5}, {"service", 0.045, 1}},
	},
	{
		name: "failing city", initialPCI: 40, multiplier: 1, cycle: 12, n: 900, blendEvery: 4,
		classes: []boardClass{{"residential", 0.040, 1}, {"tertiary", 0.035, 1}},
	},
}

// boardYear1NeedTolerance is how far the board's year-1 need may sit from the
// forecast's: what is left once every hex starts at a condition band's PCI is
// the hexes straddling a band edge and the band-to-band spread of the hexes'
// decay rates, both well under a percent on a city-sized board.
const boardYear1NeedTolerance = 0.01

// TestBoardYear1NeedMatchesForecast: the board's backlog loss threshold is the
// forecast's insolvency threshold for the same network. Each synthetic board
// holds exactly its cohorts' road area, so its year-1 need, priced hex by hex
// at the PCI it starts at, must be the year-1 annual_need forecast.json reports
// for the spread cohorts.
func TestBoardYear1NeedMatchesForecast(t *testing.T) {
	for _, c := range boardCases {
		t.Run(c.name, func(t *testing.T) {
			cfg := c.config()
			g := newGame(t, cfg)
			board := g.insolvencyAt / forecast.ResolveCycleYears(cfg.TreatmentCycleYears)
			want := forecastYear1Need(cfg)
			rel := board/want - 1
			t.Logf("board year-1 need %.0f, forecast %.0f (%+.3f%%)", board, want, 100*rel)
			if math.Abs(rel) > boardYear1NeedTolerance {
				t.Errorf("board year-1 need %.0f is %+.2f%% from the forecast's %.0f; want within %.0f%%",
					board, 100*rel, want, 100*boardYear1NeedTolerance)
			}
			if got, wantAt := g.insolvencyAt, forecast.InsolvencyThreshold(want, cfg.TreatmentCycleYears); math.Abs(got/wantAt-1) > boardYear1NeedTolerance {
				t.Errorf("backlog loss threshold %.0f, forecast insolvency threshold %.0f", got, wantAt)
			}
		})
	}
}

// startPCIByID is each hex's starting PCI in a fresh game, keyed by hex id.
func startPCIByID(t *testing.T, cfg Config) map[string]float64 {
	t.Helper()
	g := newGame(t, cfg)
	out := make(map[string]float64, len(g.hexes))
	for _, h := range g.hexes {
		out[h.id] = h.pci
	}
	return out
}

// TestBoardStartIsDeterministic: the same board starts the same way on every
// run, whatever order its hexes arrive in, and the condition varies across it.
func TestBoardStartIsDeterministic(t *testing.T) {
	cfg := boardCases[0].config()
	first := startPCIByID(t, cfg)
	if again := startPCIByID(t, cfg); !maps.Equal(first, again) {
		t.Error("two games from one config started different boards")
	}
	reversed := cfg
	reversed.Hexes = slices.Clone(cfg.Hexes)
	slices.Reverse(reversed.Hexes)
	if got := startPCIByID(t, reversed); !maps.Equal(first, got) {
		t.Error("reordering the hexes changed the board they start")
	}
	distinct := map[float64]bool{}
	for _, pci := range first {
		distinct[pci] = true
	}
	if want := len(forecast.SpreadCohorts([]forecast.RawCohort{{Area: 1}}, cfg.InitialPCI)); len(distinct) != want {
		t.Errorf("the board starts at %d distinct PCIs, want the %d condition bands", len(distinct), want)
	}
}

// TestBoardBandsScatter: the condition bands scatter across the map rather
// than filling it in blocks. The synthetic board's ids are grid cells,
// "hex:column:row" as the hex grid names them, and every band turns up in nearly every row and every column; a band
// laid down as a block would sit in a few of either.
func TestBoardBandsScatter(t *testing.T) {
	g := newGame(t, boardCases[0].config())
	for axis, name := range map[int]string{1: "column", 2: "row"} {
		lines := map[string]map[float64]bool{} // column or row -> bands seen in it
		bands := map[float64]bool{}
		for _, h := range g.hexes {
			line := strings.Split(h.id, ":")[axis]
			if lines[line] == nil {
				lines[line] = map[float64]bool{}
			}
			lines[line][h.pci] = true
			bands[h.pci] = true
		}
		for band := range bands {
			var in int
			for _, seen := range lines {
				if seen[band] {
					in++
				}
			}
			if share := float64(in) / float64(len(lines)); share < 0.9 {
				t.Errorf("band at PCI %.1f turns up in %d of %d %ss; want nearly all", band, in, len(lines), name)
			}
		}
	}
}

// TestIdleBoardBacklogTracksForecast: played idle at frame-sized ticks, the
// board defers the forecast baseline's need year after year, so the backlog
// the loss condition reads is the forecast's deferred backlog. The tick is a
// power of two so the ticks sum to each year's end exactly: a tick that
// straddles a year's end accrues to the year it ends, a frame's worth of need
// this test is not about.
func TestIdleBoardBacklogTracksForecast(t *testing.T) {
	const years = 6
	for _, c := range boardCases {
		t.Run(c.name, func(t *testing.T) {
			cfg := c.config()
			cfg.AnnualBudget = 0
			cfg.HorizonYears = 40
			want := forecastBaseline(cfg, years)
			g := newGame(t, cfg)
			for g.yearsDone < years && g.status == statusRunning {
				before := g.yearsDone
				g.Tick(1.0 / 64)
				if g.yearsDone == before {
					continue
				}
				fc := want.Years[g.yearsDone-1].DeferredBacklog
				if rel := g.backlog/fc - 1; math.Abs(rel) > boardYear1NeedTolerance {
					t.Errorf("year %d: board backlog %.0f is %+.2f%% from the forecast's %.0f",
						g.yearsDone, g.backlog, 100*rel, fc)
				}
			}
			if g.yearsDone < years {
				t.Logf("the game ended (%s) after %d of %d years", g.lossReason, g.yearsDone, years)
			}
		})
	}
}
