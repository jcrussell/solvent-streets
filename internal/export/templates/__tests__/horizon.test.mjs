// One forecast horizon: Financials, Compare and
// Aggregate label the same year, and each figure "at the horizon" is read at
// the year its label names. A city whose series stops short of the horizon
// shows its last year and is flagged, the same way on Compare and Aggregate.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load, boardWith } from './harness.mjs';
import { fullData, seedJSON, scenariosJSON, readFixture } from './fixtures.mjs';

const HORIZON = seedJSON.horizon_years;
const baselineYears = scenariosJSON.city[0].years;

test('the fixture forecasts to its seeded horizon', () => {
  assert.ok(HORIZON > 12, 'the specs below shorten a series to 12 years');
  assert.equal(baselineYears.length, HORIZON);
  assert.notEqual(baselineYears[11].deferred_backlog, baselineYears[HORIZON - 1].deferred_backlog,
    'year 12 and the horizon year must be distinguishable');
});

// namesNumber matches a text that carries n as a whole number ("20-Year",
// "20yr", "Year 20"), not as part of a longer one.
/** @param {number} n */
const namesNumber = (n) => new RegExp('(^|[^\\d.])' + n + '([^\\d.]|$)');

// The Aggregate cards carry no key; the deferred backlog is the fifth.
const backlogCard = (doc) => doc.querySelectorAll('#aggregate-headlines .headline-card')[4];

// backlogBoard is the Compare ranking of the horizon backlog per area: the
// one that shows `c` at that figure (see boardWith).
/** @param {any} win @param {any[]} cities @param {any} c */
function backlogBoard(win, cities, c) {
  const ms = cities.map((x) => win.extractMetrics(x));
  win.applyRegionHorizon(ms);
  return boardWith(win.document, c.slug, win.PvmtCommon.fmtMoney(ms[cities.indexOf(c)].horizonBacklogPerAreaUnit));
}

/** @param {any} seed */
async function financials(seed) {
  const h = load({ data: { ...fullData, 'forecast_seed.json': seed } });
  await h.flush();
  return h;
}

test('Financials: the deficit tile is read at the year its label names', async () => {
  const { win, $ } = await financials({ ...readFixture('forecast_seed.json'), horizon_years: 12 });
  assert.match($('#hl-deficit-label').textContent, namesNumber(12));
  assert.match($('#hl-spend-label').textContent, namesNumber(12));
  assert.equal($('#hl-total-deficit').textContent, win.PvmtCommon.fmtMoney(baselineYears[11].deferred_backlog));
  assert.equal($('#hl-end-pci').textContent, baselineYears[11].pci.toFixed(0));
});

test('Financials: a seed without horizon_years is labelled with the series length', async () => {
  const seed = readFixture('forecast_seed.json');
  delete seed.horizon_years;
  const { win, $ } = await financials(seed);
  assert.match($('#hl-deficit-label').textContent, namesNumber(HORIZON));
  assert.match($('#cohort-deficit-th').textContent, namesNumber(HORIZON));
  assert.equal($('#hl-total-deficit').textContent, win.PvmtCommon.fmtMoney(baselineYears[HORIZON - 1].deferred_backlog));
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
  assert.notEqual(HORIZON, 12, 'fixture: the short city must stop short of the horizon');
  const cities = [city('Full'), city('Short', 12)];
  const { win, doc } = await tabs(cities);
  for (const id of ['compare-cost-title', 'compare-need-title', 'agg-backlog-title', 'agg-contrib-backlog-th']) {
    assert.match(doc.getElementById(id).textContent, namesNumber(HORIZON), id + ' does not name the horizon');
    assert.doesNotMatch(doc.getElementById(id).textContent, namesNumber(12), id + ' names the short city\'s last year');
  }
  const board = backlogBoard(win, cities, cities[0]);
  assert.ok(board, 'no ranking shows the horizon backlog per area');
  assert.match(board.querySelector('h4').textContent, namesNumber(HORIZON));
  const card = backlogCard(doc);
  assert.match(card.querySelector('.label').textContent, namesNumber(HORIZON));
  assert.match(card.querySelector('.sub').textContent, namesNumber(HORIZON));
});

test('a city that stops short shows its last year on both pages, flagged', async () => {
  const cities = [city('Full'), city('Short', 12)];
  const { win, doc, chartsOn } = await tabs(cities);

  // Compare: the horizon-year bars beside the year-1 bars.
  const need = chartsOn('#compare-need-chart').at(-1);
  assert.ok(need, 'the need chart did not render');
  assert.match(need.data.datasets[1].label, namesNumber(HORIZON), 'the need chart is not labelled with the horizon');
  assert.deepEqual(Array.from(need.data.datasets[1].data), [full.annual_need, short.annual_need]);

  // Compare: the backlog leaderboard row. Each row shows its per-area
  // denominator in a note; the short city's has a second, naming its last year.
  const board = backlogBoard(win, cities, cities[0]);
  const notes = (slug) => Array.from(board.querySelectorAll(`.lb-row[data-slug="${slug}"] .lb-name .lb-note`), (n) => n.textContent);
  assert.equal(notes('full').length, 1, 'a city at the horizon must not be flagged');
  assert.equal(notes('short').length, 2);
  assert.match(notes('short')[1], namesNumber(12));
  const m = win.extractMetrics(city('Short', 12));
  win.applyRegionHorizon([win.extractMetrics(city('Full')), m]);
  assert.equal(m.horizonBacklog, short.deferred_backlog);

  // Aggregate: the same city at the same figure, with the same flag.
  const rows = Array.from(doc.querySelectorAll('#aggregate-contrib-table tbody tr'));
  const row = rows.find((r) => r.cells[0].textContent.startsWith('Short'));
  assert.equal(row.cells[0].querySelector('.lb-note').textContent, notes('short')[1]);
  assert.equal(row.cells[4].textContent, win.PvmtCommon.fmtMoney(short.deferred_backlog));
  const fullRow = rows.find((r) => r.cells[0].textContent.startsWith('Full'));
  assert.equal(fullRow.cells[0].textContent, 'Full');
  assert.equal(fullRow.cells[4].textContent, win.PvmtCommon.fmtMoney(full.deferred_backlog));

  // The region headline is the sum of the rows and the chart's last point.
  const region = full.deferred_backlog + short.deferred_backlog;
  assert.equal(backlogCard(doc).querySelector('.value').textContent, win.PvmtCommon.fmtMoney(region));
  const chart = chartsOn('#aggregate-backlog-chart').at(-1);
  assert.equal(chart.data.labels.length, HORIZON);
  assert.equal(chart.data.datasets[0].data.at(-1), region);
});

test('with every city at the horizon nothing is flagged', async () => {
  const cities = [city('A'), city('B')];
  const { win, doc } = await tabs(cities);
  // Only the per-area denominator note on each backlog row; none in the table.
  const board = backlogBoard(win, cities, cities[0]);
  for (const row of board.querySelectorAll('.lb-row')) {
    assert.equal(row.querySelectorAll('.lb-name .lb-note').length, 1);
  }
  assert.equal(doc.querySelectorAll('#aggregate-contrib-table tbody .lb-note').length, 0);
});
