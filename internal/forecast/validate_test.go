package forecast_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/forecast/bridge"
	"github.com/jcrussell/solvent-streets/internal/game"
)

// validInputs is inside every bound, with every field set, so a case that
// moves one field tests that field alone.
func validInputs() forecast.Inputs {
	return forecast.Inputs{
		InitialPCI:     80,
		DecayRate:      0.04,
		GrowthRate:     0.01,
		Years:          30,
		Budget:         1e6,
		CycleYears:     12,
		CostMultiplier: 1.5,
		CostTiers: []forecast.CostTier{
			{MinPCI: 70, MaxPCI: 101, CostPerSqM: 5, Label: "preventive"},
			{MinPCI: 0, MaxPCI: 70, CostPerSqM: 150, Label: "reconstruction"},
		},
		Cohorts: []forecast.RawCohort{
			{Classification: "residential", Area: 1000, DecayRate: 0.04},
		},
	}
}

// boundCase moves one input of validInputs. field is the Field* the error
// must name when the result is out of bounds; "" means it stays in bounds.
type boundCase struct {
	name  string
	set   func(*forecast.Inputs)
	field string
}

func tier(f func(*forecast.CostTier)) func(*forecast.Inputs) {
	return func(in *forecast.Inputs) { f(&in.CostTiers[0]) }
}

func cohort(f func(*forecast.RawCohort)) func(*forecast.Inputs) {
	return func(in *forecast.Inputs) { f(&in.Cohorts[0]) }
}

