package export

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/game"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// lineFeature builds a roads db.Feature from a two-point GeoJSON LineString and
// a highway class. Both endpoints sit well inside boundaryGeoJSON so the
// buffered footprint clips into the grid rather than the boundary band.
func lineFeature(id, highway string, lon1, lat1, lon2, lat2 float64) db.Feature {
	gj := `{"type":"LineString","coordinates":[[` +
		ftoa(lon1) + `,` + ftoa(lat1) + `],[` + ftoa(lon2) + `,` + ftoa(lat2) + `]]}`
	return db.Feature{
		ID:           id,
		ResourceType: resource.TypeRoads,
		Tags:         map[string]string{"highway": highway},
		GeometryJSON: gj,
	}
}

func ftoa(f float64) string {
	// Six decimals is ~0.1 m at these latitudes — enough fidelity for fixtures.
	return strconv.FormatFloat(f, 'f', 6, 64)
}

// playHexEntry wires a MockStore returning the given road features and the
// shared square boundary, with a 200 m hex edge.
func playHexEntry(roads []db.Feature) CityEntry {
	store := &dbtest.MockStore{
		ListFeaturesFunc: func(_ context.Context, rt resource.Type) ([]db.Feature, error) {
			if rt == resource.TypeRoads {
				return roads, nil
			}
			return nil, nil
		},
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			return boundaryGeoJSON, nil
		},
	}
	return CityEntry{
		Config: &config.Config{Grid: config.GridConfig{HexEdgeM: 200}},
		City:   config.CityConfig{Name: "Test City"},
		Store:  store,
		Slug:   "test-city",
	}
}

// TestBuildPlayHexes_BlendsRealClasses pins the core contract: per-hex k is a
// real area-weighted blend of the actual road classes' decay rates — a hex over
// a primary arterial (k=0.025) holds while a hex over a residential street
// (k=0.040) rots faster — and is NOT uniformly the 0.035 default. It also
// asserts every emitted id belongs to the served hex grid and carries positive
// road area.
func TestBuildPlayHexes_BlendsRealClasses(t *testing.T) {
	ctx := context.Background()
	// Two city-jurisdiction roads of different classes, ~6 km apart so they
	// land in distinct hexes: a primary in the southwest, residential in the
	// northeast. Both are well inside (-122.5..-122.4, 37.5..37.6).
	roads := []db.Feature{
		lineFeature("primary-1", "primary", -122.485, 37.515, -122.470, 37.515),
		lineFeature("res-1", "residential", -122.420, 37.585, -122.405, 37.585),
	}
	entry := playHexEntry(roads)

	_, lon0, lat0, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	proj := geo.NewUTMProjector(lon0, lat0)

	hexes, err := BuildPlayHexes(ctx, entry, proj)
	if err != nil {
		t.Fatalf("BuildPlayHexes: %v", err)
	}
	if len(hexes) == 0 {
		t.Fatal("BuildPlayHexes returned no hexes; expected road-bearing hexes")
	}

	// (a) Every emitted id must belong to the served hex grid (same grid the
	// hexgrid.geojson layer is built from).
	grid, err := cityHexGrid(ctx, entry, proj)
	if err != nil {
		t.Fatalf("cityHexGrid: %v", err)
	}
	gridIDs := make(map[string]bool, len(grid))
	for _, h := range grid {
		gridIDs[h.ID] = true
	}

	primaryK := forecast.DecayRateForClass("primary")         // 0.025
	residentialK := forecast.DecayRateForClass("residential") // 0.040
	defaultK := forecast.DefaultDecayRates["default"]         // 0.035

	var minK, maxK float64
	sawDifferent := false
	for i, ph := range hexes {
		// (a) id joins the grid.
		if !gridIDs[ph.ID] {
			t.Errorf("play hex id %q is not in the served hex grid", ph.ID)
		}
		// (b) positive road area.
		if ph.RoadArea <= 0 {
			t.Errorf("hex %q road_area = %v; want > 0", ph.ID, ph.RoadArea)
		}
		// (c) k is a real per-class rate, not a guessed/zero value.
		if ph.K <= 0 {
			t.Errorf("hex %q k = %v; want > 0", ph.ID, ph.K)
		}
		if i == 0 {
			minK, maxK = ph.K, ph.K
		}
		if ph.K < minK {
			minK = ph.K
		}
		if ph.K > maxK {
			maxK = ph.K
		}
	}
	if maxK > minK {
		sawDifferent = true
	}

	// (c) The blend produces a real spread, not a uniform default.
	if !sawDifferent {
		t.Errorf("all hexes share k=%v; expected a class-driven spread (primary vs residential)", minK)
	}
	// A pure-primary hex must hold slower than a pure-residential hex, and
	// neither pure hex should read as the 0.035 default.
	if minK > defaultK {
		t.Errorf("min k = %v; expected a primary-driven hex at/below %v", minK, primaryK)
	}
	if maxK < residentialK-1e-9 {
		t.Errorf("max k = %v; expected a residential-driven hex near %v", maxK, residentialK)
	}
	// Sanity: the spread brackets the per-class rates we fed in.
	if minK < primaryK-1e-9 || maxK > residentialK+1e-9 {
		t.Errorf("k spread [%v, %v] escaped the input class rates [%v, %v]", minK, maxK, primaryK, residentialK)
	}
}

