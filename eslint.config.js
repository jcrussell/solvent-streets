import js from '@eslint/js';
import globals from 'globals';

// Flat config scoped to the browser JS extracted from the export templates.
// Each page loads exactly one of these as its whole application script, so
// top-level declarations sharing the global scope is intentional (no module
// wrapper) — hence sourceType 'script' and no no-implicit-globals rule.
export default [
  {
    files: ['internal/export/templates/*.js'],
    languageOptions: {
      ecmaVersion: 2023,
      sourceType: 'script',
      globals: {
        ...globals.browser,
        // Loaded before app.js/game.js: maplibre + Chart.js from CDN, and the
        // Go WASM runtime constructor from wasm_exec.js.
        maplibregl: 'readonly',
        Chart: 'readonly',
        Go: 'readonly',
      },
    },
    rules: {
      ...js.configs.recommended.rules,
      // Flag dead locals/functions (the payoff: real dead code), but not unused
      // function args or caught-error bindings, which are common and low-signal.
      'no-unused-vars': ['error', { args: 'none', caughtErrors: 'none' }],
      eqeqeq: ['error', 'smart'],
    },
    // no-use-before-define is deliberately NOT enabled: this code legitimately
    // references init-block `let`s (FORECAST_SEED, wasmReady, currentCharts) from
    // functions/listeners that only run after init, so the lexical rule is all
    // false positives here. Real temporal-dead-zone use is better caught by
    // tsc --checkJs flow analysis (see tsconfig.json), adopted incrementally.
  },
];
