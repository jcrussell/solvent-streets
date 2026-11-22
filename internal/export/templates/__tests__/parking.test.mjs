// Estimated parking spaces on the dashboard: the
// stats panel shows the count for the parking card only, Compare's metric
// extraction reads it (0 when absent, so the leaderboard filter drops the city
// rather than ranking it at zero), and the spaces category renders without an
// award of its own.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load, boardWith } from './harness.mjs';
import { metaJSON, fullData } from './fixtures.mjs';

const fixtureSpaces = metaJSON.stats.find((s) => s.resource_type === 'parking').parking_spaces;

test('the fixture parking stat carries parking_spaces', () => {
  assert.ok(fixtureSpaces > 0, 'the Go fixture must supply parking_spaces for parking');
  for (const s of metaJSON.stats) {
    if (s.resource_type !== 'parking') assert.equal(s.parking_spaces, undefined, s.resource_type + ' must omit parking_spaces');
  }
});

test('the stats panel shows the estimated spaces on the parking card only', async () => {
  const { win, $$, flush } = load({ data: fullData });
  await flush();
  await win.loadCityData();
  await flush();
  // The resource cards are titled with their type; each lists its area in two
  // units, and parking adds its spaces.
  const cards = $$('#stats .stat-card:not(.summary-card):not(.metric-card)');
  const values = (c) => Array.from(c.querySelectorAll('.stat-row .stat-value'), (v) => v.textContent);
  const parking = cards.find((c) => c.querySelector('h3').textContent === 'parking');
  assert.ok(parking, 'no parking card rendered');
  assert.equal(values(parking).length, 3);
  assert.equal(values(parking).at(-1), fixtureSpaces.toLocaleString('en-US'));
  assert.ok(cards.length > 1, 'only the parking card rendered');
  for (const c of cards) {
    if (c !== parking) assert.equal(values(c).length, 2, 'only parking carries a spaces row');
  }
});

test('extractMetrics reads parkingSpaces and defaults to 0 when absent', async () => {
  const { win, flush } = load({ data: fullData });
  await flush();
  const withSpaces = win.extractMetrics({ name: 'A', slug: 'a', meta: { stats: [{ resource_type: 'parking', area: 10, parking_spaces: 321 }] }, scenarios: null, seed: null, forecast: [] });
  assert.equal(withSpaces.parkingSpaces, 321);
  const without = win.extractMetrics({ name: 'B', slug: 'b', meta: { stats: [{ resource_type: 'parking', area: 10 }] }, scenarios: null, seed: null, forecast: [] });
  assert.equal(without.parkingSpaces, 0, 'a meta without the estimate must read as 0 so the leaderboard filter drops it');
});

test('the Compare leaderboard ranks parking spaces without a second parking award', async () => {
  const { win, doc, flush } = load({ data: fullData });
  await flush();
  const city = (slug, name) => ({ slug, name, meta: metaJSON, scenarios: fullData['scenarios.json'], seed: fullData['forecast_seed.json'], forecast: fullData['forecast.json'] });
  const cities = [city('alpha-ca', 'Alpha, CA'), city('beta-ca', 'Beta, CA')];
  win.renderCompare(cities);
  await flush();
  const m = win.extractMetrics(cities[0]);
  assert.equal(m.parkingSpaces, fixtureSpaces);
  // Two parking rankings, found by the figure each shows: the spaces and the
  // area. The area one gives the parking award; the spaces one adds none.
  const spaces = boardWith(doc, 'alpha-ca', fixtureSpaces.toLocaleString('en-US'));
  assert.ok(spaces, 'missing the parking-spaces category');
  assert.equal(spaces.querySelector('.lb-award'), null, 'the spaces category must not add an award');
  const area = boardWith(doc, 'alpha-ca', win.fmtNum(m.parkingAreaLarge) + ' ' + win.areaLargeUnit());
  assert.ok(area, 'missing the parking-area category');
  assert.ok(area.querySelector('.lb-award'), 'the parking-area category gives the parking award');
});