// playHexEntryWithDecay is playHexEntry with a per-config decay_rate override,
// so the test can assert BuildPlayHexes routes road-class rates through the
// override (yvlv.18) rather than the class defaults.
func playHexEntryWithDecay(roads []db.Feature, decayRate float64) CityEntry {
	entry := playHexEntry(roads)
	entry.Config.Forecast.DecayRate = decayRate
	return entry
}

// TestBuildPlayHexes_HonorsDecayOverride pins yvlv.18: a configured decay_rate
// override must reshape the per-hex k for road classes, so the game board decays
// at the same rate as the macro insolvency forecast (which already applies the
// override via resolvedDecayRate / ScaleRoadDecay). A high override (0.08) must
// push a residential-only hex above its 0.040 class default.
func TestBuildPlayHexes_HonorsDecayOverride(t *testing.T) {
	ctx := context.Background()
	roads := []db.Feature{
		lineFeature("res-1", "residential", -122.420, 37.585, -122.405, 37.585),
	}

	const override = 0.08
	classDefault := forecast.DecayRateForClass("residential") // 0.040
	if override <= classDefault {
		t.Fatalf("test setup: override %v must exceed class default %v", override, classDefault)
	}

	baseEntry := playHexEntry(roads)
	overrideEntry := playHexEntryWithDecay(roads, override)

	_, lon0, lat0, err := baseEntry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	proj := geo.NewUTMProjector(lon0, lat0)

	base, err := BuildPlayHexes(ctx, baseEntry, proj)
	if err != nil {
		t.Fatalf("BuildPlayHexes (base): %v", err)
	}
	over, err := BuildPlayHexes(ctx, overrideEntry, proj)
	if err != nil {
		t.Fatalf("BuildPlayHexes (override): %v", err)
	}
	if len(base) == 0 || len(over) == 0 {
		t.Fatal("BuildPlayHexes returned no hexes; expected a road-bearing hex")
	}

	baseByID := make(map[string]float64, len(base))
	for _, ph := range base {
		baseByID[ph.ID] = ph.K
	}
	sawShift := false
	for _, ph := range over {
		bk, ok := baseByID[ph.ID]
		if !ok {
			continue
		}
		// Class-default k stays at 0.040; the override must lift it toward 0.08.
		if ph.K > bk+1e-9 {
			sawShift = true
		}
		if ph.K < classDefault-1e-9 || ph.K > override+1e-9 {
			t.Errorf("hex %q k = %v escaped [%v, %v] after override", ph.ID, ph.K, classDefault, override)
		}
	}
	if !sawShift {
		t.Errorf("decay_rate override %v did not change any per-hex k (base k=%v); BuildPlayHexes ignores the config override", override, classDefault)
	}
}

