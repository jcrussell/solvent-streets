// Regression locks for the two app.js fixes that shipped without any executing
// coverage: solvent-streets-sqcg (forecast controls) and solvent-streets-i9pt
// (redundant WASM simulations). Both were invisible to eslint and tsc.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON } from './fixtures.mjs';

// ready boots a window, loads the Financials data, and brings WASM up.
async function ready() {
  const h = load({ data: fullData });
  await h.flush();
  h.win.onWasmReady();     // sets wasmReady, reveals controls, runs the first sim
  await h.flush();
  return h;
}

// solvent-streets-q48z.8. The whole Financials tab is four charts, and three of
// them are gated on FUNDING_SCENARIO_NAMES matching the scenario names in
// scenarios.json. When the fixtures were hand-written they said
// `do-nothing`/`maintain` while the exporter emits `baseline`/`fund-25pct`/...,
// so this tab rendered ONE card under test and every spec below still passed.
// Assert the count and the titles: this is the tripwire that drift trips.
test('all four Financials charts render from real exporter data', async () => {
  const { win, $$, flush } = await ready();
  win.selectTab('financials-tab');
  await flush();

  const titles = $$('#charts h3').map((h) => h.textContent);
  assert.deepEqual(titles, [
    'PCI Over Time by Funding Level',
    'Deferred Maintenance Backlog',
    'Cumulative Spending',
    'Annual Treatment Need by Condition Tier',
  ], 'the funding-level charts did not render; scenario names in scenarios.json ' +
     'no longer match FUNDING_SCENARIO_NAMES');
});

// The other half of the same drift: meta.json's per-resource numbers live in
// stats[], and a fixture that put them at the top level rendered a stats panel
// with zero resource cards and Total Paved reading 0.0.
test('the stats panel renders a card per resource from meta.stats[]', async () => {
  const { win, $$, flush } = await ready();
  await win.loadCityData();   // renderCityStats runs off the meta.json fetch
  await flush();

  const cards = $$('#stats .stat-card h3').map((h) => h.textContent.toLowerCase());
  assert.deepEqual(cards.filter((c) => c !== 'city summary'), ['roads', 'parking', 'sidewalks'],
    'meta.stats[] did not produce a card per resource');

  // Read the number, do not pattern-match around it: the drift this guards
  // showed up as Total Paved rendering 0.0 off a missing total_paved key, and
  // "does not say 0.0" is satisfied by a panel that says nothing at all.
  const paved = $$('#stats .stat-row').find((r) => /Total Paved/.test(r.textContent));
  assert.ok(paved, 'no Total Paved row in the stats panel');
  assert.ok(parseFloat(paved.querySelector('.stat-value').textContent) > 0,
    'Total Paved read zero — meta.total_paved did not reach the panel');
});

// The seed's area and cohort keys are what getControlValues ships to the WASM
// bridge, and a mismatched key is INVISIBLE: `FORECAST_SEED.total_area` on a
// seed that spells it `area` is undefined, which JSON.stringify turns into null,
// which the bridge reads as zero area. Every dollar figure on the tab then comes
// off a network of no size, with nothing thrown anywhere. Assert the values that
// crossed, not just that a call happened.
test('the seeded area and cohorts reach the WASM bridge', async () => {
  const { win } = await ready();

  const sent = win.__simCalls.at(-1);
  assert.ok(sent, 'no simulation ran');
  // Default scope is city, so the city-scoped paved area is the basis. Assert
  // the seed carries the key at all first: comparing two undefineds passes.
  assert.equal(typeof seedJSON.city_paved, 'number',
    'forecast_seed.json has no numeric city_paved; the Go json tag moved');
  assert.equal(sent.area, seedJSON.city_paved,
    'the bridge did not receive the seeded city_paved area');
  assert.ok(sent.area > 0, 'the bridge simulated a network of zero area');
  assert.deepEqual(
    Array.from(sent.cohorts ?? [], (c) => c.classification).sort(),
    Array.from(seedJSON.city_cohorts ?? [], (c) => c.classification).sort(),
    'the bridge did not receive the seeded city cohorts');
});

// The other half of the same key contract: the All-Roads scope reads
// total_area / cohorts instead. Both branches of getControlValues' scope
// selection need a live assertion, or a rename covers whichever one is unwatched.
test('the All-Roads scope simulates over the seeded bbox area', async () => {
  const { win, flush } = await ready();

  win.selectScope('bbox', { push: false });
  await flush();

  const sent = win.__simCalls.at(-1);
  assert.equal(typeof seedJSON.total_area, 'number',
    'forecast_seed.json has no numeric total_area; the Go json tag moved');
  assert.equal(sent.area, seedJSON.total_area,
    'the bridge did not receive the seeded total_area in bbox scope');
  assert.ok(sent.area > 0, 'the bridge simulated a network of zero area');
  assert.deepEqual(
    Array.from(sent.cohorts ?? [], (c) => c.classification).sort(),
    Array.from(seedJSON.cohorts ?? [], (c) => c.classification).sort(),
    'the bridge did not receive the seeded bbox cohorts');
});

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
