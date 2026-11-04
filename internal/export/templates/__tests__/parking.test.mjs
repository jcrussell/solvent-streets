// Estimated parking spaces (solvent-streets-l3gz) on the dashboard: the
// stats panel shows the count for the parking card only, Compare's metric
// extraction reads it (0 when absent, so the leaderboard filter drops the city
// rather than ranking it at zero), and the spaces category renders without an
// award of its own.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON, fullData } from './fixtures.mjs';

const fixtureSpaces = metaJSON.stats.find((s) => s.type === 'parking').estimated_spaces;

test('the fixture parking stat carries estimated_spaces', () => {
  assert.ok(fixtureSpaces > 0, 'the Go fixture must supply estimated_spaces for parking');
  for (const s of metaJSON.stats) {
    if (s.type !== 'parking') assert.equal(s.estimated_spaces, undefined, s.type + ' must omit estimated_spaces');
  }
});

test('the stats panel shows Est. Spaces on the parking card only', async () => {
  const { win, $$, flush } = load({ data: fullData });
  await flush();
  await win.loadCityData();
  await flush();
  const cards = $$('#stats .stat-card').filter((c) => !c.classList.contains('summary-card'));
  const parking = cards.find((c) => c.querySelector('h3').textContent === 'parking');
  assert.ok(parking, 'no parking card rendered');
  assert.match(parking.textContent, /Est\. Spaces/);
  assert.ok(parking.textContent.includes(fixtureSpaces.toLocaleString()));
  for (const c of cards) {
    if (c !== parking) assert.doesNotMatch(c.textContent, /Est\. Spaces/, 'only parking carries a spaces row');
  }
});

test('extractMetrics reads parkingSpaces and defaults to 0 when absent', async () => {
  const { win, flush } = load({ data: fullData });
  await flush();
  const withSpaces = win.extractMetrics({ name: 'A', slug: 'a', meta: { stats: [{ type: 'parking', total_area: 10, feature_count: 1, estimated_spaces: 321 }] }, scenarios: null, seed: null, forecast: [] });
  assert.equal(withSpaces.parkingSpaces, 321);
  const legacy = win.extractMetrics({ name: 'B', slug: 'b', meta: { stats: [{ type: 'parking', total_area: 10, feature_count: 1 }] }, scenarios: null, seed: null, forecast: [] });
  assert.equal(legacy.parkingSpaces, 0, 'a pre-estimate meta must read as 0 so the leaderboard filter drops it');
});

test('the Compare leaderboard lists Parking Spaces without a second parking award', async () => {
  const { win, doc, flush } = load({ data: fullData });
  await flush();
  const city = (slug, name) => ({ slug, name, meta: metaJSON, scenarios: fullData['scenarios.json'], seed: fullData['forecast_seed.json'], forecast: fullData['forecast.json'] });
  win.renderCompare([city('alpha-ca', 'Alpha, CA'), city('beta-ca', 'Beta, CA')]);
  await flush();
  const titles = Array.from(doc.querySelectorAll('.lb-title h4')).map((h) => h.textContent);
  assert.ok(titles.some((t) => t.startsWith('Parking Spaces (est.)')), 'missing the Parking Spaces category: ' + titles.join(' | '));
  const awards = Array.from(doc.querySelectorAll('.lb-award')).map((a) => a.textContent);
  assert.equal(awards.filter((a) => /Most Parking/.test(a)).length, 1, 'exactly one parking award (the area one)');
  assert.ok(!awards.some((a) => /Parking Spaces/.test(a)), 'the spaces category must not add an award');
});
