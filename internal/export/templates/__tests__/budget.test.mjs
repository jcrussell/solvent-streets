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
    out.break_even_budget = 30_000_000 * (input.cost_multiplier || 1);
    out.tipping_point_year = 7;
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
  // The readout, the screen-reader text and the Today anchor all carry the
  // dollars; the screen-reader text adds the share of hold steady ($30M).
  const money = win.PvmtCommon.fmtMoney(TODAY);
  assert.equal(money, '$20.0M');
  assert.ok($('#budget-value').textContent.includes(money));
  const spoken = $('#budget-slider').getAttribute('aria-valuetext');
  assert.ok(spoken.includes(money), spoken);
  assert.match(spoken, /\b67%/);
  assert.equal($('#budget-today .num').textContent, money);
});

test('the anchors show today and hold-steady, and snap the budget', async () => {
  const { win, $ } = await ready();
  assert.equal($('#budget-today').hidden, false);
  assert.equal($('#budget-today .num').textContent, '$20.0M');
  assert.equal($('#budget-hold .num').textContent, '$30.0M');
  assert.equal($('#budget-slider').max, '1500', 'the range runs to 150% of hold steady');
  assert.equal($('#budget-slider').value, budgetAt(TODAY), 'the thumb is not at today\'s budget');
  // Notches every 25%, named in percent with their dollars on hover; 100% is
  // labelled by the hold-steady button, in dollars.
  const notches = Array.from(win.document.querySelectorAll('.budget-notch'), (n) => n.textContent);
  assert.deepEqual(notches, ['0%', '25%', '50%', '75%', '', '125%', '150%']);
  assert.ok(win.document.querySelector('.budget-notch.hold').title.includes('$30.0M'));
  assert.equal($('#budget-hold').textContent.trim(), '$30.0M');
  // The button's visible text is only the dollars, so its accessible name
  // says more than the figure, and carries the figure too.
  const name = $('#budget-hold').getAttribute('aria-label');
  assert.ok(name.includes('$30.0M') && name.length > '$30.0M'.length, name);

  $('#budget-hold').click();
  assert.equal(lastScenario(win).annual_budget, 30_000_000);
  assert.match($('#budget-slider').getAttribute('aria-valuetext'), /\b100%/);
});

test('the dollar budget stays put when Initial PCI or the multiplier move', async () => {
  const { win, $ } = await ready();
  drag(win, '#budget-slider', budgetAt(12_000_000));
  assert.equal(lastScenario(win).annual_budget, 12_000_000);

  drag(win, '#pci-slider', '60');
  assert.equal(lastScenario(win).annual_budget, 12_000_000,
    'moving Initial PCI rescaled the dollar budget');
  drag(win, '#multiplier-slider', '2');
  assert.equal(lastScenario(win).annual_budget, 12_000_000,
    'moving the cost multiplier rescaled the dollar budget');
  // The anchor follows the inputs instead.
  assert.equal($('#budget-hold .num').textContent, '$60.0M');
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
  assert.match(headline(), /\$30\.0M/, 'the headline is not the live figure');
  // It leads with the share of the hold-steady budget today's funds: $20M of $30M.
  assert.equal(win.fundedShareText(TODAY, HOLD), '66%');
  assert.equal(doc.querySelector('#solvency-headline .lead .num').textContent, '66%', 'the headline leads with the share funded');
  // The headline leaves the condition path to the first tile, which runs
  // from Initial PCI to the PCI of the run the tiles show, at the horizon.
  const tile = (id) => Number(doc.getElementById(id).textContent);
  assert.equal(tile('hl-start-pci'), Number(win.inputById('pci-slider').value));
  assert.ok(tile('hl-end-pci') < tile('hl-start-pci'), 'fixture: the default run decays');
  assert.match(doc.getElementById('hl-pci-sub').textContent,
    new RegExp('\\b' + (win.forecastYearOne() + win.financialsHorizon() - 1) + '\\b'));

  drag(win, '#multiplier-slider', '2');
  assert.match(headline(), /\$60\.0M/, 'the headline ignored the multiplier');
  // The basis note carries the slider's multiplier, and does not attribute
  // it to the city's config (which it marks up as <code>).
  const basis = doc.getElementById('cost-regime-note');
  assert.match(basis.querySelector('strong').textContent, /\b2\.0\b/, 'the basis note must carry the multiplier');
  assert.equal(basis.querySelector('code'), null, 'a slider multiplier was attributed to the config');

  // The headline probe is roads-only at today's budget, worst-first.
  const probe = win.__simCalls.filter((c) => c.solvency && c.annual_budget === TODAY).at(-1);
  assert.ok(probe, 'no headline probe at today\'s budget');
  assert.equal(probe.strategy, 'worst_first');
  assert.ok(probe.cohorts.every((c) => c.resource_type === 'roads'),
    'the roads headline must not include parking or sidewalks');
});

