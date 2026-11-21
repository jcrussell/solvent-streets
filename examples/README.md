# Examples

Each directory here holds a ready-to-run `pvmt.toml`, and each config's
header comment says what it covers and which settings it demonstrates. They
come in two kinds. Most are metro configs, covering one metro area so the
dashboard's **Compare** tab can rank neighbouring cities against each other —
the question solvent-streets exists to answer. The rest are national rollups
that pick cities by a category instead of by place, so Compare ranks cities
that share a size, a role or a name across the whole country. To try one:

```
cd examples/livermore-ca
pvmt all ingest
pvmt all compute
pvmt snapshots create 2026-11
pvmt serve
```

**Start with [livermore-ca](livermore-ca/).** It is the smallest config that
exercises the whole pipeline: one city, from OpenStreetMap.

**[all](all/)** is the config behind <https://joncrussell.com/solvent-streets/>.
It declares no cities of its own: it `[[include]]`s every other example into one
tagged dashboard, and reads the data those examples have already ingested and
computed (see
[Including other configs](../docs/configuration.md#including-other-configs-include)).
Populate the examples you want, then run `make site`.

The national rollups:

- **[top-50-cities](top-50-cities/)**: the 50 largest US cities, ranked as
  cities rather than metros, for the reasons its config's header gives.
- **[state-capitals-us](state-capitals-us/)**: the 50 state capitals, which
  reach down to Montpelier, VT, a city of under 10,000.
- **[college-towns-us](college-towns-us/)**: the 59 towns of 20,000 or more
  where college and graduate students are at least a quarter of residents
  (ACS 2023 5-year), where a small tax base carries a network sized for a
  large daytime population, on the finest grid of any example. Davis,
  Boulder, Berkeley and Tallahassee are also in other examples, so in the
  published site each appears under both of its groups; the cost is a second
  standalone ingest of those four.
- **[springfields-us](springfields-us/)**: the 21 states whose incorporated
  Springfield resolves to a boundary polygon, each a city or town that shares
  only its name with the rest. It
  shows why every example names its cities `"City, ST"`: the state suffix is
  what gives each one its own slug (see
  [Cities](../docs/configuration.md#cities)).

No example sets `allow_private_arcgis`, on purpose. It turns off the guard
against private and loopback ArcGIS addresses (see
[Cities](../docs/configuration.md#cities)), and a shipped config that set it
would read as a recommendation.