// gameBoardFromPlayHexes builds the game.Config the /play front-end assembles
// from play-hexes.json (game.js maps each entry straight to {id, road_area, k}),
// so a test can prove the emitted magnitudes still initialize a real board.
func gameBoardFromPlayHexes(hexes []PlayHex) game.Config {
	cfg := game.Config{
		InitialPCI:          80,
		CostTiers:           forecast.DefaultCostTiers,
		StartingBudget:      1_000_000,
		HorizonYears:        20,
		TreatmentCycleYears: 12,
	}
	for _, ph := range hexes {
		cfg.Hexes = append(cfg.Hexes, game.HexConfig{ID: ph.ID, RoadArea: ph.RoadArea, K: ph.K})
	}
	return cfg
}

// TestBuildPlayHexes_RoundsEmittedMagnitudes pins pav7.3: road_area and k are
// rounded at construction (2dp with a positive floor, and 4 significant figures)
// so play-hexes.json stops shipping full float64 mantissas. Rounding must not
// drop or reorder hexes — hexgrid.geojson and play-hexes.json are joined by id
// in the browser, so the emitted ids stay a sorted, unique subset of the served
// grid.
//
// The fixture CROSSES a primary and a residential so the hexes around the
// junction carry two classes and their k is a genuine blend
// ((a1·0.025+a2·0.040)/(a1+a2), a long mantissa) rather than a class default
// that happens to be short. Blended hexes are the common case in the wild —
// portland-or has 9,972 distinct k over 18,026 hexes — and only they can tell
// a rounded k from an unrounded one.
func TestBuildPlayHexes_RoundsEmittedMagnitudes(t *testing.T) {
	ctx := context.Background()
	roads := []db.Feature{
		lineFeature("primary-1", "primary", -122.460, 37.550, -122.440, 37.550),
		lineFeature("res-1", "residential", -122.450, 37.540, -122.450, 37.560),
	}
	entry := playHexEntry(roads)

	_, lon0, lat0, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	proj := geo.NewUTMProjector(lon0, lat0)

	hexes, err := BuildPlayHexes(ctx, entry, proj)
	if err != nil {
		t.Fatalf("BuildPlayHexes: %v", err)
	}
	if len(hexes) == 0 {
		t.Fatal("BuildPlayHexes returned no hexes; expected road-bearing hexes")
	}

	grid, err := cityHexGrid(ctx, entry, proj)
	if err != nil {
		t.Fatalf("cityHexGrid: %v", err)
	}
	gridIDs := make(map[string]bool, len(grid))
	for _, h := range grid {
		gridIDs[h.ID] = true
	}

	primaryK := forecast.DecayRateForClass("primary")
	residentialK := forecast.DecayRateForClass("residential")

	seen := make(map[string]bool, len(hexes))
	prev := ""
	sawBlend := false
	for _, ph := range hexes {
		if got := roundRoadArea(ph.RoadArea); got != ph.RoadArea {
			t.Errorf("hex %q road_area = %v; want 2dp-rounded (%v)", ph.ID, ph.RoadArea, got)
		}
		// Every emitted hex passed the area filter, so it must still read as
		// road-bearing after rounding (see roundRoadArea).
		if ph.RoadArea < minPlayHexArea {
			t.Errorf("hex %q road_area = %v; want >= %v", ph.ID, ph.RoadArea, minPlayHexArea)
		}
		if got := roundSig(ph.K, 4); got != ph.K {
			t.Errorf("hex %q k = %v; want 4 significant figures (%v)", ph.ID, ph.K, got)
		}
		if ph.K <= 0 {
			t.Errorf("hex %q k = %v; want > 0 (game.New rejects the whole board otherwise)", ph.ID, ph.K)
		}
		// A hex straddling the junction mixes both classes; its k lands strictly
		// between the two class rates and is what the k assertion above bites on.
		if ph.K > primaryK+1e-12 && ph.K < residentialK-1e-12 {
			sawBlend = true
		}
		if !gridIDs[ph.ID] {
			t.Errorf("play hex id %q is not in the served hex grid", ph.ID)
		}
		if seen[ph.ID] {
			t.Errorf("duplicate play hex id %q", ph.ID)
		}
		seen[ph.ID] = true
		if prev != "" && ph.ID < prev {
			t.Errorf("play hexes out of id order: %q after %q", ph.ID, prev)
		}
		prev = ph.ID
	}
	if !sawBlend {
		t.Fatalf("fixture produced no multi-class hex (all k are class defaults in [%v, %v]); "+
			"the k rounding assertion above cannot fail", primaryK, residentialK)
	}

	// The rounded board must still start.
	if _, err := game.New(gameBoardFromPlayHexes(hexes)); err != nil {
		t.Fatalf("game.New on rounded play hexes: %v", err)
	}
}