// detailFigures are the figures the headline's detail line sets apart: the
// year the repairs put off add up to a full cycle of upkeep.
const detailFigures = (doc) => Array.from(doc.querySelectorAll('#solvency-headline .detail .num'), (n) => n.textContent);

// The tipping point links to its definition on the About tab, and following
// the link opens that tab at that heading.
test('the tipping point links to the About tab', async () => {
  const { doc } = await ready();
  const link = doc.querySelector('#solvency-headline a[data-about-link]');
  assert.ok(link, 'the fixture crosses within the forecast, yet no link');
  const id = link.getAttribute('href').slice(1);
  assert.ok(doc.querySelector('#about-tab #' + id), 'the link names no heading on the About tab: ' + id);
  link.click();
  assert.ok(doc.getElementById('about-tab').classList.contains('active'), 'the link did not open the About tab');
});

// The share funded rounds down under 100%, so a city short of need never
// reads as fully funded.
test('fundedShareText rounds down short of need', async () => {
  const { win } = await ready();
  assert.equal(win.fundedShareText(999, 1000), '99%');
  assert.equal(win.fundedShareText(1000, 1000), '100%');
  assert.equal(win.fundedShareText(1256, 1000), '126%');
});

// Forecast year 1 is the snapshot's year, not the viewer's: the insolvency
// year a page names stays the same whenever it is read.
test('the solvency headline dates insolvency from the snapshot year', async () => {
  const data = { ...fullData, 'meta.json': { ...fullData['meta.json'], snapshot_date: '2020-06-01' } };
  const { doc, fireMap, flush } = await ready({ data });
  fireMap('style.load'); await flush(10);
  // solvencySim's backlog passes a full cycle in year 7; year 1 is 2020.
  assert.ok(detailFigures(doc).includes('2026'), 'the detail does not date the full-cycle backlog: ' + detailFigures(doc));
});

test('a snapshot_date that is not a date dates insolvency from the viewer\'s year', async () => {
  const data = { ...fullData, 'meta.json': { ...fullData['meta.json'], snapshot_date: '12345' } };
  const { doc, fireMap, flush, win } = await ready({ data });
  fireMap('style.load'); await flush(10);
  const want = new win.Date().getFullYear() + 6;
  assert.ok(detailFigures(doc).includes(String(want)), 'the detail does not date the full-cycle backlog: ' + detailFigures(doc));
});

const params = (win) => new URLSearchParams(win.location.search);

// The range is not re-sized on drag (only a linked budget widens it):
// re-sizing it off the budget would double max under the pointer on a drag
// past the midpoint.
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

// A linked budget is clamped to ten times the larger anchor (hold steady, $30M
// here), and the address bar says so. The slider grows one notch per 25% of
// hold steady to fit a budget, so an unclamped ?budget=1e15 would build
// millions of DOM nodes and freeze the tab.
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
  const { $, win } = await ready({ simulate: underSpend });
  const budgeted = win.PvmtCommon.fmtMoney(TODAY * win.financialsHorizon());
  const under = $('#hl-spend-sub').textContent;
  assert.ok(under.includes(budgeted), `the sub-line does not name the ${budgeted} budgeted: ${under}`);
  assert.ok(!under.includes($('#hl-total-spend').textContent), 'the sub-line repeats the spend instead of the budget');

  const fullSpend = (input) => {
    const out = solvencySim(input);
    if (!input.solvency) {
      out.years = out.years.map((y) => ({ ...y, annual_spend: input.annual_budget }));
    }
    return out;
  };
  const h = await ready({ simulate: fullSpend });
  const full = h.$('#hl-spend-sub').textContent;
  assert.ok(full.trim().length > 0);
  assert.doesNotMatch(full, /\$/, 'a fully spent budget is still set against the spend: ' + full);
});
