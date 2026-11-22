package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/export/exporttest"
	"github.com/jcrussell/solvent-streets/internal/goldentest"
)

// TestExportForecast_SummaryGolden is the catch-all for the forecast math
// behind scenarios.json and forecast.json: anything that moves a headline
// number fails here, including changes no contract test thought to assert.
// internal/export pins those artifacts by contract (keys, types,
// invariants); this is the one whole-output check, kept summary-shaped:
// one JSON line per series with its headline numbers,
// rounded to 6 significant digits so last-digit float noise (a Go release
// reassociating a sum) does not churn it.
//
// On a mismatch the test writes the actual output under bin/golden/ and
// prints the `cp -f` that accepts it. Read the diff first.
func TestExportForecast_SummaryGolden(t *testing.T) {
	ctx := context.Background()
	fc := exporttest.ForecastConfig()
	entry := export.CityEntry{
		Config: &config.Config{},
		City:   config.CityConfig{Name: exporttest.CityName},
		Store:  exporttest.Store(),
		Slug:   exporttest.Slug,
	}

	scenarios, err := export.BuildScenariosData(ctx, entry, &fc)
	if err != nil {
		t.Fatalf("BuildScenariosData: %v", err)
	}
	forecasts, err := export.BuildForecastsForCity(ctx, entry, &fc, export.ConvertCostTiers(&fc))
	if err != nil {
		t.Fatalf("BuildForecastsForCity: %v", err)
	}

	var buf bytes.Buffer
	var sc struct {
		City []summarySeries `json:"city"`
		All  []summarySeries `json:"all"`
	}
	roundTrip(t, scenarios, &sc)
	for _, s := range sc.City {
		writeLine(t, &buf, s.summary("scenarios.json", "city", ""))
	}
	for _, s := range sc.All {
		writeLine(t, &buf, s.summary("scenarios.json", "all", ""))
	}

	var fe []struct {
		ResourceType     string          `json:"resource_type"`
		Baseline         summarySeries   `json:"baseline"`
		CityBaseline     *summarySeries  `json:"city_baseline"`
		Scenarios        []summarySeries `json:"scenarios"`
		TippingPointYear *int            `json:"tipping_point_year"`
		FundingGap       *float64        `json:"funding_gap"`
		BreakEvenBudget  *float64        `json:"break_even_budget"`
		CurrentBudget    *float64        `json:"current_budget"`
	}
	roundTrip(t, forecasts, &fe)
	for _, e := range fe {
		if e.CityBaseline != nil {
			writeLine(t, &buf, e.CityBaseline.summary("forecast.json", "city", e.ResourceType))
		}
		writeLine(t, &buf, e.Baseline.summary("forecast.json", "all", e.ResourceType))
		for _, s := range e.Scenarios {
			writeLine(t, &buf, s.summary("forecast.json", "city", e.ResourceType))
		}
		writeLine(t, &buf, map[string]any{
			"file":               "forecast.json",
			"resource":           e.ResourceType,
			"solvency":           true,
			"tipping_point_year": e.TippingPointYear,
			"funding_gap":        sig6Ptr(e.FundingGap),
			"break_even_budget":  sig6Ptr(e.BreakEvenBudget),
			"current_budget":     sig6Ptr(e.CurrentBudget),
		})
	}

	goldentest.Check(t, filepath.Join("testdata", "export_summary.golden.jsonl"), buf.Bytes())
}

type summarySeries struct {
	Scenario struct {
		Name string `json:"name"`
	} `json:"scenario"`
	Years []struct {
		PCI             float64 `json:"pci"`
		Area            float64 `json:"area"`
		AnnualSpend     float64 `json:"annual_spend"`
		DeferredBacklog float64 `json:"deferred_backlog"`
	} `json:"years"`
}

func (s summarySeries) summary(file, scope, resource string) map[string]any {
	m := map[string]any{"file": file, "scope": scope, "scenario": s.Scenario.Name, "years": len(s.Years)}
	if resource != "" {
		m["resource"] = resource
	}
	if len(s.Years) == 0 {
		return m
	}
	first, last := s.Years[0], s.Years[len(s.Years)-1]
	var spend float64
	for _, y := range s.Years {
		spend += y.AnnualSpend
	}
	m["pci_first"] = goldentest.Sig6(first.PCI)
	m["pci_last"] = goldentest.Sig6(last.PCI)
	m["area_first"] = goldentest.Sig6(first.Area)
	m["spend_total"] = goldentest.Sig6(spend)
	m["backlog_last"] = goldentest.Sig6(last.DeferredBacklog)
	return m
}

func roundTrip(t *testing.T, in, out any) {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

// writeLine appends m as one JSON line; encoding/json sorts map keys.
func writeLine(t *testing.T, buf *bytes.Buffer, m map[string]any) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	buf.Write(b)
	buf.WriteByte('\n')
}

func sig6Ptr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := goldentest.Sig6(*v)
	return &r
}
