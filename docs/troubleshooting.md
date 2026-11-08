# Troubleshooting

When pvmt fails, it prints the error and then a one-line **hint** with the
likely fix. Most sections below are named for an error and quote its hint, so
find the message you got and read on. The last two cover problems that produce
no error at all.

## Exit codes

Every command uses the same codes, so a chain like
`pvmt all compute && pvmt export && pvmt check-site` can be trusted:

| Code | Meaning |
|------|---------|
| 0 | Success, or you declined a confirmation prompt |
| 1 | The command failed, or was interrupted (Ctrl-C / SIGTERM) |
| 2 | Usage error: a bad flag, a missing argument, or an invalid config ([`#invalid-config`](#invalid-config)) |
| 3 | The command ran cleanly but produced nothing (no features, no cities with data) |

An interrupt exits 1 because the work did not finish. Treating it as success
would let `pvmt check-site && deploy` publish a site whose checks never ran.
`pvmt serve` is the exception: Ctrl-C is how you stop it, and it exits 0.

## `#config-not-found`

**Hint:** *create a pvmt.toml in your project root, or cd into a directory that contains one.*

pvmt looks for `pvmt.toml` in the current directory and every parent directory,
the way `git` finds `.git`, and found none. Run pvmt from inside a project, or
create a config there. The hint prints a minimal working one, and
[configuration.md](configuration.md) covers the rest.

## `#no-cities`

**Hint:** *add a [[cities]] section to pvmt.toml.*

The config parsed but defines no cities, either directly or through
`[[include]]`. Every command needs at least one.

## `#invalid-config`

The config parsed as TOML but a value is wrong. Examples are an unknown key, a
`hex_edge_m` below 10 m, an unknown `display.units`, a service name that isn't
registered, or a malformed tag. There is no hint, because the error itself
names the field and what is wrong with it. pvmt exits 2, so scripts can tell
bad input from an operational failure. Valid values and ranges are in
[configuration.md](configuration.md).

## `#permission-denied`

**Hint:** *check filesystem permissions on `<path>`.*

pvmt could not read the config, or could not create or write its cache or data
directory. The path in the hint is the one that failed (where each directory
lives is in [configuration.md](configuration.md#files-and-directories)). The
usual causes:

- An earlier `sudo pvmt` created the directory as root. Fix it with
  `chown -R "$USER" <path>`.
- The filesystem is read-only, as in a container snapshot. On Linux, point
  `XDG_CACHE_HOME` / `XDG_DATA_HOME` somewhere writable.
- SELinux or AppArmor denies the write. Check the audit log.

## `#incompatible-database`

**Hint:** *move `<path>` (and any `-wal`/`-shm` files beside it) aside; pvmt creates a fresh database on the next run. Then re-ingest with `pvmt all ingest` and `pvmt all compute`. pvmt never deletes it for you.*

Every database is stamped with a *schema generation*, and pvmt opens only its
own. The error says which way the mismatch runs:

- `schema generation N predates this pvmt's M`: an older pvmt created the
  database. Every database from v0.4.1 or earlier is generation 0.
- `schema generation N is newer than this pvmt's M`: a newer pvmt created it.
  Upgrade pvmt instead.

The refused file is left untouched. To start over, move it aside with its
companions:

```
for f in <path> <path>-wal <path>-shm; do [ -e "$f" ] && mv "$f" "$f.old"; done
```

Then run ingest and compute again. Everything in the database is rebuilt from
upstream sources. The HTTP cache is separate, so any response fetched within
the last 24 hours is reused. [upgrading.md](upgrading.md) lists the releases
that need this.

## `#export-errnowasm`

**Hint:** *`go install` builds from the published module, which cannot include the generated WASM. Install a release (.deb or tarball), or build from a checkout with `make build`.*

The error reads `this pvmt binary was built without the forecast WASM`. The
dashboard's interactive forecast is compiled to WASM when pvmt is built, and a
binary from `go install` has none. `pvmt export` and `pvmt serve` refuse to
run, and `pvmt check-site` fails its asset check. Everything else works.
Install a release or build from a checkout, as in the
[README](../README.md#install).

## No snapshot matches current config hash

The commands report this in different ways:

- `pvmt export` fails with `no snapshot matches current config hash for <city>`.
- `pvmt forecast` warns `<slug> was computed, but not under the current settings …`, and exits 3 when no city has results.
- `pvmt status` shows a blank *Last Compute*, with the same note.
- `pvmt serve` logs it at startup and serves the city without data.

Results are read back only under the settings they were computed with (see
[Snapshots and the config hash](configuration.md#snapshots-and-the-config-hash)).
For `pvmt export`, the message says which of three cases you are in:

- **Computed, but not under the current settings.** A setting in the hash
  changed, `PVMT_HEX_EDGE_M` was set or unset, or a new pvmt release computes
  differently. Run `pvmt --city <slug> all compute`. `pvmt status` marks which
  of the city's snapshots match the current settings.
- **Never computed.** Run `pvmt --city <slug> all compute`, after
  `pvmt all ingest` if the city has no data yet.
- **Pulled in via `[[include]]` but resolved differently.** The including
  config resolves a setting in the hash differently from the file that
  declared the city, and the message names it. The usual cause is
  `[[services]]`, which an include does not carry over. Make the two agree, or
  compute the city under the including config. Copying `[[services]]` into the
  including config works only if every included example uses the same
  overrides, because they apply to all of its cities.

A related **warning** reads `no snapshot under the current settings has <parts>,
which snapshot N (earlier settings) had`. It means only some steps were rerun
after a settings change, for example `pvmt roads compute` after editing
`hex_edge_m`. The export goes ahead without those parts: fire coverage,
population, or the combined paved total, without which the page sums the
resources and double-counts their overlap. Run `pvmt --city <slug> all compute`.

## Water strip errors and warnings

Ingest subtracts OSM water from the city boundary, so that `% paved` compares
fairly across cities. It can fail in three ways:

- **Warning `water strip skipped: …`** (`bbox:`, `interior points:` or
  `subtract:`). The geometry step failed, so the boundary was saved with its
  water still in, and area figures are inflated. Re-run the ingest with
  `--force`. If it recurs, the boundary itself is probably wrong (next item).
- **Error `water strip for <city>: water fetch failed`.** Overpass did not
  answer. Nothing was saved, so the next ingest retries. Wait a few minutes
  and re-run.
- **Error `water strip over-subtracted`, or `boundary excludes most of the
  city's own roads`.** The water polygon, or Nominatim's boundary, has the
  wrong shape. For a bad boundary, set `boundary_relation_id` (see
  [Cities](configuration.md#cities)). For bad water, the `water way:`,
  `water relation:` or `water coastline:` warnings above the error name the OSM
  element at fault (`-vv` also logs the polygons that were accepted). Open an
  issue with the city, its bbox and that warning.

## Disk usage: the HTTP cache keeps growing

This produces no error. Every Overpass, ArcGIS, Nominatim and TIGERweb response
is written to the HTTP cache. Nothing in the request path ever removes an
entry, and an edited bbox or config writes new entries beside the old ones. A
long-lived install can reach several GiB. `pvmt cache prune` reclaims it:

```
pvmt cache prune --dry-run    # what would go, and how much it frees
pvmt cache prune              # older than 30 days, then oldest-first down to 500 MiB
pvmt cache prune --max-age=168h --max-size=100mb --yes   # unattended
```

`pvmt cache prune` asks before deleting, and without a terminal it refuses
unless you pass `--yes`. It reads no config and opens no database, so it works
when either is broken, and it also sweeps entries half-written by a crash.
Pruning touches no ingested data. Every pruned response is downloaded again
when next needed, which costs community-funded endpoints, so prefer a size cap
to a wipe. The cache can't be turned off. `--force` on an ingest bypasses it
for that run, and still stores the fresh response.

## Hexes overhang the city outline at high zoom

This produces no error either. The drawn outline is simplified
(`[export].boundary_simplify_m`), but hexes are clipped to the full boundary,
so at street zoom a hex can overhang the line by a few meters. Set the
tolerance lower, or negative to draw the exact boundary (see
[Export and hosting](configuration.md#export-and-hosting)).
