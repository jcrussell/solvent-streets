# Forecast Validation: PVMT outputs vs. real city data

PVMT's forecasting math (geometry → area, PCI decay, cost tiers, solvency) was
built for *internal* correctness, but its dollar/timeline outputs had never been
checked against real cities' published numbers. This report does that check
**without changing the pipeline** — it sources real published data, drives the
existing config/forecast/export path, and tabulates tool-vs-reality residuals.

It is a lightweight comparison, not a calibration harness. Scope: **8 Alameda
County cities** (Oakland, Berkeley, Fremont, Hayward, Livermore, San Leandro,
Pleasanton, Dublin), anchored on the MTC StreetSaver PCI series, with **Berkeley**
as the one city publishing the full set of figures (PCI, budget, backlog,
treatment output, condition distribution). All figures carry a source URL and a
2026-06-27 access date (see [Sources](#sources)).

---

## Executive summary

The tool's **physical estimates are sound; its dollar outputs are not directly
comparable to municipal budgets.** Pavement *area* and *bare-construction* unit
costs hold up across all 8 cities. The decay *form* is sound but its *rate* can't
be pinned from public data. The solvency dollars (break-even, funding gap,
insolvency year) **overstate a city's hold-steady budget several-fold** because
the model implicitly assumes a **1-year treatment cycle** — it prices treating the
*entire* network every year. That overstatement is partially offset by two
*under*-statements in the per-m² cost basis.

| Dimension | Verdict | Headline |
|---|---|---|
| Geometry → pavement area | ✅ **Sound** | Tool = 1.2–1.8× bare travel-lane area across all 8 cities; no outliers |
| Decay — *form* (e^−kt) | ✅ **Sound** | Do-nothing curve is a clean lower bound |
| Decay — *rate* (k) | ⚠️ **Unverifiable here** | Public PCI is maintenance-confounded; needs raw field surveys |
| Cost tiers ($/m²) | ⚠️ **Low** | Match bare bids (Caltrans/FHWA); ~2–3× below *loaded* municipal program cost |
| Condition distribution | ⚠️ **Low** | One mean PCI under-states cost ~32–37% (convex curve, failed tail) |
| Break-even / solvency $ | ❌ **Several-× high** | Assumes a 1-yr treatment cycle; real cycles ~10–14 yr |
| Insolvency year | ❌ **Saturates** | Always year 2; can't discriminate between cities |

**Reading guide.** To turn `break_even` into a usable hold-steady budget, divide
by a realistic **~10–14-year treatment cycle** (and nudge *up* ~2–3× if you want
loaded program costs rather than bare construction). For Berkeley that recovers
$12.5–17.6M/yr — matching its real $15M current / $18.3M published hold-steady,
versus the tool's raw $175.5M break-even.

---

## Method & provenance

Every figure carries a source URL and access date, and cross-checks
were run where possible — the MTC PCI series was extracted independently three
times with exact agreement, and the committed `initial_pci` values match the MTC
2024 table to the digit (Oakland 58, Berkeley 56, Fremont 71, Hayward 73,
Livermore 75, San Leandro 57, Pleasanton 76, Dublin 78).

The end-to-end solvency results come from running `roads ingest` → `roads compute`
→ `export` for each city (ArcGIS Street_Centerlines + Overpass) and reading
`break_even_budget` / `funding_gap` / `insolvency_year` / year-1 `annual_need` /
cohort area from each `forecast.json`. Large cities ran at a coarser `hex_edge_m`;
this is safe because cohort area is hex-independent (verified: Berkeley's area was
*identical* at `hex_edge_m` 75 and 100).

---

## 1. Geometry → pavement area  ✅

The only test of the geometry half of the pipeline: does buffering centerlines by
width recover real pavement area? Tool city-area is compared to MTC's published
**lane-miles** per city (a travel-lane count, so tool area should *exceed* it —
the tool captures curb-to-curb pavement incl. parking/shoulders).

| City | Tool area | MTC lane-mi | Implied ft/lane-mi | vs 12-ft travel lane |
|---|---|---|---|---|
| Oakland | 155.1M sf | 2,052 | 14.3 | 1.19× |
| Berkeley | 35.3M sf | 450 | 14.9 | 1.24× |
| Livermore | 64.8M sf | 734 | 16.7 | 1.39× |
| San Leandro | 39.1M sf | 394 | 18.8 | 1.57× |
| Pleasanton | 55.4M sf | 520 | 20.2 | 1.68× |
| Dublin | 38.3M sf | 350 | 20.8 | 1.73× |
| Fremont | 121.1M sf | 1,095 | 20.9 | 1.75× |
| Hayward | 77.4M sf | 681 | 21.5 | 1.79× |

**Finding.** Tool area is a consistent **1.2–1.8× bare travel-lane area** —
exactly the signature of curb-to-curb pavement (parking lanes, shoulders, turn
pockets). Dense urban grids land lowest (Oakland, Berkeley), suburban widest
(Hayward, Fremont). **No city is an outlier** across an 8× range of network sizes
(350–2,052 lane-mi), so the geometry→area step is robust city-to-city.

---

## 2. PCI decay — form vs. rate

**Model:** `PCI(t) = PCI₀·e^(−k·t)` per road class (`pci.go:41-56`). Aggregate
road `k = 0.035`; a residential-dominated network blends to **k ≈ 0.040**
(verified: Berkeley's do-nothing year-5 PCI of 45.9 from PCI₀=56 implies k=0.0399).
**Backtest:** set each city's MTC network-average PCI in **2019**, run the
do-nothing forecast 5 years, compare to the audited **2024** value.

| City | PCI 2019 | Predicted 2024 (do-nothing) | Audited 2024 | Residual | Implied real k |
|---|---|---|---|---|---|
| Oakland | 53 | 44.5 | 58 | **−13.5** | −0.018 (improved) |
| Berkeley | 57 | 47.8 | 56 | −8.2 | +0.004 |
| Fremont | 73 | 61.3 | 71 | −9.7 | +0.006 |
| Hayward | 70 | 58.8 | 73 | **−14.2** | −0.008 (improved) |
| Livermore | 79 | 66.3 | 75 | −8.7 | +0.010 |
| San Leandro | 57 | 47.8 | 57 | −9.2 | ~0.000 |
| Pleasanton | 79 | 66.3 | 76 | −9.7 | +0.008 |
| Dublin | 85 | 71.4 | 78 | −6.6 | +0.017 |

**Finding.** The do-nothing model **systematically under-predicts** maintained
networks by **~7–14 PCI over 5 years** — *every* city beat the curve. That is
expected, not a defect: a do-nothing forecast asks "what if you stop spending,"
while the audited series reflects the maintenance cities actually did. The implied
real effective rates (−0.018 to +0.017/yr) all sit well below the model's
pure-deterioration 0.035–0.040, because MTC's PCI embeds that maintenance.

**Verdict.** The decay *form* is sound and the do-nothing output is a defensible
**lower bound**. The deterioration *rate* is plausible but **unverifiable** here,
because (1) MTC PCI is a 3-year moving average imputed by StreetSaver's own model
(partly model-vs-model), and (2) it reflects maintenance that happened — even the
only sustained decliner (Dublin, 85→78) decays at an effective k≈0.017, ~half the
model rate, because it still does some maintenance. A true rate check needs raw
field-survey PCI over a documented low-spend window.

---

## 3. Cost tiers — bare construction vs. loaded program cost

**Model:** default tiers **$5 / $50 / $150 per m²** for preventive / rehab /
reconstruction (`cost.go:19-23`). Validated against three regimes: Caltrans bare
bid prices (`sv08data.dot.ca.gov`, 2022–2026), historical FHWA guidance, and the
**City of Berkeley StreetSaver schedule** (2022 PMP) — *loaded* municipal costs
that include 20% ADA + 15% soft costs + 10% contingency (what a city budgets).
Converted at 1 lane-mile = 5,886 m²; 1 SY = 0.836 m².

| Tier | Default | Caltrans bare-bid | FHWA historical | **Berkeley StreetSaver (loaded)** |
|---|---|---|---|---|
| Preventive | $5 | slurry ~$3.6; crack seal ~$1 | slurry $4.5–5.5 (2009) | slurry/micro **$10–14**; heavy prev. $24–32 |
| Rehab | $50 | mill+2″ overlay ≈ $30–45 | mill-and-fill $18–41 (2009) | overlay/mill-&-fill **$62–124** |
| Reconstruction | $150 | removal+full-depth ≈ $120–200 | $16 (1998 — stale) | full reconstruction **$196–287** |

**Finding — there are two cost regimes, and the tiers sit in the lower one.**
Against **bare construction bid** prices the tiers are well-corroborated
(preventive/rehab bracketed by both Caltrans and FHWA; reconstruction matched by
current Caltrans — the 1998 FHWA $16/m² is stale, confirming the code's own "FHWA
midpoints 3–5× low" note). But against **loaded municipal program** costs the
tiers are **~2–3× low across all three** ($5 vs $10–14; $50 vs $62–124; $150 vs
$196–287), and Berkeley notes small systems run another 25–50% higher. LA's
committed 4-tier schedule (`examples/los-angeles-ca/pvmt.toml`: $200/$120/$60/$15)
sits in this loaded range, corroborating it.

So the defaults are sound as **bare-construction** $/m² but understate the all-in
cost a municipality budgets by ~2–3×. The report no longer claims the tiers are an
*upper* bound — they are a *lower*, bare-construction bound. (No default change:
the bare-vs-loaded choice is a modeling decision, and per-city
`[[forecast.cost_tiers]]` overrides already exist for cities that want a loaded
schedule.)

---

## 4. Condition-distribution (aggregation) bias

**Setup.** The model gives every cohort one network-average `initial_pci`
(`cohort.go:55`; the only config field is the scalar `ForecastConfig.InitialPCI`).
Real networks have a **PCI distribution** — some segments excellent, some failed.
Because `cost(PCI)` is **convex** (anchors PCI 85→$5, 55→$50, 20→$150 — the tier
*midpoints* the curve interpolates between; interior kink at PCI 55, clamped flat
outside 85/20; slope steepens as PCI falls), evaluating at the *mean* under-states the
area-weighted cost over the real spread (Jensen's inequality): `cost(mean) ≤
Σ frac_b · cost(PCI_b)`. This is analysis-only — the tool can't ingest a
distribution, so we compute what cost *would* be under the published distribution
vs. what the tool computes at the mean, both from the exact `cost.go` curve.

| City (source) | Avg PCI | cost(mean) | cost(distribution) | **Under-statement** |
|---|---|---|---|---|
| Berkeley — official area-weighted (2022 PMP) | 55 | $50.0/m² | $65.8/m² | **+32%** |
| MTC Bay Area — official lane-mile (2024) | 67 | $32.0/m² | $44.0/m² | **+37%** |
| SF — segment-count (computed) | 75–81 | $20–11/m² | $21.0/m² | +5% to +91% (caveat) |

**Finding.** For the two internally-consistent official distributions, the
single-average assumption under-states modeled per-m² cost by **~32–37%**, driven
by the **failed/poor tail**: Berkeley's 24.5% "Failed" share at $150/m² supplies
$37 of its $66 distribution cost, while `cost(55)=$50` ignores that the network is
*barbell*-shaped, not uniformly fair. This is a **lower bound** (band midpoints
under-state the convex within-band cost; the model also starts at zero spread).

**Caveats.** The bias is **zero when the distribution sits inside one linear band**
and grows as it straddles the **PCI-55 kink** (and the 85/20 clamp corners) — it's
about *spread relative to the kinks*, not the mean. It is **sensitive to weighting**: SF's bias is +5% against its official
length-weighted mean (75) but +91% against the segment-count mean (81), so only
the official cases are reported. And differential per-class decay adds *some*
spread over time, but the network starts at zero spread and never models
within-class variation — so the bias is largest at t=0 and is a genuine floor.

---

## 5. Budget / solvency & the treatment cycle  ❌

The full-pipeline test. For each city we compare the tool's `break_even_budget`
(smallest constant budget that holds the network steady) and `funding_gap` /
`insolvency_year` to reality. **Berkeley is the anchor** (2022 PMP/PTAP-23):
network PCI 55, deferred backlog **$259.8M**, budget to **maintain PCI 55 =
$18.3M/yr**, current ~$15M/yr, 214 centerline miles. That $18.3M/yr implies a real
hold-steady spend of **~$5.6/m²·yr** — the reality yardstick below.

| City | PCI | Break-even $/yr | Current budget | Gap (×) | Insolv | Break-even $/m² | vs $5.6 real |
|---|---|---|---|---|---|---|---|
| Berkeley | 56 | $175.5M | $15.0M | 11.7× | yr2 | $53.5 | **9.6×** |
| San Leandro | 57 | $184.9M | n/v† | — | — | $50.9 | 9.1× |
| Oakland | 58 | $707.7M | $60.0M | 11.8× | yr2 | $49.1 | 8.8× |
| Fremont | 71 | $340.7M | n/v† | — | — | $30.3 | 5.4× |
| Hayward | 73 | $197.2M | $10.1M | 19.6× | yr2 | $27.4 | 4.9× |
| Livermore | 75 | $147.6M | $4.85M | 30.4× | yr2 | $24.5 | 4.4× |
| Pleasanton | 76 | $119.0M | $4.8M | 24.8× | yr2 | $23.1 | 4.1× |
| Dublin | 78 | $71.8M | $3.7M | 19.4× | yr2 | $20.2 | 3.6× |

† San Leandro's annual paving budget **could not be pinned to a single citable
figure** (recurring appropriations and multi-year contracts give conflicting
numbers), so no funding gap is computed; the city self-reports a ~$180M
deferred-maintenance backlog. **Pleasanton is verified**: **~$4.8M/yr** = CIP 24503
*Annual Street Resurfacing & Reconstruction* (~$4.0M, FY24/25) + CIP 24504 *Annual
Slurry Sealing* (~$0.8M), per the FY23/24–26/27 CIP; the city independently confirms
PCI 76 and 215 centerline-mi / 518 lane-mi. Fremont's current budget is not published
in a citable form.

**Finding A — the overstatement is structural.** For *every* city,
`break_even ≈ year-1 annual_need` (ratio **1.002–1.004**): the break-even budget
literally equals the cost to treat the **entire network in year 1**. The
break-even $/m² ($20–53) tracks PCI monotonically — exactly the cost-tier
interpolation — confirming `break_even = area × cost(PCI)`. The tool implicitly
assumes a **1-year treatment cycle**.

**Finding B — the overstatement *is* the missing treatment cycle (sourced).**
Because `break_even = full-network-yearly cost`, the ratio `break_even / current`
*is the treatment cycle the tool implies a city is on*:

| City | Tool-implied cycle | %/yr funded | City | Tool-implied cycle | %/yr funded |
|---|---|---|---|---|---|
| Berkeley | 11.7 yr | 8.5% | Hayward | 19.6 yr | 5.1% |
| Oakland | 11.8 yr | 8.5% | Pleasanton | 24.8 yr | 4.0% |
| Dublin | 19.4 yr | 5.2% | Livermore | 30.4 yr | 3.3% |

These match **real, published** behavior: Berkeley's StreetSaver PMP reports it
treats **~31.9 lane-mi/yr at $15M = 7.1%/yr of its 448.7 lane-mi (~14-yr cycle)**
and frames its goal as "PCI 75 in 14 years"; Dublin states slurry every **5–8 yr**
(arterial) / **6–10 yr** (residential), overlays every **15–20 yr**. So the tool's
*cost basis is roughly right* (8.5%/yr-funded for Berkeley ≈ the real 7.1%/yr
treated) — it just labels **a single year of a ~10–14-yr cycle** as "break-even."
Dividing `break_even` by the real cycle recovers reality: $175.5M ÷ ~10–14 yr ≈
**$12.5–17.6M/yr**, bracketing Berkeley's $15M current and $18.3M hold-steady.

**Finding C — the overstatement factor is PCI-dependent (~3.6×–9.6×).** Against
the realistic $5.6/m²·yr, low-PCI networks overstate most (Berkeley 9.6×, Oakland
8.8×) and good-condition networks least (Dublin 3.6×). The "~10×" headline is the
worst-condition end; the error shrinks as condition improves (fewer m² fall in the
expensive rehab/reconstruction bands → a shorter effective preventive cycle).

**Finding D — insolvency saturates.** Every budgeted city goes insolvent in
**year 2**, because the threshold (one full year-1 network treatment, $72M–$708M)
is unreachable by any real budget. The metric can't discriminate between cities —
it's a structural floor, not a signal.

---

## 6. How the biases interact

The errors run in **opposite directions** on the per-m² cost vs. the time basis:

- per-m² cost is **under**-stated — ~2–3× (§3, bare vs. loaded) and a further
  ~30–40% (§4, mean vs. distribution);
- break-even is **over**-stated ~10× (§5, the 1-year cycle).

They partially offset, and the cycle term dominates → net break-even stays
several-× high. We **do not** claim they reconcile to an exact factor: a real
hold-steady program is preventive-heavy (cheap treatments on the good majority),
not full-cost re-treatment of the whole network, so the cost bases aren't directly
multipliable. The honest summary is the [reading guide](#executive-summary):
break-even ÷ a ~10–14-yr cycle ≈ a real hold-steady budget.

---

## 7. Recommendations & future work

**Calibration (no schema change needed today):**
- **Decay:** defaults are reasonable; per-city `decay_rate` overrides are *not*
  warranted from this data (the observed rates are maintenance-blended, not
  deterioration rates).
- **Cost tiers:** sound as bare-construction $/m². If the solvency dollars are
  meant to mirror municipal budgets, a *loaded* per-city `[[forecast.cost_tiers]]`
  schedule (as LA uses) is the lever.
- **Solvency $:** interpret `break_even` as an upper-bound
  "resurface-everything-this-year" figure, not a hold-steady budget. This reading
  guidance belongs in the methodology doc so the outputs aren't over-claimed.

**Future work (the real fixes, out of scope here):** a `pvmt validate`/backtest
harness; per-segment measured-PCI ingestion; a **distribution-aware initial
condition** (per-class or histogram `initial_pci`) to remove the §4 bias; and a
**treatment-cycle / triggering model** in the solvency code to remove the §5
overstatement. Together these would turn the solvency dollars from
order-of-magnitude into directly comparable.

---

## Sources

All accessed **2026-06-27**.

| # | Source | Used for |
|---|---|---|
| 1 | MTC PCI tables (`mtc.ca.gov`), 2016/2019/2021/2023/2024 | §2 decay series; provenance |
| 2 | MTC 2024 PCI table "Total Lane Miles" column (`mtc.ca.gov`) | §1 area validation (8 cities) |
| 3 | Caltrans Contract Cost Database (`sv08data.dot.ca.gov`), 2022–2026 | §3 cost tiers (bare bid) |
| 4 | FHWA `FHWA-HIF-10-020` (2010) & `FHWA-SA-98-042` (1998), `fhwa.dot.gov` | §3 cost tiers (historical) |
| 5 | Berkeley StreetSaver unit-cost schedule (2022 PMP "Table 1", `berkeleyca.gov`) | §3 loaded municipal cost |
| 6 | City of Berkeley 2022 PMP / PTAP-23 Final Report (`berkeleyca.gov`) | §5 solvency anchor; §6 cycle |
| 7 | Berkeley PMP condition-category breakdown + MTC 2024 Figure 1 (`mtc.ca.gov`) | §4 PCI distributions |
| 8 | SF DataSF Streets PCI scores (`data.sfgov.org`) | §4 distribution caveat (segment-count) |
| 9 | Hayward FY2026 Pavement Improvement Project (`hayward-ca.gov`) | §5 Hayward budget (verified) |
| 10 | Dublin Pavement Resurfacing (stated cycles) (`dublin.ca.gov`) | §5 treatment-cycle grounding |
| 11 | Pleasanton FY23/24–26/27 CIP, programs 24503 / 24504 (`cityofpleasantonca.gov`), accessed 2026-06-28 | §5 Pleasanton budget (verified) |

MTC table URLs: `2024` `/documents/2025-11/PCI_table_2024_data_11-10-2025.pdf`,
`2023` `/documents/2024-10/PCI_table_2023_data_10-30-2024.pdf`,
`2021` `/documents/2022-10/PCI_table-2021_data.pdf`,
`2019` `/PCI_table_2019_data.pdf` (all under `mtc.ca.gov/sites/default/files`).
Berkeley report: `berkeleyca.gov/sites/default/files/documents/City of Berkeley_2022 PMP Update_PTAP 23 Final Report.pdf`.

*Companion config: `examples/bay-area-ca/pvmt.toml` carries the committed
`initial_pci` (MTC 2024) and `current_budget` cites for these cities.*
