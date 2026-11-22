// The Aggregate tab's region figures, rendered through the real
// extractMetrics -> renderAggregate path over the exporter's fixture files.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load, boardWith } from './harness.mjs';
import { fullData, readFixture } from './fixtures.mjs';

/**
 * city is one element of the array renderAggregate consumes: the real fixture
 * files, with `edit` free to change the fresh copies.
 * @param {string} name
 * @param {(c: any) => void} [edit]
 */
function city(name, edit) {
  const c = {
    slug: name.toLowerCase(),
    name,
    meta: readFixture('meta.json'),
    scenarios: readFixture('scenarios.json'),
    seed: readFixture('forecast_seed.json'),
    forecast: readFixture('forecast.json'),
  };
  if (edit) edit(c);
  return c;
}

/** @param {any[]} cities @param {string} [query] */
async function page(cities, query = '') {
  const h = load({ data: fullData, url: 'https://example.test/index.html' + query });
  await h.flush();
  h.win.renderAggregate(cities);
  return h;
}

// The Aggregate cards carry no key, so a spec names one by its place in the
// row renderAggregate builds.
const CARD = { area: 0, pavedShare: 1, pci: 2, need: 3, backlog: 4, perArea: 5, spaces: 6 };

/** card returns the Aggregate headline card at a CARD position. */
function card(doc, at) {
  const cards = doc.querySelectorAll('#aggregate-headlines .headline-card');
  assert.equal(cards.length, Object.keys(CARD).length, 'the Aggregate card row changed; update CARD');
  return cards[at];
}

/** headline returns the value text of the Aggregate card at a CARD position. */
const headline = (doc, at) => card(doc, at).querySelector('.value').textContent;

const dollars = (text) => parseFloat(text.replace(/[$,]/g, ''));

// The per-area need and the weighted PCI divide by
// the area the scope's dollars cover, as Compare's per-area figures do.
test('Year-1 need per acre divides the city-scope need by the city-maintained area', async () => {
  // A city that maintains a tenth of the pavement inside its boundary: the
  // city-scope need is over seed.city_maintained_area, not the all-jurisdiction stats.
  // The year-1 need is over the year-1 area: the city-maintained area after
  // one year of growth.
  const mostlyState = city('Alpha', (c) => {
    c.seed.city_maintained_area = c.seed.maintained_area / 10;
    c.scenarios.city[0].years[0].area = c.seed.city_maintained_area * 1.01;
  });
  const { win, doc } = await page([mostlyState]);

  const need = mostlyState.scenarios.city[0].years[0].annual_need;
  const want = need / win.toAreaLarge(mostlyState.scenarios.city[0].years[0].area);
  const statsArea = mostlyState.meta.stats.reduce((s, st) => s + win.toAreaLarge(st.area || 0), 0);
  assert.ok(Math.abs(want - need / statsArea) > 1, 'fixture cannot tell the two denominators apart');
  assert.equal(dollars(headline(doc, CARD.perArea)), Math.round(want));

  // Compare shows the same city at the same figure.
  win.renderCompare([mostlyState, city('Beta')]);
  const m = win.extractMetrics(mostlyState);
  assert.equal(Math.round(m.year1PerAreaUnit), Math.round(want));
});

test('under ?scope=all the per-acre need divides by the all-jurisdiction area', async () => {
  const c = city('Alpha', (x) => { x.seed.city_maintained_area = x.seed.maintained_area / 10; });
  const { win, doc } = await page([c], '?scope=all');
  const want = c.scenarios.all[0].years[0].annual_need / win.toAreaLarge(c.scenarios.all[0].years[0].area);
  assert.equal(dollars(headline(doc, CARD.perArea)), Math.round(want));
});

// A per-area figure divides the forecast's dollars
// by the area they price, the maintained area (roads + parking + sidewalks
// summed, as meta.stats and the Maintained Area card add them), never by the
// paved footprint (meta.total_paved, the union).
test('per-area figures divide by the maintained area, not the paved footprint', async () => {
  const c = city('Alpha', (x) => { x.meta.total_paved = x.seed.maintained_area / 2; });
  const { win, doc } = await page([c], '?scope=all');
  const statsSqM = c.meta.stats.reduce((s, st) => s + (st.area || 0), 0);
  assert.ok(statsSqM > 0, 'fixture meta.json has no stats area');
  assert.ok(Math.abs(c.seed.maintained_area - statsSqM) < 1e-6 * statsSqM,
    `forecast_seed maintained_area ${c.seed.maintained_area} is not the meta.stats sum ${statsSqM}`);

  const m = win.extractMetrics(c);
  assert.equal(m.scopeAreaLarge, win.toAreaLarge(c.seed.maintained_area));
  assert.ok(Math.abs(m.scopeAreaLarge - m.totalAreaLarge) < 1e-6 * m.totalAreaLarge,
    'the per-area denominator is not the Maintained Area');
  const want = c.scenarios.all[0].years[0].annual_need / win.toAreaLarge(c.scenarios.all[0].years[0].area);
  assert.equal(dollars(headline(doc, CARD.perArea)), Math.round(want));
  assert.ok(Math.round(want) !== Math.round(c.scenarios.all[0].years[0].annual_need / win.toAreaLarge(c.meta.total_paved)),
    'fixture cannot tell the maintained area from the footprint');
});

