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
  });
});

test('the static markup names the configured horizon and initial PCI before any script runs', () => {
  const doc = served('index.html');
  const text = (id) => doc.getElementById(id).textContent;
  assert.equal(text('hl-spend-label'), '20-Year Spend');
  assert.equal(text('hl-deficit-label'), '20-Year Deficit');
  assert.equal(text('cohort-spend-th'), '20yr Spend');
  assert.equal(text('cohort-deficit-th'), '20yr Deficit');
  assert.equal(text('compare-cost-title'), '20-Year Cost Trajectory');
  assert.equal(text('compare-need-title'), 'Year 1 vs Year 20 Maintenance Need');

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
  assert.equal($('#hl-spend-label').textContent, '17-Year Spend');
  assert.equal($('#cohort-deficit-th').textContent, '17yr Deficit');
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
  assert.match($('#materials-note').textContent, /~1\/7 of the network/);
  // The fixture page is imperial: 5.5 bbl per tonne is ~5 bbl per short ton.
  assert.equal($('#hl-oil-sub').textContent, '~5 bbl per ton binder');
  win.selectUnits('metric');
  await flush(12);
  assert.equal($('#hl-oil-sub').textContent, '~5.5 bbl per tonne binder');
});

// The charts draw the scenarios the file describes, by the labels it gives
// them: no list of scenario names lives in the page.
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
  const pci = win.__charts.find((c) => c.options?.scales?.y?.max === 100);
  assert.deepEqual(pci.data.datasets.map((d) => d.label), all);
  // Cumulative Spending leaves the baseline (strategy 0) out, whatever its name.
  const spending = win.__charts.filter((c) => c.config.type === 'line')
    .find((c) => c.data.datasets.length === all.length - 1);
  assert.ok(spending, 'no chart drew the funded scenarios without the baseline');
  assert.deepEqual(spending.data.datasets.map((d) => d.label), all.slice(1));

  win.__charts.length = 0;
  win.selectTab('materials-tab');
  await flush(12);
  const mix = win.__charts.find((c) => c.config.type === 'line');
  assert.ok(mix, 'Materials drew no funding-level chart');
  assert.deepEqual(mix.data.datasets.map((d) => d.label), all);
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

test('play: the board takes its PCI jitter from the page config', async () => {
  const { get } = await bootBoard(seedJSON, (cfg) => { cfg.pci_jitter = 3; });
  assert.equal(get('gameConfig').pci_jitter, 3);

  const stock = load({ page: 'play' });
  assert.ok(stock.win.PVMT_CONFIG.pci_jitter > 0, 'the page config carries no jitter');
});

test('play: a seed without a horizon or initial PCI falls back to the page defaults', async () => {
  const { get } = await bootBoard(without(seedJSON, 'horizon_years', 'initial_pci'), (cfg) => {
    cfg.forecast_defaults.horizon_years = 44;
    cfg.forecast_defaults.initial_pci = 71;
  });
  assert.equal(get('gameConfig').horizon_years, 44);
  assert.equal(get('gameConfig').initial_pci, 71);
});

test('play: the start screen offers the configured horizon and opens on it', async () => {
  const offered = Array.from(served('play.html').querySelectorAll('#horizon-buttons .btn')).map((b) => b.dataset.horizon);
  assert.ok(offered.includes(String(seedJSON.horizon_years)), 'the configured horizon is not offered: ' + offered);
  assert.equal(offered.at(-1), 'endless');
  const years = offered.slice(0, -1).map(Number);
  assert.deepEqual(years, [...new Set(years)].sort((a, b) => a - b), 'horizons are not ascending and distinct');

  const { $$ } = await bootBoard(seedJSON);
  assert.deepEqual($$('#horizon-buttons .btn.active').map((b) => b.dataset.horizon), [String(seedJSON.horizon_years)]);
});
