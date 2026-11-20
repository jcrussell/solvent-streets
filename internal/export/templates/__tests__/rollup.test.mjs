// compare.json: Compare and Aggregate load one
// site-level rollup instead of four files per city, and render exactly what
// the per-city files would have given them. `pvmt serve` has no rollup, so a
// 404 falls back to the per-city files without an error banner.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, readFixture } from './fixtures.mjs';

const withRollup = { ...fullData, 'compare.json': readFixture('compare.json') };

/** @param {any} data @param {string} tab */
async function render(data, tab) {
  const h = load({ data });
  await h.flush();
  h.win.selectTab(tab);
  await h.flush();
  await h.flush();
  return h;
}

// The page loads its own (first) city's files for the Map and Financials
// tabs regardless, so count only the other cities' files.
const perCityFiles = (fetches) => fetches.filter((f) => /cities\/(beta-ca|gamma-or)\/data\/(meta|scenarios|forecast_seed|forecast)\.json$/.test(f));

test('Compare renders the same from compare.json as from the per-city files', async () => {
  const rollup = await render(withRollup, 'compare-tab');
  const files = await render(fullData, 'compare-tab');

  assert.equal(perCityFiles(rollup.fetches()).length, 0,
    'with compare.json the tab must not fetch per-city files');
  assert.equal(perCityFiles(files.fetches()).length, 8, 'fallback did not fetch four files for each other city');
  const board = (h) => h.doc.getElementById('leaderboard-list').innerHTML;
  assert.ok(board(rollup).length > 0, 'leaderboard did not render from the rollup');
  assert.equal(board(rollup), board(files));
});

test('Aggregate renders the same from compare.json as from the per-city files', async () => {
  const rollup = await render(withRollup, 'aggregate-tab');
  const files = await render(fullData, 'aggregate-tab');
  const cards = (h) => h.doc.getElementById('aggregate-headlines').innerHTML;
  assert.ok(cards(rollup).length > 0, 'aggregate cards did not render from the rollup');
  assert.equal(cards(rollup), cards(files));
});

test('a missing compare.json falls back quietly', async () => {
  const h = await render(fullData, 'compare-tab');
  assert.ok(h.fetches().includes('compare.json'), 'the rollup was never tried');
  const banner = h.doc.getElementById('error-banner-list');
  assert.ok(!/compare\.json/.test(banner ? banner.textContent : ''),
    'a 404 on compare.json must not raise the error banner');
});

// The rollup format the page reads is the exporter's (compare_version in
// PVMT_CONFIG); a rollup of another format falls back to the per-city files.
test('a compare.json of another format falls back to the per-city files', async () => {
  const rollup = readFixture('compare.json');
  assert.equal(rollup.v, load({ data: {} }).win.PVMT_CONFIG.compare_version, 'the fixture rollup is at the page config version');
  const h = await render({ ...fullData, 'compare.json': { ...rollup, v: rollup.v + 1 } }, 'compare-tab');
  assert.equal(perCityFiles(h.fetches()).length, 8, 'an unknown rollup format must not be read');
});
