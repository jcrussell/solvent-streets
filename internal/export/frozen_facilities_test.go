package export

import (
	"context"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/metric"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/services"
)

// TestBuildHexMetrics_MarkersAndLegendFromTheRun: the station markers and the
// coverage legend come from the service run that produced the colours. A
// station moved or added after the compute, and a turnout_s edit, change
// neither; the travel threshold, a presentation setting, still follows the
// config.
func TestBuildHexMetrics_MarkersAndLegendFromTheRun(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	c := newSQLiteCity(t, cfg, "Fire City", exportBoundaryA)
	h := drawnHexIDs(t, c.entry(), 2)

	configID := services.ConfigFacilityID("fire", -122.45, 37.55)
	hash := cfg.CityHash(&c.city)
	// A roads run with coverage, so the city has a grid to draw the metric on.
	roads, err := c.store.CreateRun(ctx, "roads", hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.SaveHexMetrics(ctx, roads.ID, results.HexCoverageRows(resource.TypeRoads, []results.HexCoverage{{HexID: h[0], Area: 1}})); err != nil {
		t.Fatal(err)
	}
	run, err := c.store.CreateRun(ctx, db.ServiceProducer("fire"), hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.SaveHexMetrics(ctx, run.ID, []db.HexMetric{
		{HexID: h[0], Metric: metric.FireResponseS, Value: 300, Ref: "osm:node:9"},
		{HexID: h[1], Metric: metric.FireResponseS, Value: 400, Ref: configID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.store.SaveRunFacilities(ctx, run.ID, []db.RunFacility{
		{ID: "osm:node:9", Name: "Station 9", Source: "osm", Lon: -122.48, Lat: 37.52},
		{ID: configID, Name: "Manual", Source: "config", Lon: -122.45, Lat: 37.55},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.store.SaveCityMetrics(ctx, run.ID, []db.CityMetric{
		{Metric: metric.TurnoutS, Subject: db.ServiceProducer("fire"), Value: 80},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbtest.Publish(ctx, c.root, c.id, hash, cfg.ResolvedHexEdge(&c.city), map[string]int64{"roads": roads.ID, db.ServiceProducer("fire"): run.ID}); err != nil {
		t.Fatal(err)
	}
	entry, err := pinNewestSnapshot(ctx, c.entry())
	if err != nil {
		t.Fatal(err)
	}

	// After the compute: the OSM station moves, a hand-placed one is added,
	// and turnout_s and travel_threshold_s are edited.
	if err := c.store.UpsertFeatures(ctx, resource.FacilityType("fire"), []db.Feature{
		{ID: "osm:node:9", Name: "Station 9 (moved)", SourceAPI: "overpass", GeometryJSON: `{"type":"Point","coordinates":[-122.41,37.59]}`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	entry.City.Facilities = []config.FacilityConfig{
		{Service: "fire", Name: "Added", Lon: -122.42, Lat: 37.58},
		{Service: "fire", Name: "Manual", Lon: -122.45, Lat: 37.55},
	}
	entry.Config = &config.Config{Services: []config.ServiceOverride{{Name: "fire", TurnoutS: new(200.0), TravelThresholdS: new(300.0)}}}

	hm, err := BuildHexMetrics(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	m := hm.Metrics[metric.FireResponseS]
	if len(m.Points) != 2 {
		t.Fatalf("points = %+v, want the run's two stations", m.Points)
	}
	if p := m.Points[m.Refs[h[0]]]; p.Name != "Station 9" || p.Position != [2]float64{-122.48, 37.52} {
		t.Errorf("h1's station = %+v, want Station 9 where the run routed from", p)
	}
	if p := m.Points[m.Refs[h[1]]]; p.Name != "Manual" || p.Source != "config" {
		t.Errorf("h2's station = %+v, want the run's Manual", p)
	}
	// 80 s turnout from the run + 300 s travel threshold from the config.
	if m.ThresholdS != 380 || m.Scale.Bands[0] != 380 {
		t.Errorf("threshold/bands = %v / %v, want 380 (the run's 80 s turnout + the config's 300 s)", m.ThresholdS, m.Scale.Bands)
	}
}

// TestBuildHexMetrics_SourceStatesTheRunsTurnout is the znqw regression: the
// published methodology line names the turnout the run's times include, not
// the registry's NFPA 1710 80 s, when [[services]] turnout_s overrode it.
func TestBuildHexMetrics_SourceStatesTheRunsTurnout(t *testing.T) {
	for _, tc := range []struct {
		turnout float64
		want    string
	}{
		{80, "NFPA 1710 80 s turnout"},
		{60, ": 60 s turnout plus travel"},
	} {
		ctx := context.Background()
		cfg := &config.Config{}
		c := newSQLiteCity(t, cfg, "Fire City", exportBoundaryA)
		h := drawnHexIDs(t, c.entry(), 1)
		hash := cfg.CityHash(&c.city)
		roads, err := c.store.CreateRun(ctx, "roads", hash, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.store.SaveHexMetrics(ctx, roads.ID, results.HexCoverageRows(resource.TypeRoads, []results.HexCoverage{{HexID: h[0], Area: 1}})); err != nil {
			t.Fatal(err)
		}
		run, err := c.store.CreateRun(ctx, db.ServiceProducer("fire"), hash, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.store.SaveHexMetrics(ctx, run.ID, []db.HexMetric{{HexID: h[0], Metric: metric.FireResponseS, Value: 300}}); err != nil {
			t.Fatal(err)
		}
		if err := c.store.SaveCityMetrics(ctx, run.ID, []db.CityMetric{
			{Metric: metric.TurnoutS, Subject: db.ServiceProducer("fire"), Value: tc.turnout},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := dbtest.Publish(ctx, c.root, c.id, hash, cfg.ResolvedHexEdge(&c.city), map[string]int64{"roads": roads.ID, db.ServiceProducer("fire"): run.ID}); err != nil {
			t.Fatal(err)
		}
		entry, err := pinNewestSnapshot(ctx, c.entry())
		if err != nil {
			t.Fatal(err)
		}
		hm, err := BuildHexMetrics(ctx, entry)
		if err != nil {
			t.Fatal(err)
		}
		if got := hm.Metrics[metric.FireResponseS].Source; !strings.Contains(got, tc.want) {
			t.Errorf("turnout %v: source = %q, want it to contain %q", tc.turnout, got, tc.want)
		}
	}
}
