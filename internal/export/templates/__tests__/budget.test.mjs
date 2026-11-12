// The Financials budget slider (solvent-streets-i68t) and the live solvency
// headline (solvent-streets-a6ci). The slider is dollars a year, anchored to
// today's cited budget and the hold-steady budget of the network it
// simulates; the headline follows the live Initial PCI and cost multiplier.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load, defaultSimulate } from './harness.mjs';
import { fullData, forecastJSON } from './fixtures.mjs';

const roads = forecastJSON.find((f) => f.resource_type === 'roads');
const TODAY = roads.current_budget;

// solvencySim answers solvency probes with a hold-steady budget that scales
// with the cost multiplier, so a spec can see the probes follow the inputs.
function solvencySim(input) {
  const out = defaultSimulate(input);
  if (input.solvency) {
    out.break_even_budget = 30_000_000 * (input.cost_overhead || 1);
    out.insolvency_year = 7;
  }
  return out;
}

async function ready(opts = {}) {
  const h = load({ data: fullData, simulate: solvencySim, ...opts });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  return h;
}

/** @param {any} win */
const lastScenario = (win) => win.__simCalls.filter((c) => !c.solvency).at(-1);

/** @param {any} win @param {string} sel @param {string} value */
function drag(win, sel, value) {
  const el = win.document.querySelector(sel);
  el.value = value;
  el.dispatchEvent(new win.Event('input', { bubbles: true }));
}

test('the slider starts at today\'s cited budget, in dollars', async () => {
  assert.ok(TODAY > 0, 'fixture must cite a current budget');
  const { win, $ } = await ready();
  assert.equal(lastScenario(win).annual_budget, TODAY);
  assert.match($('#budget-value').textContent, /^\$20\.0M\/yr$/);
  assert.match($('#budget-share').textContent, /67% of hold-steady/);
});

test('the anchors show today and hold-steady, and snap the budget', async () => {
  const { win, $ } = await ready();
  assert.equal($('#budget-today').hidden, false);
  assert.match($('#budget-today').textContent, /\$20\.0M/);
  assert.match($('#budget-hold').textContent, /\$30\.0M/);
  assert.ok(parseFloat($('#budget-slider').max) >= 60_000_000,
    'the range must reach twice the larger anchor');

  $('#budget-hold').click();
  assert.equal(lastScenario(win).annual_budget, 30_000_000);
  assert.match($('#budget-share').textContent, /100% of hold-steady/);
});

test('the dollar budget stays put when Initial PCI or the multiplier move', async () => {
  const { win, $ } = await ready();
  drag(win, '#budget-slider', '12000000');
  assert.equal(lastScenario(win).annual_budget, 12_000_000);

  drag(win, '#pci-slider', '60');
  assert.equal(lastScenario(win).annual_budget, 12_000_000,
    'moving Initial PCI rescaled the dollar budget');
  drag(win, '#overhead-slider', '2');
  assert.equal(lastScenario(win).annual_budget, 12_000_000,
    'moving the cost multiplier rescaled the dollar budget');
  // The anchor follows the inputs instead.
  assert.match($('#budget-hold').textContent, /\$60\.0M/);
});

test('?budget= seeds the slider and a drag writes it back', async () => {
  const { win } = await ready({ url: 'https://example.test/index.html?tab=financials-tab&budget=7500000' });
  assert.equal(lastScenario(win).annual_budget, 7_500_000);

  drag(win, '#budget-slider', '9000000');
  assert.equal(new URL(win.location.href).searchParams.get('budget'), '9000000');
});

test('the solvency headline follows the cost multiplier', async () => {
  const { win, doc } = await ready();
  const headline = () => doc.getElementById('solvency-headline').textContent;
  assert.match(headline(), /\$30\.0M\/yr/, 'the headline is not the live figure');

  drag(win, '#overhead-slider', '2');
  assert.match(headline(), /\$60\.0M\/yr/, 'the headline ignored the multiplier');
  assert.match(headline(), /2.*cost multiplier.*slider/i, 'the basis note must name the slider');

  // The headline probe is roads-only at today's budget, worst-first.
  const probe = win.__simCalls.filter((c) => c.solvency && c.annual_budget === TODAY).at(-1);
  assert.ok(probe, 'no headline probe at today\'s budget');
  assert.equal(probe.strategy, 'worst-first');
  assert.ok(probe.cohorts.every((c) => c.classification !== 'parking' && c.classification !== 'sidewalks'),
    'the roads headline must not include parking or sidewalks');
});

const params = (win) => new URLSearchParams(win.location.search);

// solvent-streets-5298: the range was re-sized off the budget on every input,
// so a drag past the midpoint doubled max under the pointer and ran away.
test('dragging to the top of the range does not grow it', async () => {
  const { win, $ } = await ready();
  const max = $('#budget-slider').max;
  for (const f of [0.6, 0.75, 0.9, 1]) {
    drag(win, '#budget-slider', String(parseFloat(max) * f));
    assert.equal($('#budget-slider').max, max, `max moved after a drag to ${f * 100}%`);
  }
  assert.equal(lastScenario(win).annual_budget, parseFloat(max));
});

test('a ?budget= beyond the range widens it to fit', async () => {
  const { win, $ } = await ready({ url: 'https://example.test/index.html?tab=financials-tab&budget=900000000' });
  assert.equal(lastScenario(win).annual_budget, 900_000_000);
  assert.ok(parseFloat($('#budget-slider').max) >= 900_000_000, 'the thumb cannot show the linked budget');
});

// solvent-streets-n2y3: the boot replaceState ran before the budget was
// applied and stripped ?budget= from the address bar.
test('a linked ?budget= stays in the address bar', async () => {
  const { win } = await ready({ url: 'https://example.test/index.html?tab=financials-tab&budget=7500000' });
  assert.equal(params(win).get('budget'), '7500000');
});

test('a city switch drops the previous city\'s budget from the URL', async () => {
  const { win, flush } = await ready({ url: 'https://example.test/index.html?tab=financials-tab' });
  drag(win, '#budget-slider', '9000000');
  assert.equal(params(win).get('budget'), '9000000');

  // Read the pushed entry before the load settles: a later replaceState can
  // tidy the address bar, but the history entry keeps what push wrote.
  win.selectCity('beta-ca');
  assert.equal(params(win).get('city'), 'beta-ca');
  assert.equal(params(win).get('budget'), null, 'beta-ca\'s history entry carries alpha\'s budget');
  await flush();
  assert.equal(params(win).get('budget'), null);
});

test('back/forward restores the budget of the history entry', async () => {
  const { win, flush } = await ready({ url: 'https://example.test/index.html?tab=financials-tab' });
  drag(win, '#budget-slider', '9000000');
  const city = params(win).get('city');
  win.dispatchEvent(new win.PopStateEvent('popstate', {
    state: { city, tab: 'financials-tab', budget: '5000000', tags: {} },
  }));
  await flush();
  assert.equal(lastScenario(win).annual_budget, 5_000_000);

  win.dispatchEvent(new win.PopStateEvent('popstate', { state: { city, tab: 'financials-tab', tags: {} } }));
  await flush();
  assert.equal(lastScenario(win).annual_budget, TODAY, 'an entry without a budget must restore the default');
});
