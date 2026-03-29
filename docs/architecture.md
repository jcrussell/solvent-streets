# Architecture

## Data pipeline

```mermaid
flowchart LR
    subgraph Ingest ["internal/ingest"]
        Overpass
        ArcGIS
        CSV/GeoJSON
    end

    subgraph Compute ["internal/geo + internal/resource"]
        Project["WGS84 → UTM"]
        Buffer["Buffer & union"]
        HexGrid["Hex grid"]
        RTree["R-tree intersect"]
    end

    subgraph Forecast ["internal/forecast"]
        PCI["PCI decay"]
        Cost["Cost projection"]
    end

    subgraph Output ["internal/export + internal/server"]
        Export["Static site"]
        Serve["HTTP server"]
    end

    Ingest --> DB[(SQLite)]
    DB --> Project --> Buffer --> HexGrid --> RTree --> DB
    DB --> PCI --> Cost --> DB
    DB --> Export
    DB --> Serve
```

## Factory DI

All dependencies are lazy-initialized behind `sync.Once` closures in the `Factory` struct.

```mermaid
graph TD
    Factory --> Config
    Factory --> HttpClient
    Factory --> RootDB
    Factory --> UnitSystem

    Config --> CurrentCity
    RootDB --> CityDB
    CurrentCity --> CityDB

    style Factory fill:#f5f5f5,stroke:#333
```

Commands receive a `*Factory` and call only the accessors they need. `--city` flag overrides `CurrentCity`. Multi-city commands use `ForEachCity` which creates a city-scoped factory per iteration.

## Geometry pipeline

All area math happens in projected (meter) coordinates, never in degrees.

```mermaid
flowchart TD
    GeoJSON["GeoJSON features (WGS84 lon/lat)"]
    UTM["UTMProjector → meters"]
    Width["Infer width from OSM tags"]
    Buf["Buffer LineStrings → Polygons"]
    Val["Validate via Buffer(0)"]
    Union["UnionAll"]
    Hex["HexGrid (flat-top, configurable edge)"]
    Clip["Clip hexes to city boundary"]
    RT["R-tree indexed intersection"]
    Stats["Per-hex area & % coverage"]
    Back["Reproject → WGS84 GeoJSON"]

    GeoJSON --> UTM --> Width --> Buf --> Val --> Union
    Union --> Hex --> Clip --> RT --> Stats --> Back
```

Roads: width inferred from `width` tag, `lanes` count, or classification defaults, plus parking lane addon.
Parking: polygons used directly.
Sidewalks: buffered like roads with sidewalk-specific width defaults.

## Database schema

Single file at `~/.local/share/pvmt/pvmt.db`. WAL mode. All tables scoped by `city_id`.

```mermaid
erDiagram
    cities ||--o{ features : has
    cities ||--o{ compute_results : has
    cities ||--o{ hex_stats : has
    cities ||--o{ forecast_results : has
    cities ||--o{ cohort_stats : has
    cities ||--o{ snapshots : has
    cities ||--o| city_boundaries : has

    snapshots ||--o{ compute_results : tags
    snapshots ||--o{ hex_stats : tags
    snapshots ||--o{ forecast_results : tags
    snapshots ||--o{ cohort_stats : tags

    cities {
        int id PK
        text slug UK
        text name
    }
    snapshots {
        int id PK
        int city_id FK
        text config_hash
        datetime computed_at
    }
    features {
        text id PK
        text resource_type PK
        int city_id PK
        text tags
        text geometry_json
    }
    compute_results {
        int id PK
        text resource_type
        real total_area_sqm
        int feature_count
        text geometry_json
    }
    hex_stats {
        text hex_id PK
        text resource_type PK
        int city_id PK
        real area_sqm
        real pct_covered
    }
    forecast_results {
        int id PK
        text resource_type
        int year
        real pci
        real treatment_cost
        text treatment_tier
    }
    cohort_stats {
        int id PK
        text resource_type
        text classification
        real area_sqm
        int feature_count
    }
```

## Design decisions

**Config discovery.** `pvmt.toml` is found by walking from the working directory upward to `/`. First match wins. Works from any subdirectory, like `.git`.

**Metric internals.** All areas are stored in square meters. The `--units` flag and `[display].units` config control presentation only.

**Snapshots.** Each compute run creates a snapshot with a hash of the resolved config. Results link back to their snapshot for reproducibility.

**WASM build order.** The forecast WASM binary is embedded via `go:embed`. It must be compiled (`make wasm`) before building the main binary. The Makefile enforces this dependency.

**HTTP caching.** API responses are disk-cached at `~/.cache/pvmt/http/` with a 24-hour TTL. Use `--force` on ingest to bypass.

**Overpass splitting.** Large Overpass queries auto-split into quadrants (up to depth 3 / 64 requests) and deduplicate at boundaries.

**Forecast model.** Exponential PCI decay: `PCI(t) = PCI_0 * exp(-k*t)`. Per-classification decay rates default to FHWA national averages. Costs are projected via configurable PCI-to-cost tiers. Pavement growth is modeled as linear annual increase.
