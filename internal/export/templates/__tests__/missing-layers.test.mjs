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
