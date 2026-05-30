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

## Featured: [livermore-ca](livermore-ca/)

Simple single-city setup with both OpenStreetMap (Overpass) and Alameda
County's ArcGIS FeatureServer. **Start here** if you're new — it's the
smallest config that exercises the full pipeline.

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
  [los-angeles-ca](los-angeles-ca/) (~8 cities, Overpass-only).
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
  80 m; [top-50-cities](top-50-cities/) overrides the largest jurisdictions.
- **Growth modeling:** [denver-metro-co](denver-metro-co/) sets
  `[forecast].growth_rate` to model an expanding Front Range road network.
- **Climate-tuned decay:** [denver-metro-co](denver-metro-co/) (freeze/thaw)
  and [greater-boston-ma](greater-boston-ma/) (road salt + harsh winters)
  both raise `[forecast].decay_rate` above the default.
- **OSM admin boundary by relation:** [denver-metro-co](denver-metro-co/)
  sets `boundary_relation_id` for Denver, whose boundary Nominatim returns
  only as a point.
