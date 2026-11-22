## Methodology

This document describes the data sources, models, and assumptions behind the
analysis presented in each dashboard. The dashboard's About tab is a short
summary that links here.

### Data sources

- **Road, parking, and sidewalk geometry.** OpenStreetMap via the Overpass
  API, under the Open Database License
  ([ODbL](https://www.openstreetmap.org/copyright)). Parking and sidewalks
  always come from OSM alone, and roads do too in every city that sets no
  agency source, so coverage inherits OSM's gaps — most notably uneven
  sidewalk tagging and inconsistent classification of alleys and unpaved
  service roads.
- **Agency road centerlines.** Optional ArcGIS FeatureServer layers,
  configured per city in `pvmt.toml`. They are not merged with the OSM roads:
  a city that sets both has both networks buffered, so a street that both
  sources hold is partly counted twice and the city's paved area is
  overstated. A city should use one road source.
- **City boundaries.** OpenStreetMap administrative boundaries, found by a
  Nominatim search for the city's name or fetched by OSM relation id, with
  OSM water subtracted so that cities with lakes or coastline compare fairly.
- **Population.** 2020 Census blocks from the Census Bureau's TIGERweb
  service (see *Population and per-resident figures*).
- **Initial PCI.** A *stand-in value* supplied through config or the
  interactive slider. pvmt does not ingest field-measured PCI and makes no
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
reported in **FHWA-RD-01-156, *Long-Term Pavement Performance***; they
are continental-US
averages and do not account for local climate, traffic, or
construction quality. A config may set a per-city `decay_rate` to tune
for local conditions (e.g. freeze/thaw or road salt); that override is
applied as the rate for a *typical* road and scales every road class
proportionally, so the per-class ordering (higher classes decay slower)
is preserved rather than flattened. Sidewalks
decay on a separate, slower track and are not treated as a highway class.

The Play board decays each hex at one blended rate: the rates of the road
classes the city maintains in that hex, weighted by each class's paved area
there, under the same `decay_rate` scaling. Its road area is the map's
city-scope roads area for the hex, so where two classes overlap (an
intersection) the shared pavement is counted once. The board starts in the
forecast's [condition spread](#condition-spread): each slice of the spread
takes its share of the board's road area, on hexes picked by a fixed hash of
the hex id, so the slices scatter across the map and the same city always
starts the same board. Each year's need on the board is priced at the
condition each hex decays to by the year's end, as the forecast prices it, so
the board's need and its insolvency threshold match the Financials tab's for
the same roads.

### Cost model

Treatment costs are banded by PCI: each band has a representative
`$/sq m` value, and costs between bands are linearly interpolated at the
tier midpoints, so the cost-versus-PCI curve is smooth rather than
step-shaped. Above the highest anchor (the midpoint of the preventive
tier) and below the lowest anchor (the midpoint of the reconstruction
tier), the cost is clamped to that anchor's value rather than
extrapolated. The default tiers are bare-construction contractor bid
prices: $5 per m² for preventive treatment, $50 for mill-and-overlay
rehabilitation and $150 for full-depth reconstruction. They are
calibration inputs, not measurements, and local bid prices will differ.
Sidewalks are priced at 0.6× the road tiers in every band ($3, $30 and $90
per m²), and a config's own `cost_tiers` are scaled the same way. That
holds wherever sidewalks are costed: on their own, and inside the
scenarios that blend roads, parking and sidewalks.

The site shows PCI as a whole number everywhere. A forecast year's tier and
its label are taken at that shown number, so a year shown at PCI 70 is in
the band that starts at 70. The dollars are not: each road class is priced
at its own unrounded condition on the interpolated curve above.

### Bare construction cost vs. loaded program cost

The cost tiers above are **bare construction** prices: what a contractor
bids to lay the pavement. A city's budget line for the same work carries
more — Berkeley's StreetSaver schedule itemizes the difference as +20%
ADA curb-ramp compliance (federal law: touch a street, rebuild the
ramps), +15% soft costs (design, inspection, project management) and
+10% contingency, together roughly 1.5×.

**No such multiplier is applied by default, and that is deliberate.** The
model's *whole-program* output already matches a loaded municipal budget:
bare tiers, the condition spread and the treatment cycle together reproduce
a real city's published hold-steady spend, so loading the tiers as well
would count the overhead twice. The
[validation report](https://github.com/jcrussell/solvent-streets/blob/main/docs/validation.md)
gives the evidence.

The multiplier exists as `cost_multiplier` (default `1.0`) so the regime is
explicit and adjustable rather than assumed, and the Financials tab exposes
it as a live slider for a reader whose local numbers say otherwise.
Changing it scales every dollar figure on the tab, the hold-steady headline
included, and nothing else.

### Condition spread

A real network is a *distribution* of conditions — some segments excellent, some
failed — not a single average. Because the cost-versus-PCI curve is non-linear,
pricing everything at the network-average PCI under-states the true program cost
(the failed/poor tail is disproportionately expensive). To correct this without
requiring per-segment condition data the model does not have, pvmt spreads the
single configured average PCI into a **distribution around that average** (a Beta
distribution on the 0–100 scale, with the average preserved exactly) and prices
each slice separately. The result is a more realistic, and higher, cost than
pricing the average alone. This is a deliberately conservative
approximation of the real spread, applied automatically: a single-peaked
spread cannot reproduce a network split between excellent and failed streets,
so it under-corrects the worst cases.

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
prices the whole network every year.

The default of 12 years is anchored to the rate at which a real city
repaves (lane-miles per year, not dollars), and with it the default cost
tiers reproduce that city's published budget. The
[validation report](https://github.com/jcrussell/solvent-streets/blob/main/docs/validation.md)
gives the derivation and the limits of that anchor.

### Materials

The Materials tab turns each forecast year's treatment into the asphalt it
takes. It covers every asphalt surface, streets and parking lots alike,
whatever the Financials tab is set to maintain. Each year `1/N` of that
area is treated (see the treatment cycle above); sidewalks are concrete,
so their area is netted out rather than priced as hot-mix. The year's network-average PCI, rounded
to the whole number the site shows, picks one treatment tier for all of it
by condition band (preventive, rehab or reconstruction), whatever the city's
cost tiers are called; the tier's material quantities follow it. The tab's
**Material Intensity by Treatment Tier** table gives each tier's mix and
binder per unit area.

Mix per area is a treatment's compacted lift thickness times dense-graded
hot-mix density (Asphalt Institute MS-2), or a slurry application rate for
preventive work. Binder (asphalt cement, or bitumen) is a typical
asphalt-content share of the mix mass, and aggregate is the rest.
**Oil-equivalent barrels** are the binder's volume in barrels, a
first-order "how much oil" figure rather than a refinery yield.

The headline cards are the yearly average over the horizon under the
best-funded comparison run, the one ending at the highest PCI. Under
imperial units, mass is shown in US short tons. The tiers are
planning-grade defaults, the same for every city, not a city's own mix
designs.

### Scenario comparisons

pvmt ships with three comparison runs driven by annual funding level, all
using the **worst-first** strategy (see [How spending moves
condition](#how-spending-moves-condition)):

- **25% Funding** — annual spend = 25% of the do-nothing baseline's year-1
  need.
- **50% Funding** — annual spend = 50% of the same need.
- **Full Funding** — no budget cap: each year's whole scheduled treatment
  need is funded.

A **do-nothing** baseline (no spend, uncontrolled decay) is shown
alongside the funded runs for comparison.

On the Financials tab these runs, the budget slider and its **Hold steady**
mark cover the part of the network the forecast maintains: streets only by
default, the same network as the solvency headline and the cited current
budget. **Advanced options → Maintaining** adds parking lots and sidewalks.
The comparison runs use the city's configured settings, so they stay put
while Initial PCI and the cost multiplier move the "Your budget" line.

### How spending moves condition

Each year a road class's **need** is the cost of holding it steady:
treating its scheduled 1/N slice at the cost of its condition. Condition
responds smoothly to spend on both sides of that need:

- **Below need**, spend recovers part of the year's decay, from none at $0
  to all of it at full need, so a fully funded class holds its condition.
- **Above need**, the extra money treats more pavement at the same cost,
  with diminishing returns. No surplus closes more than the strategy's
  efficiency times 1/12 of the class's gap to PCI 100 in one year (1/10
  under preventive-first, 1/15 under worst-first; the 1/12 is the default
  cycle, whatever `treatment_cycle_years` says). A second need's worth gets
  about two-thirds of that ceiling, and three more needs' worth about 95%
  of it. Gains shrink as roads improve, and condition approaches 100 but
  never reaches it.

Because better pavement is cheaper to treat, a budget slightly above need
improves condition a little faster each year, and one slightly below loses
it a little faster: the network tends to drift away from the hold-steady
point. The hold-steady (break-even) budget below is that point.

The budget is split across road classes in proportion to their need under
every strategy. The **strategy** sets only how efficiently that money works:
**worst-first** buys less recovery per dollar than **preventive-first**, both
below need and above it. Below need the multipliers are 3.0 and 4.5: a
worst-first program at half its need recovers about 79% of a year's decay.
That level is set by six Bay Area cities' audited condition five years after
running at their cited budgets (validation, §2). Above need they are 0.8 and
1.2. The Financials tab's **Strategy**
control switches the "Your budget" line between them; the reference runs and
the Hold steady mark are always worst-first. The 1.5× ratio between them is an
**illustrative calibration constant**
chosen to reflect the direction and sign of the effect reported in
**FHWA-HIF-12-042, *Pavement Preservation: Preserving our Investment*** —
that $1 of preventive maintenance is reported to avoid $6–$10 of future
reconstruction — not to reproduce that benefit-cost ratio as a
single-year spending efficiency.

### Area growth

Optional **linear annual growth** adds a fixed share of the starting
pavement area each year:

```
Area(y) = Area₀ · (1 + g · y)
```

where `g` is configured per city (default zero). This lets an example
model a city that is still expanding its street network. A negative `g`
models a shrinking network, floored at zero area.

### Solvency metrics (streets/roads only)

The dashboard's Financials headline and the cross-city leaderboard report
three solvency figures. They are computed on the **roads/streets** cohort
only: a city's paving budget is a street budget, so a claim about what it
covers is a claim about streets. They are derived from a
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
  *solvent through the horizon* (Compare shows *After* the last forecast
  year, e.g. "After 2055"). The metric therefore discriminates among
  the genuinely underfunded; on the funded side, **funding gap is the
  primary signal**. Reported only when a current budget is configured.

- **Hold-steady (break-even) budget** — the smallest constant annual
  budget whose **final** deferred backlog is within a small relative
  tolerance (a fraction of the annual need) of zero: the budget that funds
  the network's annually-scheduled treatment slice and keeps pace with the
  cycle, found by searching over budgets. It does not depend on the configured current
  budget, so it is reported for every city. Read it precisely:
  - Because the backlog only grows (see below), a near-zero final backlog
    means **no year of the horizon defers more than a rounding error**.
    The budget therefore covers the costliest year of its own run, not
    the average year.
  - The figure is one flat amount for every year. A network with area
    growth (`g` above, set per city) needs more each year, so its
    break-even is sized for the larger late-horizon network. In the
    early years that budget exceeds need, and the surplus raises
    condition (see *Above need*), so a growing network's "hold steady"
    improves at first.
  - It is in today's dollars, with no inflation (see Assumptions and
    limitations). Compare it with a current budget, not with a future
    year's appropriation.

- **Funding gap** — `(break-even − current budget) / current budget`,
  the primary cross-city ranking metric. Negative when a city already
  budgets at or above its hold-steady level. Reported only when a current
  budget is configured.

Three caveats apply to these figures specifically:

- The **deferred backlog is cumulative unmet need, not a recoverable
  balance.** It only ever grows; spending in a later year reduces *new*
  need but never pays down backlog already accrued. Read it as "total
  treatment value foregone to date," not as a debt that can be cleared.
- The reported **annual spend can be below the configured budget**. A
  class can use at most its need plus three more needs' worth in a year;
  money beyond that would buy almost no condition, so it is not counted as
  spend.
- Break-even assumes the cost-versus-PCI relationship is **monotone**
  (true for the default cost tiers: worse pavement costs more to treat).
  A pathological custom `cost_tiers` curve that violates this could make
  the search **overstate** the break-even budget — a conservative
  direction (it never understates the gap).

### Area figures

The site reports two areas, and each keeps one name on every tab:

- **Paved footprint** is the ground pavement covers: roads, parking and
  sidewalks buffered and unioned, so where a sidewalk or lot meets a road the
  overlap counts once. It is used only for itself (the Map's City Summary)
  and for the city's **Paved share**, which divides it by the city's area
  (the per-hex paved share coloring adds each resource's share instead; see
  [Color by](#color-by)).
- **Maintained area** is the resources' areas summed as the forecast prices
  them, so a sidewalk beside a road counts in both. Over all three
  resources it is never smaller than the footprint. Everything priced or
  apportioned uses it, but which resources it sums depends on the figure:
  - **Compare and Aggregate** sum roads + parking + sidewalks: the
    denominator of every **per-acre** figure (in the current scope,
    city-maintained or all owners, shown beside each city), the year-1
    need, and the area totals and per-city table.
  - **Financials** sums the resources under **Advanced options →
    Maintaining**, streets only by default (see the Financials tab above).
    Its **End Area** is that maintained area in the final year, after
    growth.
  - **The Map's** per-hex **Maintained area** coloring and the city's
    per-resident figures sum the layers toggled on (see
    [Color by](#color-by)).

**Road classes in the two scopes.** The road area of each scope
(city-maintained, all owners) is split across road classes in proportion to
each class's own buffered area. Where two classes cross, that crossing counts
in both classes before the split, so it shifts each scope's class mix a
little. The totals are exact, and city-maintained never exceeds all owners in
total, but a single class can show slightly more city-maintained area than
all-owner area: up to about 4% in dense grids such as Somerville, where the
city-only split no longer shares crossings with state routes. Compare the two
scopes by their totals, not class by class. `pvmt forecast`'s two cohort
breakdowns and the Financials cohort table under each scope show these
per-class figures. The effect on the forecast is the class mix alone, under
0.25% of city-scope need.

### Compare-tab scores

The multi-city Compare view presents two distinct per-city scores:

- **Solvency Score** (primary, the project's headline) — the road-budget
  **coverage ratio**, `current_budget / break_even_budget`, rendered on a
  0–100 scale: **100 = funded to hold the network steady**, 50 ≈ needs
  twice today's budget, 25 ≈ needs four times. Over-funded cities cap at
  100 for display but keep their uncapped coverage as the ranking key, so
  the most-surplus city ranks first (a single gold medal); ties break on
  the later/absent insolvency year. Only a score of 50 or more takes a
  medal: a city lower down keeps its rank but stays off the podium. The
  Street Funding Gap and Years to Insolvency leaderboards hold their medals
  and award to the same floor.
  Computed on roads/streets only and **only for cities with a cited
  `current_budget`** — cities without one
  cannot be scored and are shown `N/A`, unranked. A cited budget must be a
  recurring roads budget: a single-year program, a broad capital category,
  one grant or funding-source line, or a figure that bundles sidewalks in
  an unknown share is not comparable with a roads-only need, so such a city
  carries no `current_budget` and is unranked. A city marked **†** cites no
  pavement survey, so its forecast starts at the default PCI (85) and its
  hold-steady budget and score rest on that assumed condition.
- **Infrastructure Index** (secondary) — a purely *descriptive* composite of
  asset scale, ownership share, walkability, unit cost and blended decay,
  min-max normalized across the compared cities. It is **not** a solvency
  judgment (its "bigger network / more city-owned = higher" weights are
  descriptive, not normative); it exists to summarize network character.
  Every component reads every pavement layer, so it is scored only for
  cities with all of roads, parking and sidewalks: a city with a layer
  missing, or partial because one of its sources failed to ingest, is shown
  `N/A`, unranked, and is left out of the normalization.

### Parking space estimate

The parking stat card, hex tooltips, the Compare tab and the Aggregate
rollup show an **estimated vehicle capacity** for mapped parking lots and
garages. Per lot, in order of precedence:

1. The OpenStreetMap `capacity` tag, when it is a plain positive integer
   (a mapper's count). Values like `yes`, `~50` or `50;60` are not counts
   and fall through.
2. Otherwise the lot's footprint area divided by **35 m² per space**
   (~375 sq ft). Planning references put a surface lot at 300–400 sq ft
   per space including drive aisles and circulation; 35 m² sits in the
   middle of that range, because a mapped lot polygon also takes in
   landscaped islands, internal drives and walkways. For structured parking (`parking=multi-storey`,
   `underground`, `rooftop`) the footprint estimate is multiplied by the
   level count from `parking:levels`, `levels` or `building:levels` when
   one is tagged above 1; an untagged garage counts as a single deck.

Lot estimates are apportioned onto the hex grid by the fraction of each
lot's area inside the hex, and the city total is the sum of those
hex-clipped shares (so it is consistent with the paved-area totals, which
are hex-clipped the same way). Limitations:

- Only `amenity=parking` polygons count. On-street parking is not
  estimated, and lots mapped as a single node are not ingested.
- Overlapping lots, or a multipolygon lot whose member ways are also
  tagged, are each counted — unlike paved area, which unions overlaps. So
  a garage mapped inside a lot polygon counts its footprint levels + 1
  times: once in the lot's area, then once per level. Such nesting is
  uncommon in OpenStreetMap.
- OSM `capacity` tags are sparse and uneven; most lots use the area rule.

### Color by

The map's **Color by** selector offers every per-hex metric available for
the city. The map legend shows the chosen coloring's one-line definition.

- **Paved share** (the default): the share of the hex's land the toggled
  resources cover. Each resource's share is added and the total capped at
  100%, so where a sidewalk overlaps a road the hex reads a little high. The
  city-wide paved share uses the paved footprint instead.
- **Maintained area**: the toggled resources' area inside the hex, each
  counted as the forecast prices it (see [Area figures](#area-figures)).
- **Parking spaces**: estimated parking spaces, shared among hexes by how
  much of each lot they hold.
- **Year-1 need**: the toggled resources' first-year repair need, backlog
  included, shared among hexes by area.
- **Population**, **Housing units** and **Population density**: see
  [Population and per-resident figures](#population-and-per-resident-figures).
- **Fire response time**: see [Service coverage](#service-coverage-fire-response).

The first four are computed in the browser from the hex grid over the
resources currently toggled on, so they change as you toggle roads, parking
or sidewalks; a coloring with nothing to show (no parking, no forecast) is
not offered. Stored metrics such as fire response come from a compute pass
and can also paint hexes that hold no pavement. Each coloring paints six
bands whose edges are fixed values, the same in every city, so colors
compare across cities.

### Population and per-resident figures

**Population**, **Housing units** and **Population density** come from the
2020 Census: block-level counts from the Census Bureau's TIGERweb service.
Each block's people and homes are spread onto the hexes it overlaps in
proportion to the overlapping area. That assumes people are spread evenly
within a block. Blocks are small (about a city block in town), so this
moves people only within a block and never changes the city total. Density
divides a hex's population by its own area after clipping to the city
boundary, so edge hexes are not understated.

**Maintained area per resident** and **Year-1 need per resident**, on the
city panel and the region card, divide the whole city's (or region's)
maintained area, or its year-1 need, by the residents of the same hexes.
Both follow the resources toggled on. They are not offered per hex: a hex
holds a few dozen residents, so shopping, industrial and park hexes would
read blank or enormous. The year-1 need is the do-nothing first-year need,
including any repair backlog. It is not a recurring upkeep cost. The
figures are 2020 counts set against today's roads, so fast-growing areas
read high.

### Service coverage (fire response)

The map's **Color by → Fire response time** mode paints each hex with an
estimated response time from the nearest fire station, in the spirit of a
city-builder's service-coverage overlay. It is computed, not observed:

1. **Facilities.** Fire stations come from OpenStreetMap
   (`amenity=fire_station` nodes, buildings and multipolygons, collapsed
   to a point), plus any hand-placed `[[cities.facilities]]` in the
   config for stations OSM lacks. Stations are fetched up to 5 km beyond
   the city's bounding box, so a neighbouring city's station just across
   the line serves the edge hexes it is nearest to. 5 km is the 240 s
   first-engine travel time at 75 km/h; a station farther out could make an
   edge hex's threshold only by freeway.
2. **Road graph.** The city's stored roads, joined to the public roads in
   the same 5 km margin (fetched with the stations, without driveways and
   parking aisles) so an outside station is routed in over real streets,
   become a routable graph: ways that share an OSM node join at that
   vertex; each segment costs its
   length over the road's posted `maxspeed` when tagged, else a per-class
   free-flow speed (motorway 100 km/h … residential 40, service 20,
   default 30). These are deliberately near posted limits rather than
   emergency speeds — intersections and turns roughly offset the
   exemption. `oneway` (and the implied one-way of motorways and
   roundabouts) is respected. Ways closed to traffic are left out:
   `access`, `vehicle` or `motor_vehicle` of `no` (the most specific of the
   three wins), so an engine is not routed down a closed road. Private
   (gated) ways stay in, as engines carry the gate codes: a campus or estate
   whose roads are private is reached through them, and its own station
   serves it. A closed way that admits emergency vehicles (`emergency=yes` or
   `designated`, or an access tag of `emergency`) stays in too. One source feeds the graph:
   OpenStreetMap when ingested, otherwise the agency centerlines. Those carry no
   `maxspeed`, so they run at the class speed of their functional class,
   or the default when the class is unknown. Agency centerlines never share
   a vertex with OpenStreetMap, so a city routed over them gets no margin
   roads, and a station outside it counts only if it is within 500 m of
   the city's own roads.
3. **Placement.** Each station is placed at the nearest point along a
   road, preferring the main street network over a closer unconnected stub.
   A station farther than 500 m from any road is ignored with a warning.
4. **Travel time.** One shortest-path run from every station at once gives
   each point on the road network its travel time from the nearest station.
   A hex's value is the **turnout** time (80 s, from NFPA 1710) plus the
   travel time to the **first point an engine reaches on any road inside
   the hex**: first arrival anywhere in the hex, not at its centre. Timing
   to the road nearest the centre landed many hexes on a driveway or a
   private campus road that joins the streets far away, and made
   neighbouring hexes differ by minutes. A hex with no road inside it that
   an engine reaches (a park, a hillside, open land, or only a freeway
   passing through) is timed to the nearest reachable point along a road
   from its centre, within 300 m. Its value reads slow because an engine
   can only stop at that road. Nearest *point*, not nearest
   intersection, so a long straight road still reaches the hexes it
   crosses. An edge hex clipped to the city boundary counts only the roads
   inside what remains of it, and with none is measured from a point
   inside that remainder, so a sliver is not timed to a road across a
   river or in the next city.
5. **Freeways.** A motorway or ramp passing through a hex does not count
   as reaching it, and neither a station nor a roadless hex's centre is
   placed on one while any other road is within reach. A freeway carriageway is
   one-way with no frontage, so a point beside it placed on it would be
   timed as reachable in one direction only. Trunk roads, often surface
   arterials, are not excluded.
6. **Display.** Hexes are painted in five bands of total time (≤ 5:20,
   7:20, 9:20, 11:20, > 11:20). Any hex of the grid can be painted, paved or
   not, and a hex with no road within reach is left blank. A hex with a
   road within reach that no station can drive to (a disconnected road
   fragment, a one-way trap) is left blank too, but it is counted as
   **unreachable**: the summary's "within threshold" share is of every hex
   with a road within reach, so an unreachable hex counts as not covered.
   The median and range are over the hexes that were reached. The share
   uses NFPA 1710's 240 s first-engine travel time. `[[services]]` in the
   config overrides the turnout and the threshold.
7. **Stations serving the city.** The summary counts the stations that are
   the nearest to at least one hex, and how many of them lie outside the
   city boundary. A station fetched in the margin that is no hex's nearest
   is not counted.
8. **Incomplete station lists.** OpenStreetMap's station coverage is
   uneven, and a station missing from it reads on the map as a slow or
   uncovered neighbourhood. The summary therefore carries the caveat
   "Station list from OpenStreetMap may be incomplete" when no station
   inside the city serves it, or when the stations inside number fewer than
   **2 per 100,000 residents** (2020 Census, over the same hexes). That
   floor is about the 5th percentile of the showcase cities with census
   data, where cities known to be missing stations sit. The caveat is a
   prompt to check the list, not a finding: a city served under contract by
   a county or a neighbour's department can rightly have none inside. A
   station OSM lacks can be added with `[[cities.facilities]]`.

Limitations: traffic, signals, unit availability, dispatch time and
apparatus type are not modelled, nor is grade separation beyond what OSM
encodes; and OSM's station coverage is uneven (see 8).

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
- Treatment effectiveness is modeled as an efficiency factor on how
  much condition a dollar buys, not as a deterministic post-treatment PCI
  bump.
- Boundaries are OpenStreetMap's, so area, population and per-resident
  figures describe the OSM polygon. Where it lags the Census, a city is
  smaller than its legal limits, and where OSM maps a consolidated or
  combined government, the city is that whole area. In the published
  showcase, for example, Austin, TX is missing about 160 km² and Cheyenne,
  WY about 27 km² of recent annexations, Louisville, KY is the
  consolidated metro, and Honolulu, HI includes East Honolulu.
- Where OpenStreetMap maps few parking lots or sidewalks (much of Los
  Angeles County, among others), that layer's per-resident figures and
  rankings undercount the city.
- Jurisdiction is read from each road's OpenStreetMap `ref`, `operator`
  and `network` tags. Historic
  designations (`US HISTORIC 66`) and ref shapes the rules don't
  recognize count as city streets, so a city's maintained area can include
  a little state or federal mileage.
- Output is a planning-grade estimate, not an engineering specification.

### References

- **FHWA-RD-01-156** — *Long-Term Pavement Performance*, Federal Highway
  Administration. Source for per-class decay-rate defaults.
- **FHWA-HIF-12-042** — *Pavement Preservation: Preserving our
  Investment*, Federal Highway Administration. Motivates the direction
  of the preventive-first vs. worst-first efficiency adjustment.
- [OpenStreetMap contributors](https://www.openstreetmap.org/copyright)
  — road, parking, sidewalk and fire-station geometry (ODbL).
- **NFPA 1710** — *Standard for the Organization and Deployment of Fire
  Suppression Operations… by Career Fire Departments*, National Fire
  Protection Association. Source for the 80 s turnout and 240 s
  first-engine travel time used by the fire response coverage.
- **VTPI** — Litman, T., *Transportation Cost and Benefit Analysis II —
  Parking Costs*, Victoria Transport Policy Institute. Source for the
  300–400 sq ft per surface space rule behind the 35 m² parking-space
  estimate.
- [pvmt source](https://github.com/jcrussell/solvent-streets) —
  implementation reference. See
  [`docs/architecture.md`](https://github.com/jcrussell/solvent-streets/blob/main/docs/architecture.md)
  for the ingest and compute pipeline, and
  [`docs/validation.md`](https://github.com/jcrussell/solvent-streets/blob/main/docs/validation.md)
  for the comparison against published city data.
