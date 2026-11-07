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
//   - an env override that changes results (PVMT_HEX_EDGE_M, PVMT_FORECAST_*)
//     changes the hash, so its results never pass for the file's;
//   - a presentation- or ingest-only edit (units, [export], tags, names,
//     sources, display.min_hex_area) changes nothing;
//   - bumping ComputeEpoch changes every hash.
//
// The field set was taken from what the producers actually read, not from the
// shape of Config: the roads/parking/sidewalks compute, the combined pass, the
// census apportionment and service routing all build their grid from the
// resolved hex edge (geo.HexGrid over the STORED boundary's bbox); census reads
// only its on/off switch; service routing bakes [[services]].turnout_s into the
// stored seconds and routes from [[cities.facilities]] (service, lat, lon); the
// forecast part stores rows computed from the resolved [forecast] block, less
// current_budget. TestComputeInputs_EveryFieldClassified (hash_test.go) holds a
// classification of every config field and fails when one is added without
// deciding which side it is on; CONTRIBUTING.md says how to decide.
//
// Deliberately not hashed:
//
//   - Presentation / export-time settings: display.units, display.min_hex_area
//     and the per-city min_hex_area (the sliver filter runs at export, over
//     stored hex stats), every [export] field, tags, the city name (also an
//     ingest input, below), facility names, [[services]].travel_threshold_s
//     (legend bands and within_threshold_share are derived at export from the
//     stored seconds) and forecast.current_budget (only the export's solvency
//     metrics read it; no stored row depends on it).
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
// Stated as written: decay_rate, cost_tiers and treatment_cycle_years default
// downstream in internal/forecast, and [[services]] defaults in the
// internal/services registry. internal/config deliberately imports neither, so
// those fields are hashed as stated: an explicit value equal to the downstream
// default hashes differently from an omitted one. That errs toward a spurious
// recompute, never toward a stale read. Order is likewise kept where the
// consumer might depend on it (cost_tiers, facilities); [[services]], whose
// lookup is by name, is sorted.
//
// An [[include]]d city: see CityHash.
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
// the forecast model or its built-in defaults (internal/forecast), routing,
// speeds or registry defaults (internal/services), census apportionment
// (internal/population), or what a producer writes or drops. Do not bump it
// for changes that only affect ingest, export or serve rendering, CLI output,
// docs or tests: stored results would not change, and a needless bump costs
// every user a full recompute. When unsure, compare a city's stored results
// before and after the change. See CONTRIBUTING.md.
//
// History: 1 — v0.5.0, the first semantic hash.
const ComputeEpoch = 1

// computeInputsScheme names the encoding of hashEnvelope; part of the hashed
// bytes so a future encoding can never collide with this one.
const computeInputsScheme = "pvmt-compute-inputs/v2"

// hashEnvelope is the value actually encoded: the code epoch, then the
// resolved settings.
type hashEnvelope struct {
	Epoch    int            `json:"epoch"`
	Data     dataInputs     `json:"data"`
	Forecast forecastInputs `json:"forecast"`
}

// computeInputs is everything, resolved, that a stored result for one city
// depends on besides the stored features themselves. Split in two because the
// halves behave differently for an [[include]]d city (see CityHash).
type computeInputs struct {
	Data     dataInputs     `json:"data"`
	Forecast forecastInputs `json:"forecast"`
}

