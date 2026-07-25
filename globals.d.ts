// Ambient declarations for the globals the pages provide to app.js / game.js
// before those scripts run: the maplibre + Chart.js CDN bundles, the Go WASM
// runtime from wasm_exec.js, and the window.PVMT_CONFIG payload injected by the
// template (see TemplateData.IndexConfigJSON / GameConfigJSON). Typed as `any`
// so tsc --checkJs focuses on the extracted code's own correctness, not on
// modelling third-party surfaces we don't control.

declare const maplibregl: any;
declare const Chart: any;

declare class Go {
  importObject: WebAssembly.Imports;
  run(instance: WebAssembly.Instance): void;
}

interface Window {
  PVMT_CONFIG: any;
}
