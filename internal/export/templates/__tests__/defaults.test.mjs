// Go owns the forecast defaults: the pages get them
// as PVMT_CONFIG.forecast_defaults and in their static markup, and the scripts
// carry no copy. The fixture config has a 20-year horizon, not the 30-year
// default, so a number written into the JS or the template shows up here.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { JSDOM } from 'jsdom';
import { load, fixtureDir } from './harness.mjs';
import { fullData, seedJSON, scenariosJSON, hexgridJSON } from './fixtures.mjs';

// served is a page as the Go template rendered it, with no script run.
function served(file) {
  return new JSDOM(readFileSync(join(fixtureDir(), file), 'utf8')).window.document;
}

const without = (obj, ...keys) => {
  const out = structuredClone(obj);
  for (const k of keys) delete out[k];
  return out;
};

test('the fixture horizon is not the default, and the page carries the seed settings as its defaults', () => {
  assert.equal(seedJSON.horizon_years, 20);
  const { win } = load();
  assert.deepEqual(JSON.parse(JSON.stringify(win.PVMT_CONFIG.forecast_defaults)), {
    initial_pci: seedJSON.initial_pci,
    horizon_years: seedJSON.horizon_years,
    treatment_cycle_years: seedJSON.treatment_cycle_years,
    barrels_per_ton_binder: seedJSON.barrels_per_ton_binder,
    pci_decimals: 2,
  });
});

// namesNumber matches a text that carries n as a whole number ("20-Year",
// "20yr", "Year 20"), not as part of a longer one.
/** @param {number} n */
const namesNumber = (n) => new RegExp('(^|[^\\d.])' + n + '([^\\d.]|$)');

test('the static markup names the configured horizon and initial PCI before any script runs', () => {
  const doc = served('index.html');
  const text = (id) => doc.getElementById(id).textContent;
  for (const id of ['hl-spend-label', 'hl-deficit-label', 'cohort-spend-th', 'cohort-deficit-th',
    'compare-cost-title', 'compare-need-title']) {
    assert.match(text(id), namesNumber(seedJSON.horizon_years), id + ' does not name the horizon');
  }

  const pci = String(Math.round(seedJSON.initial_pci));
  const slider = doc.getElementById('pci-slider');
  assert.equal(slider.getAttribute('value'), pci);
  assert.equal(slider.getAttribute('aria-valuenow'), pci);
  assert.equal(text('pci-value'), pci);
});

test('a city with no forecast to read is labelled with the page horizon', async () => {
  const { $, flush } = load({
    data: {},
    config: (cfg) => { cfg.forecast_defaults.horizon_years = 17; cfg.forecast_seed = null; },
  });
  await flush();
  assert.match($('#hl-spend-label').textContent, namesNumber(17));
  assert.match($('#cohort-deficit-th').textContent, namesNumber(17));
  assert.doesNotMatch($('#hl-spend-label').textContent, namesNumber(seedJSON.horizon_years));
});

test('Materials falls back to the page treatment cycle and oil factor for a seed without them', async () => {
  const { win, $, flush } = load({
    data: { ...fullData, 'forecast_seed.json': without(seedJSON, 'treatment_cycle_years', 'barrels_per_ton_binder') },
    config: (cfg) => {
      cfg.forecast_defaults.treatment_cycle_years = 7;
      cfg.forecast_defaults.barrels_per_ton_binder = 5.5;
    },
  });
  await flush();
  win.selectTab('materials-tab');
  await flush(12);
  assert.match($('#materials-note').textContent, /\b1\/7\b/);
  // The fixture page is imperial: 5.5 bbl per tonne is ~5 bbl per short ton.
  const imperial = { text: $('#hl-oil-sub').textContent, unit: win.massUnitSingular() };
  assert.match(imperial.text, namesNumber(5));
  assert.ok(imperial.text.includes(imperial.unit), imperial.text);
  win.selectUnits('metric');
  await flush(12);
  const metric = { text: $('#hl-oil-sub').textContent, unit: win.massUnitSingular() };
  assert.match(metric.text, namesNumber(5.5));
  assert.ok(metric.text.includes(metric.unit), metric.text);
  assert.notEqual(metric.unit, imperial.unit);
});

