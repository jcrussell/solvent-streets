// The Compare podium and the About tab's in-page links:
// a Solvency Score medal needs a score of at least
// 50, the ranking's tie-break is a total order, the city in view is marked on
// the cards and leaderboards, and an About link scrolls the tab to its heading.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load, boardWith } from './harness.mjs';
import { metaJSON, scenariosJSON, forecastJSON, seedJSON, readFixture, fullData } from './fixtures.mjs';

const MEDALS = ['\u{1F947}', '\u{1F948}', '\u{1F949}'];

// city builds one element of the array renderCompare consumes: the real
// fixture with the roads budget set so its Solvency Score is `score`
// (current / hold-steady budget x 100), solvent unless `insolvency` is given.
function city(slug, score, insolvency) {
  const forecast = readFixture('forecast.json').map((f) => {
    if (f.resource_type !== 'roads') return f;
    const rest = { ...f, break_even_budget: 1e7, current_budget: score * 1e5, funding_gap: 1e7 / (score * 1e5) - 1 };
    delete rest.tipping_point_year;
    if (insolvency) rest.tipping_point_year = insolvency;
    return rest;
  });
  return { slug, name: slug.toUpperCase(), meta: metaJSON, scenarios: scenariosJSON, seed: seedJSON, forecast };
}

async function renderWith(cities) {
  const h = load({ data: { 'meta.json': metaJSON, 'scenarios.json': scenariosJSON, 'forecast.json': forecastJSON, 'forecast_seed.json': seedJSON } });
  await h.flush();
  h.win.renderCompare(cities);
  return h;
}

const cards = (doc) => Array.from(doc.querySelectorAll('#solvency-cards .score-card'));

test('a Solvency Score below 50 keeps its rank but takes no medal', async () => {
  const { doc } = await renderWith([city('alpha-ca', 120), city('beta-ca', 60), city('gamma-or', 3)]);
  const [gold, silver, third] = cards(doc);
  assert.equal(gold.querySelector('.rank-medal').textContent, MEDALS[0]);
  assert.ok(gold.classList.contains('rank-1'));
  assert.equal(silver.querySelector('.rank-medal').textContent, MEDALS[1]);
  assert.equal(third.dataset.slug, 'gamma-or');
  const rank = third.querySelector('.rank-medal').textContent;
  assert.ok(!MEDALS.includes(rank), 'a score of 3 must not take bronze');
  assert.match(rank, /^\D*3$/, 'the third city still shows its rank');
  assert.ok(!third.classList.contains('rank-3'), 'a score of 3 must not get podium styling');
});

test('a lone budgeted city still takes no medal and shows no rank', async () => {
  const solo = city('alpha-ca', 120);
  const none = { ...city('beta-ca', 50), forecast: readFixture('forecast.json').map((f) => {
    if (f.resource_type !== 'roads') return f;
    const rest = { ...f };
    delete rest.current_budget;
    return rest;
  }) };
  const { doc } = await renderWith([solo, none]);
  assert.equal(cards(doc)[0].querySelector('.rank-medal').textContent, '');
});

test('equal coverage ranks later insolvency first, and two solvent cities tie', async () => {
  const { doc } = await renderWith([
    city('alpha-ca', 80, 12), city('beta-ca', 80), city('gamma-or', 80, 18), city('delta-wa', 80),
  ]);
  assert.deepEqual(cards(doc).map((c) => c.dataset.slug), ['beta-ca', 'delta-wa', 'gamma-or', 'alpha-ca']);
});

test('the city in view is marked on its cards and leaderboard rows', async () => {
  const { win, doc } = await renderWith([city('alpha-ca', 120), city('beta-ca', 60)]);
  const marked = () => Array.from(doc.querySelectorAll('#compare-tab .current-city'));
  win.selectCity('beta-ca');
  assert.ok(marked().length > 2, 'the cards and the leaderboard rows should be marked');
  for (const el of marked()) {
    assert.equal(el.dataset.slug, 'beta-ca');
    assert.equal(el.getAttribute('aria-current'), 'true');
    assert.ok(el.querySelector('.current-city-cue').textContent.trim().length > 0, 'the screen-reader cue is empty');
  }
  assert.equal(doc.querySelectorAll('#compare-tab [aria-current]').length, marked().length);

  win.selectCity('alpha-ca');
  assert.ok(marked().every((el) => el.dataset.slug === 'alpha-ca'));
  assert.equal(doc.querySelectorAll('#compare-tab .current-city-cue').length, marked().length,
    'the previous city kept its screen-reader cue');
});

