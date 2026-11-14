package coherence

import (
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
)

var ingested = time.Date(2026, 11, 14, 10, 2, 0, 0, time.UTC)

// states builds the city's current inputs from input -> digest.
func states(digests map[string]string) []db.InputState {
	var out []db.InputState
	for in, d := range digests {
		out = append(out, db.InputState{Input: in, Digest: d, IngestedAt: ingested})
	}
	return out
}

// part is a latest-compute part that read exactly digests.
func part(name string, digests map[string]string) db.LatestPart {
	in := map[string]string{}
	for _, k := range db.ProducerInputs(name) {
		in[k] = digests[k]
	}
	return db.LatestPart{Part: name, RunID: 1, Inputs: in}
}

// full is a coherent city: every input present and every part computed from
// it.
var full = map[string]string{
	"boundary":            "b1",
	"roads":               "r1",
	"parking":             "p1",
	"sidewalks":           "s1",
	"facility-fire":       "f1",
	"layer-census-blocks": "c1",
}

func with(base map[string]string, kv ...string) map[string]string {
	out := maps.Clone(base)
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "-" {
			delete(out, kv[i])
			continue
		}
		out[kv[i]] = kv[i+1]
	}
	return out
}

func allParts(digests map[string]string) []db.LatestPart {
	var out []db.LatestPart
	for _, p := range []string{"census", "combined", "parking", "roads", "service:fire", "sidewalks"} {
		out = append(out, part(p, digests))
	}
	return out
}

func without(parts []db.LatestPart, names ...string) []db.LatestPart {
	return slices.DeleteFunc(slices.Clone(parts), func(p db.LatestPart) bool { return slices.Contains(names, p.Part) })
}

