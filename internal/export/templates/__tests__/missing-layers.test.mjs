// A layer that was never ingested is not a zero (solvent-streets-56r1).
// meta.json's missing_resources names it; the stats panel says the data is
// unavailable, Compare keeps the city out of rankings that need the layer
// (and out of their awards), and the Infrastructure Index leaves it unranked.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON, fullData } from './fixtures.mjs';

const noSidewalks = {
  ...metaJSON,
  stats: metaJSON.stats.map((s) => (s.resource_type === 'sidewalks' ? { ...s, total_area: 0 } : s)),
  missing_resources: ['sidewalks'],
};

const city = (slug, name, meta) => ({
  slug, name, meta,
  scenarios: fullData['scenarios.json'], seed: fullData['forecast_seed.json'], forecast: fullData['forecast.json'],
});

// category returns the leaderboard block whose title starts with label.
function category(doc, label) {
  return Array.from(doc.querySelectorAll('.lb-category'))
    .find((c) => c.querySelector('h4').textContent.startsWith(label));
}

const names = (root, sel) => Array.from(root.querySelectorAll(sel)).map((n) => n.textContent);

test('the stats panel says a missing layer is unavailable instead of showing 0', async () => {
  const { win, $$, flush } = load({ data: { ...fullData, 'meta.json': noSidewalks } });
  await flush();
  await win.loadCityData();
  await flush();
  const sidewalk = $$('#stats .stat-card').filter((c) => {
    const h = c.querySelector('h3');
    return h && h.textContent === 'sidewalks';
  });
  assert.equal(sidewalk.length, 1, 'one sidewalks card');
  assert.match(sidewalk[0].textContent, /Data unavailable/);
  assert.doesNotMatch(sidewalk[0].textContent, /Area/, 'no area rows for a layer with no data');
});

test('Compare drops a city missing sidewalks from Walkability and its award', async () => {
  const { win, doc, flush } = load({ data: fullData });
  await flush();
  win.renderCompare([
    city('alpha-ca', 'Alpha, CA', noSidewalks),
    city('beta-ca', 'Beta, CA', metaJSON),
    city('gamma-ca', 'Gamma, CA', metaJSON),
  ]);
  await flush();

  const walk = category(doc, 'Walkability');
  assert.ok(walk, 'Walkability category rendered');
  const ranked = names(walk, '.lb-row:not(.lb-na) .lb-name');
  assert.ok(!ranked.includes('Alpha, CA'), 'Alpha is not ranked on a missing layer: ' + ranked.join(', '));
  assert.deepEqual(names(walk, '.lb-row.lb-na .lb-name'), ['Alpha, CA']);
  const award = walk.querySelector('.lb-award');
  assert.doesNotMatch(award ? award.textContent : '', /Alpha/);

  // A metric that reads no layer still ranks it.
  const area = category(doc, 'City Area');
  assert.ok(names(area, '.lb-row:not(.lb-na) .lb-name').includes('Alpha, CA'), 'City Area needs no layer');

  assert.match(doc.getElementById('infra-cards').textContent, /No sidewalks data/);
});

// A layer one of whose sources failed is partial (meta.json ingest_failures):
// its card keeps its data with a note, and Compare ranks the city like one
// missing it, so a data gap cannot win a medal (Albany's 100% city-owned
// roads came from ArcGIS alone).
const partialRoads = {
  ...metaJSON,
  ingest_failures: { roads: ['overpass'] },
};
const failedSidewalks = {
  ...noSidewalks,
  ingest_failures: { sidewalks: ['overpass'] },
};

// statCard returns the stats-panel card titled rt.
const statCards = ($$, rt) => $$('#stats .stat-card').filter((c) => {
  const h = c.querySelector('h3');
  return h && h.textContent === rt;
});

test('a partial layer keeps its data and says which source failed', async () => {
  const { win, $$, flush } = load({ data: { ...fullData, 'meta.json': partialRoads } });
  await flush();
  await win.loadCityData();
  await flush();
  const roads = statCards($$, 'roads');
  assert.equal(roads.length, 1, 'one roads card');
  assert.match(roads[0].textContent, /Area/, 'the data that did arrive is shown');
  assert.match(roads[0].textContent, /Partial: no OpenStreetMap data/);
});

test('a missing layer whose ingest failed says so', async () => {
  const { win, $$, flush } = load({ data: { ...fullData, 'meta.json': failedSidewalks } });
  await flush();
  await win.loadCityData();
  await flush();
  const sidewalk = statCards($$, 'sidewalks');
  assert.equal(sidewalk.length, 1);
  assert.match(sidewalk[0].textContent, /the last ingest from OpenStreetMap failed/);
});

test('Compare keeps a city with partial roads out of City Ownership and its award', async () => {
  const { win, doc, flush } = load({ data: fullData });
  await flush();
  win.renderCompare([
    city('alpha-ca', 'Alpha, CA', partialRoads),
    city('beta-ca', 'Beta, CA', metaJSON),
    city('gamma-ca', 'Gamma, CA', metaJSON),
  ]);
  await flush();

  const own = category(doc, 'City Ownership');
  assert.ok(own, 'City Ownership category rendered');
  assert.ok(!names(own, '.lb-row:not(.lb-na) .lb-name').includes('Alpha, CA'), 'Alpha is not ranked on partial roads');
  assert.deepEqual(names(own, '.lb-row.lb-na .lb-name'), ['Alpha, CA']);
  const award = own.querySelector('.lb-award');
  assert.doesNotMatch(award ? award.textContent : '', /Alpha/);
  assert.match(doc.getElementById('infra-cards').textContent, /Partial roads data/);
});
