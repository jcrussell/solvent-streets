# Upgrading

This page lists the steps a release needs when you upgrade to it from the release before it. A release with no section here needs no steps. If you skip releases, apply every section in between, oldest first.

The release notes link here from their header. Their "Breaking Changes" list names each breaking commit; this page says what to do about it.

## v0.5.0

Upgrading from v0.4.x takes five steps. Do them in order. v0.5.0 checks the config when it loads, so a config it refuses fails in seconds, before the ingest of step 2, which re-fetches every city and can take hours.

### 1. Fix the config

Run `pvmt config show` in each config's directory. A config v0.5.0 refuses fails with exit 2, and the message names the field and the fix. These are the v0.4.x configs it refuses:

| In a v0.4.x config | In v0.5.0 |
|--------|--------|
| `[forecast] years` | `[forecast] horizon_years` |
| `[forecast] cost_overhead` (and per city) | `[forecast] cost_multiplier`, the same factor, named as the dashboard's Cost multiplier slider |
| `[display] min_hex_area` | `[grid] min_hex_area`, beside the `hex_edge_m` it is sized for. The per-city `min_hex_area` is unchanged |
| `years` in `[cities.forecast]` | refused: a config has one horizon, so set `horizon_years` in the top-level `[forecast]`, and every `[[include]]`d file must resolve to the same one ([Forecast](configuration.md#forecast)) |
| a horizon above 200 years | refused. The browser forecast never ran one, so such a config exported a site whose Financials page failed |
| a tag with a colon that is not `dimension:value` (`"Bay Area: East"`) | refused: a colon now separates a dimension from its value ([Tags](configuration.md#tags)). Rename it, e.g. to `"metro:Bay Area East"` |
| `[[forecast.cost_tiers]]` bands that overlap | refused, naming the two bands. Move one band's `min_pci` or `max_pci` so they meet or leave a gap |
| an `arcgis_url` that is not `https://` | refused. Use the layer's `https://` URL; the error's hint covers a self-hosted `http://` endpoint ([Cities](configuration.md#cities)) |

A renamed key fails with a message such as `forecast.years was renamed to forecast.horizon_years`. The environment variable `PVMT_FORECAST_YEARS` is now `PVMT_FORECAST_HORIZON_YEARS`, and the old name is ignored with a `warning:` naming the new one. `[cache]`, which sets how long the HTTP cache serves a response, is new in v0.5.0 and has its own rules ([HTTP cache](configuration.md#http-cache-cache)). None of these changes alters a stored result.

One change loads without an error: the default horizon is 30 years, up from 20. A config that left the horizon unset now forecasts 30 years, so its headline totals (`30-Year Spend`) and insolvency years cover a longer run. Set `horizon_years = 20` in `[forecast]` to keep the old horizon. Forecasts are simulated on read, so this needs a re-export (step 4), not a recompute.

### 2. Rebuild the database

v0.5.0 refuses a database created by v0.4.1 or earlier with `incompatible database`, and leaves the file untouched. There is no in-place migration. Move the file ([where it lives](configuration.md#files-and-directories)) aside as [`#incompatible-database`](troubleshooting.md#incompatible-database) describes, then rebuild and publish it:

```
pvmt all ingest
pvmt all compute
pvmt snapshots create 2026-11
pvmt cache prune --max-age=0 --max-size=0
```

The ingest fills the new database from Overpass, ArcGIS and TIGERweb again, so a large config takes as long as its first ingest did (hours; about a day for the showcase). The v0.4.x HTTP cache is not reused: v0.5.0 stores responses gzip-compressed and never reads the old uncompressed bodies. With both caps off, `cache prune` removes only those and other unservable leftovers, and keeps the responses the ingest just fetched; its default caps would evict a large config's.

Computes no longer publish. A compute updates each city's *latest compute*, and export and serve publish the newest *snapshot*, which only `pvmt snapshots create <name>` makes ([Snapshots and the config hash](configuration.md#snapshots-and-the-config-hash)). The config hash is computed differently too, so a `config_hash` a script recorded from v0.4.x never matches a v0.5.0 snapshot.

### 3. Update scripts that drive the CLI

- **Snapshot before you publish.** A script that ran `compute` then `export` needs `pvmt snapshots create <name>` between them: `pvmt export` refuses, before writing anything, when a city has no snapshot. `pvmt snapshots ls` and `rm` are now `list` and `delete` (the old names are aliases), and they and every other snapshot command take a snapshot's name rather than an id. `--snapshot <name>` on `export` and `serve` publishes an older snapshot.
- **`pvmt export` writes to `./site`**, not `./dist`, the directory `pvmt check-site` checks by default. A deploy script that reads `dist/` should pass `-o dist` or read `site/`. `--clean` no longer deletes the output directory: it removes only the files the previous export listed in its `.pvmt-export` manifest. A site exported by v0.4.x has no manifest, so `--clean` refuses it: export into a new directory, or delete the old tree yourself once. `-o` also refuses some directories outright; `pvmt export --help` lists them.
- **A partial ingest exits 1.** `pvmt all ingest` and each `pvmt <resource> ingest` exit 1 when any of a city's sources or steps failed. They used to warn and exit 0. What answered is still saved, and the error lists each failure with the command that retries it. A script that ran `pvmt all ingest || true` to ride out a flaky upstream now sees those failures: retry the listed commands, or keep `|| true` if a partial city is acceptable.
- **Exit codes 2 and 3.** A `--city`, `--source` or snapshot name that matches nothing is a usage error and exits 2. A command that needs an earlier step first, or has nothing to do, exits 3 and says what to run. Some of these exited 1, and `pvmt cities` on an empty database, which exited 0, now exits 3 and names `pvmt all ingest` ([Exit codes](troubleshooting.md#exit-codes)).
- **Results on stdout, prose on stderr.** `pvmt gc`, `pvmt cache prune`, `pvmt snapshots delete` and `pvmt snapshots prune` print their report on stdout instead of stderr, and take `--json` for it, as `pvmt check-site` does; `snapshots delete` and `snapshots prune` also take `--dry-run`, and `snapshots prune --keep N` keeps the N newest snapshots overall rather than per city (`--city` is not supported). `gc` heads each city's report with its slug (`=== oakland-ca ===`), even for one city. A compute heads each result block on stdout with the city's slug (`oakland-ca: roads Results (all):`), and the per-city `=== <city> ===` header goes to stderr. `pvmt snapshots create` prints each refused city's row and the summary on stdout, and its hints and warnings on stderr.
- **`pvmt status` reports every city**, not only the first, unless `--city` names one. Each row starts with the city's slug, in the table and as the `city` field of `--json`, which is one document for all cities. The sections after the table go to stdout with it, each naming the city (`=== oakland-ca: City Summary ===`), and the stale-settings note is a `hint:` on stderr.
- **`--city-only` is `--maintained-only`** on `pvmt <resource> compute`. The old spelling still works for now and prints a deprecation notice.

### 4. Re-export and redeploy

A site exported by v0.4.x cannot be refreshed in place. Its data files use the old formats, and the v0.5.0 dashboard refuses them (`unsupported hexgrid format (expected v6) — re-export this site with a current pvmt`). Once the snapshot of step 2 exists, run `pvmt export` into a fresh directory, check it with `pvmt check-site`, and redeploy the whole tree.

Update links to the dashboard:

| v0.4.x URL | v0.5.0 URL |
|------------|------------|
| `?tag=<value>` | `?tag=group:<value>`. A tag without its dimension prefix is ignored |
| `?scope=bbox` | `?scope=all` |
| `?snapshot=<id>` (`pvmt serve`) | `?snapshot=<name>`, or `?snapshot=latest` for the latest compute |

### 5. Update anything that reads pvmt's JSON

Every JSON document pvmt emits now uses one vocabulary, described in [JSON conventions](architecture.md#json-conventions). A script that reads an old name gets nothing back rather than an error, so check each one against the tables below. They list only names that existed in v0.4.x: the commands and files new in v0.5.0 (`pvmt census`, `pvmt services`, `hex-metrics.json` and the multi-city `compare.json`) have no older name.

`--jq` and `--template` see the same keys `--json` prints. `pvmt export --json` and `pvmt validate --json` stay bare flags (each prints one document) and now accept `--jq` and `--template` too.

#### CLI `--json`

| Command | v0.4.x | v0.5.0 |
|---------|--------|--------|
| `status` | `resourceType`, `featureCount`, `lastIngest`, `lastCompute` | `resource_type`, `feature_count`, `last_ingest_at`, `last_compute_at`; new `state` (`current`, `stale`, `not_computed`) and `stale_inputs` |
| `cities` | `features`, `totalArea`, `lastIngest`, `lastCompute` | `feature_counts`, `total_paved` (omitted for a city with nothing computed), `last_ingest_at`, `last_compute_at` |
| `snapshots list` (`ls`) | one row per city's snapshot: `city`, `id`, `computedAt`, `configHash` | one row per named snapshot: `name`, `created_at`, `city_count`, `compute_epoch`; each city's `config_hash`, `hex_edge_m` and `parts` (`part`, `run_id`, `computed_at`) are in `snapshots show <name>` |
| `<resource> compute` | `resourceType`, `jurisdiction`, `featureCount`, `snapshotId` | `resource_type`, `scope`, `feature_count`, `run_id` (the compute run, not a snapshot) |
| `<resource> compute` | `city`: display name (`Oakland, CA`) | `city`: slug (`oakland-ca`) |
| `forecast` | `resourceType`, `treatmentCost`, `treatmentTier` | `resource_type`, `annual_need`, `cost_tier`; new `city` |
| `export` | `total` | `file_count` |
| `validate` | `break_even`; `hold_steady_budget`; `passed`, `failed`, `context` | `break_even_budget`; `published_break_even_budget`; `pass_count`, `fail_count`, `context_count` |
| `config show` | `key` and the file `source.detail`: `units`, `display.min_hex_area`, `forecast.years`, `forecast.cost_overhead`, `cities[<slug>].forecast.cost_overhead` | `display.units`, `grid.min_hex_area`, `forecast.horizon_years`, `forecast.cost_multiplier`, `cities[<slug>].forecast.cost_multiplier` (the config keys of step 1); `--sources` prints the same keys |

#### `pvmt serve` API

| Endpoint | v0.4.x | v0.5.0 |
|----------|--------|--------|
| `/api/snapshots`, `/api/cities/{slug}/snapshots` | `id`, `computed_at` | `created_at`; no `id` (a snapshot is named by `name`); new `name`, `compute_epoch`, `hex_edge_m`, `parts` (`part`, `run_id`, `computed_at`) and `served` (the snapshot the server pinned) |
| every `/api/` endpoint | errors as plain text | errors as `{"error": "<message>"}` with `Content-Type: application/json` |
| `/api/cities` | as `cities.json` below | as `cities.json` below |
| `/data/{file}?snapshot=` | a snapshot id; absent, the city's newest snapshot | a snapshot name, or `latest`; absent, the snapshot the server pinned |

`/data/{file}` serves the site data files below. `?snapshot=<name>` picks a snapshot, and `?snapshot=latest` the city's latest compute as the server read it at startup.

#### Site data files and `window.PVMT_CONFIG`

Only a script that reads an exported site's files, or a page that embeds one, needs this table.

<details>
<summary>The site's data files, page config and Play payloads</summary>

| File | v0.4.x | v0.5.0 |
|------|--------|--------|
| `cities.json`, `meta.json` | `center_lon`, `center_lat` | `center`: `[lon, lat]` |
| `cities.json`, `meta.json` | `bbox`: `[south, west, north, east]` | `bbox`: `[west, south, east, north]` |
| `meta.json` | `project_name`; `pct_paved` (0..100); `stats[].type`; `stats[].total_area`; `stats[].feature_count` | `name`, and new `city` (the slug); `paved_share` (0..1); `stats[].resource_type`; `stats[].area`; no feature count |
| `meta.json` | `snapshot_date`: the render date | `snapshot_date`: the date of the data (the snapshot's `created_at`, or the latest compute's newest run); new `snapshot` (its name), and from `pvmt serve` `latest_compute: true` for a latest compute (each omitted when not applicable) |
| `hexgrid.geojson` | format `v` 2; per-hex scope key `bbox`; coverage `pct` (0..100) | format `v` 6; scope key `all`; coverage `share` (0..1); city road hexes carry `k`, the Play board's decay rate |
| `play-hexes.json` | the Play board: `id`, `road_area`, `k` per hex | gone: the board reads `hexgrid.geojson`. Its road area is now the map's city-scope roads area, which counts pavement where road classes overlap once rather than once per class |
| `scenarios.json` | scope key `bbox`; summary `city_count`, `all_count`, `city_pct`, `state_count`, `county_count`, `federal_count` | scope key `all`; the `summary` block is gone (its feature counts and city share were never shown) |
| `hex-cost-summary.json` | scope key `bbox`; `year1_cost`; `total_area` | scope key `all`; `year1_need`; `area` |
| `forecast.json` | `baseline`: the city-maintained run when the city has one, else every jurisdiction's; `bbox_baseline`: every jurisdiction's, beside a city-maintained `baseline`; `insolvency_year`, `funding_gap` as `null` | `baseline`: always every jurisdiction's; `city_baseline`: the city-maintained run (omitted when the city has none), which `scenarios` and the solvency figures are run on when present; `insolvency_year`, `funding_gap` omitted |
| `forecast.json`, `scenarios.json` | `final_cohorts[].total_deficit` | `final_cohorts[].deferred_backlog` |
| `forecast.json`, `scenarios.json` | `scenario.strategy`: an integer (0 do-nothing, 1 worst-first, 2 preventive-first); `scenario.annual_budget`: `0` on the baseline and full-funding scenarios, unrounded | `scenario.strategy`: a name, `do_nothing`, `worst_first` or `preventive_first`; `scenario.annual_budget`: whole dollars, omitted on the baseline and full-funding scenarios |
| `forecast_seed.json` | `years`; material `binder_pct`; `cost_overhead` | `horizon_years`; material `binder_share`; `cost_multiplier` |
| `forecast_seed.json` | `total_area`, `city_paved`: the paved footprint (the union, as `meta.json`'s `total_paved`) | `maintained_area`, `city_maintained_area`: the maintained area, roads + parking + sidewalks summed as the forecast prices them. The footprint is only in `meta.json` |
| `PVMT_CONFIG` (dashboard) | `layerColors`, `unitSystem`, `forecastSeed`, `wasmPrefix`; `cities[].lon`, `cities[].lat` | `layer_colors`, `unit_system`, `forecast_seed`, `hexgrid_version`, `hex_metrics_version`, `compare_version` (`wasmPrefix` is gone); `cities[].center` |
| `PVMT_CONFIG` (play page) | `forecastSeed`, `dataPrefix`, `mapHref`, `bandCount`, `wasmPrefix` | `data_prefix`, `map_href`, `band_count`, `hexgrid_version`; no seed (the board fetches the city's `forecast_seed.json`) and no `wasmPrefix` |
| play page game state | `backlog`, `spent`, `treatments`, `budget_rate`, `closed_count`; `insolvency_year` (`null` when solvent); `changed[].pci`; the `gameTreat` and `gameSetBudget` WASM exports | `deferred_backlog`, `total_spend`, `treatment_count`, `annual_budget`, `closed_share` (the closed share of road area, which the closures loss reads); no `insolvency_year` (the start screen projects it), no `changed[].pci`, no `gameTreat` or `gameSetBudget` |
| play page `gameInit` config | `starting_budget`; `hexes[]`: `road_area`, `k` | `annual_budget`; `hexes[]`: `roads_area`, `decay_rate` |

</details>
