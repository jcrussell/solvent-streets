package checksite

import (
	"math"
	"path/filepath"

	"github.com/jcrussell/solvent-streets/internal/export"
)

// checkDecays runs checkDecay over every city in the site.
func (r *runner) checkDecays(s *site) {
	for _, d := range s.dataDirsWithLabel() {
		r.checkDecay(d.label, d.dir)
	}
}

// maxDecayDrift is the largest relative gap checkDecay accepts between the
// map's road decay rate and the forecast's. The two agree to about 1.5% at
// worst on the release site (hex areas are clipped and k is rounded to four
// figures); a compute left over from a change to the class decay rates, such
// as a 0.7x recalibration, is off by tens
// of percent.
const maxDecayDrift = 0.05

// decayHexGrid is the part of hexgrid.geojson checkDecay reads: each hex's
// road decay rate "k" and its city-scope road area.
type decayHexGrid struct {
	Features []struct {
		Properties struct {
			K        float64                       `json:"k"`
			All      map[string]map[string]float64 `json:"all"`
			City     map[string]map[string]float64 `json:"city"`
			CitySame int                           `json:"city_same"`
		} `json:"properties"`
	} `json:"features"`
}

// decaySeed is the part of forecast_seed.json checkDecay reads.
type decaySeed struct {
	CityCohorts []struct {
		ResourceType string  `json:"resource_type"`
		Area         float64 `json:"area"`
		DecayRate    float64 `json:"decay_rate"`
	} `json:"city_cohorts"`
}

// checkDecay FAILs a city whose map and forecast disagree on how fast its
// roads decay. Each hex's "k" comes from the roads compute, which stores the
// blend of its classes' decay rates; the seed's city cohorts carry the rates
// the exporter resolves now. When the class rates change and a city is not
// recomputed, the map (and /play, which reads k) runs on the old rates while
// the forecast runs on the new ones. The check compares the road-area-weighted
// mean of k with the seed's area-weighted cohort rate.
func (r *runner) checkDecay(label, dir string) {
	var seed decaySeed
	if !readJSON(r, chkDecay, label, export.ForecastSeedFileName, filepath.Join(dir, export.ForecastSeedFileName), &seed) {
		return
	}
	var seedArea, seedRate float64
	for _, c := range seed.CityCohorts {
		if c.ResourceType == "roads" {
			seedArea += c.Area
			seedRate += c.Area * c.DecayRate
		}
	}
	if seedArea <= 0 {
		return
	}
	seedRate /= seedArea

	var grid decayHexGrid
	if !readJSON(r, chkDecay, label, export.HexGridFileName, filepath.Join(dir, export.HexGridFileName), &grid) {
		return
	}
	var num, den float64
	for _, f := range grid.Features {
		p := f.Properties
		if p.K <= 0 {
			continue
		}
		city := p.City
		if city == nil && p.CitySame == 1 {
			city = p.All
		}
		a := city["roads"]["area"]
		num += p.K * a
		den += a
	}
	if den <= 0 {
		return
	}
	mapRate := num / den
	if drift := math.Abs(mapRate/seedRate - 1); drift > maxDecayDrift {
		r.failf(chkDecay, "%s: the map's road decay rate (%.4f) is %.0f%% off the forecast's (%.4f) — the roads compute predates a decay-rate change; recompute this city",
			label, mapRate, drift*100, seedRate)
		return
	}
	r.passf(chkDecay, "%s: map and forecast road decay rates agree", label)
}