test('Area-Weighted PCI weights each city by the area its PCI describes', async () => {
  // Same published stats, but Beta maintains a tenth of what Alpha does, so
  // Beta's poor condition carries a tenth of the weight.
  const alpha = city('Alpha', (c) => { c.scenarios.city[0].years[0].pci = 80; });
  const beta = city('Beta', (c) => {
    c.scenarios.city[0].years[0].pci = 20;
    c.seed.city_maintained_area = alpha.seed.city_maintained_area / 10;
  });
  const { doc } = await page([alpha, beta]);
  const want = (80 * 1 + 20 * 0.1) / 1.1;
  assert.equal(headline(doc, CARD.pci), want.toFixed(0));
});

// The resource colours are the page config's
// (PVMT_CONFIG.layer_colors, which also colours the map toggles), so the
// Aggregate mix chart and Compare's stacked area chart cannot drift from them.
test('the resource charts take their colours from the page config', async () => {
  const { win, get, chartsOn } = await page([city('Alpha'), city('Beta')]);
  const config = get('PVMT_CONFIG.layer_colors');
  for (const t of ['roads', 'parking', 'sidewalks']) {
    assert.match(config[t], /^#[0-9a-f]{6}$/, `the page config has no ${t} colour`);
  }
  // Recolour the config: charts drawn afterwards must follow it.
  Object.assign(config, { roads: '#010101', parking: '#020202', sidewalks: '#030303' });
  win.__charts.length = 0;
  win.renderAggregate([city('Alpha'), city('Beta')]);
  win.renderCompare([city('Alpha'), city('Beta')]);

  const mix = chartsOn('#aggregate-mix-chart').at(-1);
  assert.ok(mix, 'the Aggregate mix chart did not render');
  assert.deepEqual(Array.from(mix.data.datasets[0].backgroundColor), ['#010101', '#020202', '#030303']);

  const stacked = chartsOn('#compare-area-chart').at(-1);
  assert.ok(stacked, 'the Compare area chart did not render');
  assert.deepEqual(Array.from(stacked.data.datasets, (d) => d.backgroundColor),['#010101', '#020202', '#030303']);
});

// A per-area figure shows the area it divides by: the Aggregate card names the
// summed year-1 area and the scope it is read in, and each row of Compare's
// per-area ranking names that city's own.
test('the per-area card and ranking show the denominator and scope they divide by', async () => {
  for (const query of ['', '?scope=all']) {
    const cities = [city('Alpha'), city('Beta', (c) => { c.seed.city_maintained_area /= 2; c.seed.maintained_area /= 2; })];
    const { win, doc } = await page(cities, query);
    const ms = cities.map((c) => win.extractMetrics(c));
    const denominator = ms.reduce((sum, m) => sum + m.year1AreaLarge, 0);
    const sub = card(doc, CARD.perArea).querySelector('.sub').textContent;
    assert.ok(sub.includes(win.fmtNum(denominator) + ' ' + win.areaLargeUnit()), query + ': ' + sub);
    assert.ok(sub.includes(win.scopeName()), query + ': ' + sub);

    win.renderCompare(cities);
    const cat = boardWith(doc, 'alpha', win.PvmtCommon.fmtMoney(ms[0].year1PerAreaUnit));
    assert.ok(cat, query + ': no ranking shows Alpha at its year-1 need per area');
    for (const m of ms) {
      const name = cat.querySelector(`.lb-row[data-slug="${m.slug}"] .lb-name`).textContent;
      assert.ok(name.includes(win.fmtNum(m.year1AreaLarge) + ' ' + win.areaLargeUnit()), query + ': ' + name);
    }
  }
  // The two scopes are told apart by name.
  const names = [];
  for (const query of ['', '?scope=all']) names.push((await page([city('Alpha')], query)).win.scopeName());
  assert.notEqual(names[0], names[1]);
});

// The divergence note fires for any per-city forecast setting that moves a
// city's dollars, not only initial PCI, decay and cost tiers: cities with
// different cost multipliers, treatment cycles or growth rates sum dollars
// priced differently too.
test('the Aggregate divergence note signs every dollar-moving forecast setting', async () => {
  for (const [key, delta] of [['cost_multiplier', 0.5], ['treatment_cycle_years', 8], ['growth_rate', 0.005]]) {
    const { win } = await page([city('Alpha'), city('Beta', (c) => { c.seed[key] = (c.seed[key] || 0) + delta; })]);
    const note = win.document.getElementById('aggregate-divergence-note');
    assert.equal(note.hidden, false, 'cities differing only in ' + key + ' got no divergence note');
  }
  const { win } = await page([city('Alpha'), city('Beta')]);
  assert.equal(win.document.getElementById('aggregate-divergence-note').hidden, true, 'identical cities got a divergence note');
});

// The footnote is written with the figures, and counts the cities it leaves
// out of the trajectories for want of a forecast.
test('the Aggregate footnote renders and counts the cities without forecasts', async () => {
  const { doc } = await page([city('Alpha'), city('Beta')]);
  const all = doc.getElementById('aggregate-footnote').textContent;
  assert.ok(all.trim().length > 0, 'the footnote is empty');
  assert.ok(!all.includes('2'), 'fixture: the footnote already carries the count this spec looks for');

  const bare = (c) => { c.scenarios = null; c.forecast = null; };
  const some = (await page([city('Alpha'), city('Beta', bare), city('Gamma', bare)])).doc
    .getElementById('aggregate-footnote').textContent;
  assert.match(some, /\b2\b/, 'two cities without forecasts were not counted: ' + some);
  assert.ok(some.length > all.length);
});