func TestCheck(t *testing.T) {
	city := &config.CityConfig{Name: "Oakland CA"}
	noCensus := false
	cases := []struct {
		name     string
		city     *config.CityConfig
		parts    []db.LatestPart
		inputs   map[string]string
		refusals []string
		warnings []string
		hint     string
	}{
		{
			name:   "coherent",
			parts:  allParts(full),
			inputs: full,
		},
		{
			name:     "one part stale names the input and its own command",
			parts:    append(without(allParts(full), "service:fire"), part("service:fire", with(full, "facility-fire", "f0"))),
			inputs:   full,
			refusals: []string{"fire facilities re-ingested 2026-11-14 10:02 UTC after the fire coverage compute"},
			hint:     "pvmt --city oakland-ca services compute",
		},
		{
			name:     "census stale",
			parts:    append(without(allParts(full), "census"), part("census", with(full, "layer-census-blocks", "c0"))),
			inputs:   full,
			refusals: []string{"census blocks re-ingested 2026-11-14 10:02 UTC after the census compute"},
			hint:     "pvmt --city oakland-ca census compute",
		},
		{
			name:     "roads re-ingested stales roads, combined and coverage",
			parts:    allParts(with(full, "roads", "r0")),
			inputs:   full,
			refusals: []string{"roads re-ingested 2026-11-14 10:02 UTC after the combined, roads and fire coverage computes"},
			hint:     "pvmt --city oakland-ca all compute",
		},
		{
			name:  "boundary stale collapses to all compute",
			parts: append(without(allParts(full), "census"), part("census", with(full, "boundary", "b0"))),
			// Only census read the old boundary, but the boundary is shared.
			inputs:   full,
			refusals: []string{"boundary re-ingested 2026-11-14 10:02 UTC after the census compute"},
			hint:     "pvmt --city oakland-ca all compute",
		},
		{
			name:     "an input absent at compute and present now",
			parts:    allParts(with(full, "parking", "")),
			inputs:   full,
			refusals: []string{"parking ingested 2026-11-14 10:02 UTC after the combined and parking computes"},
			hint:     "pvmt --city oakland-ca all compute",
		},
		{
			name:     "an input removed since",
			parts:    allParts(full),
			inputs:   with(full, "sidewalks", ""),
			refusals: []string{"sidewalks removed 2026-11-14 10:02 UTC after the combined and sidewalks computes"},
			hint:     "pvmt --city oakland-ca all compute",
		},
		{
			name:   "parking ingested but never computed",
			parts:  without(allParts(with(full, "parking", "-")), "parking"),
			inputs: full,
			refusals: []string{
				"parking ingested 2026-11-14 10:02 UTC after the combined compute",
				"parking ingested 2026-11-14 10:02 UTC but never computed",
			},
			hint: "pvmt --city oakland-ca all compute",
		},
		{
			name:     "nothing computed",
			inputs:   full,
			refusals: []string{"roads ingested 2026-11-14 10:02 UTC but never computed", "parking ingested 2026-11-14 10:02 UTC but never computed", "sidewalks ingested 2026-11-14 10:02 UTC but never computed", "no pavement compute (roads, parking or sidewalks)"},
			warnings: []string{
				"no fire coverage: fire facilities are ingested but never computed (pvmt --city oakland-ca services compute)",
				"no census population: census blocks are ingested but never computed (pvmt --city oakland-ca census compute)",
			},
			hint: "pvmt --city oakland-ca all compute",
		},
		{
			name:     "no pavement, nothing ingested",
			parts:    []db.LatestPart{part("census", full)},
			inputs:   with(full, "roads", "-", "parking", "-", "sidewalks", "-"),
			refusals: []string{"no pavement compute (roads, parking or sidewalks)"},
			hint:     "pvmt --city oakland-ca all compute",
		},
		{
			name:     "pavement without combined",
			parts:    without(allParts(full), "combined"),
			inputs:   full,
			refusals: []string{"no combined compute (the cross-resource total)"},
			hint:     "pvmt --city oakland-ca all compute",
		},
		{
			name:     "combined stale alone still collapses",
			parts:    append(without(allParts(full), "combined"), part("combined", with(full, "sidewalks", "s0"))),
			inputs:   full,
			refusals: []string{"sidewalks re-ingested 2026-11-14 10:02 UTC after the combined compute"},
			hint:     "pvmt --city oakland-ca all compute",
		},
		{
			name:     "optional parts missing warn, not refuse",
			parts:    without(allParts(full), "census", "service:fire"),
			inputs:   full,
			warnings: []string{"no fire coverage: fire facilities are ingested but never computed (pvmt --city oakland-ca services compute)", "no census population: census blocks are ingested but never computed (pvmt --city oakland-ca census compute)"},
		},
		{
			name:     "hand-placed facilities warn too",
			city:     &config.CityConfig{Name: "Oakland CA", Facilities: []config.FacilityConfig{{Service: "fire", Lat: 1, Lon: 2}}},
			parts:    without(allParts(with(full, "facility-fire", "-")), "service:fire"),
			inputs:   with(full, "facility-fire", "-"),
			warnings: []string{"no fire coverage: fire facilities are configured but never computed (pvmt --city oakland-ca services compute)"},
		},
		{
			name:   "no warning without roads or with census off",
			city:   &config.CityConfig{Name: "Oakland CA", Census: &noCensus},
			parts:  []db.LatestPart{part("parking", full), part("combined", with(full, "roads", "-", "sidewalks", "-"))},
			inputs: with(full, "roads", "-", "sidewalks", "-"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ct := c.city
			if ct == nil {
				ct = city
			}
			r := Check(ct, c.parts, states(c.inputs))
			if !slices.Equal(r.Refusals, c.refusals) {
				t.Errorf("refusals:\n got %q\nwant %q", r.Refusals, c.refusals)
			}
			if !slices.Equal(r.Warnings, c.warnings) {
				t.Errorf("warnings:\n got %q\nwant %q", r.Warnings, c.warnings)
			}
			if r.Hint != c.hint {
				t.Errorf("hint = %q, want %q", r.Hint, c.hint)
			}
			var refused *RefusedError
			if got := errors.As(r.Err(), &refused); got != r.Refused() {
				t.Errorf("Err() refused = %v, Refused() = %v", got, r.Refused())
			}
		})
	}
}

// TestPartStates: each part is current, stale (naming the input that moved
// on and when), or not computed, by the same digests Check compares.
func TestPartStates(t *testing.T) {
	at := time.Date(2026, 11, 14, 10, 2, 0, 0, time.UTC)
	inputs := []db.InputState{
		{Input: db.InputBoundary, Digest: "b1"},
		{Input: "roads", Digest: "r2", IngestedAt: at},
		{Input: "parking", Digest: "p1"},
	}
	parts := []db.LatestPart{
		{Part: "roads", Inputs: map[string]string{db.InputBoundary: "b1", "roads": "r1"}},
		{Part: "parking", Inputs: map[string]string{db.InputBoundary: "b1", "parking": "p1"}},
	}
	got := PartStates([]string{"roads", "parking", "sidewalks"}, parts, inputs)
	want := []string{"stale: roads re-ingested 2026-11-14 10:02 UTC", "current", "not computed"}
	for i, st := range got {
		if st.String() != want[i] {
			t.Errorf("%s: %q, want %q", st.Part, st.String(), want[i])
		}
	}
	if got[0].State != StateStale || len(got[0].StaleInputs) != 1 || got[0].StaleInputs[0] != "roads" {
		t.Errorf("roads = %+v, want stale on the roads input", got[0])
	}
}
