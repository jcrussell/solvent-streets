# Troubleshooting

When pvmt exits with an error, it prints two things:

1. The error itself (e.g., `config file not found`).
2. A one-line **hint** suggesting a fix.

This page expands the hints into longer guidance. Each section's anchor matches the short label pvmt prints when it has one (e.g., `pvmt config show` references `#config-not-found`).

## `#config-not-found`

**Hint:** *create a pvmt.toml in your project root, or cd into a directory that contains one.*

pvmt looks for `pvmt.toml` by walking from the current working directory up toward the filesystem root (like `git` finds `.git`). If nothing matches before `/`, you get this error.

Fixes:

- Run pvmt from inside a project that has a `pvmt.toml`.
- Create one in the directory you're working from. The minimum config:

  ```toml
  [[cities]]
  name = "Oakland"
  ```

  See [configuration.md](configuration.md) for the full schema.

## `#no-cities`

**Hint:** *add a [[cities]] section to pvmt.toml.*

The config parsed cleanly but contains no `[[cities]]` entries. Every pvmt command needs at least one city.

Fix — add one to `pvmt.toml`:

```toml
[[cities]]
name = "Oakland"
```

Optional fields (bbox, overpass URL, ArcGIS source) are documented in [configuration.md](configuration.md). When you omit them, pvmt resolves a bounding box from OSM Nominatim on first ingest.

## `#invalid-config`

**Hint:** *(no hint — the validation error names the field.)*

The config file parsed as TOML but failed shape validation (negative `hex_edge_m`, unknown `display.units`, etc.). pvmt returns exit code 2 (usage error) so scripts can distinguish bad input from operational failures.

Read the error message: it names the offending field and what's wrong. Cross-check against [configuration.md](configuration.md) for valid ranges.

## `#permission-denied`

**Hint:** *check filesystem permissions on `<path>`.*

pvmt couldn't read, create, or write inside one of its runtime directories. The path in the hint tells you which one:

| Directory | Default location | Used for |
|-----------|------------------|----------|
| Config | The directory holding `pvmt.toml` | reading the config file |
| Cache | `$XDG_CACHE_HOME/pvmt` (or `~/.cache/pvmt`) | HTTP cache |
| Data | `$XDG_DATA_HOME/pvmt` (or `~/.local/share/pvmt`) | the SQLite database |

Common causes:

- The directory is owned by root because an earlier `sudo pvmt` invocation created it. Fix: `chown -R "$USER" <path>`.
- The filesystem is mounted read-only (overlayfs, container snapshot). Fix: pick a writable XDG override via `XDG_CACHE_HOME` / `XDG_DATA_HOME`.
- SELinux / AppArmor denies writes to your home directory's hidden dirs. Check audit logs and adjust the policy.

If reseating permissions doesn't help, run `pvmt status` to print every resolved path — that often surfaces a typo or a stray override env var.

## `#water-strip-skipped`

**Hint:** *one-line warning emitted by `pvmt ingest`; the boundary is still saved but without OSM water subtracted.*

`stripWaterFromBoundary` (`pkg/cmd/ingest/ingest.go`) is best-effort: it fetches `natural=water` and `natural=coastline` features from Overpass and subtracts them from the Nominatim boundary so cross-city `% paved` is apples-to-apples. When it can't, it logs a `water strip skipped: …` warning and continues with the unstripped boundary. Downstream area numbers are inflated by the un-subtracted water area; roads/parking/sidewalks ingest is unaffected.

Variants you'll see in logs:

- `water strip skipped: bbox: …` — the Nominatim boundary's bbox couldn't be computed. Almost always a malformed cached boundary; `--force` re-fetches.
- `water strip skipped: interior points: …` — `geo.InteriorPoints` failed (boundary GeoJSON is neither Polygon nor MultiPolygon, or is empty). Same fix: `--force`.
- `water strip skipped: overpass: …` — the Overpass POST failed. Network, rate limit, or response > 100 MB. Retry; the HTTP cache will reuse a successful response within 24 h.
- `water strip skipped: subtract: …` — `simplefeatures.Difference` errored. Most often this is the `Overlay input is mixed-dimension` panic affecting a small set of cities (Austin, Fort Worth, …); tracked in bead `solvent-streets-i3ih`.
Per-polygon land-probe drops (not skips — the strip still runs; specific polygons are filtered out):

