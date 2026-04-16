# Methodology

This section describes the data sources, models, and assumptions behind the
analysis presented in each dashboard.

## Data sources

- **Road, parking, and sidewalk geometry.** OpenStreetMap via the Overpass
  API, under the Open Database License
  ([ODbL](https://www.openstreetmap.org/copyright)).
- **Jurisdictional layers.** Optional ArcGIS FeatureService endpoints
  configured per city in `pvmt.toml`.
- **City boundaries.** Nominatim lookups or user-supplied GeoJSON,
  configurable per city.
- **Initial PCI.** A *stand-in value* supplied through config or the
  interactive slider. PVMT does not ingest field-measured PCI and makes no
  claim that the starting condition matches reality — changing this input
  is the right way to explore sensitivity to unknown starting condition.

The exact sources and endpoints used for a given example are listed in
that example's **Config** tab.

## Decay model

Each road classification decays independently via

```
PCI(t) = PCI₀ · exp(−k · t)
```

where `k` is an annual decay constant that depends on the road
classification. Higher-class roads (motorway, trunk, primary) decay more
slowly than lower-class roads (residential, service) because they are
built to thicker, more rigorous design standards and typically receive
more frequent maintenance. Default values are derived from LTPP data
reported in **FHWA-RD-01-156, *Long-Term Pavement Performance*** and
ship as part of the `forecast` package; they are continental-US
averages and do not account for local climate, traffic, or
construction quality. Sidewalks
decay on a separate, slower track and are not treated as a highway class.

## Cost model

Treatment costs are banded by PCI: each band has a representative
`$/sq m` value, and costs between bands are linearly interpolated at the
tier midpoints, so the cost-versus-PCI curve is smooth rather than
step-shaped. Above the highest anchor (the midpoint of the preventive
tier) and below the lowest anchor (the midpoint of the reconstruction
tier), the cost is clamped to that anchor's value rather than
extrapolated. Default cost tiers are expressed in `$/sq m` and sourced
from FHWA treatment-selection guidance; they are calibration inputs, not
measurements, and local bid prices will differ. Roads and sidewalks use
independent cost tiers because the treatment economics differ
substantially.

## Scenario comparisons

PVMT ships with three comparison runs driven by annual funding level, all
using the **worst-first** allocation strategy (budget is spent on the
lowest-PCI segments first):

- **25% funding** — annual spend = 25% of year-1 worst-first need.
- **50% funding** — annual spend = 50% of year-1 worst-first need.
- **Full funding** — unconstrained budget; spend whatever it takes to
  treat every segment that falls below the worst-first trigger each year.

A **do-nothing** baseline (no spend, uncontrolled decay) is shown
alongside the funded runs for comparison.

The forecast library also implements a **preventive-first** strategy
(prioritize highest-PCI segments that are still in the preservation
window), but the default UI comparisons do not exercise it. Preventive
vs. worst-first allocation is governed by per-strategy efficiency
multipliers; those multipliers are **illustrative calibration constants**
chosen to reflect the direction and sign of the effect reported in
**FHWA-HIF-12-042, *Pavement Preservation: Preserving our Investment*** —
that $1 of preventive maintenance is reported to avoid $6–$10 of future
reconstruction — not to reproduce that benefit-cost ratio as a
single-year spending efficiency.

## Area growth

Optional **compound annual growth** applies to pavement area each year:

```
Area(y) = Area₀ · (1 + g)^y
```

where `g` is configured per city (default zero). This lets an example
model a city that is still expanding its street network; it does not
model demolition or removal.

## Assumptions and limitations

- Initial PCI is a user-chosen stand-in, not a field measurement.
- Decay-rate defaults are continental-US averages; local climate,
  traffic, and construction quality shift the true values.
- Hex aggregation loses sub-hex heterogeneity — a hex with one badly
  deteriorated street and three good ones looks like a moderately
  deteriorated hex.
- No network effects are modeled (detours, closures, access
  externalities, project bundling).
- All costs are in today's dollars. No inflation, discounting, or
  present-value adjustment is applied.
- Treatment effectiveness is modeled as an efficiency multiplier on
  spend, not as a deterministic post-treatment PCI bump.
- Output is a planning-grade estimate, not an engineering specification.

## References

- **FHWA-RD-01-156** — *Long-Term Pavement Performance*, Federal Highway
  Administration. Source for per-class decay-rate defaults.
- **FHWA-HIF-12-042** — *Pavement Preservation: Preserving our
  Investment*, Federal Highway Administration. Motivates the direction
  of the preventive-first vs. worst-first efficiency adjustment.
- [OpenStreetMap contributors](https://www.openstreetmap.org/copyright)
  — road, parking, and sidewalk geometry (ODbL).
- [pvmt source](https://github.com/jcrussell/solvent-streets) —
  implementation reference. See
  [`docs/architecture.md`](https://github.com/jcrussell/solvent-streets/blob/main/docs/architecture.md)
  for the ingest and compute pipeline.
