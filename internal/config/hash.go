package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The snapshot key: a per-city SEMANTIC hash of the settings that change a
// stored result.
//
// Every producer commits its runs under CityHash(city) (via db.Draft) and every
// reader resolves the newest snapshot for the same value (db.Store.
// WithConfigHash), so the hash decides one thing: whether results already in the
// database were computed from the settings now in force. It answers that by
// hashing a dedicated value, computeInputs, which holds only the RESOLVED
// settings (defaults applied, [[include]]s flattened, PVMT_* env overrides
// applied) that change any stored result computed from the same stored features
// — and nothing else. Consequences, each pinned by hash_test.go:
//
//   - a comment, whitespace or key-order edit changes nothing;
//   - an edit to one city's settings changes only that city's hash;
//   - an explicit default hashes like an omitted one (for every default config
//     itself owns — see "Stated as written" below);
//   - an env override that changes stored results (PVMT_HEX_EDGE_M) changes
//     the hash, so its results never pass for the file's;
//   - a presentation-, forecast- or ingest-only edit (units, [export], tags,
//     names, display.min_hex_area, any [forecast] setting or PVMT_FORECAST_*
//     override, sources) changes nothing;
//   - bumping ComputeEpoch changes every hash.
//
// The field set was taken from what the producers actually read, not from the
// shape of Config: the roads/parking/sidewalks compute, the combined pass, the
// census apportionment and service routing all build their grid from the
// resolved hex edge (geo.HexGrid over the STORED boundary's bbox); census reads
// only its on/off switch; service routing bakes [[services]].turnout_s into the
// stored seconds and routes from [[cities.facilities]] (service, lat, lon).
// TestComputeInputs_EveryFieldClassified (hash_test.go) holds a classification
// of every config field and fails when one is added without deciding which side
// it is on; CONTRIBUTING.md says how to decide.
//
// Deliberately not hashed:
//
//   - Presentation / export-time settings: display.units, display.min_hex_area
//     and the per-city min_hex_area (the sliver filter runs at export, over
//     stored hex stats), every [export] field, tags, the city name (also an
//     ingest input, below), facility names and [[services]].travel_threshold_s
//     (legend bands and within_threshold_share are derived at export from the
//     stored seconds).
//   - The [forecast] block, every field of it, at any layer and through
//     PVMT_FORECAST_*. No forecast is stored: export, serve, `pvmt forecast`
//     and the site's WASM simulate it on demand from the stored cohort stats
//     and the settings in force, so a forecast edit takes effect on the next
//     read without invalidating anything.
//   - Ingest-only settings: overpass, arcgis_url, allow_private_arcgis,
//     boundary_relation_id and the name used for the Nominatim lookup. They
//     decide which features and boundary get stored; compute reads the stored
//     copies, so changing a source does not by itself invalidate results
//     computed from what is stored — a re-ingest does, and is followed by a
//     recompute as usual.
//   - Identity and plumbing: config_id (keys the cities row instead),
//     [[include]] paths and tags (the merged cities are hashed, not the file
//     layout), SourcePath, and the stamps the merge leaves.
//
// Stated as written: [[services]] defaults live in the internal/services
// registry, which internal/config deliberately does not import, so turnout_s is
// hashed as stated: an explicit value equal to the registry default hashes
// differently from an omitted one. That errs toward a spurious recompute, never
// toward a stale read. Order is kept where the consumer might depend on it
// (facilities); [[services]], whose lookup is by name, is sorted.
//
// An [[include]]d city needs no special case: its hash is a function of the
// inputs it resolves to, so it equals its source config's hash exactly when the
// two resolve the same inputs (see SourceDivergence for when they do not).
//
// Scheme: the hashed bytes are computeInputsScheme, a newline, then the JSON
// encoding of hashEnvelope (ComputeEpoch plus computeInputs). encoding/json
// writes struct fields in declaration
// order and floats in their shortest round-trip form, so the encoding is
// canonical; -0 is folded to 0 first. Bump the scheme when the encoding itself
// changes. Adding a field to computeInputs changes every hash unless the field
// is omitempty and its zero value is today's behaviour — prefer that, so a new
// setting does not force a recompute on configs that don't use it.
//
// The 16-character truncation matches snapshots.config_hash, which the schema
// stores as TEXT; widening it desynchronizes write vs read for existing rows.

