// Ambient declarations for the globals the pages provide to app.js / game.js
// before those scripts run: the maplibre + Chart.js CDN bundles, the Go WASM
// runtime from wasm_exec.js, and the window.PVMT_CONFIG payload injected by the
// template (see TemplateData.IndexConfigJSON / GameConfigJSON in
// internal/export/meta.go). The CDN surfaces are `any` so tsc --checkJs focuses
// on the extracted code's own correctness, not on modelling third-party APIs we
// don't control; the boundaries we DO own (PVMT_CONFIG shape, the Go WASM
// exports) are typed so their call sites get checked.

declare const maplibregl: any;
declare const Chart: any;

declare class Go {
  importObject: WebAssembly.Imports;
  run(instance: WebAssembly.Instance): void;
}

// A Go WASM export registered on window after pvmt.wasm boots (see the
// js.Global().Set calls in cmd/wasm/pvmt/main.go): JSON-string in, JSON-string
// out, may throw. Declared optional on Window because they are absent until the
// runtime has run — app.js/game.js guard for that.
type WasmFn = (...args: any[]) => string;

// window.PVMT_CONFIG, injected inline by the template as the whole config
// payload. Shape is the union of IndexConfigJSON (index.html) and GameConfigJSON
// (play.html); a given page only populates its own subset, so page-specific
// fields are optional. forecastSeed/layerColors are opaque json.RawMessage
// blobs consumed loosely (forecastSeed is even reassigned from fetched JSON), so
// they stay `any` — typing them fully is high effort, low payoff under checkJs.
// One city as both pages' PVMT_CONFIG.cities carries it (jsCityInfo in
// meta.go). bbox/lon/lat are present on every emitted entry; they are optional
// here only so selectors.js can also take hand-built test cities.
interface PvmtCity {
  slug: string;
  name: string;
  bbox?: [number, number, number, number];
  lon?: number;
  lat?: number;
  tags?: string[];
}

// One tag dimension (default "group" first), each value carrying the full tag
// string a city's tags contain plus its display label (jsTagDimension).
interface PvmtTagDimension {
  name: string;
  label: string;
  values: Array<{ tag: string; label: string }>;
}

// One entry of hex-metrics.json's metrics map (export.MetricJSON).
interface PvmtHexMetric {
  label: string;
  unit: string;
  format: string;
  source: string;
  scale: { kind: string; transform: string; bands: number[] | null };
  threshold_s?: number;
  values: Record<string, number>;
  refs?: Record<string, number>;
  points?: Array<{ id: string; name: string; lon: number; lat: number; source: string }>;
}

// window.PvmtSelectors, assigned by selectors.js (loaded before the page
// script); the shared tag-filter + city-select logic both pages call.
interface PvmtSelectorsAPI {
  DEFAULT_DIM: string;
  normalizeTag(tag: string): string;
  dimensionOf(tag: string): string;
  parseTags(params: URLSearchParams, dims: PvmtTagDimension[] | undefined): Record<string, string>;
  urlTags(selected: Record<string, string>, dims: PvmtTagDimension[] | undefined): string[];
  matches(cityTags: string[] | undefined, selected: Record<string, string>): boolean;
  filterCities<T extends { tags?: string[] }>(cities: T[], selected: Record<string, string>): T[];
  readFilters(root?: ParentNode): Record<string, string>;
  writeFilters(selected: Record<string, string>, root?: ParentNode): void;
  sameFilters(a: Record<string, string>, b: Record<string, string>): boolean;
  rebuildCitySelect(sel: HTMLSelectElement, cities: PvmtCity[], dims: PvmtTagDimension[] | undefined, selected: Record<string, string>, prefer?: string): string;
}
declare const PvmtSelectors: PvmtSelectorsAPI;

interface PvmtConfig {
  center: [number, number];
  cities?: PvmtCity[];
  tagDimensions?: PvmtTagDimension[];
  layerColors?: any;
  unitSystem?: string;
  dataPrefix?: string;
  mapHref?: string;
  bandCount?: number;
  wasmPrefix: string;
  forecastSeed: any;
}

interface Window {
  PVMT_CONFIG: PvmtConfig;
  PvmtSelectors: PvmtSelectorsAPI;
  // Test seam for the play page's navigation (see game.js navigateTo).
  __pvmtNavigate?: (u: URL) => void;
  _map?: any; // debug/resize handle to the live maplibre Map (app.js sets window._map = map)
  simulateForecast?: WasmFn;
  gameInit?: WasmFn;
  gameTick?: WasmFn;
  gameTreat?: WasmFn;
  gameTreatBatch?: WasmFn;
  gameSetBudget?: WasmFn;
  gameProjectInsolvency?: WasmFn;
}
