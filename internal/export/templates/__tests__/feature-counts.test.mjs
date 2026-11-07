// The site publishes no feature counts (solvent-streets-04r7). A count of
// fetched OSM/ArcGIS features describes the ingest bounding box and how
// mappers split ways, not the city, and it sat next to boundary-clipped area
// ("7 features, 0 area"). meta.json carries none, the stats panel shows none,
// and Compare has no feature-count category or award.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON, fullData } from './fixtures.mjs';

test('meta.json stats carry no feature_count', () => {
  assert.ok(metaJSON.stats.length > 0, 'the Go fixture must supply per-resource stats');
  for (const s of metaJSON.stats) {
    assert.equal(s.feature_count, undefined, s.resource_type + ' must not publish feature_count');
  }
});

test('the stats panel has no Features row', async () => {
  const { win, $$, flush } = load({ data: fullData });
  await flush();
  await win.loadCityData();
  await flush();
  const cards = $$('#stats .stat-card');
  assert.ok(cards.length > 0, 'no stat cards rendered');
  for (const c of cards) {
    assert.doesNotMatch(c.textContent, /Features/, 'stat card lists a feature count: ' + c.textContent);
  }
});

test('Compare has no feature-count category or award', async () => {
  const { win, doc, flush } = load({ data: fullData });
  await flush();
  const city = (slug, name) => ({ slug, name, meta: metaJSON, scenarios: fullData['scenarios.json'], seed: fullData['forecast_seed.json'], forecast: fullData['forecast.json'] });
  win.renderCompare([city('alpha-ca', 'Alpha, CA'), city('beta-ca', 'Beta, CA')]);
  await flush();
  const titles = Array.from(doc.querySelectorAll('.lb-title h4')).map((h) => h.textContent);
  assert.ok(titles.length > 0, 'no Compare categories rendered');
  assert.ok(!titles.some((t) => /Features/.test(t)), 'a feature-count category is listed: ' + titles.join(' | '));
  const awards = Array.from(doc.querySelectorAll('.lb-award')).map((a) => a.textContent);
  assert.ok(!awards.some((a) => /Most Detailed/.test(a)), 'the Most Detailed award is still given');
});
