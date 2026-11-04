package services

import (
	"math"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/hexmetric"
)

func TestParseMaxspeed(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"50", 50, true},
		{" 40 ", 40, true},
		{"25 mph", 25 * mphToKmh, true},
		{"25mph", 25 * mphToKmh, true},
		{"50 km/h", 50, true},
		{"50kmh", 50, true},
		{"signals", 0, false},
		{"none", 0, false},
		{"walk", 0, false},
		{"30;50", 0, false},
		{"", 0, false},
		{"0", 0, false},
		{"999", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParseMaxspeed(tc.in)
		if ok != tc.ok || math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("ParseMaxspeed(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSpeedTable_SpeedKmh(t *testing.T) {
	tbl := DefaultRoadSpeeds
	cases := []struct {
		tags map[string]string
		want float64
	}{
		{map[string]string{"highway": "motorway"}, 100},
		{map[string]string{"highway": "residential"}, 40},
		{map[string]string{"highway": "residential", "maxspeed": "25 mph"}, 25 * mphToKmh},
		{map[string]string{"highway": "residential", "maxspeed": "signals"}, 40},
		{map[string]string{"highway": "busway"}, tbl.Default},
		{map[string]string{}, tbl.Default},
		{nil, tbl.Default},
	}
	for _, tc := range cases {
		if got := tbl.SpeedKmh(tc.tags); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("SpeedKmh(%v) = %v, want %v", tc.tags, got, tc.want)
		}
	}
}

// TestRegistry_FireRegistersItsMetric: importing the package registers the
// fire metric with bands that match the service spec, so the exporter and the
// dashboard describe the same legend the compute pass stores against.
func TestRegistry_FireRegistersItsMetric(t *testing.T) {
	spec, ok := Lookup("fire")
	if !ok {
		t.Fatal("fire service not registered")
	}
	m, ok := hexmetric.Lookup(MetricFire)
	if !ok {
		t.Fatalf("hexmetric %q not registered by init()", MetricFire)
	}
	if m.Format != hexmetric.FormatMinutes || len(m.Scale.Bands) != len(spec.LegendBands()) {
		t.Errorf("fire metric spec = %+v, want minutes format and %d bands", m, len(spec.LegendBands()))
	}
	if b := spec.LegendBands(); len(b) != 4 || b[0] != spec.TurnoutS+spec.TravelThresholdS || m.Scale.Bands[0] != b[0] {
		t.Errorf("legend bands %v should start at turnout+threshold %v and match the registered metric %v", b, spec.TurnoutS+spec.TravelThresholdS, m.Scale.Bands)
	}
	over := Resolve(spec, []config.ServiceOverride{{Name: "fire", TurnoutS: 200}, {Name: "police", TravelThresholdS: 1}})
	if over.TurnoutS != 200 || over.TravelThresholdS != spec.TravelThresholdS || over.LegendBands()[0] != 440 {
		t.Errorf("Resolve: turnout %v threshold %v bands %v", over.TurnoutS, over.TravelThresholdS, over.LegendBands())
	}
	if err := ValidateOverrides([]config.ServiceOverride{{Name: "police"}}); err == nil {
		t.Error("ValidateOverrides must reject an unregistered service name")
	}
	if err := ValidateOverrides([]config.ServiceOverride{{Name: "fire"}}); err != nil {
		t.Errorf("ValidateOverrides rejected fire: %v", err)
	}
	if fm, ok := ForMetric(MetricFire); !ok || fm.Name != "fire" {
		t.Errorf("ForMetric(%s) = %+v, %v", MetricFire, fm, ok)
	}
	if n := Names(); len(n) != 1 || n[0] != "fire" {
		t.Errorf("Names() = %v, want [fire]", n)
	}
}
