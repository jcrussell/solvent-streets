package forecast

import "math"

// Condition-distribution spread (docs/validation.md §4).
//
// The model assigns one network-average InitialPCI to every cohort. Because the
// cost-vs-PCI curve is non-linear (and flat-clamped at both ends — cost.go), the
// program cost evaluated at the mean PCI under-states the area-weighted cost over
// a real network's PCI *spread* (Jensen's inequality). Real networks are barbell-
// shaped — some segments excellent, some failed — and validation.md §4 measures
// the single-average assumption under-stating modeled cost by ~32–37%.
//
// We have no measured per-segment condition yet (that arrives with the Phase-2
// per-segment ingest, solvent-streets-mmvv.1). So ApplyConditionSpread models the
// distribution parametrically: a Beta on [0,100] re-centered on each cohort's
// InitialPCI, discretized into equal-probability sub-cohorts. This is a deliberate
// *conservative partial* correction — a unimodal Beta cannot reproduce a true
// bimodal barbell, and §4's figure is itself a lower bound.
//
// Beta is chosen over a (truncated) Gaussian on purpose: its variance
// m(1-m)/(ν+1) compresses automatically near the [0,1] bounds, so good-condition
// networks get a small correction and there is no truncation/ceiling-pile-up hack
// — the area-weighted mean PCI is preserved exactly.
const (
	// conditionConcentration is the Beta concentration ν = α+β. Lower ν ⇒ wider
	// spread ⇒ larger cost uplift. Calibrated in condition_test.go so the
	// Simulate year-1 / break-even uplift lands in validation.md §4's ~32–37%
	// band for a representative mid-range mean PCI.
	conditionConcentration = 4.0

	// conditionBands K is the equal-probability discretization resolution. K≈9
	// drives the within-band Jensen residual under ~1% (the band conditional-mean
	// pricing is itself nearly unbiased), so finer integration buys nothing.
	conditionBands = 9

	// conditionEps guards the degenerate Beta at the [0,1] bounds. A mean PCI of
	// 0 or 100 has no meaningful spread; such cohorts pass through unchanged.
	conditionEps = 1e-6
)

// ApplyConditionSpread replaces each cohort with K=conditionBands equal-probability
// sub-cohorts sampled from a Beta(α,β) on [0,100] with mean = InitialPCI/100 and
// fixed concentration ν=conditionConcentration (α=mν, β=(1−m)ν). Each band's PCI
// is its conditional mean and its area is the cohort's area / K, so the
// area-weighted mean PCI is preserved exactly (no truncation, no shift). The
// returned cohorts share each original's Classification and DecayRate.
//
// It is a pure function and deterministic. Apply it at the cohort-construction
// call sites (export, WASM bridge, CLI) — NOT inside Simulate or BuildCohorts,
// which several tests rely on leaving cohorts un-expanded. Pair it with
// AggregateCohortSummariesByClass to collapse the resulting per-band summary rows
// back to one row per classification for display.
func ApplyConditionSpread(cohorts []Cohort) []Cohort {
	if len(cohorts) == 0 {
		return cohorts
	}
	out := make([]Cohort, 0, len(cohorts)*conditionBands)
	for _, c := range cohorts {
		m := c.InitialPCI / 100.0
		if m <= conditionEps || m >= 1-conditionEps {
			out = append(out, c)
			continue
		}
		alpha := m * conditionConcentration
		beta := (1 - m) * conditionConcentration
		bandArea := c.Area / float64(conditionBands)
		for _, pci := range betaBandMeans(alpha, beta, conditionBands) {
			out = append(out, Cohort{
				Classification: c.Classification,
				Area:           bandArea,
				DecayRate:      c.DecayRate,
				InitialPCI:     pci * 100.0,
			})
		}
	}
	return out
}