var boundCases = []boundCase{
	{"valid", func(*forecast.Inputs) {}, ""},

	{"initial_pci 0", func(in *forecast.Inputs) { in.InitialPCI = 0 }, ""},
	{"initial_pci 100", func(in *forecast.Inputs) { in.InitialPCI = 100 }, ""},
	{"initial_pci negative", func(in *forecast.Inputs) { in.InitialPCI = -1 }, forecast.FieldInitialPCI},
	{"initial_pci over 100", func(in *forecast.Inputs) { in.InitialPCI = 100.5 }, forecast.FieldInitialPCI},
	{"initial_pci NaN", func(in *forecast.Inputs) { in.InitialPCI = math.NaN() }, forecast.FieldInitialPCI},

	{"decay_rate 0", func(in *forecast.Inputs) { in.DecayRate = 0 }, ""},
	{"decay_rate 1", func(in *forecast.Inputs) { in.DecayRate = 1 }, ""},
	{"decay_rate negative", func(in *forecast.Inputs) { in.DecayRate = -0.1 }, forecast.FieldDecayRate},
	{"decay_rate over 1", func(in *forecast.Inputs) { in.DecayRate = 1.5 }, forecast.FieldDecayRate},
	{"decay_rate NaN", func(in *forecast.Inputs) { in.DecayRate = math.NaN() }, forecast.FieldDecayRate},

	{"growth_rate 0", func(in *forecast.Inputs) { in.GrowthRate = 0 }, ""},
	{"growth_rate -0.5", func(in *forecast.Inputs) { in.GrowthRate = -0.5 }, ""},
	{"growth_rate 0.5", func(in *forecast.Inputs) { in.GrowthRate = 0.5 }, ""},
	{"growth_rate under -0.5", func(in *forecast.Inputs) { in.GrowthRate = -0.6 }, forecast.FieldGrowthRate},
	{"growth_rate over 0.5", func(in *forecast.Inputs) { in.GrowthRate = 0.6 }, forecast.FieldGrowthRate},
	{"growth_rate +Inf", func(in *forecast.Inputs) { in.GrowthRate = math.Inf(1) }, forecast.FieldGrowthRate},

	{"years 1", func(in *forecast.Inputs) { in.Years = 1 }, ""},
	{"years at the maximum", func(in *forecast.Inputs) { in.Years = forecast.MaxHorizonYears }, ""},
	{"years over the maximum", func(in *forecast.Inputs) { in.Years = forecast.MaxHorizonYears + 1 }, forecast.FieldYears},
	{"years 1000", func(in *forecast.Inputs) { in.Years = 1000 }, forecast.FieldYears},
	{"years negative", func(in *forecast.Inputs) { in.Years = -5 }, forecast.FieldYears},

	{"budget 0", func(in *forecast.Inputs) { in.Budget = 0 }, ""},
	{"budget negative", func(in *forecast.Inputs) { in.Budget = -1 }, forecast.FieldBudget},
	{"budget NaN", func(in *forecast.Inputs) { in.Budget = math.NaN() }, forecast.FieldBudget},
	{"budget +Inf", func(in *forecast.Inputs) { in.Budget = math.Inf(1) }, forecast.FieldBudget},

	{"cycle unset", func(in *forecast.Inputs) { in.CycleYears = 0 }, ""},
	{"cycle 1", func(in *forecast.Inputs) { in.CycleYears = 1 }, ""},
	{"cycle 40", func(in *forecast.Inputs) { in.CycleYears = 40 }, ""},
	{"cycle sub-annual", func(in *forecast.Inputs) { in.CycleYears = 1e-9 }, forecast.FieldCycleYears},
	{"cycle over 40", func(in *forecast.Inputs) { in.CycleYears = 41 }, forecast.FieldCycleYears},
	{"cycle negative", func(in *forecast.Inputs) { in.CycleYears = -3 }, forecast.FieldCycleYears},
	{"cycle NaN", func(in *forecast.Inputs) { in.CycleYears = math.NaN() }, forecast.FieldCycleYears},

	{"multiplier unset", func(in *forecast.Inputs) { in.CostMultiplier = 0 }, ""},
	{"multiplier 5", func(in *forecast.Inputs) { in.CostMultiplier = 5 }, ""},
	{"multiplier over 5", func(in *forecast.Inputs) { in.CostMultiplier = 5.5 }, forecast.FieldCostMultiplier},
	{"multiplier negative", func(in *forecast.Inputs) { in.CostMultiplier = -1 }, forecast.FieldCostMultiplier},
	{"multiplier NaN", func(in *forecast.Inputs) { in.CostMultiplier = math.NaN() }, forecast.FieldCostMultiplier},

	{"tier cost 0", tier(func(t *forecast.CostTier) { t.CostPerSqM = 0 }), forecast.FieldCostTiers},
	{"tier cost negative", tier(func(t *forecast.CostTier) { t.CostPerSqM = -5 }), forecast.FieldCostTiers},
	{"tier cost NaN", tier(func(t *forecast.CostTier) { t.CostPerSqM = math.NaN() }), forecast.FieldCostTiers},
	{"tier cost +Inf", tier(func(t *forecast.CostTier) { t.CostPerSqM = math.Inf(1) }), forecast.FieldCostTiers},
	{"tier min negative", tier(func(t *forecast.CostTier) { t.MinPCI = -1 }), forecast.FieldCostTiers},
	{"tier max 100", tier(func(t *forecast.CostTier) { t.MaxPCI = 100 }), ""},
	{"tier max over 101", tier(func(t *forecast.CostTier) { t.MaxPCI = 102 }), forecast.FieldCostTiers},
	{"tier band inverted", tier(func(t *forecast.CostTier) { t.MinPCI, t.MaxPCI = 90, 80 }), forecast.FieldCostTiers},
	{"tier band empty", tier(func(t *forecast.CostTier) { t.MinPCI, t.MaxPCI = 80, 80 }), forecast.FieldCostTiers},
	{"tier min NaN", tier(func(t *forecast.CostTier) { t.MinPCI = math.NaN() }), forecast.FieldCostTiers},
	{"tier no label", tier(func(t *forecast.CostTier) { t.Label = "" }), forecast.FieldCostTiers},
	// Bands may leave a gap (TierIndexForPCI takes the nearest) but not
	// overlap: a PCI in two bands has two prices.
	{"tier bands leave a gap", tier(func(t *forecast.CostTier) { t.MinPCI = 80 }), ""},
	{"tier bands overlap", tier(func(t *forecast.CostTier) { t.MinPCI = 60 }), forecast.FieldCostTiers},

	{"cohort area 0", cohort(func(c *forecast.RawCohort) { c.Area = 0 }), ""},
	{"cohort area negative", cohort(func(c *forecast.RawCohort) { c.Area = -1 }), forecast.FieldCohorts},
	{"cohort area NaN", cohort(func(c *forecast.RawCohort) { c.Area = math.NaN() }), forecast.FieldCohorts},
	{"cohort decay unset", cohort(func(c *forecast.RawCohort) { c.DecayRate = 0 }), ""},
	// A road class scaled around decay_rate = 1 decays faster than 1.
	{"cohort decay over 1", cohort(func(c *forecast.RawCohort) { c.DecayRate = 1.3 }), ""},
	{"cohort decay negative", cohort(func(c *forecast.RawCohort) { c.DecayRate = -0.1 }), forecast.FieldCohorts},
	{"cohort decay +Inf", cohort(func(c *forecast.RawCohort) { c.DecayRate = math.Inf(1) }), forecast.FieldCohorts},
}

