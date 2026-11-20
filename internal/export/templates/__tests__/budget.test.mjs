// The Financials budget slider and the live solvency
// headline. The slider is dollars a year, anchored to
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

// The budget slider runs in tenths of a percent of hold steady ($30M in
// solvencySim at multiplier 1): budgetAt converts dollars to a position.
const HOLD = 30_000_000;
const budgetAt = (dollars, hold = HOLD) => String(Math.round(1000 * dollars / hold));

/** @param {any} win @param {string} sel @param {string} value */
function drag(win, sel, value) {
  const el = win.document.querySelector(sel);
  el.value = value;
  el.dispatchEvent(new win.Event('input', { bubbles: true }));
  // The URL is written once the input pauses (Router.replaceSoon): stand in
  // for the pause.
  win.eval('Router.flushSoon()');
}

test('the slider starts at today\'s cited budget, in dollars', async () => {
  assert.ok(TODAY > 0, 'fixture must cite a current budget');
  const { win, $ } = await ready();
  assert.equal(lastScenario(win).annual_budget, TODAY);
  assert.match($('#budget-value').textContent, /^\$20\.0M\/yr$/);
  assert.match($('#budget-slider').getAttribute('aria-valuetext'), /^\$20\.0M a year, 67% of hold-steady$/);
  assert.match($('#budget-today').textContent, /^\(\$20\.0M\/yr today\)$/);
});

test('the anchors show today and hold-steady, and snap the budget', async () => {
  const { win, $ } = await ready();
  assert.equal($('#budget-today').hidden, false);
  assert.match($('#budget-today').textContent, /\$20\.0M/);
  assert.match($('#budget-hold').textContent, /\$30\.0M/);
  assert.equal($('#budget-slider').max, '1500', 'the range runs to 150% of hold steady');
  assert.equal($('#budget-slider').value, budgetAt(TODAY), 'the thumb is not at today\'s budget');
  // Notches every 25%, named in percent with their dollars on hover; 100% is
  // labelled by the hold-steady button, in dollars.
  const notches = Array.from(win.document.querySelectorAll('.budget-notch'), (n) => n.textContent);
  assert.deepEqual(notches, ['0%', '25%', '50%', '75%', '', '125%', '150%']);
  assert.equal(win.document.querySelector('.budget-notch.hold').title, '$30.0M/yr');
  assert.equal($('#budget-hold').textContent.trim(), '$30.0M');
  assert.match($('#budget-hold').getAttribute('aria-label'), /Hold steady/);

  $('#budget-hold').click();
  assert.equal(lastScenario(win).annual_budget, 30_000_000);
  assert.match($('#budget-slider').getAttribute('aria-valuetext'), /100% of hold-steady/);
});

test('the dollar budget stays put when Initial PCI or the multiplier move', async () => {
  const { win, $ } = await ready();
  drag(win, '#budget-slider', budgetAt(12_000_000));
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

  drag(win, '#budget-slider', budgetAt(9_000_000));
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
  assert.ok(probe.cohorts.every((c) => c.resource_type === 'roads'),
    'the roads headline must not include parking or sidewalks');
});

const params = (win) => new URLSearchParams(win.location.search);

// The range was re-sized off the budget on every input,
// so a drag past the midpoint doubled max under the pointer and ran away.
test('dragging to the top of the range does not grow it', async () => {
  const { win, $ } = await ready();
  const max = $('#budget-slider').max;
  for (const f of [0.6, 0.75, 0.9, 0.999, 1]) {
    drag(win, '#budget-slider', String(Math.round(parseFloat(max) * f)));
    assert.equal($('#budget-slider').max, max, `max moved after a drag to ${f * 100}%`);
  }
  assert.equal(lastScenario(win).annual_budget, 1.5 * HOLD, 'the end of the track is 150% of hold steady');
});

test('a ?budget= beyond the range widens it to fit', async () => {
  const { win, $ } = await ready({ url: 'https://example.test/index.html?tab=financials-tab&budget=200000000' });
  assert.equal(lastScenario(win).annual_budget, 200_000_000);
  assert.ok(parseFloat($('#budget-slider').max) >= parseFloat(budgetAt(200_000_000)), 'the thumb cannot show the linked budget');
  assert.equal(parseFloat($('#budget-slider').max) % 250, 0, 'the range grows in whole notches');
});

