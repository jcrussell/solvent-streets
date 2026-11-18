// The Financials model settings in the URL (solvent-streets-ern4.31):
// ?strategy=, ?pci= (Initial PCI), ?cost= (the cost multiplier) and ?tiers=
// (min-max:$/sq m per tier). A shared or reloaded link must reproduce the
// forecast the reader set up; the city's own seed needs no URL.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON } from './fixtures.mjs';

const BASE = 'https://example.test/index.html?tab=financials-tab';

async function ready(query = '') {
  const h = load({ data: fullData, url: BASE + query });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  return h;
}

const params = (win) => new URLSearchParams(win.location.search);
const MODEL_KEYS = ['strategy', 'pci', 'cost', 'tiers'];
const modelParams = (win) => MODEL_KEYS.map((k) => params(win).get(k));

// model is the part of the last forecast simulation the settings drive.
function model(win) {
  const c = win.__simCalls.filter((s) => !s.solvency && !s.scenarios).at(-1);
  assert.ok(c, 'no forecast was simulated');
  return {
    strategy: c.strategy,
    initial_pci: c.initial_pci,
    cost_overhead: c.cost_overhead,
    cost_tiers: Array.from(c.cost_tiers, (t) => ({ min_pci: t.min_pci, max_pci: t.max_pci, cost_per_sqm: t.cost_per_sqm })),
  };
}

const SEED_PCI = Math.round(seedJSON.initial_pci);
const SEED_COST = seedJSON.cost_overhead > 0 ? seedJSON.cost_overhead : 1;
const SEED_TIERS = seedJSON.cost_tiers.map((t) => ({ min_pci: t.min_pci, max_pci: t.max_pci, cost_per_sqm: t.cost_per_sqm }));
const seedModel = { strategy: 'worst-first', initial_pci: SEED_PCI, cost_overhead: SEED_COST, cost_tiers: SEED_TIERS };
const PCI = SEED_PCI === 60 ? 61 : 60;
const COST = SEED_COST === 1.5 ? 2 : 1.5;

/** @param {any} win @param {string} sel @param {string} value */
function type(win, sel, value) {
  const el = win.document.querySelector(sel);
  el.value = value;
  el.dispatchEvent(new win.Event('input', { bubbles: true }));
}

// setByHand moves every model control off the seed.
function setByHand(win) {
  win.document.querySelector('#strategy-buttons button[data-strategy="preventive-first"]').click();
  type(win, '#pci-slider', String(PCI));
  type(win, '#overhead-slider', String(COST));
  type(win, '#tier-inputs [data-tier="0"][data-field="cost_per_sqm"]', '7.25');
  // A gap below the top of a tier is legal; an overlap would not be run.
  type(win, '#tier-inputs [data-tier="0"][data-field="max_pci"]', String(Math.min(100, SEED_TIERS[0].max_pci) - 1));
}

test('the fixture seed has tiers to edit', () => {
  assert.ok(SEED_TIERS.length >= 2, 'the specs below edit the first two tiers');
});

test('the seed\'s settings are not written to the URL', async () => {
  const { win } = await ready();
  assert.deepEqual(model(win), seedModel);
  assert.deepEqual(modelParams(win), [null, null, null, null]);
});

test('a linked setting equal to the seed is tidied out of the URL', async () => {
  const { win } = await ready('&strategy=worst-first&pci=' + SEED_PCI + '&cost=' + SEED_COST);
  assert.deepEqual(model(win), seedModel);
  assert.deepEqual(modelParams(win), [null, null, null, null]);
});

test('a reload of the URL reproduces the forecast set by hand', async () => {
  const { win } = await ready();
  setByHand(win);
  const byHand = model(win);
  assert.equal(byHand.strategy, 'preventive-first');
  assert.equal(byHand.initial_pci, PCI);
  assert.equal(byHand.cost_overhead, COST);
  assert.equal(byHand.cost_tiers[0].max_pci, Math.min(100, SEED_TIERS[0].max_pci) - 1);
  assert.notEqual(byHand.cost_tiers[0].cost_per_sqm, SEED_TIERS[0].cost_per_sqm);
  for (const k of MODEL_KEYS) assert.ok(params(win).get(k), `?${k}= was not written`);

  const reloaded = await ready(win.location.search.replace(/^\?/, '&'));
  assert.deepEqual(model(reloaded.win), byHand, 'the reloaded link simulates a different forecast');
  assert.ok(reloaded.$('#strategy-buttons button[data-strategy="preventive-first"]').classList.contains('active'));
  assert.equal(reloaded.$('#pci-value').textContent, String(PCI));
  assert.deepEqual(modelParams(reloaded.win), modelParams(win), 'the boot dropped linked settings from the URL');
});

