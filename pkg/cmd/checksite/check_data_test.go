package checksite

import (
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/forecast"
)

// TestBaselineViolation_ChecksBothScopes: the city-maintained baseline is held
// to the same do-nothing physics as the all-jurisdiction one, and the failure
// names the baseline that broke it.
func TestBaselineViolation_ChecksBothScopes(t *testing.T) {
	decaying := forecast.ScenarioResult{Years: []forecast.ScenarioYear{{PCI: 80, DeferredBacklog: 1}, {PCI: 70, DeferredBacklog: 2}}}
	improving := forecast.ScenarioResult{Years: []forecast.ScenarioYear{{PCI: 70, DeferredBacklog: 1}, {PCI: 80, DeferredBacklog: 2}}}

	if msg := baselineViolation(export.ForecastExport{Baseline: decaying, CityBaseline: &decaying}); msg != "" {
		t.Errorf("sound baselines flagged: %s", msg)
	}
	if msg := baselineViolation(export.ForecastExport{Baseline: decaying, CityBaseline: &improving}); !strings.HasPrefix(msg, "city_baseline PCI increased") {
		t.Errorf("an improving city_baseline = %q, want it named", msg)
	}
	if msg := baselineViolation(export.ForecastExport{Baseline: improving}); !strings.HasPrefix(msg, "baseline PCI increased") {
		t.Errorf("an improving baseline = %q, want it named", msg)
	}
}
