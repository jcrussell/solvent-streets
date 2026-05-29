# city-nerd

The 50 largest US cities by 2025 population, rendered through
solvent-streets, inspired by Ray Delahanty's
[CityNerd](https://www.youtube.com/@CityNerd) YouTube channel of top-10
countdowns on American urbanism. This example is **not affiliated with
or endorsed by** CityNerd or Ray Delahanty.

## Caveat: Cities, not metros

CityNerd usually ranks **metropolitan areas** above a population
threshold. Solvent-streets ingests at the **city / municipality** level
(one Nominatim boundary per `[[cities]]` entry). This config is the
largest 50 incorporated places, which is similar in spirit but
materially different at the edges:

- **Smaller here than they "feel":** Boston (~673k as a city, top-10 as
  a metro), Atlanta (~529k city / top-10 metro), Minneapolis paired
  with St. Paul, etc.
- **Cities that are big jurisdictions but smaller central places:**
  consolidated city-counties like Nashville-Davidson, Indianapolis, and
  Louisville/Jefferson are listed under their common short name so
  Nominatim resolves the *city* boundary rather than the full county.
- **The fiscal frame this project cares about is jurisdictional.**
  Pavement maintenance liability sits with the city, not the metro
  region, which is why the city-level rollup is the right unit for a
  "solvent streets" analysis.

## Source

US Census Bureau, *Annual Estimates of the Resident Population for
Incorporated Places of 20,000 or More, Ranked by July 1, 2025
Population: April 1, 2020 to July 1, 2025* (Vintage 2025, file
`SUB-IP-EST2025-ANNRNK.xlsx`).

Retrieved 2026-05-24 from:
<https://www2.census.gov/programs-surveys/popest/tables/2020-2025/cities/totals/SUB-IP-EST2025-ANNRNK.xlsx>

## Running it

```
cd examples/city-nerd
pvmt all ingest      # 50 Nominatim + 50 Overpass pulls — expect hours
pvmt all compute
pvmt serve           # local dashboard; per-city sub-pages at /cities/<slug>/
```

Or, from the repo root, `make site` to publish via `gensite`. The
example appears on the landing page as "City Nerd".

## Why coarser hexes on big cities

The top-level grid is 150 m. A handful of geographically enormous
jurisdictions override that — Houston, Phoenix, San Antonio,
Jacksonville, Nashville, Oklahoma City, Louisville, and LA all use
300 m; New York and Indianapolis use 250 m — to keep compute time and
DB size manageable. The pattern follows
[`examples/los-angeles-ca/pvmt.toml`](../los-angeles-ca/pvmt.toml).
