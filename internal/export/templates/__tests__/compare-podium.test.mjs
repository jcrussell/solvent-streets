// The Compare podium and the About tab's in-page links
// (solvent-streets-ern4.34): a Solvency Score medal needs a score of at least
// 50, the ranking's tie-break is a total order, the city in view is marked on
// the cards and leaderboards, and an About link scrolls the tab to its heading.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON, scenariosJSON, forecastJSON, seedJSON, readFixture } from './fixtures.mjs';

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

test('an in-page About link scrolls the tab to its heading', async () => {
  const { win, doc } = await renderWith([]);
  const scrolled = [];
  win.Element.prototype.scrollIntoView = function () { scrolled.push(this.id); };
  const link = doc.querySelector('#about-tab a[href="#how-spending-moves-condition"]');
  assert.ok(link, 'the methodology has no link to #how-spending-moves-condition');
  assert.ok(doc.getElementById('how-spending-moves-condition'), 'the heading has no id');
  const ev = new win.MouseEvent('click', { bubbles: true, cancelable: true });
  link.dispatchEvent(ev);
  assert.ok(ev.defaultPrevented, 'the link navigated instead of scrolling the tab');
  assert.deepEqual(scrolled, ['how-spending-moves-condition']);
});
