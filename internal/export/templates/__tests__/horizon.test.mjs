// One forecast horizon (solvent-streets-lu32.20): Financials, Compare and
// Aggregate label the same year, and each figure "at the horizon" is read at
// the year its label names. A city whose series stops short of the horizon
// shows its last year and is flagged, the same way on Compare and Aggregate.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON, scenariosJSON, readFixture } from './fixtures.mjs';

const HORIZON = seedJSON.horizon_years;
const baselineYears = scenariosJSON.city[0].years;

test('the fixture forecasts to its seeded horizon', () => {
  assert.ok(HORIZON > 12, 'the specs below shorten a series to 12 years');
  assert.equal(baselineYears.length, HORIZON);
  assert.notEqual(baselineYears[11].deferred_backlog, baselineYears[HORIZON - 1].deferred_backlog,
    'year 12 and the horizon year must be distinguishable');
});

/** @param {any} seed */
async function financials(seed) {
  const h = load({ data: { ...fullData, 'forecast_seed.json': seed } });
  await h.flush();
  return h;
}

test('Financials: the deficit tile is read at the year its label names', async () => {
  const { win, $ } = await financials({ ...readFixture('forecast_seed.json'), horizon_years: 12 });
  assert.equal($('#hl-deficit-label').textContent, '12-Year Deficit');
  assert.equal($('#hl-spend-label').textContent, '12-Year Spend');
  assert.equal($('#hl-total-deficit').textContent, win.fmt$(baselineYears[11].deferred_backlog));
  assert.equal($('#hl-end-pci').textContent, baselineYears[11].pci.toFixed(0));
});

test('Financials: a seed without horizon_years is labelled with the series length', async () => {
  const seed = readFixture('forecast_seed.json');
  delete seed.horizon_years;
  const { win, $ } = await financials(seed);
  assert.equal($('#hl-deficit-label').textContent, HORIZON + '-Year Deficit');
  assert.equal($('#cohort-deficit-th').textContent, HORIZON + 'yr Deficit');
  assert.equal($('#hl-total-deficit').textContent, win.fmt$(baselineYears[HORIZON - 1].deferred_backlog));
});

/** @param {string} name @param {number} [years] series length; the full horizon by default */
function city(name, years) {
  const scenarios = readFixture('scenarios.json');
  if (years) {
    for (const scope of ['city', 'all']) {
      for (const s of scenarios[scope]) s.years = s.years.slice(0, years);
    }
  }
  return {
    slug: name.toLowerCase(), name,
    meta: readFixture('meta.json'), scenarios,
    seed: readFixture('forecast_seed.json'), forecast: readFixture('forecast.json'),
  };
}

async function tabs(cities) {
  const h = load({ data: fullData });
  await h.flush();
  h.win.renderCompare(cities);
  h.win.renderAggregate(cities);
  return h;
}

const full = baselineYears[HORIZON - 1];
const short = baselineYears[11];

test('Compare and Aggregate label the config horizon when one city stops short', async () => {
  const { doc } = await tabs([city('Full'), city('Short', 12)]);
  assert.equal(doc.getElementById('compare-cost-title').textContent, HORIZON + '-Year Cost Trajectory');
  assert.equal(doc.getElementById('compare-need-title').textContent, 'Year 1 vs Year ' + HORIZON + ' Maintenance Need');
  assert.match(doc.getElementById('leaderboard-list').innerHTML, new RegExp(HORIZON + '-Year Deferred Backlog per acre'));
  assert.equal(doc.getElementById('agg-backlog-title').textContent, 'Region ' + HORIZON + '-Year Deferred Backlog');
  assert.equal(doc.getElementById('agg-contrib-backlog-th').textContent, HORIZON + '-Yr Backlog');
  const card = Array.from(doc.querySelectorAll('#aggregate-headlines .headline-card'))
    .find((c) => /Deferred Backlog$/.test(c.querySelector('.label').textContent));
  assert.equal(card.querySelector('.label').textContent, HORIZON + '-Year Deferred Backlog');
  assert.match(card.querySelector('.sub').textContent, new RegExp('by year ' + HORIZON + '$'));
});

test('a city that stops short shows its last year on both pages, flagged', async () => {
  const { win, doc } = await tabs([city('Full'), city('Short', 12)]);

  // Compare: the "Year N Need" bars.
  const need = win.__charts.find((c) => c.data.datasets.some((d) => d.label === 'Year ' + HORIZON + ' Need'));
  assert.ok(need, 'the need chart is not labelled with the horizon');
  assert.deepEqual(Array.from(need.data.datasets[1].data), [full.annual_need, short.annual_need]);

  // Compare: the backlog leaderboard row.
  const board = doc.getElementById('leaderboard-list').innerHTML;
  const start = board.indexOf('Deferred Backlog per acre');
  const block = board.slice(start, board.indexOf('lb-category', start));
  assert.match(block, /Short <span class="lb-note"[^>]*>\(through year 12\)<\/span>/);
  assert.ok(!/Full <span/.test(block), 'a city at the horizon must not be flagged');
  const m = win.extractMetrics(city('Short', 12));
  win.applyRegionHorizon([win.extractMetrics(city('Full')), m]);
  assert.equal(m.horizonBacklog, short.deferred_backlog);

  // Aggregate: the same city at the same figure, with the same flag.
  const rows = Array.from(doc.querySelectorAll('#aggregate-contrib-table tbody tr'));
  const row = rows.find((r) => r.cells[0].textContent.startsWith('Short'));
  assert.match(row.cells[0].textContent, /\(through year 12\)/);
  assert.equal(row.cells[4].textContent, win.fmt$(short.deferred_backlog));
  const fullRow = rows.find((r) => r.cells[0].textContent.startsWith('Full'));
  assert.equal(fullRow.cells[0].textContent, 'Full');
  assert.equal(fullRow.cells[4].textContent, win.fmt$(full.deferred_backlog));

  // The region headline is the sum of the rows and the chart's last point.
  const card = Array.from(doc.querySelectorAll('#aggregate-headlines .headline-card'))
    .find((c) => /Deferred Backlog$/.test(c.querySelector('.label').textContent));
  const region = full.deferred_backlog + short.deferred_backlog;
  assert.equal(card.querySelector('.value').textContent, win.fmt$(region));
  const chart = win.__charts.find((c) => c.data.datasets[0].label === 'Region Deferred Backlog');
  assert.equal(chart.data.labels.length, HORIZON);
  assert.equal(chart.data.datasets[0].data.at(-1), region);
});

test('with every city at the horizon nothing is flagged', async () => {
  const { doc } = await tabs([city('A'), city('B')]);
  assert.ok(!/through year/.test(doc.getElementById('leaderboard-list').innerHTML));
  assert.ok(!/through year/.test(doc.querySelector('#aggregate-contrib-table tbody').innerHTML));
});
