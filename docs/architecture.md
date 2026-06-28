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
        Buffer["Buffer features"]
        HexGrid["Hex grid"]
        RTree["R-tree + per-hex union"]
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

Most dependencies are lazy-initialized behind `sync.OnceValues` closures in the `Factory` struct. The exception is `Logger`, which is constructed eagerly because its handler is cheap and the level is mutated through a shared `LogLevel *slog.LevelVar`.

```mermaid
graph TD
    Factory --> Config
    Factory --> HttpClient
    Factory --> RootDB
    Factory --> UnitSystem
    Factory --> Paths
    Factory --> Logger

    Config --> CurrentCity
    RootDB --> CityDB
    CurrentCity --> CityDB

    style Factory fill:#f5f5f5,stroke:#333
```

Commands receive a `*Factory` and call only the accessors they need. The root command's `PersistentPreRunE` wires the `--city/-c` and `--units` flags into `CurrentCity` and `UnitSystem` before subcommands are added, so flag-aware overrides take precedence over per-city and top-level config. Multi-city commands use `cmdutil.ForEachCity`, which builds a city-scoped factory per iteration and silently skips cities with no results.

## Geometry pipeline

All area math happens in projected (meter) coordinates, never in degrees.

```mermaid
flowchart TD
    GeoJSON["GeoJSON features (WGS84 lon/lat)"]
    UTM["UTMProjector → meters"]
    Width["Infer width from OSM tags"]
    Buf["Buffer LineStrings → Polygons"]
    Val["Validate via Buffer(0)"]
    RTIdx["R-tree index over buffered geoms"]
    Hex["HexGrid (flat-top, configurable edge)"]
    Clip["Clip hexes to city boundary"]
    PerHex["Per-hex: spatial query → local union → clip to hex"]
    Stats["Per-hex area & % coverage → hex_stats"]
    Back["Reproject hex polygons → WGS84 GeoJSON"]

    GeoJSON --> UTM --> Width --> Buf --> Val --> RTIdx
    Hex --> Clip
    RTIdx --> PerHex
    Clip --> PerHex
    PerHex --> Stats --> Back
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
        text slug
        text name
        text config_id
    }
    %% Unique key is the composite (slug, config_id), not slug alone.
    city_boundaries {
        int city_id FK
        text geometry_json
        text source
        datetime fetched_at
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
        int city_id FK
        text resource_type
        real total_area
        int feature_count
        int snapshot_id FK
    }
    hex_stats {
        int id PK
        text hex_id
        text resource_type
        int city_id FK
        int snapshot_id FK
        real area
        real pct_covered
    }
    forecast_results {
        int id PK
        int city_id FK
        text resource_type
        int year
        real pci
        real area
        real treatment_cost
        text treatment_tier
        int snapshot_id FK
    }
    cohort_stats {
        int id PK
        int city_id FK
        text resource_type
        text classification
        real area
        int feature_count
        int snapshot_id FK
    }
```

## Design decisions

**Metric internals.** All areas are stored in square meters. The `--units` flag and `[display].units` config control presentation only.

**Snapshots.** Each compute run creates a snapshot with a hash of the resolved config. Per-row `snapshot_id` columns on `hex_stats`, `compute_results`, `forecast_results`, and `cohort_stats` preserve every run's results — saves append rather than DELETE-then-INSERT, so historical snapshots stay queryable for reproducibility.

**WASM build order.** The forecast WASM binary is embedded via `go:embed`. It must be compiled (`make wasm`) before building the main binary. The Makefile enforces this dependency.

**HTTP caching.** API responses are disk-cached at `~/.cache/pvmt/http/` with a 24-hour TTL. Use `--force` on ingest to bypass.

**Overpass splitting.** Large Overpass queries auto-split into quadrants (up to depth 3 / 64 requests) and deduplicate at boundaries.

**Forecast model.** Exponential PCI decay: `PCI(t) = PCI_0 * exp(-k*t)`. Per-classification decay rates default to FHWA national averages. Costs are projected via configurable PCI-to-cost tiers. Pavement growth is modeled as linear annual increase.

**Solvency methodology.** Three roads-only metrics summarize funding adequacy, all computed on a worst-first, current-budget scenario (`internal/forecast/solvency.go`):

- **Treatment cycle.** Real pavement is treated on a multi-year cycle (~10–14 yr): each year only ~1/N of the network is scheduled, not the whole network. `Simulate` models this by gating annual need to `full-network cost / N` (`treatment_cycle_years`, default 12). Before this gating the model priced the entire network every year (an implicit 1-year cycle), which overstated the hold-steady budget 3.6×–9.6× (see `docs/validation.md` §5). Because gating is a pure `1/N` rescaling in dollar-space, the PCI trajectory is unchanged and `break_even` scales exactly as `1/N`.
- **`break_even_budget`** — the smallest constant annual budget whose final deferred backlog ≈ 0: the budget to fund the network's annually-scheduled treatment slice, i.e. keep pace with the cycle. Budget-independent of `current_budget`; always computed for roads.
- **`insolvency_year`** — the first year cumulative deferred backlog reaches one full treatment cycle of work (`cycleYears × year-1 need`), i.e. the city has deferred an entire network's worth of treatment. A do-nothing network crosses at ~N years; a city funding fraction f of each slice crosses at ~N/(1−f), so well-funded cities return null ("solvent through horizon"). It no longer saturates at year 2 (the pre-gating behavior), but on the funded side **`funding_gap` is the primary discriminator**, by design.
- **Cost basis.** Solvency dollars use the cost tiers' *bare-construction* basis (validation §3), not loaded municipal program cost (which runs ~2–3× higher); they are an order-of-magnitude planning figure, not a budget line item. The default tiers and cycle are calibration inputs — override per city when local figures differ.

**TUI progress.** `internal/tui` is a single bubbletea `StepModel` rendering a phase checklist, log tail, warnings panel, and progress bar in one view — spinner frames and bar are inline, not standalone widgets, so it carries zero extra deps. (This deviates from the byob-progress.3 recipe's `bubbles/spinner` + `schollz/progressbar` behind a `Progress` interface, which earns its ceremony only for isolated ad-hoc progress events, not an already-unified program.)
