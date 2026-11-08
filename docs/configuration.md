# Configuration

Everything pvmt does is driven by one `pvmt.toml`. It lists the cities to
analyse as `[[cities]]` entries. Each city names its data sources and
inherits shared settings from the top level: the hex grid, the forecast,
display and export. Any city can override those settings. A config can also
pull in other configs with `[[include]]`. The smallest config is one city:

```toml
[[cities]]
name = "Alameda, CA"
overpass = true
```

Unknown keys and out-of-range values are rejected when the config loads, with
exit code 2 and a message naming the field. `pvmt config show --sources` prints
every resolved value with where it came from: `flag`, `env`, `file` or
`default`. Working configs to copy from are in [`examples/`](../examples/README.md).

## Finding the config and resolving values

pvmt looks for `pvmt.toml` in the current directory and then in each parent
directory, and the first match wins. So a config at a project's root works from
any subdirectory.

A value is resolved from the first layer that sets it:

1. CLI flags: `--city`, `--units`, `--log-level`.
2. Environment variables.
3. The city's own `[[cities]]` entry.
4. The top-level tables: `[grid]`, `[forecast]`, `[display]`.
5. Built-in defaults.

A per-city `[cities.forecast]` overrides field by field, so set only the
fields that differ.

| Variable | Overrides |
|---|---|
| `PVMT_UNITS` | `[display].units` (`metric` or `imperial`) |
| `PVMT_HEX_EDGE_M` | `hex_edge_m` (meters, minimum 10) |
| `PVMT_FORECAST_YEARS` | `[forecast].years` |
| `PVMT_FORECAST_INITIAL_PCI` | `[forecast].initial_pci` |
| `PVMT_LOG` | the log level (`debug`, `info`, `warn`, `error`; default `warn`) |