test('the tiers are metric in the URL whatever the display units', async () => {
  const tiers = SEED_TIERS.map((t, i) => t.min_pci + '-' + t.max_pci + ':' + (i === 0 ? 12.5 : t.cost_per_sqm)).join(',');
  const { win } = await ready('&tiers=' + tiers);
  assert.equal(model(win).cost_tiers[0].cost_per_sqm, 12.5);
  assert.equal(params(win).get('tiers'), tiers);
});

test('the settings are written on the Financials tab only', async () => {
  const { win } = await ready();
  setByHand(win);
  win.selectTab('map-tab');
  assert.deepEqual(modelParams(win), [null, null, null, null]);
  win.selectTab('financials-tab');
  assert.ok(params(win).get('pci'));
});

test('a city switch resets the settings to the new city\'s seed and drops them from the URL', async () => {
  const { win, $, flush } = await ready();
  setByHand(win);
  win.selectCity('beta-ca');
  assert.equal(params(win).get('city'), 'beta-ca');
  assert.deepEqual(modelParams(win), [null, null, null, null], 'beta-ca\'s entry carries alpha\'s settings');
  await flush();
  assert.deepEqual(model(win), seedModel);
  assert.deepEqual(modelParams(win), [null, null, null, null]);
  assert.ok($('#strategy-buttons button[data-strategy="worst-first"]').classList.contains('active'));
});

test('malformed settings are ignored and fall back to the seed', async () => {
  for (const q of [
    '&strategy=bogus&pci=abc&cost=-1&tiers=garbage',
    '&pci=&cost=99&tiers=0-50:10',
    '&pci=101&cost=0&tiers=' + SEED_TIERS.map((t) => t.min_pci + '-' + t.max_pci + ':-3').join(','),
    '&tiers=0-60:1,50-101:2' + ',70-80:3'.repeat(Math.max(0, SEED_TIERS.length - 2)),
  ]) {
    const { win } = await ready(q);
    assert.deepEqual(model(win), seedModel, q);
    assert.deepEqual(modelParams(win), [null, null, null, null], q);
  }
});

test('back/forward restores the settings of the history entry', async () => {
  const { win, $, flush } = await ready();
  const city = params(win).get('city');
  const tiers = SEED_TIERS.map((t) => t.min_pci + '-' + t.max_pci + ':' + t.cost_per_sqm * 2).join(',');
  win.dispatchEvent(new win.PopStateEvent('popstate', {
    state: { city, tab: 'financials-tab', tags: {}, strategy: 'preventive-first', pci: String(PCI), cost: String(COST), tiers },
  }));
  await flush();
  assert.deepEqual(model(win), {
    strategy: 'preventive-first', initial_pci: PCI, cost_overhead: COST,
    cost_tiers: SEED_TIERS.map((t) => ({ ...t, cost_per_sqm: t.cost_per_sqm * 2 })),
  });
  assert.ok($('#strategy-buttons button[data-strategy="preventive-first"]').classList.contains('active'));

  win.dispatchEvent(new win.PopStateEvent('popstate', { state: { city, tab: 'financials-tab', tags: {} } }));
  await flush();
  assert.deepEqual(model(win), seedModel, 'an entry without settings must restore the seed');
});

test('back/forward to another city applies the entry\'s settings over that city\'s seed', async () => {
  const { win, flush } = await ready();
  win.dispatchEvent(new win.PopStateEvent('popstate', {
    state: { city: 'beta-ca', tab: 'financials-tab', tags: {}, strategy: 'preventive-first', pci: String(PCI) },
  }));
  await flush();
  assert.equal(params(win).get('city') || win.document.getElementById('city-select').value, 'beta-ca');
  const m = model(win);
  assert.equal(m.strategy, 'preventive-first');
  assert.equal(m.initial_pci, PCI);
});
