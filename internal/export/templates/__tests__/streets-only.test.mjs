// The Financials forecast covers streets only: the network the roads
// headline and the cited "Today" budget describe. Parking lots and
// sidewalks in the seed stay out of every figure the tab draws.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load, defaultSimulate } from './harness.mjs';
import { fullData, seedJSON } from './fixtures.mjs';

const types = (cohorts) => [...new Set(Array.from(cohorts ?? [], (c) => c.resource_type))].sort();

// refSim answers reference-set requests with a recognizable baseline, and
// solvency probes with a hold-steady budget proportional to the cohort area,
// so a spec can see both follow the maintained network.
function refSim(input) {
  const out = defaultSimulate(input);
  if (input.solvency) {
    out.break_even_budget = (input.cohorts ?? []).reduce((s, c) => s + c.area, 0);
    out.tipping_point_year = 7;
  }
  if (input.scenarios) {
    out.scenarios = ['baseline', 'fund-25pct', 'fund-50pct', 'fund-100pct'].map((name, i) => ({
      scenario: { name, label: name, strategy: i === 0 ? 'do_nothing' : 'worst_first' },
      years: [{ year: 1, pci: 11 + i, annual_need: 1, annual_spend: 0, deferred_backlog: 0, cost_tier: 'rehab', area: 1 }],
    }));
  }
  return out;
}

async function ready(url, data = fullData) {
  const h = load({ data, simulate: refSim, url: 'https://example.test/index.html?tab=financials-tab' + (url || '') });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  return h;
}

const custom = (win) => win.__simCalls.filter((c) => !c.solvency && !c.scenarios).at(-1);
const params = (win) => new URLSearchParams(win.location.search);

test('the fixture seed has every resource type', () => {
  assert.deepEqual(types(seedJSON.city_cohorts), ['parking', 'roads', 'sidewalks'],
    'the specs below need a seed with parking and sidewalk cohorts');
});

test('the forecast covers streets only, and draws streets-only reference lines', async () => {
  const { win, $ } = await ready();
  assert.deepEqual(types(custom(win).cohorts), ['roads']);
  const hold = win.__simCalls.filter((c) => c.solvency && c.annual_budget === 0).at(-1);
  assert.deepEqual(types(hold.cohorts), ['roads'], 'Hold steady must price the streets');

  const ref = win.__simCalls.filter((c) => c.scenarios).at(-1);
  assert.ok(ref, 'streets-only needs re-simulated reference lines');
  assert.deepEqual(types(ref.cohorts), ['roads']);
  assert.equal(ref.initial_pci, seedJSON.initial_pci, 'reference lines must use the seeded settings');
  const pci = win.__charts.find((c) => c.data.datasets.some((d) => d.label === 'baseline'));
  assert.ok(pci, 'the charts did not draw the re-simulated reference set');

  assert.equal($('#budget-share').textContent, '', 'the headline\'s streets need no note');
});

// Links from before the forecast went streets-only can carry ?maintain=;
// it no longer changes anything, and is not written back.
test('an old ?maintain= link still forecasts streets only', async () => {
  const { win } = await ready('&maintain=roads,parking,sidewalks');
  assert.deepEqual(types(custom(win).cohorts), ['roads']);
  const headline = win.__simCalls.filter((c) => c.solvency && c.annual_budget > 0).at(-1);
  assert.deepEqual(types(headline.cohorts), ['roads']);
  assert.equal(params(win).get('maintain'), null, 'the dropped parameter was written back');
});

// The page selects cohorts by the seed's resource_type,
// not by guessing from the class name. A parking cohort whose class is not
// literally "parking" must still stay out of the streets-only forecast.
test('streets-only follows resource_type, not the class name', async () => {
  const seed = structuredClone(seedJSON);
  const lot = seed.city_cohorts.find((c) => c.resource_type === 'parking');
  lot.classification = 'surface_lot';
  const { win } = await ready('', { ...fullData, 'forecast_seed.json': seed });
  const sentClasses = (calls) => Array.from(calls.cohorts ?? [], (c) => c.classification);
  assert.ok(!sentClasses(custom(win)).includes('surface_lot'), 'a parking cohort reached the streets forecast');
  const headline = win.__simCalls.filter((c) => c.solvency && c.annual_budget > 0).at(-1);
  assert.ok(!sentClasses(headline).includes('surface_lot'), 'a parking cohort reached the roads headline');
});

// Which cohorts a figure prices is one rule (seedCohortsFor): under the city
// scope, the city-maintained cohorts of each resource, which is none for a
// resource the city maintains none of. Only a city with no city-scope cohorts
// at all uses every jurisdiction's.
const roadsOf = (list) => list.filter((c) => c.resource_type === 'roads');
const areaOf = (list) => list.reduce((s, c) => s + c.area, 0);
const headlineProbe = (win) => win.__simCalls.filter((c) => c.solvency && c.annual_budget > 0).at(-1);
const holdProbe = (win) => win.__simCalls.filter((c) => c.solvency && c.annual_budget === 0).at(-1);

test('a city that maintains no roads has no roads in its city view', async () => {
  const seed = structuredClone(seedJSON);
  seed.city_cohorts = seed.city_cohorts.filter((c) => c.resource_type !== 'roads');
  assert.ok(seed.city_cohorts.length > 0 && roadsOf(seed.cohorts).length > 0, 'fixture cannot build the case');
  const { win } = await ready('', { ...fullData, 'forecast_seed.json': seed });

  // Every jurisdiction's roads are not borrowed for the headline or the slider.
  assert.equal(headlineProbe(win), undefined, 'the roads headline was priced with no city-maintained roads');
  for (const call of win.__simCalls) {
    assert.ok(!types(call.cohorts).includes('roads'), 'a simulation priced roads the city does not maintain');
  }
});

test('a city with no city-scope cohorts at all uses every jurisdiction\'s', async () => {
  const seed = structuredClone(seedJSON);
  delete seed.city_cohorts;
  const { win } = await ready('', { ...fullData, 'forecast_seed.json': seed });
  assert.deepEqual(Array.from(custom(win).cohorts), roadsOf(seed.cohorts));
});

test('under ?scope=all the slider\'s hold-steady budget says it covers all jurisdictions', async () => {
  const city = roadsOf(seedJSON.city_cohorts);
  const all = roadsOf(seedJSON.cohorts);
  assert.notEqual(areaOf(city), areaOf(all), 'fixture scopes must differ');
  const { win, $ } = await ready('&scope=all');

  // Two different networks on one page: the headline prices the streets the
  // cited budget pays for, the slider the scope's.
  assert.deepEqual(Array.from(headlineProbe(win).cohorts), city);
  assert.deepEqual(Array.from(holdProbe(win).cohorts), all);
  // Wider streets than the headline's: the slider carries a note, which the
  // city scope does not (the next test).
  assert.notEqual($('#budget-share').textContent, '', 'the slider does not mark the wider streets');
  assert.equal($('#budget-slider').getAttribute('aria-describedby'), 'budget-share');
});

test('under the city scope the headline and the slider price the same streets', async () => {
  const { win, $ } = await ready();
  assert.deepEqual(Array.from(headlineProbe(win).cohorts), Array.from(holdProbe(win).cohorts));
  assert.equal($('#budget-share').textContent, '');
});
