// Regression locks for app.js behavior eslint and tsc cannot see: forecast
// controls survive a city switch, and a re-render runs no redundant WASM
// simulations.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON } from './fixtures.mjs';

// ready boots a window, loads the Financials data, and brings WASM up.
async function ready(opts = {}) {
  const h = load({ data: fullData, ...opts });
  await h.flush();
  h.win.onWasmReady();     // sets wasmReady, reveals controls, runs the first sim
  await h.flush();
  return h;
}

// The whole Financials tab is four charts, and three of them are drawn from
// the scenarios scenarios.json describes. A set the page cannot read renders
// ONE card while every other spec still passes, so assert the count and the
// titles: this is the tripwire that drift trips.
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
  ], 'the funding-level charts did not render from scenarios.json');
});

// The other half of the same drift: meta.json's per-resource numbers live in
// stats[], and a fixture that put them at the top level rendered a stats panel
// with zero resource cards and Paved Footprint reading 0.0.
test('the stats panel renders a card per resource from meta.stats[]', async () => {
  const { win, $$, flush } = await ready();
  await win.loadCityData();   // renderCityStats runs off the meta.json fetch
  await flush();

  // Per-hex metric cards (fire response, …) are separate from the resource
  // cards meta.stats[] produces.
  const cards = $$('#stats .stat-card:not(.metric-card) h3').map((h) => h.textContent.toLowerCase());
  assert.deepEqual(cards.filter((c) => c !== 'city summary'), ['roads', 'parking', 'sidewalks'],
    'meta.stats[] did not produce a card per resource');

  // Read the number, do not pattern-match around it: the drift this guards
  // showed up as Paved Footprint rendering 0.0 off a missing total_paved key, and
  // "does not say 0.0" is satisfied by a panel that says nothing at all.
  const paved = $$('#stats .stat-row').find((r) => /Paved Footprint/.test(r.textContent));
  assert.ok(paved, 'no Paved Footprint row in the stats panel');
  assert.ok(parseFloat(paved.querySelector('.stat-value').textContent) > 0,
    'Paved Footprint read zero — meta.total_paved did not reach the panel');
});

// The seed's area and cohort keys are what getControlValues ships to the WASM
// bridge, and a mismatched key is INVISIBLE: `FORECAST_SEED.maintained_area` on
// a seed that spells it `area` is undefined, which JSON.stringify turns into null,
// which the bridge reads as zero area. Every dollar figure on the tab then comes
// off a network of no size, with nothing thrown anywhere. Assert the values that
// crossed, not just that a call happened.
test('the seeded area and cohorts reach the WASM bridge', async () => {
  const { win } = await ready();

  const sent = win.__simCalls.at(-1);
  assert.ok(sent, 'no simulation ran');
  // Default scope is city, so the city-maintained area is the basis. Assert
  // the seed carries the key at all first: comparing two undefineds passes.
  assert.equal(typeof seedJSON.city_maintained_area, 'number',
    'forecast_seed.json has no numeric city_maintained_area; the Go json tag moved');
  assert.equal(sent.area, seedJSON.city_maintained_area,
    'the bridge did not receive the seeded city_maintained_area');
  assert.ok(sent.area > 0, 'the bridge simulated a network of zero area');
  // By default the forecast maintains streets only.
  assert.deepEqual(
    Array.from(sent.cohorts ?? [], (c) => c.classification).sort(),
    roadClasses(seedJSON.city_cohorts),
    'the bridge did not receive the seeded city road cohorts');

  // Maintaining everything sends every seeded cohort.
  const all = await ready({ url: 'https://example.test/index.html?tab=financials-tab&maintain=roads,parking,sidewalks' });
  assert.deepEqual(
    Array.from(all.win.__simCalls.at(-1).cohorts ?? [], (c) => c.classification).sort(),
    Array.from(seedJSON.city_cohorts ?? [], (c) => c.classification).sort(),
    'the bridge did not receive the seeded city cohorts');
});

/** @param {Array<{classification: string}> | undefined} cohorts */
function roadClasses(cohorts) {
  return Array.from(cohorts ?? [], (c) => c.classification)
    .filter((k) => k !== 'parking' && k !== 'sidewalks').sort();
}

