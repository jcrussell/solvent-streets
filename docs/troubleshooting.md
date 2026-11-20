# Troubleshooting

When pvmt fails, it prints the error and then a **hint** with the likely fix. Most sections below are named for an error and quote its hint, so
find the message you got and read on. The last two cover problems that produce
no error at all.

## Exit codes

Every command uses the same codes, so a chain like
`pvmt all compute && pvmt snapshots create 2026-11 && pvmt export && pvmt check-site`
can be trusted:

| Code | Meaning |
|------|---------|
| 0 | Success, or you declined a confirmation prompt |
| 1 | The command failed, or was interrupted (Ctrl-C / SIGTERM) |
| 2 | Usage error: a bad flag, a missing argument, a `--city`, `--source` or snapshot name that matches nothing, an invalid config ([`#invalid-config`](#invalid-config)) or one with no cities ([`#no-cities`](#no-cities)), or a destructive command run without `--yes` and without a terminal to confirm on |
| 3 | Nothing to do yet: the command needs an earlier step first, and its hint names it (`run 'pvmt all ingest' first`, as `pvmt cities` says on an empty database), or it ran cleanly but produced nothing. A run over several cities exits 3 only when no city produced a result |

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
registered, or a malformed tag. Usually there is no hint, because the error
itself names the field and what is wrong with it. pvmt exits 2, so scripts can tell
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

**Hint:** *move `<path>` (and any -wal/-shm files beside it) aside, and pvmt creates a fresh database on the next run (it never deletes the old one for you); then re-ingest with 'pvmt all ingest', 'pvmt all compute' and 'pvmt snapshots create <name>'*

Every database is stamped with a *schema generation*, and pvmt opens only its
own. The error says which way the mismatch runs:

- `schema generation N predates this pvmt's M`: an older pvmt created the
  database. Every database from v0.4.1 or earlier is generation 0.
- `schema generation N is newer than this pvmt's M`: a newer pvmt created it.
  Upgrade pvmt instead.
- `not a pvmt database`: the file is some other program's SQLite database,
  sitting where pvmt keeps its own. Move it somewhere else to keep it.

The refused file is left untouched. To start over, move it aside with its
companions:

```
for f in <path> <path>-wal <path>-shm; do [ -e "$f" ] && mv "$f" "$f.old"; done
```

Then run the three commands the hint names; export and serve need the
snapshot. Everything in the database is rebuilt from upstream sources. The
HTTP cache is separate, so a response younger than `[cache] ttl` that this pvmt
can read is reused. [upgrading.md](upgrading.md) lists the releases that need
a rebuild.

## Database is locked

**Hint:** *another pvmt process is initializing or writing `<path>`; run the command again once it finishes*, or, mid-command, *another pvmt process is writing the database; run the command again once it finishes*

Several pvmt commands can run against one database at once. Writes take turns:
a command that finds another holding the write lock waits for it, for up to
two minutes, and then fails with `database is locked (5) (SQLITE_BUSY)`
rather than hanging. The first hint comes with a wait that ran out while
opening the database (`migrate: …`), the second with one that ran out later,
whose command's uncommitted work is discarded. Usually the other command is in
the middle of a long write, such as a large ingest saving its features or
`pvmt gc`. Let it finish, then run yours again.

## `#export-errnowasm`

**Hint:** *install a release (.deb or tarball), or build from a checkout with 'make build' …*

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
hint: export publishes each city's newest snapshot; create one for each first:
  Oakland, CA: pvmt snapshots create <name>
  Reno, NV: pvmt --city reno-nv all compute && pvmt snapshots create <name>
```

Computes only update a city's latest compute, and export publishes snapshots
(see [Snapshots and the config hash](configuration.md#snapshots-and-the-config-hash)).
Run the listed commands: the computes first, then one `snapshots create`,
which covers every configured city. `all compute` comes first for a city with nothing computed
under the current settings: never computed, or computed only under settings
that have changed since. A multi-city export skips a city with no boundary
stored rather than refusing it, but when no city has one (a fresh database)
it fails with `nothing to export` and exit 3, before writing anything: run
`pvmt all ingest` first.

With `--snapshot NAME`, export fails instead with `snapshot "NAME" does not
cover N cities (…): no snapshot` when a configured city was added after the
snapshot was created (or was refused then), before anything is written and
before `--clean`. Export the cities it covers with `--city`, or create a new
snapshot covering them all. `pvmt serve --snapshot NAME` refuses to start the
same way.

`pvmt serve` serves such a city from its latest compute, and says so at
startup (`<city> has no snapshot; serving its latest compute (not
snapshotted)`). With nothing computed either, it logs `no snapshot for <city>;
serving it without data` and a hint, as long as another city has data. When
none does, it refuses to start with `nothing to serve` and exit 3, naming
`pvmt all ingest` when no city has a boundary stored and `pvmt all compute`
otherwise. `pvmt forecast` and `pvmt status` read the
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

`pvmt export` warns `<city>: snapshot "<name>" was created under other settings
(hash <old>, now <new>): …; exporting it as created`, and `pvmt serve` warns the
same at startup, ending `serving it as created`. Export and serve publish each city from its snapshot whatever
settings it was frozen under, and draw it exactly as created: its hex edge,
boundary, stations and results. A setting in the hash changed since (see
[Snapshots and the config hash](configuration.md#snapshots-and-the-config-hash)),
`PVMT_HEX_EDGE_M` was set or unset, or a new pvmt release computes
differently. To publish the current settings, run
`pvmt --city <slug> all compute`, then `pvmt snapshots create <name>`.
To keep publishing the old snapshot, ignore the warning.

A related **warning** reads `<city>: the latest compute has <parts> not yet
snapshotted`. A compute ran after the newest snapshot was created, so the
export publishes the snapshot without it. Run
`pvmt snapshots create <name>` to publish it.

## Snapshot no longer exists

`pvmt serve` answers a data request with **410 Gone**, `the snapshot this page
was serving has been deleted or pruned`, or `pvmt export` fails with
`snapshot "<name>" no longer exists (deleted or pruned)`. Both pin each
city's snapshot when they start, and `pvmt snapshots delete` or `prune` deleted it
while they were running. Restart `pvmt serve`, or re-run the export, and it
reads the city's newest snapshot. `pvmt serve` also answers 410 for a city it
serves from its latest compute once `pvmt gc` has swept runs that a later
compute replaced.

## `snapshots create` refuses a city

`pvmt snapshots create <name>` is all or nothing: it judges every city, and
when any is refused it creates nothing, prints one line per refused city on
stdout, with its hint on stderr, and exits 1:

```
oakland-ca: refused: roads re-ingested 2026-11-14 10:02 UTC after the combined, roads and fire coverage computes
hint: run 'pvmt --city oakland-ca all compute'
refused "2026-11": 1 of 2 cities refused; nothing was created
```

A snapshot must be coherent; `pvmt snapshots create --help` lists what refuses
a city. Each reason names the input and when it changed. Identical data
re-ingested does not count: the check compares content digests, not times. `pvmt status` shows the same verdict before you create, as each part's
*State* (`stale: roads re-ingested …`). Run the hinted command, then create
again with the same name. The hint is one of the following.

When every refused city simply has nothing computed under the current
settings, create exits 3 instead, a missing first step, and the error's
hint names the compute to run first: `run 'pvmt --city <slug> all compute'
first` for one city, `run 'pvmt all compute' first` for several.

`snapshot "<name>" already exists` is a refusal too: snapshots never change.
Delete it with `pvmt snapshots delete <name>` and create it again (cities that
were not recomputed keep their runs, so this is cheap), or choose another
name.

### `hint: run 'pvmt --city <slug> all compute'`

The boundary changed (every part is clipped to it), the combined pass is
stale or missing, there is no pavement compute, or two or more parts are
stale. One `all compute` recomputes them together.

### `hint: run 'pvmt --city <slug> services compute'`

Only the service coverage is stale: its facilities were re-ingested after it
was computed. `pvmt services compute` routes from them again.

### `hint: run 'pvmt --city <slug> census compute'`

Only the population is stale: the census blocks were re-ingested after it was
computed.

A **warning** such as `no fire coverage: fire_stations are ingested but never
computed` does not refuse the city. The snapshot is created without that part,
and the warning, prefixed with the city, names the command that adds it.

## Water strip errors and warnings

Ingest subtracts OSM water from the city boundary, so that `% paved` compares
fairly across cities, using the city's roads to tell water from land (see
[architecture](architecture.md#city-boundary-and-water-strip)). What can go
wrong:

- **Warning `water strip skipped: …`** (`bbox:`, `interior points:` or
  `subtract:`). The geometry step failed, so the boundary was saved with its
  water still in, and area figures are inflated. Re-run the ingest with
  `--rebuild-boundary`, or with `--force` to fetch the boundary fresh from
  upstream (see `pvmt all ingest --help`). If it recurs, the boundary itself
  is probably wrong (last item).
- **Error `water strip for <city>: water fetch failed`.** Overpass did not
  answer. Nothing was saved, so the next ingest retries. Wait a few minutes
  and re-run.
- **Error `water strip for <city>: roads fetch failed`.** Every road source
  failed while the boundary was resolved, or, for a city with
  `overpass = true`, Overpass did, even if ArcGIS answered: the OSM roads are
  the strip's land evidence, and a boundary stripped without them would be
  stored for good. Nothing was saved, so the next ingest retries. The warnings
  above it name the source; an Overpass at capacity usually answers after a
  few minutes (if it does not, see
  [Overpass busy or down](#overpass-busy-or-down-switch-instance)).
- **Warning `water strip: no road probes for <city> (fewer than 30 roads
  inside its boundary), …`.** The roads answered, but too few lie inside the
  boundary (one per 0.001° cell counts), as in a very small town, or the city
  has no road source. The strip fell back to one interior point per
  sub-polygon and can keep water that roads would have exposed. Check the
  city's `overpass` and `arcgis_url` settings.
- **Warning `Water-strip inversion recovered …` or `Water-strip rollback …`.**
  The strip left too few of the city's roads inside the boundary, so the
  complement of the strip, or the unstripped boundary, was saved instead.
  Compare the saved area with the city's land area; if it is wrong, open an
  issue with the city and the warning.
- **Error `water strip over-subtracted`, or `boundary excludes most of the
  city's own roads`.** The water polygon, or Nominatim's boundary, has the
  wrong shape. For a bad boundary, set `boundary_relation_id` (see
  [Cities](configuration.md#cities)). For bad water, the `water way:`,
  `water relation:` or `water coastline:` warnings above the error name the OSM
  element at fault (`-vv` also logs the polygons that were accepted). Open an
  issue with the city, its bbox and that warning.

## Overpass busy or down: switch instance

Lines such as `overpass-api.de busy (504), retrying in 1m0s (retry 3 of 6)` are
the client backing off from an Overpass instance at capacity; it usually
answers within minutes. When it keeps failing (every city's Overpass step
ending in `overpass request:` or `overpass returned 5xx` errors, or header
timeouts), the instance is down, and waiting does not help. Point pvmt at
another public instance for the run:

```
PVMT_OVERPASS_URL=https://overpass.private.coffee/api/interpreter pvmt all ingest
```

or set `[overpass] url` in `pvmt.toml` (see
[configuration](configuration.md#overpass-endpoint-overpass)). The run prints
`overpass endpoint: <host>` when it starts. Cached responses are per instance,
so cities already ingested replay from the cache only with the endpoint they
were fetched from: unset the variable to re-run one of them. Other
instances are listed on the OpenStreetMap wiki's Overpass API page. An endpoint
refused at load (`overpass.url "..." must be ...`) names the rule it broke; one
refused before the first fetch (`overpass endpoint ...: refusing "<host>":
resolves to ...`) resolves to a private address. `allow_private_arcgis`
does not apply to it: the hint that names it comes only with an ArcGIS
refusal.

## Disk usage: the HTTP cache keeps growing

This produces no error. Every Overpass, ArcGIS, Nominatim and TIGERweb response
is written to the HTTP cache. Nothing in the request path ever removes an
entry, and an edited bbox or config writes new entries beside the old ones. A
long-lived install can reach several GiB, even though bodies are stored
gzip-compressed. `pvmt cache prune` reclaims it: start with
`pvmt cache prune --dry-run`, which says what would go and how much it frees,
and see `pvmt cache prune --help` for the caps and unattended use. It works
even when the config or the database is broken, and touches no ingested data.
Every pruned response is downloaded again when next needed, which costs
community-funded endpoints, so prefer a size cap to a wipe. The cache can't be
turned off; to see OpenStreetMap edits sooner, shorten `[cache] ttl` or pass
`--force` to one ingest (see
[configuration](configuration.md#http-cache-cache)).

## Hexes overhang the city outline at high zoom

This produces no error either. The drawn outline is simplified
(`[export].boundary_simplify_m`), but hexes are clipped to the full boundary,
so at street zoom a hex can overhang the line by a few meters. Set the
tolerance lower, or negative to draw the exact boundary (see
[Export and hosting](configuration.md#export-and-hosting)).