// ComputeEpoch identifies the compute logic behind a stored result, the half
// of "what produced this" that no setting can see (solvent-streets-mfjb). It
// is part of every CityHash, so bumping it makes every stored snapshot stop
// matching: export then refuses each city with the "settings or pvmt changed"
// hint until it is recomputed.
//
// Bump it by one, in the same commit, whenever a code change alters what a
// producer stores for the same stored features and the same settings — for
// example jurisdiction classification (internal/filter, which decides what is
// billed to the city), buffering and width rules (internal/geo,
// internal/resource, ParkingSpaceAreaM2), hex tiling, clipping or hex stats,
// cohort classification (forecast.NormalizeClass, which keys cohort_stats),
// routing, speeds or registry defaults (internal/services), census
// apportionment (internal/population), or what a producer writes or drops. Do
// not bump it for changes that only affect ingest, the forecast model or its
// defaults (simulated on demand, never stored), export or serve rendering, CLI
// output, docs or tests: stored results would not change, and a needless bump
// costs every user a full recompute. When unsure, compare a city's stored
// results before and after the change. See CONTRIBUTING.md.
//
// History: 1 — v0.5.0, the first semantic hash.
const ComputeEpoch = 1

// computeInputsScheme names the encoding of hashEnvelope; part of the hashed
// bytes so a future encoding can never collide with this one.
const computeInputsScheme = "pvmt-compute-inputs/v2"

// hashEnvelope is the value actually encoded: the code epoch, then the
// resolved settings. computeInputs is embedded, so encoding/json promotes its
// fields into the envelope and a field added there is hashed without being
// repeated here.
type hashEnvelope struct {
	Epoch int `json:"epoch"`
	computeInputs
}

// computeInputs is everything, resolved, that a stored result for one city
// depends on besides the stored features themselves: the inputs of coverage
// (hex_stats, cohort_stats, compute_results), the combined pass, service
// coverage and census population.
type computeInputs struct {
	// HexEdgeM is the resolved edge (env > city > file > default). Hex ids are
	// derived from the grid and stored rows are joined to a rebuilt grid by id.
	HexEdgeM float64 `json:"hex_edge_m"`
	// Census is CityConfig.CensusEnabled: off drops the census part.
	Census bool `json:"census"`
	// Services holds the [[services]] overrides that reach stored rows, sorted
	// by name. An entry that leaves turnout_s unset (nil) is left out; an
	// explicit 0 is kept, since services.Resolve applies it.
	Services []serviceInputs `json:"services,omitempty"`
	// Facilities are the city's hand-placed [[cities.facilities]], in order.
	Facilities []facilityInputs `json:"facilities,omitempty"`
}

type serviceInputs struct {
	Name     string  `json:"name"`
	TurnoutS float64 `json:"turnout_s"`
}

