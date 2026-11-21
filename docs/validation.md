# Validation: forecast outputs against real city data

pvmt's dollar and timeline figures are only useful if they land near what real
cities publish. This report compares each stage of the model against
published figures for eight Alameda County cities: Oakland, Berkeley, Fremont,
Hayward, Livermore, San Leandro, Pleasanton and Dublin. The stages are
pavement area, condition decay, unit costs, condition spread, and the
treatment cycle behind the solvency figures. The comparison is anchored on the
MTC StreetSaver PCI series. Berkeley is the one city that publishes the full
set: PCI, budget, backlog, treatment output and condition distribution. Every
figure has a source (see [Sources](#sources)).

What the model *is* is described in the
[methodology](../internal/export/docs/methodology.md). This page is the
evidence for it. The parts that can be checked offline are re-run on every CI
build by [`pvmt validate`](#7-what-is-gated-and-what-remains-open).

## Summary

| Stage | Result | Evidence |
|---|---|---|
| Pavement area | Length right, width short | street length matches Berkeley's published centerline miles within 7%, but area is 16% under Berkeley's published ~3.3M m² (§1) |
| Decay form | Sound | the do-nothing curve is a clean lower bound on every city (§2) |
| Decay rate | Not verifiable from public data | published PCI already includes maintenance (§2) |
| Spending response | Set by five-year budgeted history | six cities at their cited budgets, 2019→2024: RMSE 4.2 PCI at the cited decay rates (§2) |
| Unit costs | Bare-construction level | they match contractor bids, and run ~2–3× below loaded municipal costs (§3) |
| Condition spread | Partly corrected | pricing a single average understates cost ~32–37%; the default spread recovers much of it (§4) |
| Treatment cycle | Matches published cadences | N = 12 sits inside the 10–14-year cycles cities report (§5) |
| Break-even budget | Rate matches Berkeley; dollars low with the area | $5.67/m²·yr against the cited $5.6; $15.6M against the cited $18.3M hold-steady, −14.5% (§6) |

Per square metre, the solvency model reproduces the one city with an
independent hold-steady figure. Pavement area is the weak stage: the road
network's length is right, but the modelled street widths leave out most of
the curb-to-curb extras, so area, and every dollar figure priced on it, runs
low, Berkeley's by about 16%. A width calibration is planned. Until then, and
because the cost anchor rests on a single city, read the solvency dollars as a
planning-grade estimate: right in scale, on the low side, not a budget line
item.

## Method

Each city's area and cost figures come from running `all ingest` and
`all compute` on the Alameda County cities of
[`examples/bay-area-ca`](../examples/bay-area-ca/pvmt.toml), with Overpass as
the only road source (a city that also sets `arcgis_url` counts part of its
pavement twice; see
[configuration](configuration.md#one-road-source-per-city)). The solvency
figures come from `pvmt validate`, which prices each city's committed
reference data (`pkg/cmd/validate/reference.json`); `pvmt help validate`
gives the run's exact settings. The `initial_pci` values in
[`examples/bay-area-ca`](../examples/bay-area-ca/pvmt.toml) match the MTC 2024
table to the digit. The MTC series was extracted three times independently,
with exact agreement. Cohort area does not depend on the hex size: Berkeley's
area is identical at a 75 m and a 100 m edge. So large cities can run on a
coarser grid without changing the result.

## 1. Pavement area

Does buffering centerlines by width recover real pavement area? MTC publishes
**lane-miles**, a count of travel lanes. The tool measures curb-to-curb
pavement, which includes parking lanes, shoulders and turn pockets, so its
area should come out somewhat larger.

Tool area is each city's city-maintained road area.

| City | Tool area | MTC lane-mi | Implied ft/lane-mi | vs 12-ft travel lane |
|---|---|---|---|---|
| Berkeley | 29.7M sf | 450 | 12.5 | 1.04× |
| Oakland | 135.9M sf | 2,052 | 12.5 | 1.05× |
| Livermore | 59.5M sf | 734 | 15.4 | 1.28× |
| San Leandro | 35.1M sf | 394 | 16.9 | 1.41× |
| Pleasanton | 49.0M sf | 520 | 17.8 | 1.49× |
| Dublin | 33.5M sf | 350 | 18.1 | 1.51× |
| Fremont | 106.7M sf | 1,095 | 18.5 | 1.54× |
| Hayward | 67.4M sf | 681 | 18.8 | 1.56× |

Tool area is 1.0–1.6× the bare travel-lane area, lowest in the dense grids
(Berkeley, Oakland) and highest in the suburban networks. Part of it is
service roads: alleys, driveways and parking aisles, most of which no city
maintains or counts in its lane-miles. Without them, tool area is 0.80–1.12×
lane area, 0.98× on average. So the width rules recover the travel lanes and
little more. The parking lanes, shoulders and gutters that make up
curb-to-curb pavement are mostly missing.

Berkeley is the one city that publishes a curb-to-curb area, about 3.3M m².
The tool's 2.76M m² is 16% under it. The shortfall is width, not length.
Inside Berkeley, OpenStreetMap holds 367 km of city-maintained non-service
street, within 7% of the 214 centerline miles (344 km) the city publishes.
But the modelled width on those streets averages 6.35 m, against the ~9.6 m
that Berkeley's area implies. Most residential streets carry no `width` or
`lanes` tag, so across the 14 Alameda County cities 87% of residential length
takes the 5.5 m default, about two travel lanes and no parking. The parking
allowance fires on none of it: it reads `parking:left` and `parking:right`,
and these cities tag parking, where they tag it at all, as `parking:both` or
`parking:lane:both`. A width calibration, a parking and shoulder allowance by
road class, is planned. It has to be checked on more than Berkeley, the only
curb-to-curb figure here, so that it fits the network rather than one city.

## 2. Decay: form and rate

The model is `PCI(t) = PCI₀·e^(−k·t)` per road class. The backtest uses the
aggregate road rate, k = 0.035 (a residential street alone decays at 0.040). It
sets each city's 2019 MTC PCI, runs the do-nothing forecast for five years, and
compares the result with the audited 2024 value.

| City | PCI 2019 | Predicted 2024 (do-nothing) | Audited 2024 | Residual | Implied real k |
|---|---|---|---|---|---|
| Oakland | 53 | 44.5 | 58 | −13.5 | −0.018 (improved) |
| Berkeley | 57 | 47.8 | 56 | −8.2 | +0.004 |
| Fremont | 73 | 61.3 | 71 | −9.7 | +0.006 |
| Hayward | 70 | 58.8 | 73 | −14.2 | −0.008 (improved) |
| Livermore | 79 | 66.3 | 75 | −8.7 | +0.010 |
| San Leandro | 57 | 47.8 | 57 | −9.2 | ~0.000 |
| Pleasanton | 79 | 66.3 | 76 | −9.7 | +0.008 |
| Dublin | 85 | 71.4 | 78 | −6.6 | +0.017 |

Every city beat the do-nothing curve, by 7–14 PCI over five years. That is what
the curve should do. It answers "what if spending stopped", while the audited
series reflects the maintenance cities actually did. The **form** is therefore
a sound lower bound.

The **rate** cannot be checked from this data, for two reasons. MTC's PCI is a
three-year moving average imputed by StreetSaver's own model. It also embeds
maintenance: even Dublin, the one sustained decliner, decays at an effective
k ≈ 0.017, because it still maintains its streets. Checking the rate needs raw
field surveys over a documented low-spend period. Until then, this data gives
no grounds to tune `decay_rate` per city: the observed rates are maintenance,
not deterioration.

### Budgeted backtest

Six cities in the table above cite a roads budget. Each is run worst-first at
that budget for five years from its 2019 PCI, as the dashboard's "Your budget"
line runs it, on its city-maintained road classes from the §1 compute
(`internal/forecast/testdata/mtc_budgeted_backtest.json`, gated by
`TestBacktest_BudgetedFiveYears`).

| City | Budget | PCI 2019 | Predicted 2024 | Audited 2024 | Residual |
|---|---|---|---|---|---|
| Oakland | $60.0M | 53 | 52.3 | 58 | −5.7 |
| Berkeley | $15.0M | 57 | 57.6 | 56 | +1.6 |
| Hayward | $10.1M | 70 | 67.1 | 73 | −5.9 |
| Livermore | $4.85M | 79 | 75.1 | 75 | +0.1 |
| Pleasanton | $4.8M | 79 | 75.8 | 76 | −0.2 |
| Dublin | $3.7M | 85 | 83.9 | 78 | +5.9 |

RMSE 4.2 PCI. Every city but Berkeley runs below its hold-steady need here,
so this tests the partial-funding response: how much of a year's decay a budget short of
need recovers. That response and the decay rate trade off almost one for one
over five years (slower decay with a weaker response fits about as well), so
the data cannot set both. The decay rates stay at their LTPP-derived values,
and the response's strength (the worst-first partial-funding efficiency, 3.0)
is the level this table selects at those rates. Pushed further, the RMSE
falls only to about 4.1, improving Hayward while Dublin and Livermore run
higher. Oakland and Hayward improved on budgets the model reads as
underfunded, so they stay about 6 PCI low under any setting: today's budgets
stand in for 2019–24 spending, and MTC's three-year average smooths the 2024
value. The gate fits and checks the same six cities, so it guards against a
regression rather than validating independently. Spending above need is
barely tested: only Berkeley's budget covers its need on these cohorts, and
only just.

## 3. Cost basis: bare construction vs. loaded program cost

The default tiers are $5, $50 and $150 per m² for preventive work,
rehabilitation and reconstruction. They are compared below against three
sources:

- Caltrans bid prices, which are bare construction.
- Historical FHWA guidance.
- Berkeley's StreetSaver schedule, which is *loaded* with ADA, soft-cost and
  contingency allowances (see the
  [methodology](../internal/export/docs/methodology.md#bare-construction-cost-vs-loaded-program-cost)).

The conversions used are 1 lane-mile = 5,886 m² and 1 SY = 0.836 m².

| Tier | Default | Caltrans bare bid | FHWA historical | Berkeley StreetSaver (loaded) |
|---|---|---|---|---|
| Preventive | $5 | slurry ~$3.6; crack seal ~$1 | slurry $4.5–5.5 (2009) | slurry/micro $10–14; heavy preventive $24–32 |
| Rehab | $50 | mill + 2″ overlay ≈ $30–45 | mill-and-fill $18–41 (2009) | overlay / mill-and-fill $62–124 |
| Reconstruction | $150 | removal + full depth ≈ $120–200 | — | full reconstruction $196–287 |

The tiers match **bare construction** bids, and sit about 2–3× below **loaded**
municipal program costs. Los Angeles's four-tier schedule in
[`examples/los-angeles-ca`](../examples/los-angeles-ca/pvmt.toml) is in the
loaded range.

**Don't load the tiers to "fix" this.** This section compares *per-treatment
unit costs*. §6 compares *whole-program annual dollars*, which is what the site
publishes. At that level the bare tiers, with the condition spread (§4) and
the 12-year cycle (§5), already reproduce Berkeley's loaded hold-steady rate,
$5.67 per m² a year against its $5.6. Multiplying the tiers by 1.5 would put
that rate at about $8.50, 52% above it. A city whose program costs genuinely
run higher has `cost_multiplier` (see the
[methodology](../internal/export/docs/methodology.md#bare-construction-cost-vs-loaded-program-cost)).

## 4. Condition spread

A real network is a distribution of conditions. Cost rises convexly as PCI
falls: the tier midpoints anchor PCI 85 at $5, PCI 55 at $50 and PCI 20 at
$150. So pricing the network at its *average* PCI understates the
area-weighted cost of the real spread. Here is what each published
distribution costs, against pricing at its mean:

| City (source) | Avg PCI | cost(mean) | cost(distribution) | Understatement |
|---|---|---|---|---|
| Berkeley, area-weighted (2022 PMP) | 55 | $50.0/m² | $65.8/m² | +32% |
| MTC Bay Area, lane-mile (2024) | 67 | $32.0/m² | $44.0/m² | +37% |

The failed tail drives it. Berkeley's 24.5% "Failed" share supplies $37 of its
$66 per m². The gap is zero when a distribution sits inside one linear band,
and it grows as the distribution straddles the PCI-55 kink. It is also
sensitive to how the distribution is weighted. San Francisco's segment data
gives +5% against its length-weighted mean and +91% against its segment-count
mean, so only the official, internally consistent distributions are
tabulated.

The forecast prices an assumed spread around the configured average (see the
[methodology](../internal/export/docs/methodology.md#condition-spread)). It
moves Berkeley's break-even from about $4.7 to $5.67 per m² a year. A single-peaked
spread cannot reproduce Berkeley's barbell, though, so the correction is
partial, and only measured per-segment condition would close the gap.

## 5. Treatment cycle

Cities treat a slice of their network each year, not the whole of it. The
evidence on cycle length:

- Berkeley treats 31.9 of its 448.7 lane-miles a year at its $15M budget, 7.1%
  a year, which is a ~14-year cycle. It frames its goal as "PCI 75 in 14
  years".
- Dublin states slurry seals every 5–8 years (arterials) or 6–10 years
  (residential), and overlays every 15–20 years.

The model schedules 1/N of the network each year, with N = 12, the middle of
the 10–14-year municipal range (see the
[methodology](../internal/export/docs/methodology.md#treatment-cycle)). The
cycle has to be in the model. Pricing the whole network every year (N = 1)
multiplies every break-even by 12, which would put Berkeley at about $68 per m²
a year against its real $5.6. The insolvency threshold would shrink to a
single year's need, which a city funding only a small share of it crosses
within a year or two, so the metric could not tell cities apart. With the cycle, `insolvency_year` separates the underfunded cities from
the funded ones.

## 6. How the cost and cycle are anchored

The break-even budget scales exactly as cost ÷ N, so the cost level and the
cycle length can't be told apart from dollar figures alone: a 2.5× lower cost
gives the same break-even as a 2.5× longer cycle. Breaking the tie needs
an anchor that is not in dollars. That anchor is the **physical paving
cadence**. Berkeley (PCI 55, a published deferred backlog of $259.8M) treats
its network on a 14.1-year cadence at $15M a year.
Its $18.3M hold-steady budget buys proportionally more, a cadence of
14.1 × 15 / 18.3 ≈ 11.6 years, which is the model's N = 12. With the cycle
fixed there, the cost level is no longer free:

- Bare tiers with the §4 spread give **$5.67/m²·yr**, against Berkeley's cited
  **$5.6**. Without the spread they give $4.7, so the spread is load-bearing.
- Loaded tiers (~2.5×) would give ~$14, which only N ≈ 30 could match, and
  that contradicts the published cadence.

The shipped defaults, that is, together reproduce Berkeley's published cost
rate: bare-construction tiers, the condition spread and N = 12. The dollar
break-even is that rate times the tool's area, so it inherits §1's area
shortfall. Here is the current model on the six cities with committed
reference data (from `pvmt validate`):

| City | PCI | Break-even | $/m²·yr | Published hold-steady | Current budget | Funding gap |
|---|---|---|---|---|---|---|
| Berkeley | 56 | $15.6M | $5.67 | $18.3M (−14.5%) | $15.0M | +4% |
| Oakland | 58 | $67.5M | $5.35 | — | $60.0M | +13% |
| Hayward | 73 | $18.4M | $2.93 | — | $10.1M | +82% |
| Dublin | 78 | $7.0M | $2.24 | — | $3.7M | +89% |
| Pleasanton | 76 | $11.3M | $2.49 | — | $4.8M | +136% |
| Livermore | 75 | $14.5M | $2.62 | — | $4.85M | +199% |

Berkeley's break-even is 14.5% under its published figure, and nearly all of
that is the area: the tool prices 2.76M m² where Berkeley maintains about
3.3M m² (−16%), at a rate 1% above Berkeley's. The other cities' areas are
short by a similar width effect (§1), so their break-evens and funding gaps
are likely understated too. The paved areas in that data are the §1 tool
areas.
San Leandro and Fremont publish no single citable annual budget, so they have
no row; San Leandro self-reports a deferred backlog of about $180M.

**The limit.** The anchor rests on Berkeley, the only city with an independent
budget, hold-steady cost and cadence. Dublin cross-checks the cadence only.
The decay rate (§2) and the condition spread (§4) cannot be identified
separately from this data.

## 7. What is gated, and what remains open

`pvmt validate` runs the table above offline, from data that ships with the
binary and cites a source on every row. CI fails if Berkeley's break-even
leaves a ±25% band around its published figure. It runs in CI, and anywhere
the binary does. The other cities are reported as context and are not gated,
because none publishes an independent hold-steady figure. The band is wide on
purpose: the model is calibrated on one city, with an unverifiable decay rate
and an assumed spread. What the gate exists to catch is a change to the cost
level or the cycle that moves the dollars by a *factor*. Separately,
`internal/forecast/backtest_test.go` brackets Berkeley's $/m²·yr between $4.5
and $6.5.

Three things remain open:

- **Road width.** Modelled widths leave out most curb-to-curb pavement, so
  area and dollars run low (§1). A width calibration is planned.
- **Decay rate.** It needs raw field-survey PCI over a low-maintenance period
  (§2).
- **Real condition distribution.** Ingesting measured per-segment PCI would
  replace the assumed spread (§4), and would allow a calibration that rests on
  more than one city.

## Sources

All accessed 2026-06-27 unless noted.

| # | Source | Used for |
|---|---|---|
| 1 | MTC PCI tables (`mtc.ca.gov`), 2016/2019/2021/2023/2024 | §2 decay series; provenance |
| 2 | MTC 2024 PCI table, "Total Lane Miles" column (`mtc.ca.gov`) | §1 area (8 cities) |
| 3 | Caltrans Contract Cost Database (`sv08data.dot.ca.gov`), 2022–2026 | §3 bare bid prices |
| 4 | FHWA `FHWA-HIF-10-020` (2010), `fhwa.dot.gov` | §3 historical costs |
| 5 | Berkeley StreetSaver unit-cost schedule (2022 PMP "Table 1", `berkeleyca.gov`) | §3 loaded municipal cost |
| 6 | City of Berkeley 2022 PMP / PTAP-23 Final Report (`berkeleyca.gov`) | §5 cadence; §6 anchor |
| 7 | Berkeley PMP condition categories and MTC 2024 Figure 1 (`mtc.ca.gov`) | §4 distributions |
| 8 | SF DataSF Streets PCI scores (`data.sfgov.org`) | §4 weighting caveat |
| 9 | Hayward FY2026 Pavement Improvement Project (`hayward-ca.gov`) | §6 Hayward budget |
| 10 | Dublin Pavement Resurfacing, stated cycles (`dublin.ca.gov`) | §5 cadence; §6 Dublin budget |
| 11 | Pleasanton FY23/24–26/27 CIP, programs 24503 and 24504 (`cityofpleasantonca.gov`), accessed 2026-06-28 | §6 Pleasanton budget |

MTC table URLs, all under `mtc.ca.gov/sites/default/files`:

- 2024: `/documents/2025-11/PCI_table_2024_data_11-10-2025.pdf`
- 2023: `/documents/2024-10/PCI_table_2023_data_10-30-2024.pdf`
- 2021: `/documents/2022-10/PCI_table-2021_data.pdf`
- 2019: `/PCI_table_2019_data.pdf`

Berkeley report:
`berkeleyca.gov/sites/default/files/documents/City of Berkeley_2022 PMP Update_PTAP 23 Final Report.pdf`.
