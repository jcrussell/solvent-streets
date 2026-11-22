// A city whose config cites no PCI forecasts from
// the default condition, so its hold-steady budget and solvency score rest
// on an assumed starting point. forecast.json's roads entry says so
// (initial_pci_source "default"), and Compare and Financials mark the city with a †.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON, scenariosJSON, forecastJSON, seedJSON, readFixture } from './fixtures.mjs';

/** @param {string} name @param {boolean} assumed */
function city(name, assumed) {
  const forecast = readFixture('forecast.json').map((f) =>
    f.resource_type === 'roads' ? { ...f, current_budget: 5e6, initial_pci_source: assumed ? 'default' : 'file' } : f);
  return { slug: name.toLowerCase(), name, meta: metaJSON, scenarios: scenariosJSON, seed: seedJSON, forecast };
}

async function ready(forecast = forecastJSON, url = undefined) {
  const h = load({
    data: { 'meta.json': metaJSON, 'scenarios.json': scenariosJSON, 'forecast.json': forecast, 'forecast_seed.json': seedJSON },
    ...(url ? { url } : {}),
  });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  return h;
}

test('Compare marks only the cities whose PCI is assumed, and explains the mark', async () => {
  const h = await ready();
  h.win.renderCompare([city('Alpha', true), city('Beta', false)]);
  // On a card the mark is a corner badge, not text after the name.
  const names = h.$$('#solvency-cards .city-name').map((el) => el.textContent);
  assert.deepEqual(names.slice().sort(), ['Alpha', 'Beta']);
  const card = (name) => h.$$('#solvency-cards .score-card').find((c) => c.querySelector('.city-name').textContent === name);
  const badge = card('Alpha').querySelector(':scope > .default-pci-badge');
  assert.ok(badge, 'the default-PCI city has no card badge');
  assert.equal(badge.textContent, '†');
  assert.match(badge.getAttribute('aria-label'), /^Condition assumed/);
  assert.equal(badge.getAttribute('title'), badge.getAttribute('aria-label'));
  assert.equal(card('Beta').querySelector('.default-pci-badge'), null, 'a city with a cited PCI is badged');
  assert.equal(h.$$('#solvency-cards .default-pci').length, 0, 'a card still carries the inline mark');
  assert.equal(h.win.document.getElementById('default-pci-note').hidden, false);
  assert.ok(h.$$('#leaderboard-list .default-pci').length > 0, 'the leaderboard does not mark Alpha');

  h.win.renderCompare([city('Beta', false), city('Gamma', false)]);
  assert.equal(h.win.document.getElementById('default-pci-note').hidden, true,
    'the footnote stays up with no marked city on the page');
});

test('the Financials headline names an assumed PCI', async () => {
  const assumed = forecastJSON.map((f) => (f.resource_type === 'roads' ? { ...f, initial_pci_source: 'default' } : f));
  const h = await ready(assumed, 'https://example.test/index.html?tab=financials-tab');
  h.win.renderSolvencyHeadline();
  assert.match(h.$('#solvency-headline').textContent, /Condition assumed: no cited pavement survey/);

  const cited = await ready(forecastJSON, 'https://example.test/index.html?tab=financials-tab');
  cited.win.renderSolvencyHeadline();
  assert.doesNotMatch(cited.$('#solvency-headline').textContent, /Condition assumed/);
});

test('the Financials headline says when a city funds its streets\' upkeep in full', async () => {
  const solvent = forecastJSON.map((f) => {
    if (f.resource_type !== 'roads') return f;
    const rest = { ...f, current_budget: f.break_even_budget * 2 };
    delete rest.insolvency_year;
    return rest;
  });
  const h = await ready(solvent, 'https://example.test/index.html?tab=financials-tab');
  h.win.eval('liveSolvency = null');
  h.win.renderSolvencyHeadline();
  assert.match(h.$('#solvency-headline .lead').textContent, /fully funds its streets’ upkeep\.$/);
  assert.doesNotMatch(h.$('#solvency-headline').textContent, /full cycle/, 'a funded city never names a backlog year');
});