// TestRoundRoadArea_FloorsPositiveSliversAboveZero pins the road_area half of
// pav7.3. BuildPlayHexes filters on the UNROUNDED area, so a hex clipping the
// tip of a buffered road (0.00042 m² in the real export) reaches serialization
// as a live board cell; plain 2dp rounding would publish it as 0.00 and, per
// TestGameBoard_ZeroAreaHexKillsTheOutOfFundsSignal, disable that city's
// out-of-funds warning. Zero in must stay zero out — that value never reaches
// here, and inventing area for it would be a different bug.
func TestRoundRoadArea_FloorsPositiveSliversAboveZero(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"typical hex", 704.5835576057434, 704.58},
		{"rounds half away from zero", 0.005, 0.01},
		{"real-export smallest sliver", 0.00042, minPlayHexArea},
		{"far below the floor", 1e-9, minPlayHexArea},
		{"exactly the floor", 0.01, 0.01},
		{"zero stays zero", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := roundRoadArea(c.in); got != c.want {
				t.Errorf("roundRoadArea(%v) = %v; want %v", c.in, got, c.want)
			}
		})
	}
	// The premise: plain round2 is what would have zeroed those hexes.
	if got := round2(0.00042); got != 0 {
		t.Fatalf("test premise: round2(0.00042) = %v; want 0", got)
	}
}

// TestBuildPlayHexes_TinyDecayOverrideSurvivesRounding is the trap pav7.3 exists
// to avoid: config accepts any forecast.decay_rate in [0,1], and game.New aborts
// the ENTIRE board on k <= 0, so rounding k to a fixed decimal count would flatten
// a legal 1e-7 override to 0 and take out /play for that city. Significant-figure
// rounding must keep it nonzero (and near the override).
func TestBuildPlayHexes_TinyDecayOverrideSurvivesRounding(t *testing.T) {
	ctx := context.Background()
	roads := []db.Feature{
		lineFeature("res-1", "residential", -122.420, 37.585, -122.405, 37.585),
	}
	const tiny = 1e-7 // legal per config.ForecastConfig.Validate (0 <= rate <= 1)
	entry := playHexEntryWithDecay(roads, tiny)

	_, lon0, lat0, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}

	hexes, err := BuildPlayHexes(ctx, entry, geo.NewUTMProjector(lon0, lat0))
	if err != nil {
		t.Fatalf("BuildPlayHexes: %v", err)
	}
	if len(hexes) == 0 {
		t.Fatal("BuildPlayHexes returned no hexes; expected a road-bearing hex")
	}
	for _, ph := range hexes {
		if ph.K <= 0 {
			t.Fatalf("hex %q k = %v after rounding a %v decay_rate; game.New would reject the whole board", ph.ID, ph.K, tiny)
		}
		if math.Abs(ph.K-tiny) > tiny*1e-3 {
			t.Errorf("hex %q k = %v; want ~%v (the configured override)", ph.ID, ph.K, tiny)
		}
	}

	// End to end: the board the browser would build from this payload starts.
	if _, err := game.New(gameBoardFromPlayHexes(hexes)); err != nil {
		t.Fatalf("game.New with a %v decay_rate board: %v", tiny, err)
	}
}

