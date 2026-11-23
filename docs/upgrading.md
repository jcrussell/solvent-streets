# Upgrading

Each section lists the steps a release needs when you upgrade from the one
before it; a release with no section needs none. Skipping releases, apply
every section in between, oldest first. The release notes' Breaking Changes
list names each breaking commit; this page says what to do about it.

## v0.5.0

Do these steps in order. Fixing the config first fails in seconds, before
the rebuild of step 2, which takes hours.

### 1. Fix the config

Run `pvmt config show` in each config's directory. A refused config exits 2
with a message naming the field and the fix:

| v0.4.x | v0.5.0 |
|--------|--------|
| `[forecast] years` | `[forecast] horizon_years` |
| `[forecast] cost_overhead` (and per city) | `cost_multiplier` |
| `[display] min_hex_area` | `[grid] min_hex_area` (the per-city key is unchanged) |
| `years` in `[cities.forecast]` | refused: set one `horizon_years` in the top-level `[forecast]` ([Forecast](configuration.md#forecast)) |
| a horizon over 200 years | refused |
| a tag with a colon that is not `dimension:value` | refused: rename it, e.g. `"metro:Bay Area East"` ([Tags](configuration.md#tags)) |
| overlapping `[[forecast.cost_tiers]]` | refused, naming the two bands |
| an `arcgis_url` that is not `https://` | refused ([Cities](configuration.md#cities)) |
| `PVMT_FORECAST_YEARS` | `PVMT_FORECAST_HORIZON_YEARS` (the old name is ignored with a warning) |

The default horizon is now 30 years, up from 20. Set `horizon_years = 20` to
keep the old one.

### 2. Rebuild the database

v0.5.0 refuses a v0.4.x database (`incompatible database`) and leaves it
untouched; there is no in-place migration. Move it aside
([`#incompatible-database`](troubleshooting.md#incompatible-database)), then:

```
pvmt all ingest
pvmt all compute
pvmt snapshots create 2026-11
pvmt cache prune --max-age=0 --max-size=0
```

The ingest re-fetches every city, as long as its first ingest took. The
v0.4.x HTTP cache is not reused (responses are now gzip-compressed); the
`cache prune` above removes the old entries and keeps the new ones.

Computes no longer publish: export and serve publish each city's newest
*snapshot*, which only `pvmt snapshots create <name>` makes
([Snapshots](configuration.md#snapshots-and-the-config-hash)). A
`config_hash` recorded from v0.4.x never matches a v0.5.0 snapshot.

### 3. Update scripts that drive the CLI

- **Snapshot before you export.** `pvmt export` refuses a city with no
  snapshot. Snapshot commands take a name, not an id, and `snapshots ls` and
  `rm` are now `list` and `delete` (the old names are aliases).
  `snapshots prune --keep N` keeps the N newest overall, not per city.
- **`pvmt export` writes `./site`**, not `./dist`. `--clean` removes only the
  files listed in the previous export's `.pvmt-export` manifest, so it
  refuses a v0.4.x site: export into a new directory.
- **Exit codes.** A partial ingest exits 1 (it used to warn and exit 0); the
  error lists a retry command for each failure. A usage error, including a
  stray positional argument or a `--city`, `--source` or snapshot name that
  matches nothing, exits 2. A command that needs an earlier step first exits
  3 and names it: `pvmt cities` on an empty database, and `pvmt serve` when
  no city has data ([Exit codes](troubleshooting.md#exit-codes)).
- **Reports on stdout.** `gc`, `cache prune`, `snapshots delete` and
  `snapshots prune` print their report on stdout and take `--json`.
- **`pvmt status` reports every city**, each row starting with its slug.
- **`--city-only` is `--maintained-only`** on `pvmt <resource> compute`; the
  old name still works, with a deprecation notice.
- **Log records** use the key `resource`, not `resource_type`.

### 4. Re-export and redeploy

The v0.5.0 dashboard refuses v0.4.x data files. Export into a fresh
directory, run `pvmt check-site`, and redeploy the whole tree. Update links
to the dashboard:

| v0.4.x URL | v0.5.0 URL |
|------------|------------|
| `?tag=<value>` | `?tag=group:<value>` (a tag without a dimension is ignored) |
| `?scope=bbox` | `?scope=all` |
| `?tab=<name>-tab` | `?tab=<name>`, e.g. `?tab=materials` (the old form opens the map) |
| `?snapshot=<id>` (`pvmt serve`) | `?snapshot=<name>`, or `latest` for the latest compute |

### 5. Update anything that reads pvmt's JSON

All JSON now uses one snake_case vocabulary
([JSON conventions](architecture.md#json-conventions)). A `--json` field
list naming an old field fails with exit 2, but a script reading an old name
from the output, the API or the site files just gets nothing back, so check
it against the tables below. `--jq` and `--template` see the `--json` keys;
`export --json` and `validate --json` now accept them too.

#### CLI `--json`

| Command | v0.4.x | v0.5.0 |
|---------|--------|--------|
| `status` | `resourceType`, `featureCount`, `lastIngest`, `lastCompute` | `resource_type`, `feature_count`, `last_ingest_at`, `last_compute_at`; new `city`, `state`, `stale_inputs` |
| `cities` | `features`, `totalArea`, `lastIngest`, `lastCompute` | `feature_counts`, `total_paved`, `last_ingest_at`, `last_compute_at` |
| `snapshots list` | per city: `city`, `id`, `computedAt`, `configHash` | per named snapshot: `name`, `created_at`, `city_count`, `compute_epoch` (per-city detail is in `snapshots show`) |
| `<resource> compute` | `resourceType`, `jurisdiction`, `featureCount`, `snapshotId`; `city` as a display name | `resource_type`, `scope`, `feature_count`, `run_id`; `city` as a slug |
| `forecast` | `resourceType`, `treatmentCost`, `treatmentTier` | `resource_type`, `annual_need`, `cost_tier`; new `city`, `scope` |
| `export` | `total` | `file_count` |
| `validate` | `break_even`, `hold_steady_budget`, `passed`, `failed`, `context` | `break_even_budget`, `published_break_even_budget`, `pass_count`, `fail_count`, `context_count` |
| `config show` | `units`, `display.min_hex_area`, `forecast.years`, `forecast.cost_overhead` | `display.units`, `grid.min_hex_area`, `forecast.horizon_years`, `forecast.cost_multiplier` |

#### `pvmt serve` API

| Endpoint | v0.4.x | v0.5.0 |
|----------|--------|--------|
| `/api/snapshots`, `/api/cities/{slug}/snapshots` | `id`, `computed_at` | `name`, `created_at`, `compute_epoch`, `hex_edge_m`, `parts`, `served` |
| every `/api/` endpoint | plain-text errors | `{"error": "<message>"}` |
| `/api/cities` | as `cities.json` below | as `cities.json` below |
| `/data/{file}?snapshot=` | a snapshot id | a snapshot name, or `latest` |

#### Site data files

Only a script that reads an exported site's files needs this table.

<details>
<summary>Data files, page config and Play payloads</summary>

| File | v0.4.x | v0.5.0 |
|------|--------|--------|
| `cities.json`, `meta.json` | `center_lon`, `center_lat`; `bbox` as `[south, west, north, east]` | `center` as `[lon, lat]`; `bbox` as `[west, south, east, north]` |
| `meta.json` | `project_name`, `pct_paved` (0..100), `stats[].type`, `stats[].total_area`, `stats[].feature_count`; `snapshot_date` the render date | `name`, `city`, `paved_share` (0..1), `stats[].resource_type`, `stats[].area`; `snapshot_date` the data's date, plus `snapshot` |
| `hexgrid.geojson` | format 2; scope key `bbox`; coverage `pct` | format 6; scope key `all`; coverage `share`; road hexes carry `k` |
| `play-hexes.json` | the Play board | gone: the board reads `hexgrid.geojson` |
| `scenarios.json` | scope key `bbox`; a `summary` block | scope key `all`; no `summary` |
| `hex-cost-summary.json` | scope key `bbox`; `year1_cost`; `total_area` | scope key `all`; `year1_need`; `area` |
| `forecast.json` | `baseline` the city's run if any; `bbox_baseline`; `insolvency_year`, `funding_gap` as `null` | `baseline` always every jurisdiction's; `city_baseline`; `tipping_point_year` and `funding_gap` omitted when unset |
| `forecast.json`, `scenarios.json` | `final_cohorts[].total_deficit`; `scenario.strategy` an integer; `scenario.annual_budget` `0` on the bounds | `final_cohorts[].deferred_backlog`; `scenario.strategy` a name (`worst_first`); `scenario.annual_budget` whole dollars, omitted on the bounds |
| `forecast_seed.json` | `years`, `binder_pct`, `cost_overhead`; `total_area`, `city_paved` (the footprint) | `horizon_years`, `binder_share`, `cost_multiplier`; `maintained_area`, `city_maintained_area` (the priced area) |
| `PVMT_CONFIG` (dashboard) | `layerColors`, `unitSystem`, `forecastSeed`, `wasmPrefix`; `cities[].lon`, `.lat` | `layer_colors`, `unit_system`, `forecast_seed`, `hexgrid_version`, `hex_metrics_version`, `compare_version`; `cities[].center` |
| `PVMT_CONFIG` (play) | `forecastSeed`, `dataPrefix`, `mapHref`, `bandCount`, `wasmPrefix` | `data_prefix`, `map_href`, `band_count`, `hexgrid_version` |
| Play game state | `backlog`, `spent`, `treatments`, `budget_rate`, `closed_count`, `insolvency_year`, `changed[].pci`; `gameTreat`, `gameSetBudget` | `deferred_backlog`, `total_spend`, `treatment_count`, `annual_budget`, `closed_share`; the rest are gone |
| Play `gameInit` | `starting_budget`; `hexes[].road_area`, `.k` | `annual_budget`; `hexes[].roads_area`, `.decay_rate` |

</details>
