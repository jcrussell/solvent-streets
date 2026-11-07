package services

import (
	"errors"
	"math"
	"strings"
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
	over := Resolve(spec, []config.ServiceOverride{{Name: "fire", TurnoutS: new(200.0)}, {Name: "police", TravelThresholdS: new(1.0)}})
	if over.TurnoutS != 200 || over.TravelThresholdS != spec.TravelThresholdS || over.LegendBands()[0] != 440 {
		t.Errorf("Resolve: turnout %v threshold %v bands %v", over.TurnoutS, over.TravelThresholdS, over.LegendBands())
	}
	if fm, ok := ForMetric(MetricFire); !ok || fm.Name != "fire" {
		t.Errorf("ForMetric(%s) = %+v, %v", MetricFire, fm, ok)
	}
	if n := Names(); len(n) != 1 || n[0] != "fire" {
		t.Errorf("Names() = %v, want [fire]", n)
	}
}

// TestResolve_ExplicitZeroIsHonoured: turnout_s = 0 and
// travel_threshold_s = 0 replace the registry defaults (travel time alone,
// covered only at a station) rather than reading as "unset", while an
// override that leaves a field unset keeps that field's default.
func TestResolve_ExplicitZeroIsHonoured(t *testing.T) {
	spec := Registry["fire"]
	got := Resolve(spec, []config.ServiceOverride{{Name: "fire", TurnoutS: new(0.0), TravelThresholdS: new(0.0)}})
	if got.TurnoutS != 0 || got.TravelThresholdS != 0 || got.LegendBands()[0] != 0 {
		t.Errorf("explicit zeros: turnout %v threshold %v first band %v, want all 0", got.TurnoutS, got.TravelThresholdS, got.LegendBands()[0])
	}
	if got := Resolve(spec, []config.ServiceOverride{{Name: "fire"}}); got.TurnoutS != spec.TurnoutS || got.TravelThresholdS != spec.TravelThresholdS {
		t.Errorf("unset fields: %+v, want the registry defaults", got)
	}
}

// TestValidateConfig: a [[services]] name or a [[cities.facilities]] service
// that is not registered is an invalid config, naming the bad value, so a
// typo cannot silently keep the defaults or drop a hand-placed station.
func TestValidateConfig(t *testing.T) {
	city := func(service string) config.CityConfig {
		return config.CityConfig{Name: "Oakland", Facilities: []config.FacilityConfig{{Service: service, Lat: 37.8, Lon: -122.2}}}
	}
	for name, tc := range map[string]struct {
		cfg  config.Config
		want string // "" = valid
	}{
		"registered":            {cfg: config.Config{Services: []config.ServiceOverride{{Name: "fire"}}, Cities: []config.CityConfig{city("fire")}}},
		"typo'd services name":  {cfg: config.Config{Services: []config.ServiceOverride{{Name: "fier"}}}, want: `"fier"`},
		"typo'd facility":       {cfg: config.Config{Cities: []config.CityConfig{city("fier")}}, want: `facilities[0].service "fier"`},
		"name with stray space": {cfg: config.Config{Services: []config.ServiceOverride{{Name: "fire "}}}, want: `"fire "`},
	} {
		err := ValidateConfig(&tc.cfg)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", name, err)
		case tc.want != "" && (!errors.Is(err, config.ErrInvalidConfig) || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: got %v, want ErrInvalidConfig naming %s", name, err, tc.want)
		}
	}
}
