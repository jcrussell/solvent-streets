// Compare's tile sections show the top N cities (10 by default; 3, 10, 25
// or All from #compare-top, routed as ?top=), so on a large site the
// leaderboards and charts are not buried under hundreds of tiles. A site
// the default shows in full has no selector.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON, scenariosJSON, forecastJSON, seedJSON, readFixture } from './fixtures.mjs';

// cities builds n cities with distinct Solvency Scores, best first.
function cities(n) {
  return Array.from({ length: n }, (_, i) => {
    const forecast = readFixture('forecast.json').map((f) =>
      (f.resource_type === 'roads' ? { ...f, break_even_budget: 1e7, current_budget: 1e7 * (2 - i / n) } : f));
    const slug = 'city-' + String(i).padStart(3, '0');
    return { slug, name: slug.toUpperCase(), meta: metaJSON, scenarios: scenariosJSON, seed: seedJSON, forecast };
  });
}

async function render(n, url = 'https://example.test/index.html?tab=compare-tab') {
  // The page's own Compare load (the fixture's few cities) is held, so the
  // tiles are the n given here.
  const h = load({ url, hold: ['compare.json'], data: { 'meta.json': metaJSON, 'scenarios.json': scenariosJSON, 'forecast.json': forecastJSON, 'forecast_seed.json': seedJSON } });
  await h.flush();
  h.win.renderCompare(cities(n));
  return h;
}

const tiles = (h, id) => h.$$(`#${id} .score-card`).map((c) => c.dataset.slug);
const top = (h) => new URL(h.win.location.href).searchParams.get('top');

function choose(h, value) {
  const sel = h.$('#compare-top');
  sel.value = value;
  sel.dispatchEvent(new h.win.Event('change'));
}

test('a large site shows the top 10 tiles in each section, in rank order', async () => {
  const h = await render(30);
  assert.equal(h.$('#compare-toolbar').hidden, false);
  assert.equal(h.$('#compare-top').value, '10');
  assert.equal(h.$('#compare-top-of').textContent, 'of 30 cities');
  assert.deepEqual(tiles(h, 'solvency-cards'), cities(10).map((c) => c.slug));
  assert.equal(tiles(h, 'infra-cards').length, 10);
  // The podium keeps its medals.
  assert.equal(h.$('#solvency-cards .score-card').querySelector('.rank-medal').textContent, '\u{1F947}');
  // The leaderboards follow the same N, and say how many they leave out.
  const board = h.$$('#leaderboard-list .lb-category')[0];
  assert.equal(board.querySelectorAll('.lb-row').length, 10);
  assert.equal(board.querySelector('.lb-more').textContent, '20 more cities not shown');
});

test('the selector changes N and writes it to the URL, leaving the default out', async () => {
  const h = await render(30);
  choose(h, '3');
  assert.equal(tiles(h, 'solvency-cards').length, 3);
  assert.equal(tiles(h, 'infra-cards').length, 3);
  assert.equal(top(h), '3');
  assert.equal(h.$$('#leaderboard-list .lb-category')[0].querySelectorAll('.lb-row').length, 3);

  choose(h, 'all');
  assert.equal(tiles(h, 'solvency-cards').length, 30);
  assert.equal(h.$$('#leaderboard-list .lb-category')[0].querySelectorAll('.lb-row').length, 30);
  assert.equal(h.$('#leaderboard-list .lb-more'), null);
  assert.equal(top(h), 'all');

  choose(h, '10');
  assert.equal(tiles(h, 'solvency-cards').length, 10);
  assert.equal(top(h), null, 'the default N was written to the URL');
});

test('?top= is read back, and Back restores the previous N', async () => {
  const h = await render(30, 'https://example.test/index.html?tab=compare-tab&top=25');
  assert.equal(h.$('#compare-top').value, '25');
  assert.equal(tiles(h, 'solvency-cards').length, 25);
  assert.equal(top(h), '25', 'the boot dropped ?top= from the URL');

  choose(h, '3');
  h.win.history.back();
  await h.flush();
  assert.equal(tiles(h, 'solvency-cards').length, 25, 'Back did not restore N');
  assert.equal(h.$('#compare-top').value, '25');
});

test('a count at or past the site\'s size reads as All', async () => {
  const h = await render(20, 'https://example.test/index.html?tab=compare-tab&top=25');
  assert.equal(h.$('#compare-top').value, 'all');
  assert.deepEqual(Array.from(h.$('#compare-top').options, (o) => o.value), ['3', '10', 'all']);
  assert.equal(tiles(h, 'solvency-cards').length, 20);
});

test('a small site shows every tile and no selector', async () => {
  const h = await render(6, 'https://example.test/index.html?tab=compare-tab&top=3');
  assert.equal(h.$('#compare-toolbar').hidden, true);
  assert.equal(tiles(h, 'solvency-cards').length, 6);
  assert.equal(tiles(h, 'infra-cards').length, 6);
  assert.equal(top(h), null, 'a ?top= the page ignores stayed in the URL');
});