An environment value that doesn't parse, or is out of range, is ignored with a
`warning:` on stderr, and the next layer wins. `PVMT_HEX_EDGE_M` changes
computed results, so it is part of the
[config hash](#snapshots-and-the-config-hash). The forecast variables take
effect on the next read.

## Files and directories

| What | Linux | macOS |
|---|---|---|
| Database (`pvmt.db`) | `$XDG_DATA_HOME/pvmt`, default `~/.local/share/pvmt` | `~/Library/Application Support/pvmt/data` |
| HTTP response cache | `$XDG_CACHE_HOME/pvmt/http`, default `~/.cache/pvmt/http` | `~/Library/Caches/pvmt/http` |

Every config on the machine shares one database and one cache. On macOS the
`XDG_*` variables are ignored. The cache keeps
upstream responses for 24 hours. It is not configurable from `pvmt.toml`,
because it belongs to no single config; `pvmt cache prune` manages it (see
[troubleshooting](troubleshooting.md#disk-usage-the-http-cache-keeps-growing)).

## Cities

A city's `name` is how pvmt finds its boundary, and it derives the city's slug
from it: `"Berkeley, CA"` becomes `berkeley-ca`. Without `--city`, a command
runs over every city. `--city` picks one, by name or slug.

**Data sources.** `overpass = true` fetches roads, parking and sidewalks from
OpenStreetMap. `arcgis_url` adds an ArcGIS FeatureServer of road centerlines.
A city may use both, and features are deduplicated by ID. A private or
loopback ArcGIS address is refused unless the city also sets
`allow_private_arcgis = true`, which is for self-hosted or staging endpoints
only. Fire stations always come from OpenStreetMap, and census blocks always
come from the Census Bureau's TIGERweb, whatever these settings say.

**Boundary.** The city boundary comes from a Nominatim search for the name.
When Nominatim has no polygon for the city, set `boundary_relation_id` to the
city's OSM `admin_level=8` relation, and the boundary is fetched from Overpass
instead. Ingest's error names this fix. Find the ID with
[Overpass Turbo](https://overpass-turbo.eu/):
`relation["name"="<city>"]["boundary"="administrative"]["admin_level"="8"];out;`.
A relation whose bbox spans more than 5° is rejected as a likely county or
state.

**Census.** By default `pvmt all` also apportions 2020 Census population onto
the city's hexes. Set `census = false` for a city outside the US, where
TIGERweb has no blocks.

### Tags

`tags` group cities in the dashboard's city selector and drive its filters:

```toml
[[cities]]
name = "San Jose, CA"
tags = ["Bay Area", "metro:South Bay"]
```

A tag is either a `dimension:value` pair or a plain label. A plain label
belongs to the `group` dimension. A dimension name is a lowercase identifier,
and the value is everything after the first colon. The `group` dimension
builds the selector's sections. Every dimension with at least two values gets
a filter in the gear menu. Filters combine with AND, apply on every tab, and
are recorded in the URL as `?tag=dimension:value`. Every city whose name ends
in a comma and a two-letter code (`"Livermore, CA"`, but also `"Toronto, ON"`)
also gets a `state:XX` tag at export, unless it sets a `state:` tag itself. Malformed tags are rejected at load, for example
`":CA"`, `"state:"` or `"State:CA"`.

## Including other configs (`[[include]]`)

`[[include]]` merges the cities of other configs into this one and tags them
at the include site. This is how [`examples/all`](../examples/all/pvmt.toml)
builds the published site from every example without repeating a city:

```toml
[[include]]
path = "../bay-area-ca/pvmt.toml"   # relative to this file
tags = ["Bay Area"]
```

Cities are merged by slug. A city reached through several includes appears
once, with the union of their tags. Two *different* names that slugify alike
are an error.

Calibration merges per field. That covers `hex_edge_m`, `min_hex_area` and
every `[forecast]` field. The first include to set a field wins it, and later
includes fill only the fields still unset. So list the more specific config
first. When two includes set the same field to different values, the load
prints a `warning:` naming the city, the field, both values and both files.

An included file's calibration is flattened into per-city values. So the
including file may not set top-level `[grid]`, `[forecast]` or
`[display].min_hex_area`, because those would silently recalibrate the
included cities. Its `[display].units` and `[export]` apply as usual. An
included file's `[[services]]` and `[export]` are not carried over.

### Config identity (`config_id`)

Cities in the database are keyed by slug *and* config. That way two unrelated
configs that both define "Livermore, CA" keep separate data. The config's key
is `config_id`. It defaults to a hash of the config file's absolute path. Set
it explicitly to keep the key stable when the file moves, or across machines
sharing a database:

```toml
config_id = "austin-tx"
```

A city pulled in through `[[include]]` keeps the `config_id` of the file that
declared it. So `examples/all` reads exactly the data each example's own ingest
and compute produce, with no ingest of its own, and an ingest or compute run
from `examples/all` writes into, and replaces, those examples' data. This holds
while the including config resolves the same
[compute inputs](#snapshots-and-the-config-hash) for the city. When it doesn't,
the city reads nothing until it is computed under the including config, and
export names the setting that differs.

Two configs that set the *same* `config_id` and define the same city share its
row and overwrite each other's data, so give each config its own.

## Snapshots and the config hash

Computed results are stored per city under a hash of just the resolved
settings that change them. Export, serve and `pvmt forecast` read only results
whose hash matches the city's current one.

| Changes the hash (recompute the city) | Does not |
|---|---|
| `hex_edge_m`, including `PVMT_HEX_EDGE_M` | comments, whitespace, key order |
| `[[services]].turnout_s`, an explicit `0` too | `[[services]].travel_threshold_s` |
| a facility's `service`, `lat` or `lon` | a facility's `name` |
| `census = false` | `[display]`, `min_hex_area`, `[export]`, `tags`, city names |
| a pvmt release whose compute logic changed | every `[forecast]` setting and `PVMT_FORECAST_*` |
| | data sources: `overpass`, `arcgis_url`, `allow_private_arcgis`, `boundary_relation_id` |

Forecasts are never stored. Every reader simulates them from the stored
results under the settings in force, so a forecast edit takes effect on the
next read. Editing a data source changes what the next ingest fetches, and the
recompute that follows an ingest picks it up. An explicit default
(`hex_edge_m = 100`) hashes the same as leaving it out. The exception is
`turnout_s`, whose default lives in the services registry.

When a city's results were computed under other settings, export refuses the
city, and forecast, status and serve report the same thing (see
[troubleshooting](troubleshooting.md#no-snapshot-matches-current-config-hash)).
`pvmt status` and `pvmt snapshots ls` show each snapshot's hash.

## Grid and display

**`hex_edge_m`** sets the hex edge in meters. Set it in `[grid]` or per city.
The default is 100 and the minimum is 10. Hex count grows with the square of
the inverse edge, so large cities run coarser: the examples range from 60 m to
300 m.

**`min_hex_area`** drops boundary slivers smaller than this many m² from the
map, the stats and the play board. Set it in `[display]` or per city; the
default is 100. It is coupled to the edge: a threshold above a full hex's area
(`3√3/2 · edge²`) drops every hex.

**`[display].units`** is `imperial` (the default) or `metric`. It changes
presentation only.

## Forecast

The forecast models each road class's condition decaying over the horizon,
priced by PCI-banded cost tiers, against the city's budget. What each setting
means is explained in the [methodology](../internal/export/docs/methodology.md).
Every setting goes in `[forecast]` or a city's `[cities.forecast]`:

| Key | Default | Range | Sets |
|---|---|---|---|
| `initial_pci` | 85 | (0, 100] | the network's average starting condition ([Data sources](../internal/export/docs/methodology.md#data-sources)) |
| `decay_rate` | per class | 0–1 | the decay rate of a typical road; every road class scales with it, and parking and sidewalks keep their own rates ([Decay model](../internal/export/docs/methodology.md#decay-model)) |
| `growth_rate` | 0 | −0.5–0.5 | linear annual growth of paved area; a per-city `0` overrides the top level ([Area growth](../internal/export/docs/methodology.md#area-growth)) |
| `years` | 20 | ≥ 0 | the forecast horizon |
| `cost_tiers` | 3 tiers | PCI 0–101 | treatment cost per m² by PCI band ([Cost model](../internal/export/docs/methodology.md#cost-model)) |
| `cost_overhead` | 1.0 | 0–5 | a multiplier on every tier ([Bare construction cost vs. loaded program cost](../internal/export/docs/methodology.md#bare-construction-cost-vs-loaded-program-cost)) |
| `treatment_cycle_years` | 12 | 1–40 | the share of the network treated per year, 1/N ([Treatment cycle](../internal/export/docs/methodology.md#treatment-cycle)) |
| `current_budget` | unset | ≥ 0 | the annual pavement budget in dollars; it enables `insolvency_year` and `funding_gap` ([Solvency metrics](../internal/export/docs/methodology.md#solvency-metrics-streetsroads-only)) |

A tier is one `[[forecast.cost_tiers]]` table with `min_pci`, `max_pci`,
`cost_per_sqm` and a `label`. Setting tiers replaces the defaults entirely.
Except for `growth_rate`, a `0` means "use the default". The default tiers and
cycle are calibrated together against published city figures. The evidence is
in [validation.md](validation.md#6-how-the-cost-and-cycle-are-anchored), and
it is why `cost_overhead` defaults to 1.0.

## Services (`[[services]]`, `[[cities.facilities]]`)

`pvmt services` computes how long a public service takes to reach each hex
over the road network. The dashboard shows it as **Color by → Fire response**.
`fire` is the one registered service. `pvmt all` runs the service steps after
the pavement passes: a city with no stations mapped is skipped quietly, and any
other failure is a warning. Coverage routes over the ingested roads, so it
needs a roads ingest but not a roads compute.

```toml
[[services]]
name = "fire"
turnout_s = 80            # added to every travel time
travel_threshold_s = 240  # travel time that counts as covered

[[cities]]
name = "Livermore, CA"
overpass = true

  [[cities.facilities]]   # a station OpenStreetMap is missing
  service = "fire"
  name = "Station 10"
  lat = 37.6819
  lon = -121.7680
```

`[[services]]` overrides only the fields it sets, and an explicit `0` counts
as set. The defaults and their sources are in the
[methodology](../internal/export/docs/methodology.md#service-coverage-fire-response).
A hand-placed facility is routed from exactly like a mapped one, and may sit
just outside the boundary. A service name that isn't registered is a config
error.

## Export and hosting

**`[export].title`** names a multi-city dashboard. It defaults to the output
directory's name.

**`[export].coordinate_decimals`** sets the precision of emitted GeoJSON. The
default is 5, about 1 m, which is far finer than any hex.

**`[export].boundary_simplify_m`** sets the simplification tolerance, in
meters, for the *drawn* city outline only. The default is 10. A negative value
disables simplification, and the maximum is 1000. Hex clipping and every area
figure use the full stored boundary, so at high zoom a clipped hex can overhang
the drawn outline by up to the tolerance. A simplified ring is not guaranteed
to be a valid polygon, which matters only to a consumer that fills
`boundary.geojson` rather than drawing it.

`pvmt serve` pins each city's snapshot when it starts and sends
`Cache-Control: public, max-age=300` on every response, so restart it to pick
up a new compute. `pvmt export` writes plain files, so caching is whatever the
host applies. After re-publishing, purge the
CDN if the new build has to show immediately.
