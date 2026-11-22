// A layer that was never ingested is not a zero.
// meta.json's missing_resources names it; the stats panel says the data is
// unavailable, Compare keeps the city out of rankings that need the layer
// (and out of their awards), and the Infrastructure Index leaves it unranked.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load, boardWith } from './harness.mjs';
import { metaJSON, fullData } from './fixtures.mjs';

const noSidewalks = {
  ...metaJSON,
  stats: metaJSON.stats.map((s) => (s.resource_type === 'sidewalks' ? { ...s, area: 0 } : s)),
  missing_resources: ['sidewalks'],
};

const city = (slug, name, meta) => ({
  slug, name, meta,
  scenarios: fullData['scenarios.json'], seed: fullData['forecast_seed.json'], forecast: fullData['forecast.json'],
});

// slugs lists the cities of a leaderboard block's rows matching sel, in order.
const slugs = (root, sel) => Array.from(root.querySelectorAll(sel), (n) => n.getAttribute('data-slug'));

// panel loads a city with `meta` and returns its stats-panel cards for the
// resource type rt (a card is titled with its type), plus the window.
/** @param {any} meta @param {string} rt */
async function panel(meta, rt) {
  const { win, $$, flush } = load({ data: { ...fullData, 'meta.json': meta } });
  await flush();
  await win.loadCityData();
  await flush();
  const cards = $$('#stats .stat-card').filter((c) => {
    const h = c.querySelector('h3');
    return h && h.textContent === rt;
  });
  return { win, cards };
}

// whyMissing is the reason a city's card gives for having no data for rt:
// the one line of its missing-layer card.
/** @param {any} meta @param {string} rt */
async function whyMissing(meta, rt) {
  const { win, cards } = await panel(meta, rt);
  assert.equal(cards.length, 1, 'one ' + rt + ' card');
  assert.ok(cards[0].classList.contains('missing-card'), rt + ' is not shown as missing');
  assert.equal(cards[0].querySelector('.stat-value'), null, 'no figures for a layer with no data');
  const rows = cards[0].querySelectorAll('.stat-row');
  assert.equal(rows.length, 1);
  assert.ok(rows[0].textContent.trim().length > 0, 'the missing card gives no reason');
  return { win, why: rows[0].textContent };
}

test('the stats panel says a missing layer is unavailable instead of showing 0', async () => {
  // whyMissing asserts the card: marked missing, one reason, no figures.
  await whyMissing(noSidewalks, 'sidewalks');
  // A layer with data has figures and is not marked.
  const { cards } = await panel(noSidewalks, 'roads');
  assert.equal(cards.length, 1);
  assert.ok(!cards[0].classList.contains('missing-card'));
  assert.ok(cards[0].querySelectorAll('.stat-value').length > 0);
});

// unrankedCard is the city's Infrastructure Index card, asserted unranked:
// muted, without a place, and labelled as the page describes its gaps.
/** @param {any} win @param {any} c */
function unrankedCard(win, c) {
  const card = win.document.querySelector(`#infra-cards .score-card[data-slug="${c.slug}"]`);
  assert.ok(card, 'no Infrastructure Index card for ' + c.slug);
  assert.ok(card.classList.contains('score-card-muted'), 'the card is not muted');
  assert.equal(card.querySelector('.rank-medal').textContent, '', 'an unranked city shows a place');
  const label = win.unrankedLabel(win.extractMetrics(c));
  assert.ok(label.length > 0 && card.querySelector('.score-label').textContent.includes(label),
    'the card does not say why it is unranked: ' + card.querySelector('.score-label').textContent);
  return card;
}

test('Compare drops a city missing sidewalks from Walkability and its award', async () => {
  const { win, doc, flush } = load({ data: fullData });
  await flush();
  const cities = [
    city('alpha-ca', 'Alpha, CA', noSidewalks),
    city('beta-ca', 'Beta, CA', metaJSON),
    city('gamma-ca', 'Gamma, CA', metaJSON),
  ];
  win.renderCompare(cities);
  await flush();
  const [alpha, beta] = cities.map((c) => win.extractMetrics(c));
  assert.deepEqual(Array.from(alpha.missing), ['sidewalks']);

  // The sidewalk-to-road ranking: the one showing Beta at its ratio.
  const walk = boardWith(doc, 'beta-ca', beta.sidewalkToRoadPct.toFixed(1) + '%');
  assert.ok(walk, 'Walkability category rendered');
  const ranked = slugs(walk, '.lb-row:not(.lb-na)');
  assert.deepEqual(ranked.slice().sort(), ['beta-ca', 'gamma-ca'], 'Alpha is not ranked on a missing layer');
  assert.deepEqual(slugs(walk, '.lb-row.lb-na'), ['alpha-ca']);
  const award = walk.querySelector('.lb-award');
  assert.doesNotMatch(award ? award.textContent : '', /Alpha/);

  // A metric that reads no layer still ranks it: the city-area ranking.
  const area = boardWith(doc, 'alpha-ca', win.fmtNum(alpha.cityAreaLarge) + ' ' + win.areaLargeUnit());
  assert.ok(area, 'City Area needs no layer');
  assert.ok(slugs(area, '.lb-row:not(.lb-na)').includes('alpha-ca'));

  unrankedCard(win, cities[0]);
  assert.ok(win.unrankedLabel(alpha).includes('sidewalks'), win.unrankedLabel(alpha));
});