func TestInputsValidate(t *testing.T) {
	for _, tc := range boundCases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInputs()
			tc.set(&in)
			err := in.Validate(nil)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			var ie *forecast.InputError
			if !errors.As(err, &ie) {
				t.Fatalf("Validate = %v, want an *InputError for %s", err, tc.field)
			}
			if ie.Field != tc.field {
				t.Errorf("error names %q, want %q (%v)", ie.Field, tc.field, err)
			}
		})
	}
}

func TestInputsValidate_NamesAndFormat(t *testing.T) {
	in := validInputs()
	in.Budget = -1
	err := in.Validate(map[string]string{forecast.FieldBudget: "current_budget"})
	if got, want := err.Error(), "current_budget -1 must be a non-negative finite number"; got != want {
		t.Errorf("scalar error = %q, want %q", got, want)
	}

	in = validInputs()
	in.CostTiers[1].CostPerSqM = 0
	if got, want := in.Validate(nil).Error(), "cost_tiers[1] (reconstruction): cost_per_sqm 0 must be a positive finite number"; got != want {
		t.Errorf("tier error = %q, want %q", got, want)
	}

	in = validInputs()
	in.CostTiers[0].Label = ""
	if got, want := in.Validate(nil).Error(), "cost_tiers[0]: label must not be empty"; got != want {
		t.Errorf("unlabelled tier error = %q, want %q", got, want)
	}
}

