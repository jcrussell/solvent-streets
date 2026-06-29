## Methodology

This section describes the data sources, models, and assumptions behind the
analysis presented in each dashboard.

### Data sources

- **Road, parking, and sidewalk geometry.** OpenStreetMap via the Overpass
  API, under the Open Database License
  ([ODbL](https://www.openstreetmap.org/copyright)). When a city has no
  ArcGIS or local-layer source configured, OSM is the sole source and
  coverage inherits OSM's gaps — most notably uneven sidewalk tagging and
  inconsistent classification of alleys and unpaved service roads.
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

### Decay model

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
construction quality. A config may set a per-city `decay_rate` to tune
for local conditions (e.g. freeze/thaw or road salt); that override is
applied as the rate for a *typical* road and scales every road class
proportionally, so the per-class ordering (higher classes decay slower)
is preserved rather than flattened. Sidewalks
decay on a separate, slower track and are not treated as a highway class.

### Cost model

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

### Condition spread

A real network is a *distribution* of conditions — some segments excellent, some
failed — not a single average. Because the cost-versus-PCI curve is non-linear,
pricing everything at the network-average PCI under-states the true program cost
(the failed/poor tail is disproportionately expensive). To correct this without
requiring per-segment condition data the model does not have, PVMT spreads the
single configured average PCI into a **distribution around that average** (a Beta
distribution on the 0–100 scale, with the average preserved exactly) and prices
each slice separately. The result is a more realistic — and modestly higher —
cost than pricing the average alone. This is a deliberately conservative
approximation of the real spread; it is applied automatically and moves the
solvency dollars in the direction validated against published city data. When
field-measured per-segment condition becomes available, it will replace this
assumed spread.

### Treatment cycle

Real pavement is maintained on a multi-year cycle: a city treats roughly
one slice of its network each year, not the whole network annually. The
model captures this with a **treatment cycle** of `N` years (default 12,
the midpoint of a typical 10–14 year municipal cycle, configurable via
`treatment_cycle_years`). Each forecast year only `1/N` of the network is
scheduled for treatment, so the **annual treatment need** is the
full-network retreatment cost divided by `N`. This is what makes the
break-even budget a realistic *annual* program cost rather than the
one-off cost of rebuilding the entire network at once. A cycle of `N = 1`
reproduces the older behavior (the whole network priced every year), which
overstated the hold-steady budget several-fold.

The cost level and the cycle length are not independent — the break-even
budget scales as cost ÷ `N`, so a cheaper cost basis and a shorter cycle
trade off exactly. We anchor the cycle to the **physical** rate at which a
city actually repaves (lane-miles treated per year, not dollars): for
Berkeley that hold-steady cadence is ≈ 12 years, which is the default. With
the cycle fixed there, the default bare-construction cost tiers reproduce
the city's cited real hold-steady spend (~$5.6 per m² per year), so the
break-even dollars are calibrated to reality rather than chosen freely. See
the validation report for the derivation and the limits of this anchor.

### Scenario comparisons

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

### Area growth

Optional **compound annual growth** applies to pavement area each year:

```
Area(y) = Area₀ · (1 + g)^y
```

where `g` is configured per city (default zero). This lets an example
model a city that is still expanding its street network; it does not
model demolition or removal.

### Solvency metrics (streets/roads only)

The dashboard's Financials headline and the cross-city leaderboard report
three solvency figures. They are computed on the **roads/streets** cohort
only — the aggregate scenarios blend roads, parking, and sidewalks but
cost the blend at road tiers, which would mis-price sidewalks, so an
absolute dollar claim must be roads-only. They are derived from a
**worst-first** run at the city's configured annual budget.

- **Insolvency year** — the first forecast year in which the cumulative
  deferred backlog reaches **one full treatment cycle of deferred work**
  (`N ×` the annual scheduled need — a whole network's worth of treatment).
  Because the deferred backlog is a monotonically non-decreasing
  accumulator (see below), once a city is an entire cycle behind it does
  not recover within the model, so this is the "unrecoverable" threshold.
  A do-nothing network defers a full slice every year and crosses around
  year `N`; a city funding a fraction `f` of each slice crosses around
  `N / (1 − f)`, so well-funded cities never reach it and are reported as
  *solvent through the horizon*. The metric therefore discriminates among
  the genuinely underfunded; on the funded side, **funding gap is the
  primary signal**. Reported only when a current budget is configured.

- **Hold-steady (break-even) budget** — the smallest constant annual
  budget whose **final** deferred backlog is within a small relative
  tolerance (a fraction of the annual need) of zero: the budget that funds
  the network's annually-scheduled treatment slice and keeps pace with the
  cycle. Found by bisection over budget; the search's upper bound is the
  peak do-nothing annual need over the horizon, which is sufficient to
  fully fund every year.

- **Funding gap** — `(break-even − current budget) / current budget`,
  the primary cross-city ranking metric. Negative when a city already
  budgets at or above its hold-steady level. Reported only when a current
  budget is configured.

Three caveats apply to these figures specifically:

- The **deferred backlog is cumulative unmet need, not a recoverable
  balance.** It only ever grows; spending in a later year reduces *new*
  need but never pays down backlog already accrued. Read it as "total
  treatment value foregone to date," not as a debt that can be cleared.
- In the years **before** the insolvency crossing, the reported
  `annual_spend` series can **exceed the configured budget**. The
  allocator routes leftover budget on a fully-funded cohort into extra
  PCI recovery (a surplus branch), and that extra is counted as spend.
  So an `annual_spend` above `current_budget` in early years is expected,
  not an error.
- Break-even assumes the cost-versus-PCI relationship is **monotone**
  (true for the default cost tiers: worse pavement costs more to treat).
  A pathological custom `cost_tiers` curve that violates this could make
  the bisection **overstate** the break-even budget — a conservative
  direction (it never understates the gap).

### Assumptions and limitations

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

### References

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