// A layer one of whose sources failed is partial (meta.json failing_sources):
// its card keeps its data with a note, and Compare ranks the city like one
// missing it, so a data gap cannot win a medal (Albany's 100% city-owned
// roads came from ArcGIS alone).
const partialRoads = {
  ...metaJSON,
  failing_sources: { roads: ['overpass'] },
};
const failedSidewalks = {
  ...noSidewalks,
  failing_sources: { sidewalks: ['overpass'] },
};

test('a partial layer keeps its data and says which source failed', async () => {
  const { win, cards: roads } = await panel(partialRoads, 'roads');
  assert.equal(roads.length, 1, 'one roads card');
  assert.ok(!roads[0].classList.contains('missing-card'));
  assert.ok(roads[0].querySelectorAll('.stat-value').length > 0, 'the data that did arrive is shown');
  // The note names the failed source as the page names sources.
  const note = roads[0].querySelector('.stat-note');
  assert.ok(note, 'a partial layer carries no note');
  assert.equal(win.sourceNames(['overpass', 'arcgis']), 'OpenStreetMap, ArcGIS');
  assert.ok(note.textContent.includes(win.sourceNames(['overpass'])), note.textContent);
  // A complete layer carries none.
  assert.equal((await panel(metaJSON, 'roads')).cards[0].querySelector('.stat-note'), null);
});

// A missing layer says why, and each cause reads differently: never
// ingested, not yet computed, an ingest that failed, a source with none
// mapped. The last two name the source.
const neverIngested = async () => (await whyMissing(noSidewalks, 'sidewalks')).why;

// Serve's Latest compute view after a settings change
// has nothing computed; its cards say that, not that nothing was ingested.
test('an empty latest compute says not computed, not not ingested', async () => {
  const empty = { ...metaJSON, latest_compute: true, stats: [], missing_resources: ['roads', 'parking', 'sidewalks'] };
  const { why } = await whyMissing(empty, 'roads');
  // The same city outside the latest-compute view is simply not ingested.
  const snapshot = (await whyMissing({ ...empty, latest_compute: false }, 'roads')).why;
  assert.notEqual(why, snapshot);
  assert.equal(snapshot, await neverIngested());
});

test('a missing layer whose ingest failed says so', async () => {
  const { win, why } = await whyMissing(failedSidewalks, 'sidewalks');
  assert.notEqual(why, await neverIngested());
  assert.ok(why.includes(win.sourceNames(['overpass'])), why);
});

// A layer every source answered empty for is not
// un-ingested: OpenStreetMap maps none there (meta.json empty_sources).
test('a missing layer no source has says none is mapped, not not ingested', async () => {
  const emptySidewalks = { ...noSidewalks, empty_sources: { sidewalks: ['overpass'] } };
  const { win, why } = await whyMissing(emptySidewalks, 'sidewalks');
  assert.notEqual(why, await neverIngested());
  assert.notEqual(why, (await whyMissing(failedSidewalks, 'sidewalks')).why, 'an empty source reads as a failed one');
  assert.ok(why.includes(win.sourceNames(['overpass'])), why);
});

test('Compare keeps a city with partial roads out of City Ownership and its award', async () => {
  const { win, doc, flush } = load({ data: fullData });
  await flush();
  const cities = [
    city('alpha-ca', 'Alpha, CA', partialRoads),
    city('beta-ca', 'Beta, CA', metaJSON),
    city('gamma-ca', 'Gamma, CA', metaJSON),
  ];
  win.renderCompare(cities);
  await flush();
  const [alpha, beta] = cities.map((c) => win.extractMetrics(c));
  assert.deepEqual(Array.from(alpha.partial), ['roads']);
  assert.deepEqual(Array.from(alpha.missing), []);

  // The city-ownership ranking: the one showing Beta at its city-owned share.
  const own = boardWith(doc, 'beta-ca', beta.cityPct.toFixed(0) + '%');
  assert.ok(own, 'City Ownership category rendered');
  assert.deepEqual(slugs(own, '.lb-row:not(.lb-na)').sort(), ['beta-ca', 'gamma-ca'], 'Alpha is not ranked on partial roads');
  assert.deepEqual(slugs(own, '.lb-row.lb-na'), ['alpha-ca']);
  const award = own.querySelector('.lb-award');
  assert.doesNotMatch(award ? award.textContent : '', /Alpha/);

  unrankedCard(win, cities[0]);
  // A partial layer is described differently from a missing one.
  assert.ok(win.unrankedLabel(alpha).includes('roads'), win.unrankedLabel(alpha));
  assert.notEqual(win.unrankedLabel(alpha), win.unrankedLabel({ missing: alpha.partial, partial: [] }));
});
