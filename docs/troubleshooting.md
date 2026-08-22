# Troubleshooting

When pvmt exits with an error, it prints two things:

1. The error itself (e.g., `config file not found`).
2. A one-line **hint** suggesting a fix.

This page expands the hints into longer guidance. Most sections are named for a hint and quote its text verbatim, so you can match the message pvmt printed to its section below. A few sections at the end cover conditions that produce no error and therefore no hint — they are named for the symptom instead, and say so.

## Exit codes

Every command uses the same mapping, so a shell chain like `pvmt compute && pvmt export && pvmt check-site` can be trusted:

| Code | Meaning |
|------|---------|
| 0 | Success, or you declined a confirmation prompt |
| 1 | The command failed, **or was interrupted** (Ctrl-C / SIGTERM) |
| 2 | Usage error — a bad flag, a missing argument, or an invalid config ([`#invalid-config`](#invalid-config)) |
| 3 | The command ran cleanly but produced nothing (no features, no cities with data) |

An interrupt exits **1**, not 0: the command did not finish its work, so treating it as success would let `pvmt check-site && deploy` publish a site whose remaining checks never ran. Declining a prompt is different — that is a deliberate choice you made, and it exits 0.

`pvmt serve` is the one command where Ctrl-C is the normal way to stop: it shuts down gracefully and exits 0.

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

## Disk usage: the HTTP cache keeps growing

Not tied to a hint — pvmt never errors on this, it just quietly uses disk.

Every ingest that talks to Overpass, ArcGIS, or Nominatim writes the response into `$XDG_CACHE_HOME/pvmt/http` (`~/.cache/pvmt/http` by default), and nothing in the request path ever removes an entry. Responses are large (a city's Overpass extract runs to tens of MiB) and re-ingesting after a bbox change or a config edit writes *new* entries rather than replacing the old ones, so a long-lived install can reach several GiB of entries that will never be read again.

Check and reclaim:

```
# What would go, and how much it would free — writes nothing
pvmt cache prune --dry-run

# Apply the defaults: drop entries older than 30 days, then cap the
# total at 500 MiB by evicting oldest-first
pvmt cache prune

# Tighter caps, no confirmation prompt (scripts, CI)
pvmt cache prune --max-age=168h --max-size=100mb --yes
```

`pvmt cache prune` shows what it plans to remove and asks before deleting; `--yes` skips the prompt and `--dry-run` reports without prompting or deleting. Without a TTY and without `--yes` it refuses rather than deleting unattended.

No ingested data is touched — only downloaded responses — but pruning is not free: every removed entry is re-fetched on next use, which costs bandwidth and load on community-funded endpoints (Overpass, Nominatim). Prefer a size cap to a blanket wipe.

The command reads no config and opens no database, so unlike `pvmt gc` it still works when your `pvmt.toml` is broken or the database won't open. It also sweeps half-written entries left behind if pvmt was killed mid-write.

There is no way to turn the cache off. The 24-hour TTL is a compiled-in constant with no flag, environment variable, or config key behind it, so pruning (or deleting the directory) is the only lever you have. `--force` on an ingest bypasses the cache for that one run, but still writes the fresh response back.

Deleting the directory by hand (`rm -rf ~/.cache/pvmt/http`) is equally safe, just less selective — and unlike `pvmt cache prune` it takes no confirmation and gives no size preview.
