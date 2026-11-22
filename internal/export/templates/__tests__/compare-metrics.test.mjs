// Compare's per-city figures (extractMetrics) follow the active scope: the
// seed cohorts and the scenario area they read are the scope's own.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, readFixture } from './fixtures.mjs';

/** @param {(c: any) => void} [edit] */
function city(edit) {
  const c = {
    slug: 'alpha',
    name: 'Alpha',
    meta: readFixture('meta.json'),
    scenarios: readFixture('scenarios.json'),
    seed: readFixture('forecast_seed.json'),
    forecast: readFixture('forecast.json'),
  };
  if (edit) edit(c);
  return c;
}

/** @param {string} [query] */
async function page(query = '') {
  const h = load({ data: fullData, url: 'https://example.test/index.html' + query });
  await h.flush();
  return h;
}

/** @param {any[]} cohorts */
function blended(cohorts) {
  let area = 0, w = 0;
  for (const c of cohorts) { area += c.area; w += c.area * c.decay_rate; }
  return w / area;
}

// The city scope's Blended Decay is the city-maintained cohorts', not every
// jurisdiction's; a city with no city cohorts falls back to all of them.
test('Blended Decay reads the city cohorts under the city scope', async () => {
  const c = city((x) => {
    x.seed.city_cohorts = x.seed.cohorts.slice(0, 1).map((co) => ({ ...co, decay_rate: co.decay_rate * 3 }));
  });
  const wantCity = blended(c.seed.city_cohorts);
  const wantAll = blended(c.seed.cohorts);
  assert.ok(Math.abs(wantCity - wantAll) > 1e-6, 'fixture cannot tell the scopes apart');

  const { win } = await page('?scope=city');
  assert.ok(Math.abs(win.extractMetrics(c).blendedDecay - wantCity) < 1e-12);

  const all = await page('?scope=all');
  assert.ok(Math.abs(all.win.extractMetrics(c).blendedDecay - wantAll) < 1e-12);

  const noCity = city((x) => { delete x.seed.city_cohorts; });
  assert.ok(Math.abs(win.extractMetrics(noCity).blendedDecay - blended(noCity.seed.cohorts)) < 1e-12);
});

// The year-1 need prices the year-1 area (grown by a year), so the per-area
// need divides by that area, not the base seed area; a scenario without an
// area falls back to the scope's maintained area.
test('Year-1 need per area divides by the year-1 area', async () => {
  const c = city((x) => { x.scenarios.city[0].years[0].area = x.seed.city_maintained_area * 1.016; });
  const { win } = await page('?scope=city');
  const y1 = c.scenarios.city[0].years[0];
  const m = win.extractMetrics(c);
  const want = y1.annual_need / win.toAreaLarge(y1.area);
  assert.ok(Math.abs(m.year1PerAreaUnit - want) < 1e-9 * want, `${m.year1PerAreaUnit} != ${want}`);
  const base = y1.annual_need / win.toAreaLarge(c.seed.city_maintained_area);
  assert.ok(Math.abs(want - base) > 1e-3 * base, 'fixture cannot tell the two areas apart');

  win.renderCompare([c, city()]);
  const cat = Array.from(win.document.querySelectorAll('.lb-category'))
    .find((el) => el.querySelector('h4').textContent.startsWith('Year-1 Need per'));
  assert.ok(cat, 'per-area category rendered');
  assert.match(cat.querySelector('.lb-row .lb-name').textContent,
    new RegExp('\\(÷ ' + win.fmtNum(win.toAreaLarge(y1.area)).replace(/[.,]/g, '\\$&') + ' '));

  const noArea = city((x) => { delete x.scenarios.city[0].years[0].area; });
  const n = win.extractMetrics(noArea);
  assert.equal(n.year1PerAreaUnit, noArea.scenarios.city[0].years[0].annual_need / n.scopeAreaLarge);
});

// The horizon backlog is deferred on the network as grown to the horizon
// year, so its per-area figure divides by that year's area.
test('Deferred backlog per area divides by the horizon year\'s area', async () => {
  const c = city((x) => {
    const ys = x.scenarios.city[0].years;
    ys[ys.length - 1].area = x.seed.city_maintained_area * 1.2;
  });
  const { win } = await page('?scope=city');
  const m = win.extractMetrics(c);
  win.applyRegionHorizon([m]);
  const ys = c.scenarios.city[0].years;
  const at = ys[Math.min(m.horizon, ys.length) - 1];
  assert.equal(at, ys[ys.length - 1], 'fixture horizon is not its last year');
  assert.ok(at.deferred_backlog > 0, 'fixture has no horizon backlog');
  const want = at.deferred_backlog / win.toAreaLarge(at.area);
  assert.ok(Math.abs(m.horizonBacklogPerAreaUnit - want) < 1e-9 * want, `${m.horizonBacklogPerAreaUnit} != ${want}`);
  assert.ok(Math.abs(want - at.deferred_backlog / m.scopeAreaLarge) > 1e-3 * want, 'fixture cannot tell the two areas apart');
});