// dataInputs are the inputs of the parts every reader consumes as stored:
// coverage (hex_stats, cohort_stats, compute_results), the combined pass,
// service coverage and census population.
type dataInputs struct {
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

// forecastInputs is the resolved [forecast] block the forecast:<resource>
// parts store rows from (ResolvedForecast, so env overrides and config-owned
// defaults are applied). current_budget is absent: no stored row reads it.
type forecastInputs struct {
	InitialPCI          float64          `json:"initial_pci"`
	DecayRate           float64          `json:"decay_rate"`
	GrowthRate          float64          `json:"growth_rate"`
	Years               int              `json:"years"`
	CostTiers           []costTierInputs `json:"cost_tiers,omitempty"`
	TreatmentCycleYears float64          `json:"treatment_cycle_years"`
	CostOverhead        float64          `json:"cost_overhead"`
}

type costTierInputs struct {
	MinPCI     float64 `json:"min_pci"`
	MaxPCI     float64 `json:"max_pci"`
	CostPerSqM float64 `json:"cost_per_sqm"`
	Label      string  `json:"label"`
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
	return computeInputs{Data: c.cityDataInputs(city), Forecast: c.cityForecastInputs(city)}
}

func (c *Config) cityDataInputs(city *CityConfig) dataInputs {
	d := dataInputs{
		HexEdgeM: canon(c.ResolvedHexEdge(city)),
		Census:   city == nil || city.CensusEnabled(),
	}
	for _, s := range c.Services {
		if s.TurnoutS != nil {
			d.Services = append(d.Services, serviceInputs{Name: s.Name, TurnoutS: canon(*s.TurnoutS)})
		}
	}
	slices.SortFunc(d.Services, func(a, b serviceInputs) int { return strings.Compare(a.Name, b.Name) })
	if city != nil {
		for _, f := range city.Facilities {
			d.Facilities = append(d.Facilities, facilityInputs{Service: f.Service, Lat: canon(f.Lat), Lon: canon(f.Lon)})
		}
	}
	return d
}

func (c *Config) cityForecastInputs(city *CityConfig) forecastInputs {
	fc := c.ResolvedForecast(city)
	f := forecastInputs{
		InitialPCI:          canon(fc.InitialPCI),
		DecayRate:           canon(fc.DecayRate),
		GrowthRate:          canon(fc.GrowthRate),
		Years:               fc.Years,
		TreatmentCycleYears: canon(fc.TreatmentCycleYears),
		CostOverhead:        canon(fc.CostOverhead),
	}
	for _, t := range fc.CostTiers {
		f.CostTiers = append(f.CostTiers, costTierInputs{
			MinPCI: canon(t.MinPCI), MaxPCI: canon(t.MaxPCI), CostPerSqM: canon(t.CostPerSqM), Label: t.Label,
		})
	}
	return f
}

// hashComputeInputs encodes in, under compute epoch epoch, canonically and
// returns the 16-hex-char prefix of its sha256. CityHash passes ComputeEpoch;
// the parameter exists so a test can show a bump moves the hash.
func hashComputeInputs(epoch int, in computeInputs) string {
	b, err := json.Marshal(hashEnvelope{Epoch: epoch, Data: in.Data, Forecast: in.Forecast})
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

// key is a comparable rendering of d, for telling whether two resolutions of
// one city agree.
func (d dataInputs) key() string {
	b, err := json.Marshal(d)
	if err != nil {
		panic(fmt.Sprintf("config: encode data inputs: %v", err))
	}
	return string(b)
}

// CityHash returns the snapshot key for city: the hash every producer commits
// its runs under and every reader resolves snapshots by. See the top of this
// file for what goes into it.
//
// A city pulled in through [[include]] carries its source config's hash
// (SourceConfigHash, stamped by the merge as the source's own CityHash), so a
// union config like examples/all reads the snapshots the example computed. The
// stamp is used only while it is still the truth about the stored data: if
// this config resolves the city's dataInputs differently from the source — a
// different hex edge, [[services]], facilities or census switch — the stored
// coverage, metrics and grid were computed under settings this config does
// not have, so the city gets its own hash instead and reads nothing until it
// is computed under this config (export explains why; see SourceDivergence).
//
// The forecast half is allowed to differ. The [[include]] merge deliberately
// gives some cities a calibration their source example lacks (examples/all
// backfills cited initial_pci/budget figures from top-50-cities onto metro
// cities), and every reader — export, serve, `pvmt forecast` — recomputes
// forecasts from its own config rather than reading the stored forecast rows.
// Keying those cities on their union-resolved forecast would make the union
// unable to read any of their data. A forecast run from the union does store
// its rows under the source's hash; nothing reads them back, and the source's
// next compute or `pvmt forecast` replaces them.
//
// A hand-built CityConfig carrying a SourceConfigHash but no stamped data
// inputs (tests, mostly) has nothing to compare against; its stamp is trusted.
//
// Env overrides are read when this is called, and for an included city's stamp
// when the config was loaded; a process sees one environment, so both agree.
func (c *Config) CityHash(city *CityConfig) string {
	in := c.cityComputeInputs(city)
	if city != nil && city.SourceConfigHash != "" {
		if city.sourceData == nil || city.sourceData.key() == in.Data.key() {
			return city.SourceConfigHash
		}
	}
	return hashComputeInputs(ComputeEpoch, in)
}

// SourceDivergence describes, field by field, how this config resolves an
// [[include]]d city's data inputs differently from the config that declared
// it — the reason CityHash stops using the source's snapshots. Empty when the
// city is not included, when the two agree, or when the stamp carries no
// inputs to compare (a CityConfig built by hand).
func (c *Config) SourceDivergence(city *CityConfig) []string {
	if city == nil || city.SourceConfigHash == "" || city.sourceData == nil {
		return nil
	}
	here, src := c.cityDataInputs(city), *city.sourceData
	if here.key() == src.key() {
		return nil
	}
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

// hashBytes returns the 16-hex-char sha256 prefix of b. Used for the
// path-derived ConfigID default, which shares snapshots.config_hash's format.
func hashBytes(b []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(b))[:16]
}