// surfaceVerdicts runs in through each surface that accepts forecast inputs
// and reports whether it was accepted. A surface that has no such input is
// left out of the map.
func surfaceVerdicts(t *testing.T, in forecast.Inputs) map[string]bool {
	t.Helper()
	verdicts := map[string]bool{}

	// pvmt.toml. It carries no cohorts, and its horizon is a whole number.
	if in.Years == math.Trunc(in.Years) {
		fc := config.ForecastConfig{
			InitialPCI:          in.InitialPCI,
			DecayRate:           in.DecayRate,
			GrowthRate:          in.GrowthRate,
			HorizonYears:        int(in.Years),
			CurrentBudget:       in.Budget,
			TreatmentCycleYears: in.CycleYears,
			CostMultiplier:      in.CostMultiplier,
		}
		for _, t := range in.CostTiers {
			fc.CostTiers = append(fc.CostTiers, config.CostTierCfg{
				MinPCI: t.MinPCI, MaxPCI: t.MaxPCI, CostPerSqM: t.CostPerSqM, Label: t.Label,
			})
		}
		verdicts["config"] = fc.Validate() == nil
	}

	// The browser forecast.
	bin := bridge.Input{
		Area:           1000,
		InitialPCI:     in.InitialPCI,
		DecayRate:      in.DecayRate,
		GrowthRate:     in.GrowthRate,
		HorizonYears:   int(in.Years),
		AnnualBudget:   in.Budget,
		Strategy:       "worst_first",
		CycleYears:     in.CycleYears,
		CostMultiplier: in.CostMultiplier,
		CostTiers:      in.CostTiers,
	}
	for _, c := range in.Cohorts {
		bin.Cohorts = append(bin.Cohorts, bridge.Cohort{Classification: c.Classification, Area: c.Area, DecayRate: c.DecayRate})
	}
	_, _, _, _, err := bridge.Translate(bin)
	verdicts["bridge"] = err == nil

	// The game. It has no single decay rate.
	gcfg := game.Config{
		Hexes:               []game.HexConfig{{ID: "a", RoadsArea: 1000, DecayRate: 0.04}},
		InitialPCI:          in.InitialPCI,
		CostTiers:           in.CostTiers,
		CostMultiplier:      in.CostMultiplier,
		AnnualBudget:        in.Budget,
		HorizonYears:        in.Years,
		TreatmentCycleYears: in.CycleYears,
		GrowthRate:          in.GrowthRate,
	}
	for _, c := range in.Cohorts {
		gcfg.Cohorts = append(gcfg.Cohorts, game.CohortConfig{Classification: c.Classification, Area: c.Area, DecayRate: c.DecayRate})
	}
	_, err = game.New(gcfg)
	verdicts["game"] = err == nil

	return verdicts
}

// TestForecastBounds_OneAnswerOnEverySurface pins the config loader, the
// browser bridge and the game to forecast.Inputs.Validate: whatever one of
// them accepts, the others accept, so a config that loads exports a seed the
// browser runs.
func TestForecastBounds_OneAnswerOnEverySurface(t *testing.T) {
	// Inputs a surface does not take, so a case on one cannot move its verdict.
	absent := map[string][]string{
		"config": {forecast.FieldCohorts},
		"game":   {forecast.FieldDecayRate},
	}
	for _, tc := range boundCases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInputs()
			tc.set(&in)
			want := in.Validate(nil) == nil
			for surface, got := range surfaceVerdicts(t, in) {
				skip := false
				for _, f := range absent[surface] {
					skip = skip || f == tc.field
				}
				// A non-finite budget is refused by the game too, but a
				// negative one is clamped to no funding (game.noDebt).
				if skip || (surface == "game" && tc.field == forecast.FieldBudget && in.Budget < 0) {
					continue
				}
				if got != want {
					t.Errorf("%s accepted = %v, forecast.Inputs.Validate accepted = %v", surface, got, want)
				}
			}
		})
	}
}

// TestForecastBounds_ExportedSeedRunsInTheBrowser is the failure the shared
// bounds exist to prevent, end to end: the longest horizon a config may state
// is one the browser forecast and the game both run.
func TestForecastBounds_ExportedSeedRunsInTheBrowser(t *testing.T) {
	fc := config.ForecastConfig{HorizonYears: forecast.MaxHorizonYears}
	if err := fc.Validate(); err != nil {
		t.Fatalf("config rejects forecast.horizon_years = %d: %v", fc.HorizonYears, err)
	}
	raw, err := json.Marshal(bridge.Input{Area: 1000, InitialPCI: 80, HorizonYears: fc.HorizonYears, Strategy: "worst_first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Run(raw); err != nil {
		t.Errorf("bridge refuses horizon_years = %d: %v", fc.HorizonYears, err)
	}

	fc.HorizonYears++
	err = fc.Validate()
	if err == nil || !strings.Contains(err.Error(), "forecast.horizon_years") {
		t.Errorf("config accepts forecast.horizon_years = %d (err %v); the bridge would refuse its seed", fc.HorizonYears, err)
	}
}
