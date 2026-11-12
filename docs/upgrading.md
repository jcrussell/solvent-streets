# Upgrading

This page lists the steps a release needs when you upgrade to it from the release before it. A release with no section here needs no steps. If you skip releases, apply every section in between, oldest first.

The release notes link here from their header. Their "Breaking Changes" list names each breaking commit; this page says what to do about it.

## v0.5.0

Upgrading from v0.4.x takes seven steps. Plan for the first one: it re-fetches every city from the upstream sources.

### 1. Move the database aside and re-ingest

pvmt v0.5.0 refuses a database created by v0.4.1 or earlier with `incompatible database` and leaves the file untouched. The same goes for a database built by a pre-release v0.5.0 binary (schema generation 3 or earlier): only the result tables changed, but there is no in-place migration, so it is re-ingested like any other. Move it aside with its `-wal`/`-shm` files and rebuild it, as [`#incompatible-database`](troubleshooting.md#incompatible-database) describes:

```
pvmt all ingest
pvmt all compute
```

The new database fills from Overpass, ArcGIS and TIGERweb again, so a large config takes as long as its first ingest did (hours; about a day for the 277-city showcase). Responses still in the HTTP cache (24-hour TTL) are reused.

Snapshot config hashes are also computed differently: per city, over only the settings that affect results (see [Snapshots and the config hash](configuration.md#snapshots-and-the-config-hash)). A `config_hash` a script recorded from v0.4.x never matches a v0.5.0 snapshot; the recompute above is all that is needed.

### 2. Re-export sites

A site exported by v0.4.x cannot be refreshed in place. Its data files use the old formats, and the v0.5.0 dashboard refuses them (`unsupported hexgrid format (expected v5) — re-export this site`). After the compute, run `pvmt export` again (and `pvmt check-site` on the result) and redeploy the whole tree.

### 3. Update links to the dashboard

| v0.4.x URL | v0.5.0 URL |
|------------|------------|
| `?tag=<value>` | `?tag=group:<value>`. A tag without its dimension prefix is ignored |
| `?scope=bbox` | `?scope=all` |

### 4. Update anything that reads pvmt's JSON

Every JSON document pvmt emits (`--json` output, the `pvmt serve` API, the exported site's data files and `window.PVMT_CONFIG`) now uses one vocabulary, described in [JSON conventions](architecture.md#json-conventions). A script that reads an old name gets nothing back rather than an error, so check each one against the tables below.

`--jq` and `--template` see the same keys `--json` prints. `pvmt export --json` and `pvmt validate --json` stay bare flags (each prints one document) and now accept `--jq` and `--template` too.

Only names that existed in v0.4.x are listed. Fields of the commands and files that are new in v0.5.0 (`pvmt census`, `pvmt services`, `hex-metrics.json`, and the multi-city site's `compare.json` rollup) have no older name.

#### CLI `--json`

| Command | v0.4.x | v0.5.0 |
|---------|--------|--------|
| `status` | `resourceType`, `featureCount`, `lastIngest`, `lastCompute` | `resource_type`, `feature_count`, `last_ingest_at`, `last_compute_at` |
| `cities` | `features`, `totalArea`, `lastIngest`, `lastCompute` | `feature_counts`, `total_paved`, `last_ingest_at`, `last_compute_at` |
| `snapshots ls` | `computedAt`, `configHash` | `created_at`, `config_hash`; new `parts` (`part`, `run_id`, `computed_at`) |
| `<resource> compute` | `resourceType`, `featureCount`, `snapshotId` | `resource_type`, `feature_count`, `run_id` (the compute run, not a snapshot) |
| `<resource> compute` | `city`: display name (`Oakland, CA`) | `city`: slug (`oakland-ca`) |
| `forecast` | `resourceType`, `treatmentCost`, `treatmentTier` | `resource_type`, `annual_need`, `cost_tier`; new `city` |
| `export` | `total` | `file_count` |
| `validate` | `break_even`; `passed`, `failed`, `context` | `break_even_budget`; `pass_count`, `fail_count`, `context_count` |

#### `pvmt serve` API

| Endpoint | v0.4.x | v0.5.0 |
|----------|--------|--------|
| `/api/snapshots`, `/api/cities/{slug}/snapshots` | `computed_at` | `created_at`; new `parts` (`part`, `run_id`, `computed_at`) |
| `/api/cities` | as `cities.json` below | as `cities.json` below |

`/data/{file}` serves the site data files below.

#### Site data files and `window.PVMT_CONFIG`

| File | v0.4.x | v0.5.0 |
|------|--------|--------|
| `cities.json`, `meta.json` | `center_lon`, `center_lat` | `center`: `[lon, lat]` |
| `cities.json`, `meta.json` | `bbox`: `[south, west, north, east]` | `bbox`: `[west, south, east, north]` |
| `meta.json` | `pct_paved` (0..100); `stats[].type`; `stats[].feature_count` | `paved_share` (0..1); `stats[].resource_type`; no feature count |
| `hexgrid.geojson` | format `v` 2; per-hex scope key `bbox`; coverage `pct` (0..100) | format `v` 5; scope key `all`; coverage `share` (0..1) |
| `scenarios.json` | scope key `bbox`; summary `city_count`, `all_count`, `city_pct`, `state_count`, `county_count`, `federal_count` | scope key `all`; the `summary` block is gone (its feature counts and city share were never shown) |
| `hex-cost-summary.json` | scope key `bbox`; `year1_cost` | scope key `all`; `year1_need` |
| `forecast.json` | `bbox_baseline`; `insolvency_year`, `funding_gap` as `null` | `all_baseline`; `insolvency_year`, `funding_gap` omitted |
| `forecast_seed.json` | `years`; material `binder_pct` | `horizon_years`; material `binder_share` |
| `PVMT_CONFIG` (dashboard) | `layerColors`, `unitSystem`, `forecastSeed`, `wasmPrefix`; `cities[].lon`, `cities[].lat` | `layer_colors`, `unit_system`, `forecast_seed` (`wasmPrefix` is gone); `cities[].center` |
| `PVMT_CONFIG` (play page) | `forecastSeed`, `dataPrefix`, `mapHref`, `bandCount`, `wasmPrefix` | `forecast_seed`, `data_prefix`, `map_href`, `band_count` (`wasmPrefix` is gone) |
| play page game state | `backlog`, `spent`, `treatments`; `insolvency_year` as `null` | `deferred_backlog`, `total_spend`, `treatment_count`; `insolvency_year` omitted |

### 5. Check where `pvmt export` writes

`pvmt export` without `-o` writes to `./site`, the directory `pvmt check-site` checks by default, rather than `./dist`. A deploy script that picks the site up from `dist/` should pass `-o dist` or read `site/`.

### 6. Check tags that contain a colon

A tag is now read as `dimension:value` when it contains a colon, and the config refuses one whose dimension is not an identifier or whose value is empty ([Tags](configuration.md#tags)). A v0.4.x tag such as `"Bay Area: East"` fails to load with exit 2; rename it, e.g. to `"metro:Bay Area East"`.

### 7. Set the forecast horizon once per config

A config now has one forecast horizon, and its default is 30 years (up from 20). A `years` inside `[cities.forecast]` fails to load with exit 2: move it to the top-level `[forecast]`. A file pulled in with `[[include]]` must state the same `years` as the file that includes it, or both must leave it unset. A config that left `years` unset now forecasts 30 years, so its headline totals (`30-Year Spend`) and insolvency years cover a longer run. Set `years = 20` at the top level to keep the old horizon. Forecasts are not part of the snapshot hash, so this needs a re-export, not a recompute.