// AggregateCohortSummariesByClass collapses per-band sub-cohort summaries back to
// one row per classification, preserving first-seen order. EndPCI is area-weighted;
// Area/TotalSpend/TotalDeficit are summed; DecayRate is taken from the first
// occurrence (sub-cohorts of one class share it). Idempotent — a slice already
// unique by classification is returned with equivalent values.
func AggregateCohortSummariesByClass(summaries []CohortSummary) []CohortSummary {
	if len(summaries) == 0 {
		return summaries
	}
	idx := make(map[string]int)
	out := make([]CohortSummary, 0, len(summaries))
	pciArea := make([]float64, 0, len(summaries)) // Σ EndPCI·Area per output row
	for _, s := range summaries {
		i, ok := idx[s.Classification]
		if !ok {
			idx[s.Classification] = len(out)
			out = append(out, CohortSummary{
				Classification: s.Classification,
				DecayRate:      s.DecayRate,
			})
			pciArea = append(pciArea, 0)
			i = len(out) - 1
		}
		out[i].Area += s.Area
		out[i].TotalSpend += s.TotalSpend
		out[i].TotalDeficit += s.TotalDeficit
		pciArea[i] += s.EndPCI * s.Area
	}
	for i := range out {
		if out[i].Area > 0 {
			out[i].EndPCI = pciArea[i] / out[i].Area
		}
	}
	return out
}

// betaBandMeans partitions [0,1] into k equal-probability intervals of Beta(a,b)
// and returns each interval's conditional mean. Using the identity
// E[X·1_{X≤x}] = m·I_x(a+1,b) (m = a/(a+b)), the conditional mean over band i is
// m·(I_{x_{i+1}}(a+1,b) − I_{x_i}(a+1,b))·k. The means therefore sum to k·(1/k·m
// weighting) = m exactly, so the discretization is mean-preserving.
func betaBandMeans(a, b float64, k int) []float64 {
	m := a / (a + b)
	bounds := make([]float64, k+1)
	bounds[0] = 0
	bounds[k] = 1
	for i := 1; i < k; i++ {
		bounds[i] = betaQuantile(float64(i)/float64(k), a, b)
	}
	means := make([]float64, k)
	kf := float64(k)
	for i := range means {
		hi := regIncBeta(bounds[i+1], a+1, b)
		lo := regIncBeta(bounds[i], a+1, b)
		means[i] = m * (hi - lo) * kf
	}
	return means
}

// betaQuantile inverts the Beta(a,b) CDF for probability p via bisection. The CDF
// (regIncBeta) is continuous and strictly increasing on (0,1), so bisection is
// robust and needs no derivative; ~60 iterations reach machine precision.
func betaQuantile(p, a, b float64) float64 {
	if p <= 0 {
		return 0
	}
	if p >= 1 {
		return 1
	}
	lo, hi := 0.0, 1.0
	for range 100 {
		mid := (lo + hi) / 2
		if regIncBeta(mid, a, b) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// regIncBeta is the regularized incomplete beta function I_x(a,b) — the Beta(a,b)
// CDF at x. Lentz continued fraction (Numerical Recipes "betai"), with the
// standard x vs (a+1)/(a+b+2) symmetry pivot for fast convergence. Pure Go;
// normalization uses math.Lgamma.
func regIncBeta(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	la, _ := math.Lgamma(a + b)
	lb, _ := math.Lgamma(a)
	lc, _ := math.Lgamma(b)
	front := math.Exp(la - lb - lc + a*math.Log(x) + b*math.Log(1-x))
	if x < (a+1)/(a+b+2) {
		return front * betacf(x, a, b) / a
	}
	return 1 - front*betacf(1-x, b, a)/b
}

// betacf evaluates the continued fraction for the incomplete beta function via
// the modified Lentz algorithm.
func betacf(x, a, b float64) float64 {
	const (
		maxIter = 300
		eps     = 1e-15
		fpMin   = 1e-300
	)
	qab := a + b
	qap := a + 1
	qam := a - 1
	c := 1.0
	d := 1 - qab*x/qap
	if math.Abs(d) < fpMin {
		d = fpMin
	}
	d = 1 / d
	h := d
	for i := 1; i <= maxIter; i++ {
		fi := float64(i)
		// even step
		aa := fi * (b - fi) * x / ((qam + 2*fi) * (a + 2*fi))
		d = 1 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1 / d
		h *= d * c
		// odd step
		aa = -(a + fi) * (qab + fi) * x / ((a + 2*fi) * (qap + 2*fi))
		d = 1 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < eps {
			break
		}
	}
	return h
}