// The charts draw the scenarios the file describes, by the labels it gives
// them: no list of scenario names lives in the page. Financials plots the
// do-nothing and full-funding bounds; Materials the full-funding one.
test('Financials and Materials plot whatever scenarios the set describes', async () => {
  const scenarios = structuredClone(scenariosJSON);
  for (const scope of ['city', 'all']) {
    scenarios[scope].forEach((s, i) => {
      s.scenario.name = 'plan-' + i;
      s.scenario.label = 'Plan ' + i;
    });
  }
  const { win, $$, flush } = load({ data: { ...fullData, 'scenarios.json': scenarios } });
  await flush(12);
  win.selectTab('financials-tab');
  await flush();
  assert.equal($$('#charts h3').length, 4, 'a renamed scenario set dropped Financials charts');

  const all = scenarios.city.map((s) => s.scenario.label);
  // Financials draws the bounds only: the baseline (strategy do_nothing) and
  // the full-funding run, picked out by their flags, whatever their names.
  const bounds = scenarios.city
    .filter((s) => s.scenario.strategy === 'do_nothing' || s.scenario.full_funding)
    .map((s) => s.scenario.label);
  assert.ok(bounds.length === 2 && bounds.length < all.length, 'fixture lacks partial funding levels');
  // The interactive lines (today's budget, the what-if) are not reference runs.
  const interactive = [win.eval('TODAY_LINE'), win.eval('WHATIF_LINE')];
  const pci = win.__charts.find((c) => c.options?.scales?.y?.max === 100);
  assert.deepEqual(pci.data.datasets.map((d) => d.label).filter((l) => !interactive.includes(l)), bounds);
  // Cumulative Spending leaves the baseline out.
  const spending = win.__charts.filter((c) => c.config.type === 'line' && c !== pci)
    .find((c) => !c.data.datasets.some((d) => d.label === bounds[0]));
  assert.ok(spending, 'no chart drew the funded scenarios without the baseline');
  assert.deepEqual(spending.data.datasets.map((d) => d.label).filter((l) => !interactive.includes(l)), bounds.slice(1));

  win.__charts.length = 0;
  win.selectTab('materials-tab');
  await flush(12);
  const mix = win.__charts.find((c) => c.config.type === 'line');
  assert.ok(mix, 'Materials drew no funding-level chart');
  // Material is what spending lays: the do-nothing baseline lays none and
  // draws no line.
  assert.deepEqual(mix.data.datasets.map((d) => d.label), bounds.slice(1));
});

// ---- Play ---------------------------------------------------------------

async function bootBoard(seed, config) {
  const data = { ...fullData, 'forecast_seed.json': seed, 'hexgrid.geojson': hexgridJSON };
  const h = load({ page: 'play', wasm: true, data, config });
  await h.flush();
  h.fireMap('load');
  await h.flush(12);
  return h;
}

test('play: a seed without a horizon or initial PCI falls back to the page defaults', async () => {
  const { get } = await bootBoard(without(seedJSON, 'horizon_years', 'initial_pci'), (cfg) => {
    cfg.forecast_defaults.horizon_years = 44;
    cfg.forecast_defaults.initial_pci = 71;
  });
  assert.equal(get('gameConfig').horizon_years, 44);
  assert.equal(get('gameConfig').initial_pci, 71);
});

// The start screen is rendered from the page's settings, which on a
// multi-city site are the region's; the board opens on the city's own
// horizon, offering it when the page did not.
test("play: the start screen offers the city's own horizon and opens on it", async () => {
  const { $$ } = await bootBoard({ ...structuredClone(seedJSON), horizon_years: 37 });
  const offered = $$('#horizon-buttons .btn').map((b) => b.dataset.horizon);
  assert.ok(offered.includes('37'), "the city's horizon is not offered: " + offered);
  const years = offered.slice(0, -1).map(Number);
  assert.deepEqual(years, [...years].sort((a, b) => a - b), 'horizons are not ascending');
  assert.equal(offered.at(-1), 'endless');
  assert.deepEqual($$('#horizon-buttons .btn.active').map((b) => b.dataset.horizon), ['37']);
});

// The page seed is the region's on a multi-city site, so the play page's
// fallbacks do not follow it.
test('play: the start screen offers the configured horizon and opens on it', async () => {
  // Precondition: opening on the seed's horizon is distinguishable from
  // opening on the page default only while the two differ.
  assert.notEqual(load({ page: 'play' }).win.PVMT_CONFIG.forecast_defaults.horizon_years, seedJSON.horizon_years,
    'the play page defaults follow the seed');
  const offered = Array.from(served('play.html').querySelectorAll('#horizon-buttons .btn')).map((b) => b.dataset.horizon);
  assert.ok(offered.includes(String(seedJSON.horizon_years)), 'the configured horizon is not offered: ' + offered);
  assert.equal(offered.at(-1), 'endless');
  const years = offered.slice(0, -1).map(Number);
  assert.deepEqual(years, [...new Set(years)].sort((a, b) => a - b), 'horizons are not ascending and distinct');

  const { $$ } = await bootBoard(seedJSON);
  assert.deepEqual($$('#horizon-buttons .btn.active').map((b) => b.dataset.horizon), [String(seedJSON.horizon_years)]);
});
