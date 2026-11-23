// The Financials status quo against the reader's what-if. The status quo
// runs at the city's cited budget, worst-first, and fills the tiles under
// the headline; the controls' run fills the tiles beneath them. A budget off
// it or another strategy makes a what-if, drawn beside it and named above
// the controls with the way back. Initial PCI and the cost multiplier are
// assumptions about the city: both runs (and the headline) follow them, and
// moving them alone is no what-if.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load, defaultSimulate } from './harness.mjs';
import { fullData, forecastJSON, seedJSON } from './fixtures.mjs';

const TODAY = forecastJSON.find((f) => f.resource_type === 'roads').current_budget;
const SEED_PCI = seedJSON.initial_pci;

// sim gives each run a PCI that names its budget and strategy, so a spec can
// tell which run a line or tile came from: 40 + $M, plus 1 for preventive
// first, shifted by Initial PCI's distance from the seed's.
function sim(input) {
  const out = defaultSimulate(input);
  if (input.solvency) {
    out.break_even_budget = 30_000_000;
    out.tipping_point_year = 7;
    return out;
  }
  const pci = 40 + (input.annual_budget || 0) / 1e6 + (input.strategy === 'preventive_first' ? 1 : 0) + ((input.initial_pci ?? SEED_PCI) - SEED_PCI);
  out.years.forEach((y) => { y.pci = pci; });
  return out;
}
const pciOf = (budget, strategy = 'worst_first', initial = SEED_PCI) =>
  40 + budget / 1e6 + (strategy === 'preventive_first' ? 1 : 0) + (initial - SEED_PCI);

async function ready(opts = {}) {
  const h = load({ data: fullData, simulate: sim, url: 'https://example.test/index.html?tab=financials', ...opts });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  return h;
}

/** @param {any} win @param {string} sel @param {string} value */
function drag(win, sel, value) {
  const el = win.document.querySelector(sel);
  el.value = value;
  el.dispatchEvent(new win.Event('input', { bubbles: true }));
  win.eval('Router.flushSoon()');
}
// The slider runs in tenths of a percent of hold steady ($30M above).
const budgetAt = (dollars) => String(Math.round(1000 * dollars / 30_000_000));

/** The interactive lines of the PCI chart, by label, as their first value. */
function pciLines(win) {
  // The live chart: each render draws new ones.
  const chart = win.__charts.filter((c) => c.options?.scales?.y?.max === 100).at(-1);
  const want = [win.eval('TODAY_LINE'), win.eval('WHATIF_LINE')];
  return Object.fromEntries(chart.data.datasets.filter((d) => want.includes(d.label)).map((d) => [d.label, d.data[0]]));
}

const endPCI = ($, pre) => Number($('#' + pre + '-end-pci').textContent);

test('at the status quo the charts draw today\'s budget alone and there is nothing to go back to', async () => {
  assert.ok(TODAY > 0, 'fixture must cite a current budget');
  const { win, $ } = await ready();
  assert.deepEqual(pciLines(win), { [win.eval('TODAY_LINE')]: pciOf(TODAY) });
  assert.equal($('#whatif-reset').hidden, true);
  assert.equal(endPCI($, 'hl'), pciOf(TODAY), 'the status quo tiles do not show the status quo');
  assert.equal(endPCI($, 'wi'), pciOf(TODAY), 'the controls\' tiles do not show the controls\' run');
});

test('a budget off today\'s draws the what-if beside the status quo and compares the tiles', async () => {
  const { win, $ } = await ready();
  const what = 6_000_000; // on a slider step
  drag(win, '#budget-slider', budgetAt(what));
  assert.deepEqual(pciLines(win), {
    [win.eval('TODAY_LINE')]: pciOf(TODAY),
    [win.eval('WHATIF_LINE')]: pciOf(what),
  });
  assert.equal($('#whatif-reset').hidden, false, 'no way back from the what-if');
  assert.ok($('#whatif-what').textContent.includes(win.PvmtCommon.fmtMoney(what)), 'the what-if is not named');
  // The status quo's tiles stay; the controls' tiles show the what-if.
  assert.equal(endPCI($, 'hl'), pciOf(TODAY));
  assert.equal(endPCI($, 'wi'), pciOf(what));
  // The headline stays at today's budget.
  const probe = win.__simCalls.filter((c) => c.solvency && c.annual_budget > 0).at(-1);
  assert.equal(probe.annual_budget, TODAY);
});

test('another strategy alone is a what-if', async () => {
  const { win, $, doc } = await ready();
  doc.querySelector('#strategy-buttons [data-strategy="preventive_first"]').click();
  assert.deepEqual(pciLines(win), {
    [win.eval('TODAY_LINE')]: pciOf(TODAY),
    [win.eval('WHATIF_LINE')]: pciOf(TODAY, 'preventive_first'),
  });
  assert.equal($('#whatif-reset').hidden, false);
  assert.equal(endPCI($, 'wi'), pciOf(TODAY, 'preventive_first'));
});

test('Back to today\'s budget returns the budget and the strategy, and keeps Initial PCI', async () => {
  const { win, $, doc } = await ready();
  drag(win, '#pci-slider', String(SEED_PCI - 5));
  drag(win, '#budget-slider', budgetAt(6_000_000));
  doc.querySelector('#strategy-buttons [data-strategy="preventive_first"]').click();
  assert.equal($('#whatif-reset').hidden, false);

  $('#whatif-reset').click();
  assert.equal($('#whatif-reset').hidden, true);
  assert.deepEqual(pciLines(win), { [win.eval('TODAY_LINE')]: pciOf(TODAY, 'worst_first', SEED_PCI - 5) });
  const params = new URLSearchParams(win.location.search);
  assert.equal(params.get('budget'), null, 'the what-if budget stayed in the URL');
  assert.equal(params.get('strategy'), null, 'the what-if strategy stayed in the URL');
  assert.equal(params.get('pci'), String(SEED_PCI - 5), 'the reset dropped the Initial PCI assumption');
});

test('Initial PCI alone is no what-if: the status quo follows it', async () => {
  const { win, $ } = await ready();
  drag(win, '#pci-slider', String(SEED_PCI - 5));
  assert.deepEqual(pciLines(win), { [win.eval('TODAY_LINE')]: pciOf(TODAY, 'worst_first', SEED_PCI - 5) });
  assert.equal($('#whatif-reset').hidden, true);
  assert.equal(endPCI($, 'hl'), pciOf(TODAY, 'worst_first', SEED_PCI - 5), 'the status quo tiles ignored Initial PCI');
});

test('without a cited budget there is no status quo: the controls\' run is the line', async () => {
  const forecast = structuredClone(forecastJSON);
  for (const f of forecast) delete f.current_budget;
  const { win, $ } = await ready({ data: { ...fullData, 'forecast.json': forecast } });
  const lines = pciLines(win);
  assert.deepEqual(Object.keys(lines), [win.eval('WHATIF_LINE')]);
  assert.equal($('#whatif-reset').hidden, true, 'a way back to a status quo that does not exist');
  assert.equal($('#headlines').style.display, 'none', 'status quo tiles without a status quo');
  assert.equal($('#wi-tiles').hidden, false);
});
