// The Compare tab's funding-gap leaderboard.
//
// fundingGap is the only signed leaderboard metric: negative means the city
// already budgets above its hold-steady level. Bar widths are a ratio against
// the category max, so a scope where every city is over-funded made maxVal
// negative -- and negative/negative is positive, so every bar clamped to full
// width and the ranking the bars exist to show conveyed nothing.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
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

// barWidths returns the bar widths, in order, for the funding-gap category.
function barWidths(win) {
  const html = win.document.getElementById('leaderboard-list').innerHTML;
  // Each category renders its own block; isolate the funding-gap one by its
  // label so a sibling category's bars can never be read by mistake.
  const start = html.indexOf('Street Funding Gap');
  assert.ok(start >= 0, 'funding-gap category did not render');
  const next = html.indexOf('Years to Insolvency', start);
  const block = html.slice(start, next >= 0 ? next : html.length);
  return [...block.matchAll(/class="lb-bar" style="width:([\d.]+)%/g)].map((m) => parseFloat(m[1]));
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
  const win = await renderWith([city('Alpha', -0.5), city('Beta', -0.1)]);
  const widths = barWidths(win);
  assert.equal(widths.length, 2, `expected 2 bars, got ${widths.length}`);
  assert.deepEqual(widths, [0, 0],
    'an all-over-funded scope has no funding gap to show, so every bar is empty');
});

test('mixed sign: under-funded cities keep a proportional bar', async () => {
  // The common case must be untouched by the floor. +40% is the max, so it is
  // the full-width reference and +20% is half of it; the over-funded city
  // clamps to 0 exactly as it did before.
  const win = await renderWith([city('Under', 0.4), city('Half', 0.2), city('Over', -0.3)]);
  const widths = barWidths(win);
  assert.equal(widths.length, 3, `expected 3 bars, got ${widths.length}`);
  // Sorted ascending (lower gap is better): Over, Half, Under.
  assert.deepEqual(widths, [0, 50, 100]);
});

test('all gaps exactly zero: no division by zero', async () => {
  const win = await renderWith([city('A', 0), city('B', 0)]);
  assert.deepEqual(barWidths(win), [0, 0]);
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
    delete rest.insolvency_year;
    return rest;
  });
  const noBudget = { ...city('Beta', 0), forecast: unbudgeted };
  const win = await renderWith([solo, noBudget]);
  const html = win.document.getElementById('leaderboard-list').innerHTML;
  const start = html.indexOf('Street Funding Gap');
  const block = html.slice(start, html.indexOf('Years to Insolvency', start) >= 0 ? html.indexOf('Years to Insolvency', start) : html.length);
  assert.ok(!block.includes('lb-award'), `a one-city category must not award gold:\n${block}`);
  assert.ok(!block.includes('\u{1F947}'), 'a one-city category must not draw a medal');
  const cards = win.document.getElementById('solvency-cards').innerHTML;
  assert.ok(!cards.includes('\u{1F947}'), 'the only budgeted city must not take a solvency gold by default');
});

test('awards use neutral wording and the backlog ranks per acre', async () => {
  const win = await renderWith([city('Alpha', 0.4), city('Beta', 0.2)]);
  const html = win.document.getElementById('leaderboard-list').innerHTML;
  assert.ok(html.includes('Smallest Funding Gap: Beta'), 'funding-gap award should be "Smallest Funding Gap"');
  assert.ok(!html.includes('Best Funded'), '"Best Funded" praised a funding gap');
  assert.match(html, /Deferred Backlog per acre/, 'the backlog leaderboard should rank per acre');
  assert.match(html, /Maintenance Cost per acre/, 'per-area labels use the singular unit');
});
