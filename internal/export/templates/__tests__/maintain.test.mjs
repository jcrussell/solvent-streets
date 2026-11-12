// What the interactive Financials forecast maintains (solvent-streets-ma98):
// streets only by default, the network the roads headline and the cited
// "Today" budget describe; Advanced options can add parking lots and
// sidewalks. Everything the slider drives follows the choice.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load, defaultSimulate } from './harness.mjs';
import { fullData, seedJSON } from './fixtures.mjs';

const typeOf = (k) => (k === 'parking' || k === 'sidewalks' ? k : 'roads');
const types = (cohorts) => [...new Set(Array.from(cohorts ?? [], (c) => typeOf(c.classification)))].sort();

// refSim answers reference-set requests with a recognizable baseline, and
// solvency probes with a hold-steady budget proportional to the cohort area,
// so a spec can see both follow the maintained network.
function refSim(input) {
  const out = defaultSimulate(input);
  if (input.solvency) {
    out.break_even_budget = (input.cohorts ?? []).reduce((s, c) => s + c.area, 0);
    out.insolvency_year = 7;
  }
  if (input.scenarios) {
    out.scenarios = ['baseline', 'fund-25pct', 'fund-50pct', 'fund-100pct'].map((name, i) => ({
      scenario: { name, label: name, strategy: i === 0 ? 0 : 1 },
      years: [{ year: 1, pci: 11 + i, annual_need: 1, annual_spend: 0, deferred_backlog: 0, cost_tier: 'rehab', area: 1 }],
    }));
  }
  return out;
}

async function ready(url) {
  const h = load({ data: fullData, simulate: refSim, url: 'https://example.test/index.html?tab=financials-tab' + (url || '') });
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

test('by default the forecast maintains streets only, and draws streets-only reference lines', async () => {
  const { win, $ } = await ready();
  assert.deepEqual(types(custom(win).cohorts), ['roads']);
  const hold = win.__simCalls.filter((c) => c.solvency && c.annual_budget === 0).at(-1);
  assert.deepEqual(types(hold.cohorts), ['roads'], 'Hold steady must price the maintained network');

  const ref = win.__simCalls.filter((c) => c.scenarios).at(-1);
  assert.ok(ref, 'streets-only needs re-simulated reference lines');
  assert.deepEqual(types(ref.cohorts), ['roads']);
  assert.equal(ref.initial_pci, seedJSON.initial_pci, 'reference lines must use the seeded settings');
  const pci = win.__charts.find((c) => c.data.datasets.some((d) => d.label === 'baseline'));
  assert.ok(pci, 'the charts did not draw the re-simulated reference set');

  assert.equal(params(win).get('maintain'), null, 'the default needs no URL');
  assert.equal($('#budget-share').textContent, '', 'the default network needs no note');
});

test('the checkboxes add parking and sidewalks, and the URL records it', async () => {
  const { win, $, doc } = await ready();
  const box = (t) => doc.querySelector(`#maintain-toggles input[data-type="${t}"]`);
  assert.equal(box('roads').checked, true);
  assert.equal(box('roads').disabled, true, 'the last maintained type must not be unchecked');
  assert.equal(box('parking').checked, false);

  box('parking').checked = true;
  box('parking').dispatchEvent(new win.Event('change', { bubbles: true }));
  assert.deepEqual(types(custom(win).cohorts), ['parking', 'roads']);
  assert.equal(params(win).get('maintain'), 'roads,parking');
  assert.equal($('#budget-share').textContent, 'for streets + lots');
  assert.equal(box('roads').disabled, false);
});

test('maintaining everything uses the exported reference lines', async () => {
  const { win } = await ready('&maintain=roads,parking,sidewalks');
  assert.deepEqual(types(custom(win).cohorts), ['parking', 'roads', 'sidewalks']);
  assert.equal(win.__simCalls.filter((c) => c.scenarios).length, 0,
    'scenarios.json already covers the whole network');
});

test('?maintain= naming nothing this city has falls back to streets', async () => {
  const { win } = await ready('&maintain=bogus');
  assert.deepEqual(types(custom(win).cohorts), ['roads']);
});

test('the solvency headline stays streets-only whatever is maintained', async () => {
  const { win } = await ready('&maintain=roads,parking,sidewalks');
  const headline = win.__simCalls.filter((c) => c.solvency && c.annual_budget > 0).at(-1);
  assert.deepEqual(types(headline.cohorts), ['roads']);
});
