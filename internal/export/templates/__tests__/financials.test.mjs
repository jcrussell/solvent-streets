// Regression locks for the two app.js fixes that shipped without any executing
// coverage: solvent-streets-sqcg (forecast controls) and solvent-streets-i9pt
// (redundant WASM simulations). Both were invisible to eslint and tsc.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData } from './fixtures.mjs';

// ready boots a window, loads the Financials data, and brings WASM up.
async function ready() {
  const h = load({ data: fullData });
  await h.flush();
  h.win.onWasmReady();     // sets wasmReady, reveals controls, runs the first sim
  await h.flush();
  return h;
}

// solvent-streets-sqcg. loadFinancials strips .visible from #forecast-controls
// on every teardown, and onWasmReady — which only fires once per page load —
// used to be the only thing that put it back. So the first city switch after
// WASM came up hid the PCI slider and cost-tier inputs for the rest of the
// session.
test('forecast controls survive a city switch', async () => {
  const { win, $, flush } = await ready();
  assert.ok($('#forecast-controls').classList.contains('visible'),
    'controls should be visible once WASM is up');

  await win.loadFinancials();   // the teardown-and-refetch path a city switch takes
  await flush();

  assert.ok($('#forecast-controls').classList.contains('visible'),
    'controls vanished after a city switch and nothing re-reveals them');

  // Twice, because the bug only bit on switches AFTER the one-shot onWasmReady.
  await win.loadFinancials();
  await flush();
  assert.ok($('#forecast-controls').classList.contains('visible'),
    'controls vanished on the second city switch');
});

// The controls must stay hidden when this city's seed did not load:
// FORECAST_SEED and the inputs still describe the PREVIOUS city, so showing
// them would invite edits against the wrong parameters.
test('forecast controls stay hidden when the seed is missing', async () => {
  const noSeed = { ...fullData };
  delete noSeed['forecast_seed.json'];
  const h = load({ data: noSeed });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  await h.win.loadFinancials();
  await h.flush();

  assert.equal(h.$('#forecast-controls').classList.contains('visible'), false,
    'controls revealed without this city\'s seed');
});

// solvent-streets-i9pt. Three call sites each ran runCustomScenario twice —
// renderFinancialsFromCache, selectScope, and the renderFinancials wrapper
// firing inside both. Each runCustomScenario is TWO simulateForecast calls (the
// year-1 baseline inside getControlValues, then the result), so a render cost
// four sims where two would do.
test('a cache re-render runs exactly one custom scenario', async () => {
  const { win, resetSimCalls, simCalls, flush } = await ready();

  resetSimCalls();
  win.renderFinancialsFromCache();
  await flush();

  assert.equal(simCalls(), 2,
    `expected 2 simulateForecast calls (one runCustomScenario), got ${simCalls()}`);
});

test('a scope change runs exactly one custom scenario', async () => {
  const { win, resetSimCalls, simCalls, flush } = await ready();

  resetSimCalls();
  win.selectScope('bbox', { push: false });
  await flush();

  assert.equal(simCalls(), 2,
    `expected 2 simulateForecast calls (one runCustomScenario), got ${simCalls()}`);
});

// The dedupe must not silently drop the re-run: the headline tiles are written
// by runCustomScenario AFTER renderFinancialsFromCache's baseline write, so
// suppressing the wrong one leaves baseline numbers under a custom chart line.
test('the custom scenario, not the baseline, has the last word on the tiles', async () => {
  const { win, flush } = await ready();

  // A distinguishable custom result: spend far above the baseline's.
  win.simulateForecast = (json) => {
    const input = JSON.parse(json);
    return JSON.stringify({
      scenario: { name: 'custom', strategy: 0 },
      years: [{ year: 1, pci: 60, area: input.area, annual_need: 99_000_000, annual_spend: 99_000_000, deferred_backlog: 0, cost_tier: 'rehab' }],
      final_cohorts: [],
    });
  };
  win.renderFinancialsFromCache();
  await flush();

  const spend = win.document.getElementById('hl-total-spend');
  assert.ok(spend, '#hl-total-spend missing from the template');
  assert.match(spend.textContent, /99|\$/,
    'headline tiles were not written by the custom scenario');
});
