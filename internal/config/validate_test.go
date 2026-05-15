package config

import "testing"

func TestForecastConfig_Validate_RejectsBad(t *testing.T) {
	cases := map[string]ForecastConfig{
		"initial too high":     {InitialPCI: 200},
		"initial negative":     {InitialPCI: -5},
		"decay negative":       {DecayRate: -0.1},
		"decay too high":       {DecayRate: 1.5},
		"growth too high":      {GrowthRate: 1.5},
		"growth too negative":  {GrowthRate: -1.0},
		"years negative":       {Years: -1},
	}
	for name, fc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := fc.Validate(); err == nil {
				t.Errorf("expected error for %+v, got nil", fc)
			}
		})
	}
}

func TestForecastConfig_Validate_AcceptsOK(t *testing.T) {
	ok := ForecastConfig{InitialPCI: 85, DecayRate: 0.05, GrowthRate: 0.02, Years: 20}
	if err := ok.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	zero := ForecastConfig{}
	if err := zero.Validate(); err != nil {
		t.Errorf("zero values must be allowed (used as 'default' sentinels): %v", err)
	}
}
