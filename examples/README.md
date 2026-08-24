# Examples

Each subdirectory contains a `pvmt.toml` ready to use. To try one:

```
cd examples/livermore-ca
pvmt all ingest
pvmt all compute
pvmt serve
```

The showcase is metro-focused: each config covers several jurisdictions so
the **Compare** tab can rank neighboring cities against each other — the
question solvent-streets exists to answer.

## The published site: [all](all/)

`all/pvmt.toml` is the config behind <https://joncrussell.com/solvent-streets/>
and the one `make site` builds. It declares no cities of its own — a
`[[include]]` block per example unions them all into one tagged config (277
cities), so a city reached through several includes is merged once and carries
the union of their tags. See
[Including other configs](../docs/configuration.md#including-other-configs-include).

It declares no cities of its own and stores no data of its own: an included
city keeps the identity of the file that declared it, so this config reads
whatever the individual examples have already ingested and computed. Populate
the examples you want (`cd examples/<name> && pvmt all ingest && pvmt all
compute`), then run `make site`. Try a single example below before reaching for
this one.

## Featured: [livermore-ca](livermore-ca/)

Simple single-city setup with both OpenStreetMap (Overpass) and Alameda
County's ArcGIS FeatureServer. **Start here** if you're new — it's the
smallest config that exercises the full pipeline.

It is also the config the release workflow smoke-tests the built binary
against, so this directory's path is release-blocking: renaming or moving it
breaks the tag build. `TestShippedExamplesLoad` (`integration/`) fails on the
PR if you do, and also checks that every example here still loads — so a new
config validation rule cannot quietly outdate one.

## National sample: [top-50-cities](top-50-cities/)

The 50 largest US cities by 2025 Census population — a national rollup
(inspired by CityNerd) that demonstrates a large multi-city config with
per-city `hex_edge_m` overrides for geographically enormous jurisdictions.
See the example's [README](top-50-cities/README.md) for the metros-vs-cities
caveat and the Census source.

## Metro areas

Grouped by what they demonstrate. Some examples appear under more than
one heading because their configs combine techniques.

- **Multi-city / regional configs:** [bay-area-ca](bay-area-ca/) (all 98
  incorporated cities across the 9-county region, with the Alameda County
  ArcGIS feed mixed in), [greater-boston-ma](greater-boston-ma/) (~8
  cities), [denver-metro-co](denver-metro-co/) (~8 cities),
  [portland-metro-or](portland-metro-or/) (~7 cities),
  [los-angeles-ca](los-angeles-ca/) (87 of the 88 incorporated cities in
  Los Angeles County — the SoCal analogue to bay-area-ca, Overpass-only).
- **More California metros** (Overpass-only, minimal configs on the
  top-level forecast defaults): [san-diego-ca](san-diego-ca/) (~10 cities),
  [sacramento-ca](sacramento-ca/) (~8 cities), [inland-empire-ca](inland-empire-ca/)
  (~8 cities, Riverside + San Bernardino counties),
  [central-valley-ca](central-valley-ca/) (~6 cities along the Hwy-99 corridor).
- **Per-city overrides:** [bay-area-ca](bay-area-ca/) (Berkeley and
  San Jose override `hex_edge_m`), [greater-boston-ma](greater-boston-ma/)
  (compact Cambridge/Somerville drop to 60 m), [los-angeles-ca](los-angeles-ca/)
  (LA proper and Long Beach use coarser hexes than their neighbors).
- **Custom cost tiers:** [los-angeles-ca](los-angeles-ca/) (four tiers),
  [greater-boston-ma](greater-boston-ma/) (three-tier reconstruct/rehab/preventive).
- **Display units:** [portland-metro-or](portland-metro-or/) shows metric
  output via `[display].units`.
- **Hex grid tuning:** [greater-boston-ma](greater-boston-ma/) drops to 60 m
  for compact cities; [los-angeles-ca](los-angeles-ca/) goes up to 300 m for
  sprawling LA; [portland-metro-or](portland-metro-or/) sits in the middle at
  80 m; [san-diego-ca](san-diego-ca/) (San Diego at 300 m) and
  [central-valley-ca](central-valley-ca/) (Bakersfield at 250 m) push their
  largest cities up; [top-50-cities](top-50-cities/) overrides the largest
  jurisdictions.
- **Growth modeling:** [denver-metro-co](denver-metro-co/) sets
  `[forecast].growth_rate` to model an expanding Front Range road network;
  [sacramento-ca](sacramento-ca/) and [inland-empire-ca](inland-empire-ca/)
  do the same for two fast-growing California regions, and
  [san-diego-ca](san-diego-ca/) uses a smaller rate for steady coastal
  build-out.
- **Climate-tuned decay:** [denver-metro-co](denver-metro-co/) (freeze/thaw)
  and [greater-boston-ma](greater-boston-ma/) (road salt + harsh winters)
  both raise `[forecast].decay_rate` above the default;
  [inland-empire-ca](inland-empire-ca/) (heat + UV) and
  [central-valley-ca](central-valley-ca/) (heavy truck traffic) do the same
  for warmer stressors.
- **OSM admin boundary by relation:** [denver-metro-co](denver-metro-co/)
  sets `boundary_relation_id` for Denver, whose boundary Nominatim returns
  only as a point; [los-angeles-ca](los-angeles-ca/) uses it for several
  small cities Nominatim mis-types as a suburb or returns as a node
  (Industry, Rosemead, the Palos Verdes / Rolling Hills enclaves).
