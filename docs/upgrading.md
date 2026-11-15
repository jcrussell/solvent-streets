# Upgrading

This page lists the steps a release needs when you upgrade to it from the release before it. A release with no section here needs no steps. If you skip releases, apply every section in between, oldest first.

The release notes link here from their header. Their "Breaking Changes" list names each breaking commit; this page says what to do about it.

## v0.5.0

Upgrading from v0.4.x takes ten steps. Plan for the first one: it re-fetches every city from the upstream sources.

### 1. Move the database aside and re-ingest

pvmt v0.5.0 refuses a database created by v0.4.1 or earlier with `incompatible database` and leaves the file untouched. There is no in-place migration. Move it aside with its `-wal`/`-shm` files and rebuild it, as [`#incompatible-database`](troubleshooting.md#incompatible-database) describes:

```
pvmt all ingest
pvmt all compute
```

The new database fills from Overpass, ArcGIS and TIGERweb again, so a large config takes as long as its first ingest did (hours; about a day for the 277-city showcase). Responses still in the HTTP cache (one-week TTL by default, `[cache] ttl`) are reused.

Snapshot config hashes are also computed differently: per city, over only the settings that affect results (see [Snapshots and the config hash](configuration.md#snapshots-and-the-config-hash)). A `config_hash` a script recorded from v0.4.x never matches a v0.5.0 snapshot; the recompute above is all that is needed.

### 2. Create a snapshot before exporting

Computes no longer publish. `pvmt all compute` and every `pvmt <resource> compute` update the city's *latest compute*, and `pvmt export` and `pvmt serve` publish each city's newest snapshot, which only `pvmt snapshots create` makes. After the compute, run:

```
pvmt snapshots create
```

`pvmt export` refuses, before writing anything, when any city has no snapshot, and lists each with the command that makes one. A script that ran `compute` then `export` needs `snapshots create` between them. `create` refuses a city whose parts were computed from features or a boundary ingested again since, and names the compute to rerun (see [troubleshooting](troubleshooting.md#snapshots-create-refuses-a-city)). A snapshot never changes after it is created: export draws it as created, whatever was ingested or edited since. Roll a city back with `pvmt --city <slug> snapshots delete <id>` of its newest snapshot. `pvmt snapshots ls` and `rm` are now `list` and `delete`, with the old names kept as aliases.

### 3. Re-export sites

A site exported by v0.4.x cannot be refreshed in place. Its data files use the old formats, and the v0.5.0 dashboard refuses them (`unsupported hexgrid format (expected v6) — re-export this site`). After `pvmt snapshots create`, run `pvmt export` again (and `pvmt check-site` on the result) and redeploy the whole tree.

### 4. Update links to the dashboard

| v0.4.x URL | v0.5.0 URL |
|------------|------------|
| `?tag=<value>` | `?tag=group:<value>`. A tag without its dimension prefix is ignored |
| `?scope=bbox` | `?scope=all` |

### 5. Update anything that reads pvmt's JSON

Every JSON document pvmt emits (`--json` output, the `pvmt serve` API, the exported site's data files and `window.PVMT_CONFIG`) now uses one vocabulary, described in [JSON conventions](architecture.md#json-conventions). A script that reads an old name gets nothing back rather than an error, so check each one against the tables below.

`--jq` and `--template` see the same keys `--json` prints. `pvmt export --json` and `pvmt validate --json` stay bare flags (each prints one document) and now accept `--jq` and `--template` too.

Only names that existed in v0.4.x are listed. Fields of the commands and files that are new in v0.5.0 (`pvmt census`, `pvmt services`, `hex-metrics.json`, and the multi-city site's `compare.json` rollup) have no older name.

#### CLI `--json`

| Command | v0.4.x | v0.5.0 |
|---------|--------|--------|
| `status` | `resourceType`, `featureCount`, `lastIngest`, `lastCompute` | `resource_type`, `feature_count`, `last_ingest_at`, `last_compute_at`; new `state` (`current`, `stale`, `not computed`) and `stale_inputs` |
| `cities` | `features`, `totalArea`, `lastIngest`, `lastCompute` | `feature_counts`, `total_paved`, `last_ingest_at`, `last_compute_at` |
| `snapshots list` (`ls`) | `computedAt`, `configHash` | `created_at`, `config_hash`; new `hex_edge_m` and `parts` (`part`, `run_id`, `computed_at`) |
| `<resource> compute` | `resourceType`, `featureCount`, `snapshotId` | `resource_type`, `feature_count`, `run_id` (the compute run, not a snapshot) |
| `<resource> compute` | `city`: display name (`Oakland, CA`) | `city`: slug (`oakland-ca`) |
| `forecast` | `resourceType`, `treatmentCost`, `treatmentTier` | `resource_type`, `annual_need`, `cost_tier`; new `city` |
| `export` | `total` | `file_count` |
| `validate` | `break_even`; `passed`, `failed`, `context` | `break_even_budget`; `pass_count`, `fail_count`, `context_count` |

#### `pvmt serve` API

| Endpoint | v0.4.x | v0.5.0 |
|----------|--------|--------|
| `/api/snapshots`, `/api/cities/{slug}/snapshots` | `computed_at` | `created_at`; new `hex_edge_m` and `parts` (`part`, `run_id`, `computed_at`) |
| `/api/cities` | as `cities.json` below | as `cities.json` below |

`/data/{file}` serves the site data files below. `?snapshot=N` picks a snapshot, and `?snapshot=latest` the city's latest compute as the server read it at startup.

#### Site data files and `window.PVMT_CONFIG`

| File | v0.4.x | v0.5.0 |
|------|--------|--------|
| `cities.json`, `meta.json` | `center_lon`, `center_lat` | `center`: `[lon, lat]` |
| `cities.json`, `meta.json` | `bbox`: `[south, west, north, east]` | `bbox`: `[west, south, east, north]` |
| `meta.json` | `pct_paved` (0..100); `stats[].type`; `stats[].feature_count` | `paved_share` (0..1); `stats[].resource_type`; no feature count |
| `meta.json` | — | `missing_resources`: the resources with no ingested data (omitted when none) |
| `meta.json` | — | `ingest_failures`: resource → the configured sources whose last ingest failed and hold none of its rows (omitted when none) |
| `meta.json` | `snapshot_date`: the render date | `snapshot_date`: the date of the data (the snapshot's `created_at`, or the latest compute's newest run); new `snapshot_id`, and from `pvmt serve` `latest_compute: true` for a latest compute (each omitted when not applicable) |
| `hexgrid.geojson` | format `v` 2; per-hex scope key `bbox`; coverage `pct` (0..100) | format `v` 6; scope key `all`; coverage `share` (0..1); city road hexes carry `k`, the Play board's decay rate |
| `play-hexes.json` | the Play board: `id`, `road_area`, `k` per hex | gone: the board reads `hexgrid.geojson`. Its road area is now the map's city-scope roads area, which counts pavement where road classes overlap once rather than once per class |
| `scenarios.json` | scope key `bbox`; summary `city_count`, `all_count`, `city_pct`, `state_count`, `county_count`, `federal_count` | scope key `all`; the `summary` block is gone (its feature counts and city share were never shown) |
| `hex-cost-summary.json` | scope key `bbox`; `year1_cost` | scope key `all`; `year1_need` |
| `forecast.json` | `bbox_baseline`; `insolvency_year`, `funding_gap` as `null` | `all_baseline`; `insolvency_year`, `funding_gap` omitted |
| `forecast_seed.json` | `years`; material `binder_pct` | `horizon_years`; material `binder_share` |
| `PVMT_CONFIG` (dashboard) | `layerColors`, `unitSystem`, `forecastSeed`, `wasmPrefix`; `cities[].lon`, `cities[].lat` | `layer_colors`, `unit_system`, `forecast_seed`, `hexgrid_version` (`wasmPrefix` is gone); `cities[].center` |
| `PVMT_CONFIG` (play page) | `forecastSeed`, `dataPrefix`, `mapHref`, `bandCount`, `wasmPrefix` | `forecast_seed`, `data_prefix`, `map_href`, `band_count`, `hexgrid_version` (`wasmPrefix` is gone) |
| play page game state | `backlog`, `spent`, `treatments`; `insolvency_year` as `null` | `deferred_backlog`, `total_spend`, `treatment_count`; `insolvency_year` omitted |

### 6. Check where `pvmt export` writes

`pvmt export` without `-o` writes to `./site`, the directory `pvmt check-site` checks by default, rather than `./dist`. A deploy script that picks the site up from `dist/` should pass `-o dist` or read `site/`.

### 7. Check tags that contain a colon

A tag is now read as `dimension:value` when it contains a colon, and the config refuses one whose dimension is not an identifier or whose value is empty ([Tags](configuration.md#tags)). A v0.4.x tag such as `"Bay Area: East"` fails to load with exit 2; rename it, e.g. to `"metro:Bay Area East"`.

### 8. Set the forecast horizon once per config

A config now has one forecast horizon, and its default is 30 years (up from 20). A `years` inside `[cities.forecast]` fails to load with exit 2: move it to the top-level `[forecast]`. A file pulled in with `[[include]]` must resolve to the same `years` as the file that includes it, where an unset `years` counts as the default 30. A config that left `years` unset now forecasts 30 years, so its headline totals (`30-Year Spend`) and insolvency years cover a longer run. Set `years = 20` at the top level to keep the old horizon. The horizon is also capped at 200 years: a `years` above 200 fails to load with exit 2, and a `PVMT_FORECAST_YEARS` above 200 is ignored with a warning. The browser forecast never ran a longer one, so such a config used to export a site whose Financials page failed. Forecasts are not part of the snapshot hash, so this needs a re-export, not a recompute.

### 9. Expect a partial ingest to exit 1

`pvmt all ingest`, and each `pvmt <resource> ingest`, now exit 1 when any of a city's sources or steps failed, including the fire and Census steps of `all ingest`. They used to warn and exit 0. What did answer is still saved, and `all ingest` still runs every remaining step and city. The final error lists each failure with the command that retries it. A script that ran `pvmt all ingest || true` before `compute` to ride out a flaky upstream now sees those failures. Retry the listed commands, or keep `|| true` if a partial city is acceptable.

### 10. Remove overlapping cost tiers

A config whose `[[forecast.cost_tiers]]` bands overlap now fails to load with exit 2, naming the two bands. A PCI inside both had two prices, and the dashboard already refused to run its forecast controls on such a schedule. Move one band's `min_pci` or `max_pci` so the bands meet or leave a gap ([Forecast](configuration.md#forecast)).
