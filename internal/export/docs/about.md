## About

Solvent Streets estimates what it costs a city to keep its streets in good
repair, and whether today's paving budget is enough. The map shows a city's
streets, parking lots and sidewalks. The Financials tab forecasts their
condition and repair costs over the next 30 years, and where several cities
are loaded, Compare ranks them side by side.

### Where the data comes from

- **Streets, parking lots and sidewalks:** OpenStreetMap, the volunteer-built
  map of the world.
- **Population and housing:** the 2020 U.S. Census, block by block.
- **Budgets and pavement condition:** each city's own published figures,
  where it has them. The Config tab lists the sources.

### How the forecast works

Pavement condition is scored from 0 to 100, the Pavement Condition Index
(PCI). In the model every street wears down a little each year, side
streets faster than main roads, and repairs cost more the worse a road gets:
about $5 per square metre to seal a good road, $50 to resurface a worn one
and $150 to rebuild a failed one. The **hold-steady budget** is what a city
would have to spend each year to keep its streets from falling further
behind.

### Solvency Score and insolvency year

The **Solvency Score** compares a city's current street budget with its
hold-steady budget, on a scale of 0 to 100. A score of 100 means the city
spends enough to hold its streets steady; 50 means it would need twice
today's budget. A city that cites no budget cannot be scored and shows N/A.

The **insolvency year** is the year a city's postponed repairs add up to a
whole repair cycle's worth of work, the point at which, in the model, it can
no longer catch up. A well-funded city may never reach it.
[How both are computed](../../../docs/methodology.md#solvency-metrics-streetsroads-only).

### Map colorings

The map's **Color by** menu shades each hexagon by one measure, and its
legend says what that measure is.

- **Paved share** (the default): how much of the hexagon is road, parking
  lot or sidewalk.
- **Maintained area**: that pavement's area, as the forecast prices it.
- **Parking spaces**: estimated parking spaces.
- **Year-1 need**: what the first year of repairs would cost, backlog
  included.
- **Population**, **Housing units** and **Population density**: from the
  2020 Census.
- **Maintained area per resident** and **Year-1 need per resident**: those
  two divided by the people who live in the hexagon.
- **Fire response time**: the estimated time for a fire engine to arrive
  from the nearest station.

The pavement colorings follow the layers switched on under the gear menu.
[How each is computed](../../../docs/methodology.md#color-by).

### Keep in mind

- **Modelled, not measured.** These are planning-grade estimates built on
  national averages for how fast pavement wears and typical contractor
  prices, not on a city's own engineering data.
- **Starting condition is often assumed.** Where a city cites no pavement
  survey, every street starts at PCI 85 and the city is marked †. Its score
  rests on that assumption.
- **OpenStreetMap coverage varies.** Where volunteers have mapped few
  parking lots or sidewalks, those figures run low. City boundaries are
  OpenStreetMap's too, and can lag recent annexations.
- **Today's dollars.** Costs include no inflation.

See all the [assumptions and
limitations](../../../docs/methodology.md#assumptions-and-limitations).

### Learn more

- [Full methodology](../../../docs/methodology.md): every model, setting and source.
- [Validation](../../../docs/validation.md): how the estimates compare with
  cities' published figures.
- [README](../../../README.md) and the
  [source code](https://github.com/jcrussell/solvent-streets) on GitHub.
