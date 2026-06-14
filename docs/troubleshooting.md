# Troubleshooting

When pvmt exits with an error, it prints two things:

1. The error itself (e.g., `config file not found`).
2. A one-line **hint** suggesting a fix.

This page expands the hints into longer guidance. Each section quotes the hint text verbatim, so you can match the message pvmt printed to its section below.

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

Optional fields — data sources (`overpass`, `arcgis_url`) and per-city overrides — are documented in [configuration.md](configuration.md). With no source set, pvmt resolves the city boundary from OSM Nominatim on first ingest.

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

The `<path>` in the hint is the exact directory pvmt failed on, so start there. If it's not the one you expected, an `XDG_CACHE_HOME` / `XDG_DATA_HOME` override may be redirecting it — check those env vars and the directory holding your `pvmt.toml`. (Note: `pvmt status` won't help here — it opens the database, so it fails on the same permission error, and it reports per-resource counts rather than paths.)

## `#water-strip-skipped`

**Hint:** *one-line warning emitted by `pvmt ingest`; the boundary is still saved but without OSM water subtracted.*

Before computing coverage, pvmt subtracts OSM water (`natural=water`, `natural=coastline`) from the Nominatim boundary so cross-city `% paved` is comparable. This is best-effort: when it fails it logs `water strip skipped: …` and keeps the unstripped boundary. Area numbers are then inflated by the un-subtracted water; roads/parking/sidewalks ingest itself is unaffected.

Fixes:

- **Transient** (`bbox:`, `interior points:`, or `overpass:` variants) — a malformed cached boundary or a failed/rate-limited Overpass fetch. Re-run with `--force` to re-fetch.
- **Persistent**, or the hard error `boundary excludes most of the city's own roads` — Nominatim returned the wrong shape for this city. Set `[[cities]].boundary_relation_id` to the OSM `admin_level=8` relation (see [Configuration › Resolution hierarchy](configuration.md#resolution-hierarchy)), or remove the city from `pvmt.toml` if it's intentionally untracked.

For per-polygon detail on what was dropped and why, re-run with `-vv` and grep the log for `water`.
