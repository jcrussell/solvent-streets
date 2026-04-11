# Examples

Each subdirectory contains a `pvmt.toml` ready to use. To try one:

```
cd examples/livermore-ca
pvmt all ingest
pvmt all compute
pvmt serve
```

| Example | Description |
|---------|-------------|
| [livermore-ca](livermore-ca/) | Simple single-city with Overpass + ArcGIS |
| [bay-area-ca](bay-area-ca/) | Multi-city Bay Area with per-city overrides |
| [los-angeles-ca](los-angeles-ca/) | Multi-city LA area with custom cost tiers |
| [austin-tx](austin-tx/) | Single city, growth rate for expanding network |
| [portland-or](portland-or/) | Single city, metric display units |
| [denver-co](denver-co/) | Single city, higher decay rate for cold climate |
| [chicago-il](chicago-il/) | Large city with coarser hex grid |
| [washington-dc](washington-dc/) | Compact city with fine hex detail |
| [nashville-tn](nashville-tn/) | Fast-growing city with high growth rate |
| [boston-ma](boston-ma/) | Harsh winters with custom cost tiers |
