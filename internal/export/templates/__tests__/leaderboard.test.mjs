// The Compare tab's funding-gap leaderboard.
//
// fundingGap is the only signed leaderboard metric: negative means the city
// already budgets above its hold-steady level. Bar widths are a ratio against
// the category max, so a scope where every city is over-funded made maxVal
// negative -- and negative/negative is positive, so every bar clamped to full
// width and the ranking the bars exist to show conveyed nothing.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load, boardWith } from './harness.mjs';
import { metaJSON, scenariosJSON, forecastJSON, seedJSON, readFixture } from './fixtures.mjs';

// city builds one element of the array renderCompare consumes, with the roads
// forecast's funding_gap overridden. Everything else is the real fixture, so
// this exercises the true extractMetrics -> renderCompare path rather than a
// stubbed metrics object.
function city(name, gapFraction) {
  const forecast = readFixture('forecast.json').map((f) =>
    f.resource_type === 'roads' ? { ...f, current_budget: 5e6, funding_gap: gapFraction } : f);
  return {
    slug: name.toLowerCase(),
    name,
    meta: metaJSON,
    scenarios: scenariosJSON,
    seed: seedJSON,
    forecast,
  };
}

// gapBoard is the funding-gap category: a category carries no key, so it is
// the one that ranks the city `c` at its funding gap, a signed percentage
// (see boardWith). A sibling category's bars can never be read by mistake.
/** @param {any} win @param {any} c a city passed to renderWith */
function gapBoard(win, c) {
  const gap = win.extractMetrics(c).fundingGap;
  return boardWith(win.document, c.slug, (gap >= 0 ? '+' : '') + gap.toFixed(0) + '%');
}

// barWidths returns the bar widths, in order, for the funding-gap category.
/** @param {any} win @param {any} c a city passed to renderWith */
function barWidths(win, c) {
  const board = gapBoard(win, c);
  assert.ok(board, 'funding-gap category did not render');
  return Array.from(board.querySelectorAll('.lb-row .lb-bar'), (b) => parseFloat(b.style.width));
}

async function renderWith(cities) {
  const h = load({ data: { 'meta.json': metaJSON, 'scenarios.json': scenariosJSON, 'forecast.json': forecastJSON, 'forecast_seed.json': seedJSON } });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  h.win.renderCompare(cities);
  return h.win;
}

test('every city over-funded: bars do not all collapse to full width', async () => {
  // -50% and -10%: before the floor, maxVal = -10 and both ratios came out
  // positive (500% and 100%), clamping to two identical full-width bars.
  const alpha = city('Alpha', -0.5);
  const win = await renderWith([alpha, city('Beta', -0.1)]);
  const widths = barWidths(win, alpha);
  assert.equal(widths.length, 2, `expected 2 bars, got ${widths.length}`);
  assert.deepEqual(widths, [0, 0],
    'an all-over-funded scope has no funding gap to show, so every bar is empty');
});

test('mixed sign: under-funded cities keep a proportional bar', async () => {
  // The common case must be untouched by the floor. +40% is the max, so it is
  // the full-width reference and +20% is half of it; the over-funded city
  // clamps to 0 exactly as it did before.
  const under = city('Under', 0.4);
  const win = await renderWith([under, city('Half', 0.2), city('Over', -0.3)]);
  const widths = barWidths(win, under);
  assert.equal(widths.length, 3, `expected 3 bars, got ${widths.length}`);
  // Sorted ascending (lower gap is better): Over, Half, Under.
  assert.deepEqual(widths, [0, 50, 100]);
});

test('all gaps exactly zero: no division by zero', async () => {
  const a = city('A', 0);
  const win = await renderWith([a, city('B', 0)]);
  assert.deepEqual(barWidths(win, a), [0, 0]);
});

// Medals need a field, and awards must not read as
// praise for a bad outcome.
test('one eligible city gets no medal and no award', async () => {
  // Only Alpha has a cited budget, so the funding-gap pool has one city.
  const solo = city('Alpha', 2.15);
  const unbudgeted = readFixture('forecast.json').map((f) => {
    if (f.resource_type !== 'roads') return f;
    const rest = { ...f };
    delete rest.current_budget;
    delete rest.funding_gap;
    delete rest.tipping_point_year;
    return rest;
  });
  const noBudget = { ...city('Beta', 0), forecast: unbudgeted };
  const win = await renderWith([solo, noBudget]);
  const board = gapBoard(win, solo);
  assert.ok(board, 'funding-gap category did not render');
  assert.equal(board.querySelectorAll('.lb-row:not(.lb-na)').length, 1);
  assert.equal(board.querySelector('.lb-award'), null, `a one-city category must not award gold:\n${board.innerHTML}`);
  assert.ok(!board.innerHTML.includes('\u{1F947}'), 'a one-city category must not draw a medal');
  const cards = win.document.getElementById('solvency-cards').innerHTML;
  assert.ok(!cards.includes('\u{1F947}'), 'the only budgeted city must not take a solvency gold by default');
});

test('the smallest gap takes the award, and the per-area rankings name the singular unit', async () => {
  const cities = [city('Alpha', 0.4), city('Beta', 0.2)];
  const win = await renderWith(cities);
  // Lower is better: the award goes to the smaller gap, named on it.
  const gap = gapBoard(win, cities[0]);
  assert.deepEqual(Array.from(gap.querySelectorAll('.lb-row'), (r) => r.getAttribute('data-slug')), ['beta', 'alpha']);
  const award = gap.querySelector('.lb-award').textContent;
  assert.ok(award.includes('Beta') && !award.includes('Alpha'), 'the funding-gap award went to the wrong city: ' + award);

  // The backlog and the year-1 need each rank per area unit, and their titles
  // name that unit in the singular ("per acre").
  const ms = cities.map((c) => win.extractMetrics(c));
  win.applyRegionHorizon(ms);
  const money = win.PvmtCommon.fmtMoney;
  const one = win.areaLargeUnitSingular();
  const many = win.areaLargeUnit();
  assert.ok(many.startsWith(one) && many !== one, 'fixture: the units read ' + one + ' / ' + many);
  for (const key of ['horizonBacklogPerAreaUnit', 'year1PerAreaUnit']) {
    assert.ok(ms[0][key] > 0, 'fixture: Alpha has no ' + key);
    const board = boardWith(win.document, 'alpha', money(ms[0][key]));
    assert.ok(board, 'no ranking shows ' + key);
    const title = board.querySelector('h4').firstChild.textContent;
    assert.ok(title.includes(one), key + ': the title does not name the unit: ' + title);
    assert.ok(!title.includes(many), key + ': the title names the unit in the plural: ' + title);
  }
});