// TestBuildPlayHexes_SliverHexKeepsPositiveArea drives the floor through the
// PRODUCTION path instead of asserting on roundRoadArea directly: it builds a
// road whose buffered footprint pokes 3 cm past a hex's leftmost vertex, which
// is the shape the real export hits 42 times across 34 cities.
//
// The grid is flat-top, so min-X is a vertex with a 120° interior angle and the
// intruding region is an isoceles triangle of area tan(60°)·d² — 0.0016 m² at
// d = 3 cm, a third of the 0.005 m² that 2dp rounds to 0.00. The road position
// is derived from the buffer's own measured half-width, so a change to the road
// width moves the road, not the penetration depth.
func TestBuildPlayHexes_SliverHexKeepsPositiveArea(t *testing.T) {
	ctx := context.Background()
	base := playHexEntry(nil)
	_, lon0, lat0, err := base.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	proj := geo.NewUTMProjector(lon0, lat0)

	grid, err := cityHexGrid(ctx, base, proj)
	if err != nil {
		t.Fatalf("cityHexGrid: %v", err)
	}
	if len(grid) == 0 {
		t.Fatal("cityHexGrid returned no hexes")
	}
	target := grid[len(grid)/2] // interior hex, clear of the boundary clip
	mn, mx, ok := target.Geom.Envelope().MinMaxXYs()
	if !ok {
		t.Fatalf("hex %q has an empty envelope", target.ID)
	}
	midY := (mn.Y + mx.Y) / 2

	// Measure the buffered half-width of a north-south residential road by
	// running the real buffer, so no road-width constant is baked in here.
	probeLon1, probeLat1 := proj.FromProjected(mn.X, midY-50)
	probeLon2, probeLat2 := proj.FromProjected(mn.X, midY+50)
	probe := lineFeature("probe", "residential", probeLon1, probeLat1, probeLon2, probeLat2)
	buffered := (&resource.Pavement{}).BufferFeaturesPaired(ctx, []resource.Feature{{
		ID: probe.ID, Tags: probe.Tags, GeometryJSON: probe.GeometryJSON,
	}}, proj)
	if len(buffered) != 1 {
		t.Fatalf("buffering the probe road produced %d features; want 1", len(buffered))
	}
	bmn, bmx, ok := buffered[0].Geom.Envelope().MinMaxXYs()
	if !ok {
		t.Fatal("buffered probe road has an empty envelope")
	}
	halfWidth := (bmx.X - bmn.X) / 2

	const penetration = 0.03 // m past the hex vertex
	wantArea := math.Tan(math.Pi/3) * penetration * penetration
	if round2(wantArea) != 0 {
		t.Fatalf("test premise: a %v m² sliver does not round to 0.00 at 2dp", wantArea)
	}

	// Centerline placed so the buffer's east edge lands penetration metres
	// inside the hex.
	x := mn.X - halfWidth + penetration
	lonA, latA := proj.FromProjected(x, midY-50)
	lonB, latB := proj.FromProjected(x, midY+50)
	entry := playHexEntry([]db.Feature{lineFeature("sliver-1", "residential", lonA, latA, lonB, latB)})

	hexes, err := BuildPlayHexes(ctx, entry, proj)
	if err != nil {
		t.Fatalf("BuildPlayHexes: %v", err)
	}
	var got float64
	found := false
	for _, ph := range hexes {
		if ph.ID == target.ID {
			got, found = ph.RoadArea, true
		}
	}
	if !found {
		t.Fatalf("hex %q (clipped by ~%v m²) was not emitted; the clip no longer reaches it — retune penetration", target.ID, wantArea)
	}
	if got != minPlayHexArea {
		t.Errorf("sliver hex %q road_area = %v; want the %v floor (plain 2dp rounding would publish 0.00 and "+
			"disable this city's out-of-funds warning)", target.ID, got, minPlayHexArea)
	}
}

// playState is the subset of game.StateJSON's payload these tests read. The
// keys must match stateOut's json tags exactly — a typo unmarshals to the zero
// value and quietly turns the assertion off.
type playState struct {
	NetworkPCI float64 `json:"network_pci"`
	Status     string  `json:"status"`
	OutOfFunds bool    `json:"out_of_funds"`
}

