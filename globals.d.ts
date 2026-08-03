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
interface PvmtConfig {
  center: [number, number];
  cities?: Array<{ slug: string; name: string; bbox: [number, number, number, number]; lon: number; lat: number; tags?: string[] }>;
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
  _map?: any; // debug/resize handle to the live maplibre Map (app.js sets window._map = map)
  simulateForecast?: WasmFn;
  gameInit?: WasmFn;
  gameTick?: WasmFn;
  gameTreat?: WasmFn;
  gameTreatBatch?: WasmFn;
  gameSetBudget?: WasmFn;
  gameProjectInsolvency?: WasmFn;
}