test('the About tab links the full methodology on GitHub, in a new tab', async () => {
  const { doc } = await renderWith([]);
  const link = doc.querySelector('#about-tab a[href$="/docs/methodology.md"]');
  assert.ok(link, 'the About tab has no link to docs/methodology.md');
  assert.ok(link.textContent.trim().length > 0, 'the methodology link has no text');
  assert.match(link.getAttribute('href'),
    /^https:\/\/github\.com\/jcrussell\/solvent-streets\/blob\/[^/]+\/docs\/methodology\.md$/);
  assert.equal(link.getAttribute('target'), '_blank');
  assert.equal(link.getAttribute('rel'), 'noopener');
});

// A leaderboard category carries no key, so the two roads-solvency boards are
// found by the figure they rank a city at (see boardWith): the funding gap is
// the only signed percentage, the backlog-cycle year the only count of years
// (inYearsText).
/** @param {any} h @param {any} c a city passed to renderWith */
function gapBoard(h, c) {
  const gap = h.win.extractMetrics(c).fundingGap;
  return boardWith(h.doc, c.slug, (gap >= 0 ? '+' : '') + gap.toFixed(0) + '%');
}
/** @param {any} h @param {any} c a city passed to renderWith, with an insolvency year */
function cycleBoard(h, c) {
  return boardWith(h.doc, c.slug, h.win.inYearsText(h.win.extractMetrics(c).tippingPointYear));
}
const lbMedals = (block) => Array.from(block.querySelectorAll('.lb-row .lb-medal')).map((e) => e.textContent);
const rowSlugs = (block) => Array.from(block.querySelectorAll('.lb-row'), (r) => r.getAttribute('data-slug'));
// place reads an unmedalled row's marker as its number; a medal is NaN.
const place = (marker) => (/^\d+\D*$/.test(marker) ? parseInt(marker, 10) : NaN);

test('a Street Funding Gap row takes a medal only at a Solvency Score of 50', async () => {
  const alpha = city('alpha-ca', 120);
  const h = await renderWith([alpha, city('beta-ca', 60), city('gamma-or', 3)]);
  const gap = gapBoard(h, alpha);
  assert.deepEqual(rowSlugs(gap), ['alpha-ca', 'beta-ca', 'gamma-or']);
  const medals = lbMedals(gap);
  assert.deepEqual(medals.slice(0, 2), [MEDALS[0], MEDALS[1]]);
  assert.ok(!MEDALS.includes(medals[2]), 'a +3233% gap must not take bronze');
  assert.match(medals[2], /^3\D*$/, 'the third row still shows its place');
  assert.ok(gap.querySelector('.lb-award'), 'the best gap is still a fair winner');
});

test('when every gap is poor, the Street Funding Gap board gives no medal and no award', async () => {
  const alpha = city('alpha-ca', 30);
  const gap = gapBoard(await renderWith([alpha, city('beta-ca', 10)]), alpha);
  assert.deepEqual(rowSlugs(gap), ['alpha-ca', 'beta-ca']);
  assert.deepEqual(lbMedals(gap).map(place), [1, 2]);
  assert.equal(gap.querySelector('.lb-award'), null, 'a +233% gap must not be crowned');
});

test('Backlog-cycle medals follow the same floor', async () => {
  const alpha = city('alpha-ca', 60, 20);
  const ins = cycleBoard(await renderWith([alpha, city('beta-ca', 20, 12), city('gamma-or', 10, 8)]), alpha);
  assert.deepEqual(rowSlugs(ins), ['alpha-ca', 'beta-ca', 'gamma-or']);
  assert.equal(lbMedals(ins)[0], MEDALS[0]);
  assert.deepEqual(lbMedals(ins).slice(1).map(place), [2, 3]);
  assert.ok(ins.querySelector('.lb-award'));
});

// That board sorts by insolvency year, not by score, so its leader can fail
// the floor while the runner-up passes. Medals stop at the first city under
// the floor: no silver without a gold, and no award.
test('the backlog-cycle board shows no silver under an unmedalled leader', async () => {
  const alpha = city('alpha-ca', 40, 20);
  const ins = cycleBoard(await renderWith([alpha, city('beta-ca', 60, 12)]), alpha);
  assert.deepEqual(rowSlugs(ins), ['alpha-ca', 'beta-ca']);
  assert.deepEqual(lbMedals(ins).map(place), [1, 2]);
  assert.equal(ins.querySelector('.lb-award'), null);
});