- `water way: dropped polygon containing city land` (`internal/ingest/water.go`, debug log fields `way`, `land_hits`, `land_probes`) — a closed `natural=water` way whose outer ring contains one or more of the `PointOnSurface` samples from the Nominatim boundary. Caused by an OSM tagging error where land was tagged as water. The way is excluded from the subtraction; legitimate water polygons in the same response still strip normally.
- `water relation: dropped polygon containing city land` — the relation analogue. The hole-aware check exempts probes that fall inside an inner ring (a non-water "island" within the polygon); only probes inside the polygon's set-theoretic interior count as hits.
- `water coastline: dropped chain — both candidates overlap city land` — both CW and CCW bbox-edge closures of an open coastline chain contain at least one land probe (the chain weaves through multiple landmasses; NYC chain=8774). Refusing to close is the correct posture for ambiguous chains; the chain's water is simply not subtracted.
- `water coastline: dropped CW closed ring containing city land` — a closed CW coastline ring (would normally pass through as a lake) that wraps around the city's land instead. Dropped to prevent over-subtraction.

Hard failure (no `skipped` prefix — pvmt exits non-zero):

- `water strip for <City>: water strip over-subtracted: stripped … sq m is …% of original … sq m` — the 0.1 aggregate area-ratio backstop at `pkg/cmd/ingest/ingest.go` rejected the result. This is the LAST line of defense, reachable only when every prior per-polygon filter (`acceptWaterPolygon`'s bbox-area cap, the per-polygon land-probe filter, `closeOpenSubChain`'s coastline disambiguation) accepted polygons that collectively still over-subtract. If you hit it, run the diagnostic:

  ```
  pvmt --city '<City>' --force -vv roads ingest 2>&1 \
    | grep -E 'water (way|relation|coastline):'
  ```

  That surfaces every accepted/rejected polygon by OSM id and bbox-area fraction. To inspect the cached Nominatim boundary for the offending city:

  ```
  sqlite3 ~/.local/share/pvmt/pvmt.db \
    "SELECT source, length(geometry_json)
       FROM city_boundaries
       WHERE city_id = (SELECT id FROM cities WHERE slug = 'new-york-ny')"
  ```

  If the boundary has fewer sub-polygons than the city visibly has landmasses (e.g., NYC with <5), `geo.InteriorPoints` is producing too few probes for the per-polygon filter to be a complete defense; consider whether the city needs a `BoundaryRelationID` override (memory `osm-place-city-node-fallback`).

- `boundary excludes most of the city's own roads: city="<City>" stripped=… original=…` — the post-strip road-coverage gate (`pkg/cmd/ingest/coverage.go`, threshold `stripCoverageMinRatio = 0.15`) fired and NONE of its recovery paths worked: not the stripped boundary, not the inverted complement, not the unstripped Nominatim. This means even the raw Nominatim shape excludes most of the city's `highway=*` features — almost always a Nominatim resolution problem (wrong administrative unit returned, or the boundary's bbox doesn't cover where the roads are). Fix: find the OSM `admin_level=8` relation at https://overpass-turbo.eu/ and set `[[cities]].boundary_relation_id` in `pvmt.toml`, or set `skip = true` if this city is intentionally untracked.

  When the gate fires but a recovery path succeeds, ingest continues and you'll see one of two stderr notices (both are expected for the `solvent-streets-e5mk` inversion failure mode):
  - `Water-strip inversion recovered for <City>: … restored land via complement (…% coverage, source=…-inverted)` — the **preferred** path. The stitched OSM water polygon came back covering land instead of water (often too few `geo.InteriorPoints` land-probes to disambiguate the coastline), so `boundary − water` yielded the water part. The gate uses the geometric complement `boundary − (boundary − water)` to recover the land, which fixes both the paved-area numerator AND the `pct_paved` denominator (a land-only boundary). The `-inverted` source suffix in `city_boundaries` flags cities that took this path.
  - `Water-strip rollback for <City>: … restored unstripped Nominatim (…% coverage, source=nominatim)` — the fallback when the complement also fails the gate. Safe for the paved-area numerator, but leaves any bay/ocean in the boundary, so `pct_paved` is understated for coastal cities. A city stuck here usually needs a `boundary_relation_id` override.
