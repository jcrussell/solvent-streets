package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/logs"
)

// replayFixture is a trimmed real water strip from the batch-1 replay
// (testdata/water/*.json; OpenStreetMap data, ODbL): the query bbox, the
// unstripped boundary, the dense road probes, and the coastline ways and
// water relations that decide the case, with every vertex past a stride
// dropped and coordinates rounded. land_km2 is the city's 2023 Census
// Gazetteer land area.
type replayFixture struct {
	BBox    [4]float64 `json:"bbox"`
	LandKm2 float64    `json:"land_km2"`
	// A fixture cropped to part of a big city stores, instead of the
	// Census land, the area of the full-data strip inside the crop
	// (expect_km2), and the full run's allowance (max_hits), since the
	// crop holds only some of the city's probes.
	ExpectKm2 float64         `json:"expect_km2"`
	MaxHits   int             `json:"max_hits"`
	Boundary  json.RawMessage `json:"boundary"`
	Probes    [][2]float64    `json:"probes"`
	Coastline [][][2]float64  `json:"coastline"`
	Relations []struct {
		ID    int64          `json:"id"`
		Outer [][][2]float64 `json:"outer"`
		Inner [][][2]float64 `json:"inner"`
	} `json:"relations"`
}

func geometryOf(pts [][2]float64) []overpassGeometryPoint {
	out := make([]overpassGeometryPoint, len(pts))
	for i, p := range pts {
		out[i] = overpassGeometryPoint{Lon: p[0], Lat: p[1]}
	}
	return out
}

func (f replayFixture) elements() []overpassElement {
	var els []overpassElement
	for i, c := range f.Coastline {
		els = append(els, overpassElement{Type: elementWay, ID: int64(i + 1), Tags: map[string]string{"natural": "coastline"}, Geometry: geometryOf(c)})
	}
	for _, r := range f.Relations {
		e := overpassElement{Type: elementRelation, ID: r.ID, Tags: map[string]string{"natural": "water", "type": "multipolygon"}}
		for i, w := range r.Outer {
			e.Members = append(e.Members, overpassRelationMember{Type: elementWay, Ref: int64(i + 1), Role: "outer", Geometry: geometryOf(w)})
		}
		for i, w := range r.Inner {
			e.Members = append(e.Members, overpassRelationMember{Type: elementWay, Ref: int64(100000 + i), Role: "inner", Geometry: geometryOf(w)})
		}
		els = append(els, e)
	}
	return els
}

// replayStrip strips the fixture's water from its boundary with its road
// probes and returns the stripped area in km² and the WARN log.
func replayStrip(t *testing.T, name string) (float64, replayFixture, string) {
	t.Helper()
	data, err := os.ReadFile("testdata/water/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var f replayFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	ctx := logs.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	probes := LandProbes{Points: f.Probes, MaxHits: waterLandHitAllowance(len(f.Probes)), Dense: true}
	if f.MaxHits > 0 {
		probes.MaxHits = f.MaxHits
	}
	water, err := assembleWaterGeoJSON(ctx, f.elements(), f.BBox, probes)
	if err != nil {
		t.Fatal(err)
	}
	boundary := string(f.Boundary)
	if water != "" {
		if boundary, err = geo.SubtractGeoJSON(ctx, boundary, water); err != nil {
			t.Fatal(err)
		}
	}
	area, err := geo.BoundaryArea(ctx, boundary)
	if err != nil {
		t.Fatal(err)
	}
	return area / 1e6, f, buf.String()
}

// TestReplay_WaterStripMatchesCensusLand pins the batch-1 replay cities whose
// strip went wrong to their
// land area, on trimmed real data:
//   - Alameda (an island city): its bay face holds 4 wharf roads against the
//     island's 424; judged chain by chain both closures were over the
//     allowance of 3 and the bay stayed in (1.92x land).
//   - Juneau (cropped to Douglas Island, the mainland shore and the
//     borough's part of Admiralty Island): judged chain by chain the
//     channels stayed in (1.18x), or, taking the low side, Admiralty went
//     with them (0.96x); as faces Admiralty is land and the sea is water.
//   - New York (cropped to Lower Manhattan, Brooklyn and the harbor): its
//     chains cut through several boroughs, so both closures of each held
//     land and the harbor stayed in (1.52x); as faces each borough is land.
//   - Burlington and Detroit: their lake and river relations' islands far
//     outside the bbox were kept as unclipped holes, whose area made the
//     clipped polygon read as waterless, so Lake Champlain and the Detroit
//     River were dropped (1.45x and 1.03x land).
//
// The two crops are pinned to the full-data strip's area inside the crop.
func TestReplay_WaterStripMatchesCensusLand(t *testing.T) {
	for _, c := range []struct {
		name string
		tol  float64 // allowed |stripped/land - 1|
	}{
		{"alameda", 0.05},
		{"juneau", 0.02},
		{"new-york", 0.02},
		{"burlington", 0.05},
		{"detroit", 0.02},
	} {
		t.Run(c.name, func(t *testing.T) {
			km2, f, warns := replayStrip(t, c.name)
			want := f.LandKm2
			if f.ExpectKm2 > 0 {
				want = f.ExpectKm2
			}
			if r := km2 / want; r < 1-c.tol || r > 1+c.tol {
				t.Errorf("stripped boundary %.1f km² is %.3fx the expected land %.1f km²\nwarnings:\n%s", km2, r, want, warns)
			}
		})
	}
}