// A linked budget was taken as given, and the slider
// grew one notch per 25% of hold steady to fit it, so ?budget=1e15 built
// millions of DOM nodes and froze or crashed the tab. It is clamped to ten
// times the larger anchor (hold steady, $30M here), and the address bar says so.
test('a huge ?budget= is clamped and the slider keeps a bounded number of notches', async () => {
  for (const linked of ['1e15', '1e300', '900000000']) {
    const { win, $$ } = await ready({ url: 'https://example.test/index.html?tab=financials-tab&budget=' + linked });
    assert.equal(lastScenario(win).annual_budget, 10 * HOLD, 'budget=' + linked + ' was not clamped');
    const notches = $$('#budget-scale .budget-notch').length;
    assert.ok(notches <= 41, 'budget=' + linked + ': ' + notches + ' notches');
    assert.equal(params(win).get('budget'), String(10 * HOLD), 'budget=' + linked + ': the URL keeps the unclamped value');
  }
});

test('a budget at the clamp is kept as linked', async () => {
  const { win } = await ready({ url: 'https://example.test/index.html?tab=financials-tab&budget=' + (10 * HOLD) });
  assert.equal(lastScenario(win).annual_budget, 10 * HOLD);
});

// The boot replaceState ran before the budget was
// applied and stripped ?budget= from the address bar.
test('a linked ?budget= stays in the address bar', async () => {
  const { win } = await ready({ url: 'https://example.test/index.html?tab=financials-tab&budget=7500000' });
  assert.equal(params(win).get('budget'), '7500000');
});

test('a city switch drops the previous city\'s budget from the URL', async () => {
  const { win, flush } = await ready({ url: 'https://example.test/index.html?tab=financials-tab' });
  drag(win, '#budget-slider', budgetAt(9_000_000));
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
  drag(win, '#budget-slider', budgetAt(9_000_000));
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

test('a drag close to Today or Hold steady lands on it exactly', async () => {
  const { win } = await ready();
  drag(win, '#budget-slider', '1010'); // 101% of hold steady
  assert.equal(lastScenario(win).annual_budget, HOLD);
  drag(win, '#budget-slider', String(Math.round(1000 * TODAY / HOLD) - 9));
  assert.equal(lastScenario(win).annual_budget, TODAY);
  drag(win, '#budget-slider', '1100'); // 110%: no snap, a tidy figure
  assert.equal(lastScenario(win).annual_budget, 33_000_000);
});

test('a range grown past 150% labels the ends and 100% without crowding', async () => {
  const { win } = await ready({ url: 'https://example.test/index.html?tab=financials-tab&budget=100000000' });
  const labels = Array.from(win.document.querySelectorAll('.budget-notch:not(.quiet)'), (n) => n.textContent);
  assert.equal(labels[0], '0%');
  assert.ok(win.document.querySelector('.budget-notch.hold:not(.quiet)'), '100% must keep its notch');
  const notches = win.document.querySelectorAll('.budget-notch');
  assert.equal(labels.at(-1), notches[notches.length - 1].textContent, 'the end of the range must be labelled');
  assert.ok(labels.length <= 8, `${labels.length} labels crowd the track: ${labels}`);
  const pcts = Array.from(win.document.querySelectorAll('.budget-notch:not(.quiet)'),
    (n) => (n.classList.contains('hold') ? 100 : parseInt(n.textContent, 10)));
  for (let i = 1; i < pcts.length; i++) {
    assert.ok(pcts[i] - pcts[i - 1] >= 50, `labels ${pcts[i - 1]}% and ${pcts[i]}% sit too close`);
  }
});

// The spend tile counts only spend that buys
// condition. When that is below budget x years, the sub-line says how much
// was budgeted instead of calling the spend "total budget allocated".
test('the spend tile names the budget when part of it buys nothing', async () => {
  const underSpend = (input) => {
    const out = solvencySim(input);
    if (!input.solvency) {
      out.years = out.years.map((y) => ({ ...y, annual_spend: input.annual_budget / 4 }));
    }
    return out;
  };
  const { $ } = await ready({ simulate: underSpend });
  assert.match($('#hl-spend-sub').textContent, /^Used of \$[\d.]+[KMB] budgeted$/);

  const fullSpend = (input) => {
    const out = solvencySim(input);
    if (!input.solvency) {
      out.years = out.years.map((y) => ({ ...y, annual_spend: input.annual_budget }));
    }
    return out;
  };
  const h = await ready({ simulate: fullSpend });
  assert.equal(h.$('#hl-spend-sub').textContent, 'Spend that buys condition');
});