func playStateOf(t *testing.T, g *game.Game) playState {
	t.Helper()
	raw, err := g.StateJSON()
	if err != nil {
		t.Fatalf("StateJSON: %v", err)
	}
	var st playState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	return st
}

// TestGameBoard_ZeroAreaHexKillsTheOutOfFundsSignal is the justification for
// roundRoadArea's floor: it asserts what a road_area of 0.00 actually does to a
// board, so the floor is not defended by a vague "it might be bad".
//
// Two concrete consequences, both asserted here:
//   - StateJSON takes minOpenArea over OPEN hexes, so a single zero-area hex
//     drives the out-of-funds bound to `treasury < cost x 0`. The treasury is
//     clamped at >= 0, so the warning can never fire again for that city — even
//     with an empty treasury and a real hex begging for treatment.
//   - A board of nothing but zero-area hexes reports networkPCI 0 (its
//     area-weighted mean has a zero denominator) and is therefore LOST on the
//     first tick, below LosePCIFloor.
//
// Neither is reachable from BuildPlayHexes now — TestRoundRoadArea and
// TestBuildPlayHexes_RoundsEmittedMagnitudes hold the floor — but both were one
// unfloored round2 away.
func TestGameBoard_ZeroAreaHexKillsTheOutOfFundsSignal(t *testing.T) {
	// Broke, with a real hex on the board: the warning must fire.
	broke := gameBoardFromPlayHexes([]PlayHex{{ID: "hex:1:0", RoadArea: 1000, K: 0.03}})
	broke.StartingBudget = 0
	if st := playStateOf(t, mustGame(t, broke)); !st.OutOfFunds {
		t.Fatalf("test premise: a 0-treasury board with a 1000 m² hex reports out_of_funds=false")
	}

	// Same board plus one hex whose footprint rounded to zero: signal gone.
	withSliver := gameBoardFromPlayHexes([]PlayHex{
		{ID: "hex:0:0", RoadArea: 0, K: 0.04}, // sliver clip, rounded away
		{ID: "hex:1:0", RoadArea: 1000, K: 0.03},
	})
	withSliver.StartingBudget = 0
	if st := playStateOf(t, mustGame(t, withSliver)); st.OutOfFunds {
		t.Errorf("out_of_funds still fires with a zero-area hex on the board; " +
			"the game.go minOpenArea bound must have changed — recheck whether roundRoadArea's floor is still needed")
	}

	// All-zero board: networkPCI has no denominator, so the run is lost at once.
	allZero := gameBoardFromPlayHexes([]PlayHex{{ID: "hex:0:0", RoadArea: 0, K: 0.04}})
	g := mustGame(t, allZero)
	if st := playStateOf(t, g); st.Status != "running" {
		t.Fatalf("all-zero board starts as %q; want running", st.Status)
	}
	g.Tick(1)
	st := playStateOf(t, g)
	if st.NetworkPCI != 0 {
		t.Errorf("all-zero board network_pci = %v; want 0 (zero-denominator mean)", st.NetworkPCI)
	}
	if st.Status != "lost" {
		t.Errorf("all-zero board status after one tick = %q; want lost", st.Status)
	}
}

func mustGame(t *testing.T, cfg game.Config) *game.Game {
	t.Helper()
	g, err := game.New(cfg)
	if err != nil {
		t.Fatalf("game.New: %v", err)
	}
	return g
}

// TestBuildPlayHexes_NoFeatures returns no hexes (and no error) when the city
// has no road features — a legitimate empty the server turns into [].
func TestBuildPlayHexes_NoFeatures(t *testing.T) {
	ctx := context.Background()
	entry := playHexEntry(nil)
	_, lon0, lat0, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	hexes, err := BuildPlayHexes(ctx, entry, geo.NewUTMProjector(lon0, lat0))
	if err != nil {
		t.Fatalf("BuildPlayHexes: %v", err)
	}
	if len(hexes) != 0 {
		t.Errorf("expected no hexes for a city with no roads, got %d", len(hexes))
	}
}
