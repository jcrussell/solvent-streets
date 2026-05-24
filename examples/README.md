# Examples

Each subdirectory contains a `pvmt.toml` ready to use. To try one:

```
cd examples/livermore-ca
pvmt all ingest
pvmt all compute
pvmt serve
```

## Featured: [livermore-ca](livermore-ca/)

Simple single-city setup with both OpenStreetMap (Overpass) and Alameda
County's ArcGIS FeatureServer. **Start here** if you're new — it's the
smallest config that exercises the full pipeline.

## National sample: [city-nerd](city-nerd/)

The 50 largest US cities by 2025 Census population — a CityNerd-flavored
rollup that demonstrates a large multi-city config (with per-city
`hex_edge_m` overrides for geographically enormous jurisdictions). See
the example's [README](city-nerd/README.md) for the metros-vs-cities
caveat and the Census source.

## Other examples

Grouped by what they demonstrate. Some examples appear under more than
one heading because their configs combine techniques.

- **Multi-city configs:** [bay-area-ca](bay-area-ca/) (all 98
  incorporated cities across the 9-county region, with the Alameda
  County ArcGIS feed mixed in), [los-angeles-ca](los-angeles-ca/)
  (3 cities, Overpass-only).
- **Per-city overrides:** [bay-area-ca](bay-area-ca/) (Berkeley and
  San Jose override `hex_edge_m`), [los-angeles-ca](los-angeles-ca/)
  (LA proper uses a coarser hex than its neighbors).
- **Custom cost tiers:** [los-angeles-ca](los-angeles-ca/) (four tiers),
  [boston-ma](boston-ma/) (three-tier reconstruct/rehab/preventive).
- **Display units:** [portland-or](portland-or/) shows metric output via
  `[display].units`.
- **Hex grid tuning:** [chicago-il](chicago-il/) uses a 200 m edge for a
  large city; [washington-dc](washington-dc/) drops to 60 m for a
  compact one; [portland-or](portland-or/) sits in the middle at 80 m.
- **Growth modeling:** [austin-tx](austin-tx/) and
  [nashville-tn](nashville-tn/) set `[forecast].growth_rate` to model
  expanding road networks.
- **Climate-tuned decay:** [denver-co](denver-co/) (freeze/thaw),
  [boston-ma](boston-ma/) (road salt + harsh winters), and
  [chicago-il](chicago-il/) all raise `[forecast].decay_rate` above
  the default.
