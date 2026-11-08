# Examples

Each directory here holds a ready-to-run `pvmt.toml`, and each config's
header comment says what it covers and which settings it demonstrates. Most
cover a whole metro area, so the dashboard's **Compare** tab can rank
neighbouring cities against each other — the question solvent-streets exists
to answer. To try one:

```
cd examples/livermore-ca
pvmt all ingest
pvmt all compute
pvmt serve
```

**Start with [livermore-ca](livermore-ca/).** It is the smallest config that
exercises the whole pipeline: one city, with both OpenStreetMap and Alameda
County's ArcGIS centerlines.

**[all](all/)** is the config behind <https://joncrussell.com/solvent-streets/>.
It declares no cities of its own: it `[[include]]`s every other example into one
tagged dashboard, and reads the data those examples have already ingested and
computed (see
[Including other configs](../docs/configuration.md#including-other-configs-include)).
Populate the examples you want, then run `make site`.

**[top-50-cities](top-50-cities/)** is the national sample: the 50 largest US
cities, ranked as cities rather than metros, for the reasons its config's
header gives.