// The other half of the same key contract: the All-Roads scope reads
// maintained_area / cohorts instead. Both branches of getControlValues' scope
// selection need a live assertion, or a rename covers whichever one is unwatched.
test('the All-Roads scope simulates over the seeded all-scope area', async () => {
  const { win, flush } = await ready();

  win.selectScope('all', { push: false });
  await flush();

  const sent = win.__simCalls.at(-1);
  assert.equal(typeof seedJSON.maintained_area, 'number',
    'forecast_seed.json has no numeric maintained_area; the Go json tag moved');
  assert.equal(sent.area, seedJSON.maintained_area,
    'the bridge did not receive the seeded maintained_area in all scope');
  assert.ok(sent.area > 0, 'the bridge simulated a network of zero area');
  assert.deepEqual(
    Array.from(sent.cohorts ?? [], (c) => c.classification).sort(),
    roadClasses(seedJSON.cohorts),
    'the bridge did not receive the seeded all-scope road cohorts');
});

// loadFinancials strips .visible from #forecast-controls on every teardown,
// and onWasmReady fires only once per page load, so something else must put
// it back on a city switch — or the PCI slider and cost-tier inputs vanish for
// the rest of the session.
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

// Three call sites each ran runCustomScenario twice —
// renderFinancialsFromCache, selectScope, and the renderFinancials wrapper
// firing inside both, so a render cost two custom scenarios where one would
// do. Each runCustomScenario is one scenario simulation, plus solvency probes
// (the slider's hold-steady anchor and the live headline) that are memoised
// on their inputs, so count the scenario runs.
/** @param {any} win */
const scenarioRuns = (win) => win.__simCalls.filter((c) => !c.solvency && !c.scenarios).length;

test('a cache re-render runs exactly one custom scenario', async () => {
  const { win, resetSimCalls, flush } = await ready();

  resetSimCalls();
  win.renderFinancialsFromCache();
  await flush();

  assert.equal(scenarioRuns(win), 1,
    `expected one custom scenario, got ${scenarioRuns(win)}`);
  assert.equal(win.__simCalls.length, 1,
    'unchanged inputs must reuse the memoised solvency probes');
});

test('a scope change runs exactly one custom scenario', async () => {
  const { win, resetSimCalls, flush } = await ready();

  resetSimCalls();
  win.selectScope('all', { push: false });
  await flush();

  assert.equal(scenarioRuns(win), 1,
    `expected one custom scenario, got ${scenarioRuns(win)}`);
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

// The four Financials charts are created without the
// entry animation, because the interactive series is pushed with
// update('none') right after creation and that froze the other series
// mid-animation, drawn below their data.
test('the Financials charts are built without the entry animation', async () => {
  const { win, flush } = await ready();
  win.selectTab('financials-tab');
  await flush();
  // renderFinancials builds the four charts last, in this order.
  const financial = win.__charts.slice(-4);
  assert.equal(financial.length, 4, `expected the four Financials charts, got ${financial.length}`);
  for (const c of financial) {
    assert.equal(c.options.animation, false, 'a Financials chart animates its first draw');
  }
});

// Financials renders once scenarios.json, forecast_seed.json and forecast.json
// have all landed. They are fetched at once, so a slow scenarios.json does not
// hold back the other two requests, and the tab still renders only when the
// last one arrives.
test('the Financials files are fetched together and render once all land', async () => {
  const { win, $$, flush, hold, release, fetches } = load({ data: fullData });
  await flush();
  win.selectTab('financials-tab');
  await flush();

  // Clear the first load's charts, so what is drawn below is this load's.
  win.document.getElementById('charts').innerHTML = '';
  hold('scenarios.json');
  const before = fetches().length;
  const loading = win.loadFinancials();
  await flush();
  const asked = fetches().slice(before);
  for (const f of ['scenarios.json', 'forecast_seed.json', 'forecast.json']) {
    assert.ok(asked.some((p) => p.endsWith(f)), `${f} was not requested while scenarios.json was in flight: ${asked}`);
  }
  assert.equal($$('#charts h3').length, 0, 'Financials rendered before scenarios.json landed');

  release('scenarios.json');
  await loading;
  await flush();
  assert.equal($$('#charts h3').length, 4, 'Financials did not render once every file landed');
});