// Compare's per-area figures depend on the scope, so the scope toggle must
// re-render it, and a units round trip must not change it.
test('Compare follows the scope toggle, and a units round trip leaves it unchanged', async () => {
  const { win, $, flush } = load({ data: { ...fullData, 'compare.json': readFixture('compare.json') },
    url: 'https://example.test/index.html?tab=compare' });
  await flush(12);
  const text = () => $('#compare-tab').textContent.replace(/\s+/g, ' ');
  const city = text();
  win.selectScope('all'); await flush(8);
  const all = text();
  assert.notEqual(all, city, 'the scope toggle did not re-render Compare');
  const flip = () => win.selectUnits(win.eval('UNIT_SYSTEM') === 'metric' ? 'imperial' : 'metric');
  flip(); await flush(8);
  flip(); await flush(8);
  assert.equal(text(), all, 'a units round trip changed Compare');
});

test('back to an entry in another scope re-renders Compare in that scope', async () => {
  const { win, $, flush } = load({ data: { ...fullData, 'compare.json': readFixture('compare.json') },
    url: 'https://example.test/index.html?city=alpha-ca&tab=compare' });
  await flush(12);
  const text = () => $('#compare-tab').textContent.replace(/\s+/g, ' ');
  const city = text();
  win.selectCity('beta-ca'); await flush(8);
  win.selectScope('all'); await flush(8);
  assert.notEqual(text(), city);
  win.history.go(-2); await flush(12);
  assert.equal(win.eval('currentScope'), 'city');
  assert.equal(text(), city, 'Compare still shows the all-jurisdiction figures');
});

// The breakdown rows carry no key: they are read by position and told apart
// by the figures they show.
const breakdown = (card) => Array.from(card.querySelectorAll('.sb-row'), (r) => r.lastElementChild);

test('the solvency card lists city-maintained roads before the backlog year', async () => {
  const roads = city('alpha-ca', 120, 12);
  roads.forecast = roads.forecast.map((f) => (f.resource_type === 'roads' ? { ...f, roads_area: 4e6 } : f));
  const { doc, win } = await renderWith([roads, city('beta-ca', 60)]);
  const m = win.extractMetrics(roads);
  const money = win.PvmtCommon.fmtMoney;
  const values = breakdown(cards(doc).find((c) => c.dataset.slug === 'alpha-ca')).map((v) => v.textContent);
  assert.equal(values.length, 5);
  assert.ok(values[0].startsWith(money(m.currentBudget)), values[0]);
  assert.ok(values[1].startsWith(money(m.breakEvenBudget)), values[1]);
  assert.equal(values[2], (m.fundingGap >= 0 ? '+' : '') + m.fundingGap.toFixed(0) + '%');
  assert.ok(m.solvencyRoadsAreaLarge > 0, 'fixture: no city-maintained roads area');
  assert.equal(values[3], win.fmtNum(m.solvencyRoadsAreaLarge) + ' ' + win.areaLargeUnit());
  assert.equal(values[4], win.inYearsText(12));
});

test('a city never a full cycle behind reads the horizon it stays within, explained in a tooltip', async () => {
  const h = await renderWith([city('alpha-ca', 120), city('beta-ca', 60, 12)]);
  const horizon = seedJSON.horizon_years;
  assert.ok(horizon > 0 && horizon !== 12);
  // The backlog year is the breakdown's last row.
  const cycle = (slug) => breakdown(cards(h.doc).find((c) => c.dataset.slug === slug)).at(-1);
  const never = cycle('alpha-ca');
  assert.match(never.textContent, new RegExp('\\b' + horizon + '\\b'));
  assert.match(never.querySelector('[title]').getAttribute('title'), new RegExp('\\b' + horizon + '\\b'), 'the horizon is explained in a tooltip');
  // A crossing city reads its own forecast year as a count of years, not a
  // calendar year, and needs no tooltip.
  const crossing = cycle('beta-ca');
  assert.equal(crossing.textContent, h.win.inYearsText(12));
  assert.equal(crossing.querySelector('[title]'), null);
  assert.notEqual(never.textContent, h.win.inYearsText(horizon), 'a city that never crosses reads as one crossing in the last year');
  // inYearsText counts years, singular for one.
  assert.match(h.win.inYearsText(12), /\b12\b/);
  assert.notEqual(h.win.inYearsText(1).replace('1', ''), h.win.inYearsText(2).replace('2', ''));
});