type facilityInputs struct {
	Service string  `json:"service"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

// canon folds -0 to 0 so a TOML `-0.0` hashes like `0.0`.
func canon(f float64) float64 {
	if f == 0 {
		return 0
	}
	return f
}

// cityComputeInputs resolves city's compute inputs under c. A nil city means
// "no per-city overrides".
func (c *Config) cityComputeInputs(city *CityConfig) computeInputs {
	in := computeInputs{
		HexEdgeM: canon(c.ResolvedHexEdge(city)),
		Census:   city == nil || city.CensusEnabled(),
	}
	for _, s := range c.Services {
		if s.TurnoutS != nil {
			in.Services = append(in.Services, serviceInputs{Name: s.Name, TurnoutS: canon(*s.TurnoutS)})
		}
	}
	slices.SortFunc(in.Services, func(a, b serviceInputs) int { return strings.Compare(a.Name, b.Name) })
	if city != nil {
		for _, f := range city.Facilities {
			in.Facilities = append(in.Facilities, facilityInputs{Service: f.Service, Lat: canon(f.Lat), Lon: canon(f.Lon)})
		}
	}
	return in
}

// hashComputeInputs encodes in, under compute epoch epoch, canonically and
// returns the 16-hex-char prefix of its sha256. CityHash passes ComputeEpoch;
// the parameter exists so a test can show a bump moves the hash.
func hashComputeInputs(epoch int, in computeInputs) string {
	b, err := json.Marshal(hashEnvelope{Epoch: epoch, computeInputs: in})
	if err != nil {
		// Unreachable: the envelope holds only strings, ints, finite floats
		// (Validate rejects NaN/Inf) and slices of them.
		panic(fmt.Sprintf("config: encode compute inputs: %v", err))
	}
	h := sha256.New()
	h.Write([]byte(computeInputsScheme + "\n"))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// CityHash returns the snapshot key for city: the hash every producer commits
// its runs under and every reader resolves snapshots by. See the top of this
// file for what goes into it.
//
// It depends on nothing but the inputs city resolves to under c (and
// ComputeEpoch). So a city pulled in through [[include]] hashes exactly as its
// source config hashes it whenever the two resolve the same inputs — which is
// how a union config like examples/all reads the snapshots each example
// computed — and gets a different hash, reading nothing until it is computed
// under this config, when they do not (SourceDivergence names the setting).
//
// Env overrides are read when this is called; a process sees one environment.
func (c *Config) CityHash(city *CityConfig) string {
	return hashComputeInputs(ComputeEpoch, c.cityComputeInputs(city))
}

// SourceDivergence describes, field by field, how this config resolves an
// [[include]]d city's compute inputs differently from the config that declared
// it — the reason the city's hash differs from its source's, so the snapshots
// that config computed do not apply here. Empty when the city is not included,
// when the two agree, or when the city carries no stamped source inputs (a
// CityConfig built by hand).
func (c *Config) SourceDivergence(city *CityConfig) []string {
	if city == nil || city.sourceInputs == nil {
		return nil
	}
	here, src := c.cityComputeInputs(city), *city.sourceInputs
	var out []string
	if here.HexEdgeM != src.HexEdgeM {
		out = append(out, fmt.Sprintf("hex_edge_m (%s here, %s in its own config)",
			plainFloat(here.HexEdgeM), plainFloat(src.HexEdgeM)))
	}
	if here.Census != src.Census {
		out = append(out, fmt.Sprintf("census (%t here, %t in its own config)", here.Census, src.Census))
	}
	if !slices.Equal(here.Services, src.Services) {
		// The likely cause is solvent-streets-qtue: the [[include]] merge does
		// not carry an included file's [[services]] into the including config.
		out = append(out, "[[services]] turnout_s (an included file's [[services]] are not "+
			"applied by the including config; only its own top-level [[services]] are)")
	}
	if !slices.Equal(here.Facilities, src.Facilities) {
		out = append(out, fmt.Sprintf("[[cities.facilities]] (%d here, %d in its own config)",
			len(here.Facilities), len(src.Facilities)))
	}
	return out
}

// StaleHint is the one explanation export, `pvmt forecast` and `pvmt status`
// give for a city with no snapshot under its current CityHash although it has
// snapshots (db.StaleFor). It lives here because what invalidates a snapshot
// is this file's business: a result-affecting setting, an env override, a
// ComputeEpoch bump — or, for an [[include]]d city, this config resolving the
// city's inputs differently from the config that declared it, which StaleHint
// names when it is the cause (SourceDivergence).
func (c *Config) StaleHint(city *CityConfig) string {
	slug, hash := city.Slug(), c.CityHash(city)
	if d := c.SourceDivergence(city); len(d) > 0 {
		return fmt.Sprintf("%s is pulled in via [[include]], so it reads the snapshots its own config "+
			"computed — but this config resolves %s, which changes the stored results, so those "+
			"snapshots do not apply here. Make the including config agree with the city's own, or run "+
			"`pvmt --city %s all compute` under this config.", city.Name, strings.Join(d, "; "), slug)
	}
	return fmt.Sprintf("%s was computed, but not under the current settings (hash %s): a setting that "+
		"affects results changed since its last compute (hex_edge_m, [[services]] turnout_s, "+
		"facilities, census, or a PVMT_HEX_EDGE_M override), or this pvmt computes "+
		"differently from the one that did. Run `pvmt --city %s all compute`; "+
		"`pvmt snapshots ls --city %s` lists the older snapshots.", slug, hash, slug, slug)
}

// hashBytes returns the 16-hex-char sha256 prefix of b. Used for the
// path-derived ConfigID default, which shares snapshots.config_hash's format.
func hashBytes(b []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(b))[:16]
}
