// The Compare podium and the About tab's in-page links:
// a Solvency Score medal needs a score of at least
// 50, the ranking's tie-break is a total order, the city in view is marked on
// the cards and leaderboards, and an About link scrolls the tab to its heading.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON, scenariosJSON, forecastJSON, seedJSON, readFixture, fullData } from './fixtures.mjs';

const MEDALS = ['\u{1F947}', '\u{1F948}', '\u{1F949}'];

// city builds one element of the array renderCompare consumes: the real
// fixture with the roads budget set so its Solvency Score is `score`
// (current / hold-steady budget x 100), solvent unless `insolvency` is given.
function city(slug, score, insolvency) {
  const forecast = readFixture('forecast.json').map((f) => {
    if (f.resource_type !== 'roads') return f;
    const rest = { ...f, break_even_budget: 1e7, current_budget: score * 1e5, funding_gap: 1e7 / (score * 1e5) - 1 };
    delete rest.insolvency_year;
    if (insolvency) rest.insolvency_year = insolvency;
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
  assert.equal(third.querySelector('.rank-medal').textContent, '#3', 'a score of 3 must not take bronze');
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
    assert.equal(el.querySelector('.current-city-cue').textContent, ' (selected city)');
  }
  assert.equal(doc.querySelectorAll('#compare-tab [aria-current]').length, marked().length);

  win.selectCity('alpha-ca');
  assert.ok(marked().every((el) => el.dataset.slug === 'alpha-ca'));
  assert.equal(doc.querySelectorAll('#compare-tab .current-city-cue').length, marked().length,
    'the previous city kept its screen-reader cue');
});

test('the About tab links the full methodology on GitHub, in a new tab', async () => {
  const { doc } = await renderWith([]);
  const link = Array.from(doc.querySelectorAll('#about-tab a'))
    .find((a) => a.textContent.startsWith('Full methodology'));
  assert.ok(link, 'the About tab has no Full methodology link');
  assert.match(link.getAttribute('href'),
    /^https:\/\/github\.com\/jcrussell\/solvent-streets\/blob\/[^/]+\/internal\/export\/docs\/methodology\.md$/);
  assert.equal(link.getAttribute('target'), '_blank');
  assert.equal(link.getAttribute('rel'), 'noopener');
});

// lbBlock returns the leaderboard category whose title starts with `label`.
function lbBlock(doc, label) {
  return Array.from(doc.querySelectorAll('#leaderboard-list .lb-category'))
    .find((c) => c.querySelector('h4').textContent.startsWith(label));
}
const lbMedals = (block) => Array.from(block.querySelectorAll('.lb-row .lb-medal')).map((e) => e.textContent);

test('a Street Funding Gap row takes a medal only at a Solvency Score of 50', async () => {
  const { doc } = await renderWith([city('alpha-ca', 120), city('beta-ca', 60), city('gamma-or', 3)]);
  const gap = lbBlock(doc, 'Street Funding Gap');
  assert.deepEqual(lbMedals(gap), [MEDALS[0], MEDALS[1], '3.'], 'a +3233% gap must not take bronze');
  assert.ok(gap.querySelector('.lb-award'), 'the best gap is still a fair winner');
});

test('when every gap is poor, the Street Funding Gap board gives no medal and no award', async () => {
  const { doc } = await renderWith([city('alpha-ca', 30), city('beta-ca', 10)]);
  const gap = lbBlock(doc, 'Street Funding Gap');
  assert.deepEqual(lbMedals(gap), ['1.', '2.']);
  assert.equal(gap.querySelector('.lb-award'), null, 'a +233% gap must not be crowned');
});

test('Years to Insolvency medals follow the same floor', async () => {
  const { doc } = await renderWith([city('alpha-ca', 60, 20), city('beta-ca', 20, 12), city('gamma-or', 10, 8)]);
  const ins = lbBlock(doc, 'Years to Insolvency');
  assert.deepEqual(lbMedals(ins), [MEDALS[0], '2.', '3.']);
  assert.ok(ins.querySelector('.lb-award'));
});

// That board sorts by insolvency year, not by score, so its leader can fail
// the floor while the runner-up passes. Medals stop at the first city under
// the floor: no silver without a gold, and no award.
test('Years to Insolvency shows no silver under an unmedalled leader', async () => {
  const { doc } = await renderWith([city('alpha-ca', 40, 20), city('beta-ca', 60, 12)]);
  const ins = lbBlock(doc, 'Years to Insolvency');
  assert.deepEqual(lbMedals(ins), ['1.', '2.']);
  assert.equal(ins.querySelector('.lb-award'), null);
});

// Compare's per-area figures depend on the scope, so the scope toggle must
// re-render it, and a units round trip must not change it.
test('Compare follows the scope toggle, and a units round trip leaves it unchanged', async () => {
  const { win, $, flush } = load({ data: { ...fullData, 'compare.json': readFixture('compare.json') },
    url: 'https://example.test/index.html?tab=compare-tab' });
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
    url: 'https://example.test/index.html?city=alpha-ca&tab=compare-tab' });
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

test('the solvency card lists city-maintained roads before insolvency', async () => {
  const roads = city('alpha-ca', 120, 12);
  roads.forecast = roads.forecast.map((f) => (f.resource_type === 'roads' ? { ...f, roads_area: 4e6 } : f));
  const { doc } = await renderWith([roads, city('beta-ca', 60)]);
  const card = cards(doc).find((c) => c.dataset.slug === 'alpha-ca');
  const labels = Array.from(card.querySelectorAll('.sb-row > span:first-child')).map((s) => s.textContent);
  assert.deepEqual(labels, ['Current Budget', 'Hold-Steady Budget', 'Funding Gap', 'City-Maintained Roads', 'Insolvency']);
});
