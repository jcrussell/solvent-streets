// The Aggregate tab's region figures, rendered through the real
// extractMetrics -> renderAggregate path over the exporter's fixture files.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
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

/** headline returns the value text of the Aggregate card whose label matches. */
function headline(doc, label) {
  const cards = Array.from(doc.querySelectorAll('#aggregate-headlines .headline-card'));
  const hit = cards.find((c) => label.test(c.querySelector('.label').textContent));
  assert.ok(hit, `no Aggregate card matches ${label}`);
  return hit.querySelector('.value').textContent;
}

const dollars = (text) => parseFloat(text.replace(/[$,]/g, ''));

// solvent-streets-lu32.19: the per-area need and the weighted PCI divide by
// the area the scope's dollars cover, as Compare's per-area figures do.
test('Year-1 need per acre divides the city-scope need by the city-maintained area', async () => {
  // A city that maintains a tenth of the pavement inside its boundary: the
  // city-scope need is over seed.city_paved, not the all-jurisdiction stats.
  const mostlyState = city('Alpha', (c) => { c.seed.city_paved = c.seed.total_area / 10; });
  const { win, doc } = await page([mostlyState]);

  const need = mostlyState.scenarios.city[0].years[0].annual_need;
  const want = need / win.toAreaLarge(mostlyState.seed.city_paved);
  const statsArea = mostlyState.meta.stats.reduce((s, st) => s + win.toAreaLarge(st.total_area || 0), 0);
  assert.ok(Math.abs(want - need / statsArea) > 1, 'fixture cannot tell the two denominators apart');
  assert.equal(dollars(headline(doc, /^Year-1 Need per acre$/)), Math.round(want));

  // Compare shows the same city at the same figure.
  win.renderCompare([mostlyState, city('Beta')]);
  const m = win.extractMetrics(mostlyState);
  assert.equal(Math.round(m.year1PerAreaUnit), Math.round(want));
});

test('under ?scope=all the per-acre need divides by the all-jurisdiction area', async () => {
  const c = city('Alpha', (x) => { x.seed.city_paved = x.seed.total_area / 10; });
  const { win, doc } = await page([c], '?scope=all');
  const want = c.scenarios.all[0].years[0].annual_need / win.toAreaLarge(c.seed.total_area);
  assert.equal(dollars(headline(doc, /^Year-1 Need per acre$/)), Math.round(want));
});

test('Area-Weighted PCI weights each city by the area its PCI describes', async () => {
  // Same published stats, but Beta maintains a tenth of what Alpha does, so
  // Beta's poor condition carries a tenth of the weight.
  const alpha = city('Alpha', (c) => { c.scenarios.city[0].years[0].pci = 80; });
  const beta = city('Beta', (c) => {
    c.scenarios.city[0].years[0].pci = 20;
    c.seed.city_paved = alpha.seed.city_paved / 10;
  });
  const { doc } = await page([alpha, beta]);
  const want = (80 * 1 + 20 * 0.1) / 1.1;
  assert.equal(headline(doc, /^Area-Weighted PCI$/), want.toFixed(0));
});

// solvent-streets-lu32.26: the resource colours are the page config's
// (PVMT_CONFIG.layer_colors, which also colours the map toggles), so the
// Aggregate mix chart and Compare's stacked area chart cannot drift from them.
test('the resource charts take their colours from the page config', async () => {
  const { win, get } = await page([city('Alpha'), city('Beta')]);
  const config = get('PVMT_CONFIG.layer_colors');
  for (const t of ['roads', 'parking', 'sidewalks']) {
    assert.match(config[t], /^#[0-9a-f]{6}$/, `the page config has no ${t} colour`);
  }
  // Recolour the config: charts drawn afterwards must follow it.
  Object.assign(config, { roads: '#010101', parking: '#020202', sidewalks: '#030303' });
  win.__charts.length = 0;
  win.renderAggregate([city('Alpha'), city('Beta')]);
  win.renderCompare([city('Alpha'), city('Beta')]);

  const mix = win.__charts.find((c) => c.data.labels.join() === 'Roads,Parking,Sidewalks');
  assert.ok(mix, 'the Aggregate mix chart did not render');
  assert.deepEqual(Array.from(mix.data.datasets[0].backgroundColor), ['#010101', '#020202', '#030303']);

  const stacked = win.__charts.find((c) => c.data.datasets.map((d) => d.label).join() === 'Roads,Parking,Sidewalks');
  assert.ok(stacked, 'the Compare area chart did not render');
  assert.deepEqual(Array.from(stacked.data.datasets, (d) => d.backgroundColor),['#010101', '#020202', '#030303']);
});

// solvent-streets-mea7: the two area figures carry one name each across the
// tabs, a per-area ranking shows its denominator, and the Year-1 need is not
// called the hold-steady budget.
test('area labels name the maintained area and the per-area denominator', async () => {
  const { win, doc } = await page([city('Alpha'), city('Beta')]);
  const cards = Array.from(doc.querySelectorAll('#aggregate-headlines .headline-card'));
  const card = (label) => cards.find((c) => label.test(c.querySelector('.label').textContent));
  assert.ok(card(/^Maintained Area$/), 'no Maintained Area card');
  assert.ok(!card(/Total Network/), 'the old Total Network Area label is still shown');
  const need = card(/^Year-1 Maintenance Need$/);
  assert.doesNotMatch(need.querySelector('.sub').textContent, /hold the whole network steady/);
  assert.match(need.querySelector('.sub').textContent, /backlog included/);
  assert.match(card(/^Year-1 Need per acre$/).querySelector('.sub').textContent, /paved footprint \((city-maintained|all owners)\)/);

  win.renderCompare([city('Alpha'), city('Beta'), city('Gamma')]);
  const cat = Array.from(doc.querySelectorAll('.lb-category'))
    .find((c) => c.querySelector('h4').textContent.startsWith('Year 1 Maintenance Cost per'));
  assert.ok(cat, 'per-area category rendered');
  assert.match(cat.querySelector('.lb-row .lb-name').textContent, /\(÷ [\d,]+ acres\)/);
  assert.ok(Array.from(doc.querySelectorAll('.lb-category h4')).some((h) => h.textContent.startsWith('Maintained Area (all owners)')));
});
