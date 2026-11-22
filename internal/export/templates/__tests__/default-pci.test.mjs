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
  const card = (slug) => h.$(`#solvency-cards .score-card[data-slug="${slug}"]`);
  const badge = card('alpha').querySelector(':scope > .default-pci-badge');
  assert.ok(badge, 'the default-PCI city has no card badge');
  // The badge is a mark, explained to a pointer and to a screen reader alike.
  const mark = badge.textContent;
  assert.equal(mark.trim().length, 1, 'the badge is a single mark, not a sentence');
  assert.ok(badge.getAttribute('aria-label').trim().length > mark.length, 'the badge is not explained');
  assert.equal(badge.getAttribute('title'), badge.getAttribute('aria-label'));
  assert.equal(card('beta').querySelector('.default-pci-badge'), null, 'a city with a cited PCI is badged');
  assert.equal(h.$$('#solvency-cards .default-pci').length, 0, 'a card still carries the inline mark');
  assert.equal(h.win.document.getElementById('default-pci-note').hidden, false);
  // The leaderboard marks Alpha's rows, and only Alpha's, with the same mark
  // and explanation; the footnote the mark points at carries it too.
  const marks = h.$$('#leaderboard-list .default-pci');
  assert.ok(marks.length > 0, 'the leaderboard does not mark Alpha');
  for (const m of marks) {
    assert.equal(m.closest('.lb-row').getAttribute('data-slug'), 'alpha');
    assert.equal(m.textContent, mark);
    assert.equal(m.getAttribute('aria-label'), badge.getAttribute('aria-label'));
  }
  assert.ok(h.win.document.getElementById('default-pci-note').textContent.includes(mark),
    'the footnote does not carry the mark it explains');

  h.win.renderCompare([city('Beta', false), city('Gamma', false)]);
  assert.equal(h.win.document.getElementById('default-pci-note').hidden, true,
    'the footnote stays up with no marked city on the page');
});

test('the Financials headline names an assumed PCI', async () => {
  const assumed = forecastJSON.map((f) => (f.resource_type === 'roads' ? { ...f, initial_pci_source: 'default' } : f));
  const h = await ready(assumed, 'https://example.test/index.html?tab=financials-tab');
  h.win.renderSolvencyHeadline();
  const detail = h.$('#solvency-headline .default-pci-detail');
  assert.ok(detail, 'the headline does not flag the assumed PCI');
  assert.ok(detail.textContent.includes('(' + seedJSON.initial_pci + ')'), 'the assumed PCI is not named: ' + detail.textContent);

  const cited = await ready(forecastJSON, 'https://example.test/index.html?tab=financials-tab');
  cited.win.renderSolvencyHeadline();
  assert.ok(cited.$('#solvency-headline .lead'), 'the cited city has no headline');
  assert.equal(cited.$('#solvency-headline .default-pci-detail'), null, 'a cited PCI is flagged as assumed');
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
  // Fully funded: the lead quotes no share, and the detail sets the budget
  // against the need without a backlog year (either would be a .num figure).
  const money = h.win.PvmtCommon.fmtMoney;
  const roads = solvent.find((f) => f.resource_type === 'roads');
  const lead = h.$('#solvency-headline .lead');
  assert.ok(lead.textContent.trim().length > 0);
  assert.equal(lead.querySelector('.num'), null, 'a fully funded city is given a share');
  const detail = h.$('#solvency-headline .detail');
  assert.ok(detail.textContent.includes(money(roads.current_budget)) && detail.textContent.includes(money(roads.break_even_budget)),
    detail.textContent);
  assert.equal(detail.querySelector('.num'), null, 'a funded city never names a backlog year');

  // The same city short of need leads with the share it funds, and names
  // the year its backlog passes a full cycle.
  const short = solvent.map((f) => (f.resource_type === 'roads'
    ? { ...f, current_budget: f.break_even_budget / 2, insolvency_year: 9 } : f));
  const s = await ready(short, 'https://example.test/index.html?tab=financials-tab');
  s.win.eval('liveSolvency = null');
  s.win.renderSolvencyHeadline();
  assert.equal(s.$('#solvency-headline .lead .num').textContent, '50%');
  assert.equal(s.$('#solvency-headline .detail .num').textContent, String(s.get('forecastYearOne()') + 8));
});
