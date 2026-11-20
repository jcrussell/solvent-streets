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
| `PVMT_FORECAST_HORIZON_YEARS` | `[forecast].horizon_years` (1–200) |
| `PVMT_FORECAST_INITIAL_PCI` | `[forecast].initial_pci` |
| `PVMT_OVERPASS_URL` | `[overpass].url` (see [Overpass endpoint](#overpass-endpoint-overpass)) |
| `PVMT_LOG_LEVEL` | the log level, as `--log-level` (`debug`, `info`, `warn`, `error`; default `warn`). `PVMT_LOG` is accepted too; `PVMT_LOG_LEVEL` wins when both are set |

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
`XDG_*` variables are ignored. The cache stores
upstream responses gzip-compressed. Because it belongs to no single config, a
config only sets how old an entry its own runs accept (`[cache] ttl`, a week by
default; see [HTTP cache](#http-cache-cache)); `pvmt cache prune` manages its
size (see
[troubleshooting](troubleshooting.md#disk-usage-the-http-cache-keeps-growing)).

## Cities

A city's `name` is how pvmt finds its boundary, and it derives the city's slug
from it: `"Berkeley, CA"` becomes `berkeley-ca`. Without `--city`, a command
runs over every city. `--city` picks one, by name or slug. Every city needs a
`name` whose slug is not empty, and no two cities in a file may share a slug.

**Data sources.** `overpass = true` fetches roads, parking and sidewalks from
OpenStreetMap. `arcgis_url` adds an ArcGIS FeatureServer of road centerlines.
A city may use both, and features are deduplicated by ID. A city must set at
least one of them: `overpass` defaults to false, so a city with neither is
rejected. A private or
loopback ArcGIS address is refused unless the city also sets
`allow_private_arcgis = true`, which is for self-hosted or staging endpoints
only. Fire stations always come from OpenStreetMap, and census blocks always
come from the Census Bureau's TIGERweb, whatever these settings say.

**Boundary.** The city boundary comes from a Nominatim search for the name.
When Nominatim has no polygon for the city, set `boundary_relation_id` to the
city's OSM `admin_level=8` relation, and the boundary is fetched from Overpass
instead (the ID must not be negative). Ingest's error names this fix. Find the ID with
[Overpass Turbo](https://overpass-turbo.eu/):
`relation["name"="<city>"]["boundary"="administrative"]["admin_level"="8"];out;`.
A relation whose bbox spans more than 5° is rejected as a likely county or
state.

**Census.** By default `pvmt all` also apportions 2020 Census population onto
the city's hexes: `census` defaults to true, unlike `overpass`. Set
`census = false` for a city outside the US, where TIGERweb has no blocks.

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
in a comma and a US state, DC or territory postal code (`"Livermore, CA"`)
also gets a `state:XX` tag at export, unless it sets a `state:` tag itself.
Any other code (`"Toronto, ON"`) derives no tag; set one explicitly if you want it. Malformed tags are rejected at load, for example
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
are an error. A city the including file declares itself replaces every
included definition of that city whole, tags and calibration included.

Calibration merges per field. That covers `hex_edge_m`, `min_hex_area` and
every `[forecast]` field. The first include to set a field wins it, and later
includes fill only the fields still unset. So list the more specific config
first. When two includes set the same field to different values, the load
prints a `warning:` naming the city, the field, both values and both files.

An included file's calibration is flattened into per-city values. So the
including file may not set top-level `[grid]` or `[forecast]`, because those
would silently recalibrate the included cities. The exception is `[forecast].horizon_years`, the one horizon every
included file must already agree with ([Forecast](#forecast)). Its `[display].units` and `[export]` apply as usual. An
included file's `[display].units` and `[export]` are not carried over. Its
`[[services]]` apply to its own cities: an included city's coverage is
computed and published under the `[[services]]` of the file that declares it,
and the including file's apply only to the cities it declares itself.

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
export's warning names the setting that differs.

Two configs that set the *same* `config_id` and define the same city share its
row and overwrite each other's data, so give each config its own.

## Snapshots and the config hash

Computed results are stored per city under a hash of just the resolved
settings that change them. Every compute updates the city's *latest compute*
under its current hash, which `pvmt forecast` and `pvmt status` read.
`pvmt snapshots create <name>` freezes every city's latest compute as one named
snapshot, and export and serve publish the newest (or `--snapshot NAME`).

| Changes the hash (recompute the city) | Does not |
|---|---|
| `hex_edge_m`, including `PVMT_HEX_EDGE_M` | comments, whitespace, key order |
| `[[services]].turnout_s`, an explicit `0` too | `[[services]].travel_threshold_s` |
| a facility's `service`, `lat` or `lon` | a facility's `name` |
| `census = false` | `[display]`, `min_hex_area`, `[export]`, `tags`, city names |
| a pvmt release whose compute logic changed | every `[forecast]` setting and `PVMT_FORECAST_*` |
| | data sources: `overpass`, `arcgis_url`, `allow_private_arcgis`, `boundary_relation_id` |
| | ingest settings: `[cache]`, `[overpass]`, `PVMT_OVERPASS_URL` |

Forecasts are never stored. Every reader simulates them from the stored
results under the settings in force, so a forecast edit takes effect on the
next read. Editing a data source changes what the next ingest fetches, not the
hash. What the re-ingest changes is caught when the snapshot is created: each
part records the features and boundary it was computed from, and
`pvmt snapshots create` refuses the snapshot when any city has a part computed
from data ingested again since, naming the compute to rerun (see
[troubleshooting](troubleshooting.md#snapshots-create-refuses-a-city)). After
a single `pvmt roads compute`, for example, the combined total and the service
coverage still read the old roads, so create asks for `pvmt all compute`. An
explicit default (`hex_edge_m = 100`) hashes the same as leaving it out. The exception is
`turnout_s`, whose default lives in the services registry.

A service run freezes the facilities it routed from, names included, and the
turnout it used. The map's station markers and the coverage legend therefore
show the run as computed, and a renamed or moved facility appears after the
next `pvmt services compute`. `travel_threshold_s` is read when exporting.

After a hash change the latest compute under the new hash is empty until the
city is recomputed, and `pvmt forecast` and `pvmt status` say so. The latest
compute under the old hash is kept, so reverting the setting makes it current
again without a recompute. The exception is a new pvmt release that changes
compute logic: its hashes differ from every older release's, so `pvmt gc`
drops latest computes made by an older pvmt and sweeps their runs, except the
ones a snapshot holds. A snapshot
never changes after it is created: it keeps the hash, `hex_edge_m`, boundary,
stations and turnout it was created with, and exports exactly as created after
any later ingest or config edit. Export and serve warn when a city in the
published snapshot was frozen under other settings than the current ones (see
[troubleshooting](troubleshooting.md#snapshot-was-created-under-other-settings)).
Export refuses, before writing anything, when a city has no snapshot (see
[troubleshooting](troubleshooting.md#city-has-no-snapshot)). `pvmt status` and
`pvmt snapshots show <name>` show each city's hash in a snapshot.

A snapshot is a named, immutable tag over every configured city, like a git
tag. `pvmt snapshots create 2026-11` freezes each city's latest compute under
its current hash and `hex_edge_m`, all at once or not at all: it refuses the
snapshot when any city's parts were not all computed from the features and
boundary stored now, listing every refused city (see
[troubleshooting](troubleshooting.md#snapshots-create-refuses-a-city)). A
name is 1 to 64 letters, digits, `.`, `_` or `-`, starting with a letter or
digit; `latest` is reserved. By convention a snapshot is named for the
month its data was cut (`2026-11`): that keeps data snapshots apart from
pvmt's own version tags (`v0.5.0`) and sorts them in date order. A second
cut in the same month can take a suffix (`2026-11.1`). Creating a name that
exists is refused: to
replace a snapshot, `pvmt snapshots delete 2026-11` and create it again. To
roll back, delete the newest snapshot, or publish an older one with
`pvmt export --snapshot NAME`. `pvmt snapshots diff A B` compares two
snapshots' results city by city, and `pvmt snapshots prune --keep N` keeps
the N newest.

## Grid and display

**`hex_edge_m`** sets the hex edge in meters. Set it in `[grid]` or per city.
The default is 100 and the minimum is 10. Hex count grows with the square of
the inverse edge, so large cities run coarser: the examples range from 60 m to
300 m.

**`min_hex_area`** drops boundary slivers smaller than this many square meters
(m², whatever `[display].units` shows) from the map, the stats and the play
board. Set it in `[grid]` or per city; the default is 100. It is coupled to the edge: a threshold above a full hex's area
(`3√3/2 · edge²`) drops every hex. A snapshot is drawn at the edge it was
computed at, so export checks the threshold against that edge and warns when
it would drop every hex.

**`[display].units`** is `imperial` (the default) or `metric`. It changes
presentation only.

## HTTP cache (`[cache]`)

**`ttl`** is how long a cached upstream response is served before pvmt asks
again. It is a quoted Go duration string such as `"24h"` (hours are the
largest unit, so a month is `"720h"`), at least `"1m"`; a bare number fails to
load. The default is a week. Overpass and
Nominatim send no `ETag` or `Last-Modified`, so a stale entry is downloaded in
full again; the long default keeps a retry of a failed city on the same data as
the rest of the run. Shorten it to pick up OpenStreetMap edits sooner, or pass
`--force` to an ingest command (`pvmt all ingest --force` included) to bypass
the cache for one run. `--rebuild-boundary` on the same commands re-resolves a
stored city boundary and its water strip through the cache, without bypassing
it, so a fix to the strip can be replayed: the boundary and water responses
come from the cache while they are fresh. The roads fetched with the boundary
are queried over the unstripped boundary's bbox, so for a city whose earlier
strip shrank the bbox (water stripped off an edge), the first rebuild fetches
its roads from upstream. Only the loaded file's `[cache]` applies; an
`[[include]]`d file's is ignored. It changes no stored result, so it is not part
of the config hash. Disk use is separate: see `pvmt cache prune`.

```toml
[cache]
ttl = "24h"
```

## Overpass endpoint (`[overpass]`)

**`url`** is the Overpass API instance every OpenStreetMap query goes to:
roads, parking, sidewalks, facilities, OSM water and `boundary_relation_id`
boundaries. The default is the public `https://overpass-api.de/api/interpreter`.
Point it at another public instance when that one is busy or down (see
[troubleshooting](troubleshooting.md#overpass-busy-or-down-switch-instance)).
`PVMT_OVERPASS_URL` overrides it for one run without editing the file, and it
applies with no `pvmt.toml` too.

The URL must be `https://`, name its host by DNS name rather than an IP
address, carry no credentials, query string or fragment, and end in
`/interpreter` (the API endpoint, not the instance's web page). A file value
that breaks these rules fails the load; an env value that does is ignored with
a `warning:`. Before the first Overpass request, a non-default endpoint must
also resolve to public addresses only, the guard `arcgis_url` gets, and the run
prints `overpass endpoint: <host>` on stderr. Only the loaded file's
`[overpass]` applies; an `[[include]]`d file's is ignored.

Every instance serves the same OpenStreetMap data, give or take replication
lag, so the endpoint is not part of the config hash, and its rows are still
stored under the source `overpass`. The HTTP cache keys on the full URL, so
responses are never shared between instances: a city already ingested from the
default stays cached for it, and a replay of that city should unset
`PVMT_OVERPASS_URL`.

```toml
[overpass]
url = "https://overpass.private.coffee/api/interpreter"
```

## Forecast

The forecast models each road class's condition decaying over the horizon,
priced by PCI-banded cost tiers, against the city's budget. What each setting
means is explained in the [methodology](../internal/export/docs/methodology.md).
Every setting except `horizon_years` goes in `[forecast]` or a city's
`[cities.forecast]`. `horizon_years` is top-level only (see below the table):

| Key | Default | Range | Sets |
|---|---|---|---|
| `initial_pci` | 85 | (0, 100] | the network's average starting condition ([Data sources](../internal/export/docs/methodology.md#data-sources)) |
| `decay_rate` | per class | 0–1 | the annual decay constant `k` of a typical road; every road class scales with it, and parking and sidewalks keep their own rates ([Decay model](../internal/export/docs/methodology.md#decay-model)) |
| `growth_rate` | 0 | −0.5–0.5 | linear annual growth of paved area as a fraction, not a percentage (`0.01` is 1% a year); a per-city `0` overrides the top level ([Area growth](../internal/export/docs/methodology.md#area-growth)) |
| `horizon_years` | 30 | 1–200 | the forecast horizon in years, one per config |
| `cost_tiers` | 3 tiers | PCI 0–101 | treatment cost per m² by PCI band ([Cost model](../internal/export/docs/methodology.md#cost-model)) |
| `cost_multiplier` | 1.0 | 0–5 | a factor on every tier's cost, not a percentage: 1.0 prices the tiers as written and 1.5 raises every cost by half ([Bare construction cost vs. loaded program cost](../internal/export/docs/methodology.md#bare-construction-cost-vs-loaded-program-cost)) |
| `treatment_cycle_years` | 12 | 1–40 | the share of the network treated per year, 1/N ([Treatment cycle](../internal/export/docs/methodology.md#treatment-cycle)) |
| `current_budget` | unset | ≥ 0 | the annual pavement budget in dollars; it enables `insolvency_year` and `funding_gap` ([Solvency metrics](../internal/export/docs/methodology.md#solvency-metrics-streetsroads-only)) |

A tier is one `[[forecast.cost_tiers]]` table with `min_pci`, `max_pci`,
`cost_per_sqm` and a `label`: `min_pci` must be below `max_pci`, `cost_per_sqm`
must be positive and the label must not be empty. Bands must not overlap; a
gap between them is allowed, and a PCI in the gap is priced at the nearest
band. Setting tiers replaces the
road defaults entirely, and sidewalks are priced at 0.6× the same tiers
([Cost model](../internal/export/docs/methodology.md#cost-model)).
Except for `growth_rate`, a `0` means "use the default". The browser forecast
and Play check the exported settings against these same ranges, so a config
that loads exports a site whose forecast runs. The default tiers and
cycle are calibrated together against published city figures. The evidence is
in [validation.md](validation.md#6-how-the-cost-and-cycle-are-anchored), and
it is why `cost_multiplier` defaults to 1.0.

A config has one forecast horizon, so every city in it, and every city shown
side by side on Compare and Aggregate, is forecast over the same years. A
`horizon_years` inside `[cities.forecast]` fails to load. A file pulled in with
`[[include]]` must resolve to the same `horizon_years` as the file that
includes it, where an unset one counts as the default 30.

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

`turnout_s` and `travel_threshold_s` are in seconds. `[[services]]` overrides
only the fields it sets, and an explicit `0` counts as set. The defaults and their sources are in the
[methodology](../internal/export/docs/methodology.md#service-coverage-fire-response).
A hand-placed facility is routed from exactly like a mapped one. Mapped
stations are fetched up to 5 km beyond the boundary's bounding box, with the
roads in that margin, so a station in that margin, hand-placed or mapped, is
routed in over real roads; one farther out reaches the city only if it is
within 500 m of the stored roads. It needs a `service` and a valid
`lat`/`lon`. A service name that isn't registered, a service listed twice,
or a negative `turnout_s` or `travel_threshold_s` is a config error.

## Export and hosting

**What `pvmt export` writes.** The site goes to `-o` (default `./site`),
which must be missing or empty unless `--clean` is passed. Every export lists
each file it writes in a `.pvmt-export` manifest at the site root, and
`--clean` removes exactly the files that manifest lists, then the
directories they leave empty, so anything else in the directory (a `.git`
checkout, notes, another tool's files) stays. `--clean` refuses a non-empty
directory without a manifest, a manifest entry that points outside the
directory, and an export that would overwrite a file the manifest does not
list, all before removing anything. The manifest is plain text: the line
`pvmt export manifest v1`, then `state: complete` (or `state: in-progress`
while an export runs or after one failed; its files are still listed, so
`--clean` recovers it, along with the `<name>.tmp-<digits>` temp file an
interrupted write of a listed file left beside it),
then one path per line, relative to the site root. Run one export into a
directory at a time.

**`[export].title`** names a multi-city dashboard built by `pvmt export`. It
defaults to the output directory's name. `pvmt serve` does not use it.

**`[export].coordinate_decimals`** sets the precision of emitted GeoJSON. The
default is 5, about 1 m, which is far finer than any hex, and the range is
0–15.

**`[export].boundary_simplify_m`** sets the simplification tolerance, in
meters, for the *drawn* city outline only. The default is 10. A negative value
disables simplification, and the maximum is 1000. Hex clipping and every area
figure use the full stored boundary, so at high zoom a clipped hex can overhang
the drawn outline by up to the tolerance. A simplified ring is not guaranteed
to be a valid polygon, which matters only to a consumer that fills
`boundary.geojson` rather than drawing it.

`pvmt serve` pins each city's newest snapshot, or the one `--snapshot NAME`
names (its latest compute, for a city with none), when it starts and sends `Cache-Control: public, max-age=300` on
every response, so restart it to pick up a new snapshot. Its dashboard's
snapshot picker also lists each older snapshot and *Latest compute (not
snapshotted)*, which shows what the next `pvmt snapshots create <name>` would
publish, with a badge saying so. `pvmt export` writes plain files, so caching is whatever the
host applies. After re-publishing, purge the
CDN if the new build has to show immediately.
