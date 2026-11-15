# Troubleshooting

When pvmt fails, it prints the error and then a **hint** with the likely fix. Most sections below are named for an error and quote its hint, so
find the message you got and read on. The last two cover problems that produce
no error at all.

## Exit codes

Every command uses the same codes, so a chain like
`pvmt all compute && pvmt export && pvmt check-site` can be trusted:

| Code | Meaning |
|------|---------|
| 0 | Success, or you declined a confirmation prompt |
| 1 | The command failed, or was interrupted (Ctrl-C / SIGTERM) |
| 2 | Usage error: a bad flag, a missing argument, an invalid config ([`#invalid-config`](#invalid-config)) or one with no cities ([`#no-cities`](#no-cities)), or a destructive command run without `--yes` and without a terminal to confirm on |
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

**Hint:** *add a [[cities]] section to pvmt.toml*, followed by an example.

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

**Hint:** *check filesystem permissions on `<path>`* (or *on the pvmt.toml path*
when the config itself is unreadable).

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

## City has no snapshot

`pvmt export` fails with `no snapshot to export: N cities have none`, before
it writes anything, and its hint lists each such city with the command that
fixes it:

```
hint: Export publishes each city's newest snapshot. Create one for each first:
  Oakland, CA: pvmt --city oakland-ca snapshots create
  Reno, NV: pvmt --city reno-nv all compute && pvmt --city reno-nv snapshots create
```

Computes only update a city's latest compute; `pvmt snapshots create` freezes
it as the snapshot export publishes. Run the listed commands. `all compute`
comes first for a city with nothing computed under the current settings:
never computed, or computed only under settings that have changed since. A
multi-city export skips a city with no boundary stored (its ingest failed)
rather than refusing it.

`pvmt serve` serves such a city from its latest compute, and says so at
startup (`<city> has no snapshot; serving its latest compute (not
snapshotted)`). With nothing computed either, it logs `no snapshot for <city>;
serving it without data` and a hint. `pvmt forecast` and `pvmt status` read the
latest compute: for a city with nothing computed under the current settings,
`pvmt forecast` warns `<slug> was computed, but not under the current settings
…` and exits 3 when no city has results, and `pvmt status` shows a blank *Last
Compute* with the same note.

For an `[[include]]`d city, the message can instead say the including config
resolves a setting in the hash differently from the file that declared the
city, and name it. The usual cause is `[[services]]`, which an include does not
carry over. Make the two agree, or compute and snapshot the city under the
including config. Copying `[[services]]` into the including config works only
if every included example uses the same overrides, because they apply to all of
its cities.

## Snapshot was created under other settings

`pvmt export` warns `<city>: snapshot #N was created under other settings
(hash <old>, now <new>): …; exporting it as created`, and `pvmt serve` logs the
same at startup. Export and serve publish each city's newest snapshot whatever
settings it was created under, and draw it exactly as created: its hex edge,
boundary, stations and results. A setting in the hash changed since (see
[Snapshots and the config hash](configuration.md#snapshots-and-the-config-hash)),
`PVMT_HEX_EDGE_M` was set or unset, or a new pvmt release computes
differently. To publish the current settings, run
`pvmt --city <slug> all compute`, then `pvmt --city <slug> snapshots create`.
To keep publishing the old snapshot, ignore the warning.

A related **note** reads `<city>: the latest compute has <parts> not yet
snapshotted`. A compute ran after the newest snapshot was created, so the
export publishes the snapshot without it. Run
`pvmt --city <slug> snapshots create` to publish it.

## Snapshot no longer exists

`pvmt serve` answers a data request with **410 Gone**, `the snapshot this page
was serving has been deleted or pruned`, or `pvmt export` fails with
`snapshot N: snapshot no longer exists (deleted or pruned)`. Both pin each
city's snapshot when they start, and `pvmt snapshots delete` or `prune` deleted it
while they were running. Restart `pvmt serve`, or re-run the export, and it
reads the city's newest snapshot. `pvmt serve` also answers 410 for a city it
serves from its latest compute once `pvmt gc` has swept runs that a later
compute replaced.

## `snapshots create` refuses a city

`pvmt snapshots create` prints one line per city, and exits 1 once every city
is done if any was refused:

```
oakland-ca: refused: roads re-ingested 2026-11-14 10:02 UTC after the combined, roads and fire coverage computes
  hint: pvmt --city oakland-ca all compute
berkeley-ca: created #12
```

A snapshot must be coherent, so create refuses a city when a part was
computed from a feature set or boundary that has been ingested again (or
first ingested, or removed) since, when it has no pavement compute, when its
pavement has no combined pass, or when a pavement resource was ingested but
never computed. Each reason names the input and when it changed. Identical
data re-ingested does not count: the check compares content digests, not
times. `pvmt status` shows the same verdict before you create, as each part's
*State* (`stale: roads re-ingested …`). Run the hinted command, then create
again. The hint is one of the following.

### `hint: pvmt --city <slug> all compute`

The boundary changed (every part is clipped to it), the combined pass is
stale or missing, there is no pavement compute, or two or more parts are
stale. One `all compute` recomputes them together.

### `hint: pvmt --city <slug> services compute`

Only the service coverage is stale: its facilities were re-ingested after it
was computed. `pvmt services compute` routes from them again.

### `hint: pvmt --city <slug> census compute`

Only the population is stale: the census blocks were re-ingested after it was
computed.

A **warning** such as `no fire coverage: fire facilities are ingested but never
computed` does not refuse the city. The snapshot is created without that part,
and the warning names the command that adds it.

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
to a wipe. The cache can't be turned off. `--force` on an ingest (including `pvmt all
ingest`) bypasses it for that run, and still stores the fresh response. To see
OpenStreetMap edits sooner without `--force`, shorten `[cache] ttl` (see
[configuration](configuration.md#http-cache-cache)).

## Hexes overhang the city outline at high zoom

This produces no error either. The drawn outline is simplified
(`[export].boundary_simplify_m`), but hexes are clipped to the full boundary,
so at street zoom a hex can overhang the line by a few meters. Set the
tolerance lower, or negative to draw the exact boundary (see
[Export and hosting](configuration.md#export-and-hosting)).
