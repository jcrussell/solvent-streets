// The site publishes no feature counts. A count of
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

// The page must not show a feature count even when a file carries one (an
// export from before the field was dropped): plant a number that appears
// nowhere else and look for it.
const PLANTED = 987654;
const planted = (text) => /987[,.\s]?654/.test(text);

function metaWithCounts() {
  return { ...metaJSON, stats: metaJSON.stats.map((s) => ({ ...s, feature_count: PLANTED })) };
}

test('the stats panel shows no feature count', async () => {
  const { win, $$, flush } = load({ data: { ...fullData, 'meta.json': metaWithCounts() } });
  await flush();
  await win.loadCityData();
  await flush();
  const cards = $$('#stats .stat-card');
  assert.ok(cards.length > 0, 'no stat cards rendered');
  for (const c of cards) {
    assert.ok(!planted(c.textContent), 'stat card lists a feature count: ' + c.textContent);
  }
});

test('Compare ranks and awards nothing by feature count', async () => {
  const { win, doc, flush } = load({ data: fullData });
  await flush();
  const city = (slug, name) => ({ slug, name, meta: metaWithCounts(), scenarios: fullData['scenarios.json'], seed: fullData['forecast_seed.json'], forecast: fullData['forecast.json'] });
  const cities = [city('alpha-ca', 'Alpha, CA'), city('beta-ca', 'Beta, CA')];
  win.renderCompare(cities);
  await flush();
  assert.ok(doc.querySelectorAll('.lb-category').length > 0, 'no Compare categories rendered');
  assert.ok(!planted(doc.getElementById('compare-tab').textContent), 'Compare shows a feature count');
  // Twice the count would be a region total.
  assert.ok(!Object.values(win.extractMetrics(cities[0])).some((v) => v === PLANTED || v === 2 * PLANTED),
    'a Compare metric reads feature_count');
});
