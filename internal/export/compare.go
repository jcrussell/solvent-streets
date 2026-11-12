package export

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// CompareFileName is the site-level rollup a multi-city export writes beside
// cities.json for the Compare and Aggregate tabs (solvent-streets-l0qo).
// Without it those tabs fetch meta.json, scenarios.json, forecast_seed.json and
// forecast.json for every city before rendering anything: ~1,100 requests and
// ~21 MB raw for a 277-city site, whatever the tag filter. They read a small
// slice of each, so the rollup carries exactly that slice, in the same shape.
const CompareFileName = "compare.json"

// CompareFileVersion is compare.json's format version. The client falls back
// to the per-city files when the version is not one it knows, or the file is
// absent (`pvmt serve` does not serve it).
const CompareFileVersion = 1

// CompareJSON is compare.json: one entry per exported city, each holding the
// parts of its data files the Compare and Aggregate tabs read. Every field
// keeps its per-city file's name and shape, so the client consumes an entry
// exactly as it would the four files, and a value matches the file it came
// from byte for byte (both are written from the same rounded emission).
type CompareJSON struct {
	V      int           `json:"v"`
	Cities []CompareCity `json:"cities"`
}

// CompareCity is one city's slice. A part is absent when its file was not
// written (e.g. no forecast for a city that was never computed), as a 404 is
// for the per-city fetch.
type CompareCity struct {
	Slug      string                       `json:"slug"`
	Meta      *compareMeta                 `json:"meta,omitempty"`
	Seed      *compareSeed                 `json:"seed,omitempty"`
	Scenarios map[string][]compareScenario `json:"scenarios,omitempty"`
	Forecast  []compareForecast            `json:"forecast,omitempty"`
}

type compareMeta struct {
	Stats      []compareStat `json:"stats"`
	CityArea   float64       `json:"city_area,omitempty"`
	TotalPaved float64       `json:"total_paved,omitempty"`
	PavedShare float64       `json:"paved_share,omitempty"`
}

type compareStat struct {
	ResourceType    string  `json:"resource_type"`
	TotalArea       float64 `json:"total_area"`
	EstimatedSpaces int     `json:"estimated_spaces,omitempty"`
}

// compareSeed keeps the cohorts and areas the tabs read, the horizon, and the
// three settings the Aggregate divergence note compares (initial PCI, decay,
// cost tiers). cost_tiers passes through verbatim so that comparison sees
// exactly the seed's value.
type compareSeed struct {
	InitialPCI   float64         `json:"initial_pci"`
	DecayRate    float64         `json:"decay_rate"`
	HorizonYears int             `json:"horizon_years"`
	CostTiers    json.RawMessage `json:"cost_tiers,omitempty"`
	TotalArea    float64         `json:"total_area,omitempty"`
	CityPaved    float64         `json:"city_paved,omitempty"`
	Cohorts      []compareCohort `json:"cohorts,omitempty"`
}

type compareCohort struct {
	Classification string  `json:"classification"`
	Area           float64 `json:"area"`
	DecayRate      float64 `json:"decay_rate"`
}

// compareScenario is the baseline (first) scenario of a scope, with only the
// per-year values the tabs plot.
type compareScenario struct {
	Years []compareYear `json:"years"`
}

type compareYear struct {
	PCI             float64 `json:"pci"`
	AnnualNeed      float64 `json:"annual_need"`
	DeferredBacklog float64 `json:"deferred_backlog"`
}

// compareForecast is the roads entry of forecast.json, solvency fields only,
// plus RoadsArea: the year-1 area of the baseline those figures are measured
// on (city-maintained roads), so the Solvency card can show the network the
// hold-steady budget actually maintains rather than an all-jurisdiction,
// all-resource total (solvent-streets-3ij1).
type compareForecast struct {
	ResourceType    string   `json:"resource_type"`
	BreakEvenBudget float64  `json:"break_even_budget,omitempty"`
	CurrentBudget   float64  `json:"current_budget,omitempty"`
	InsolvencyYear  *int     `json:"insolvency_year,omitempty"`
	FundingGap      *float64 `json:"funding_gap,omitempty"`
	RoadsArea       float64  `json:"roads_area,omitempty"`
}

// forecastFileEntry is how compareCityFromDir reads one forecast.json entry:
// the solvency fields plus the baseline's year-1 area.
type forecastFileEntry struct {
	compareForecast
	Baseline struct {
		Years []struct {
			Area float64 `json:"area"`
		} `json:"years"`
	} `json:"baseline"`
}

// compareCityFromDir builds slug's entry from the data files exported into
// dataDir. A missing file leaves its part absent; a malformed one is an error,
// since the exporter just wrote it.
func compareCityFromDir(slug, dataDir string) (CompareCity, error) {
	c := CompareCity{Slug: slug}
	var meta compareMeta
	if ok, err := readDataFile(dataDir, "meta.json", &meta); err != nil {
		return c, err
	} else if ok {
		c.Meta = &meta
	}
	var seed compareSeed
	if ok, err := readDataFile(dataDir, "forecast_seed.json", &seed); err != nil {
		return c, err
	} else if ok {
		c.Seed = &seed
	}
	var scenarios map[string][]compareScenario
	if ok, err := readDataFile(dataDir, "scenarios.json", &scenarios); err != nil {
		return c, err
	} else if ok {
		c.Scenarios = map[string][]compareScenario{}
		for scope, list := range scenarios {
			if len(list) > 0 {
				c.Scenarios[scope] = list[:1]
			}
		}
	}
	roads, err := readRoadsForecast(dataDir)
	if err != nil {
		return c, err
	}
	c.Forecast = roads
	return c, nil
}

// readRoadsForecast returns forecast.json's roads entry as a one-element
// slice, or nil when the file or the entry is absent.
func readRoadsForecast(dataDir string) ([]compareForecast, error) {
	var forecasts []forecastFileEntry
	ok, err := readDataFile(dataDir, "forecast.json", &forecasts)
	if err != nil || !ok {
		return nil, err
	}
	var out []compareForecast
	for _, f := range forecasts {
		if f.ResourceType != "roads" {
			continue
		}
		cf := f.compareForecast
		if len(f.Baseline.Years) > 0 {
			cf.RoadsArea = f.Baseline.Years[0].Area
		}
		out = []compareForecast{cf}
	}
	return out, nil
}

// readDataFile decodes dataDir/name into v. It reports false, with no error,
// when the file does not exist.
func readDataFile(dataDir, name string, v any) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", name, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("decode %s: %w", name, err)
	}
	return true, nil
}
