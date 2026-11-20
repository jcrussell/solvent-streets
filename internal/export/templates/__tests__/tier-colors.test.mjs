// What the page derives from the city's cost tiers:
// the cohort table's PCI badge, the tier colours and the chart legend all
// follow forecast_seed.json's cost_tiers, including a config's own bands and
// labels.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON, readFixture } from './fixtures.mjs';

const GREEN = '#22c55e', AMBER = '#f59e0b', RED = '#ef4444', GREY = '#9ca3af';

/** @param {any[]} [tiers] the seed's cost tiers; the fixture's by default */
async function page(tiers, scenarios = readFixture('scenarios.json')) {
  const seed = readFixture('forecast_seed.json');
  if (tiers) seed.cost_tiers = tiers;
  const h = load({ data: { ...fullData, 'forecast_seed.json': seed, 'scenarios.json': scenarios } });
  await h.flush();
  return h;
}

/** badges renders the cohort table for the given end PCIs and returns each badge's class. */
function badges(win, pcis) {
  win.renderCohortBreakdown(pcis.map((pci, i) => ({
    classification: 'class' + i, area: 1000 - i, decay_rate: 0.04, end_pci: pci, total_spend: 0, deferred_backlog: 0,
  })));
  // Rows sort by area descending, which is the order given.
  return Array.from(win.document.querySelectorAll('#cohort-table tbody .pci-badge'),
    (b) => b.className.replace('pci-badge ', ''));
}

const tier = (min, max, label) => ({ min_pci: min, max_pci: max, cost_per_sqm: 1, label });

test('the fixture schedule is three tiers split at 50 and 70', () => {
  assert.deepEqual(seedJSON.cost_tiers.map((t) => [t.min_pci, t.label]),
    [[70, 'preventive'], [50, 'rehab'], [0, 'reconstruction']]);
});

test('the PCI badge follows the city\'s tier bands', async () => {
  const { win } = await page();
  assert.deepEqual(badges(win, [85, 70, 65, 50, 45, 10]),
    ['pci-good', 'pci-good', 'pci-fair', 'pci-fair', 'pci-poor', 'pci-poor']);
});

// The site shows PCI as a whole number, and the badge follows that number:
// 69.995 shows as 70, in the band that starts at 70, and so does 69.497,
// which the export writes as 69.50.
test('the PCI badge follows the whole number it shows', async () => {
  const { win, doc } = await page();
  assert.deepEqual(badges(win, [69.995, 69.497, 69.494]), ['pci-good', 'pci-good', 'pci-fair']);
  assert.deepEqual(Array.from(doc.querySelectorAll('#cohort-table tbody .pci-badge'), (b) => b.textContent),
    ['70', '70', '69']);
});

// The PCI chart's tooltip shows the same whole number, not the raw value.
test('the PCI Over Time tooltip shows a whole-number PCI', async () => {
  const { win, flush } = await page();
  win.selectTab('financials-tab');
  await flush();
  const chart = win.__charts.find((c) => c.options && c.options.scales && c.options.scales.y &&
    c.options.scales.y.title && c.options.scales.y.title.text === 'PCI');
  assert.ok(chart, 'no PCI chart rendered');
  const label = chart.options.plugins.tooltip.callbacks.label;
  assert.equal(label({ dataset: { label: 'Do nothing' }, parsed: { y: 69.497 } }), 'Do nothing: 70');
  assert.equal(label({ dataset: { label: 'Do nothing' }, parsed: { y: 72.34 } }), 'Do nothing: 72');
});

test('the PCI badge moves with a config\'s own bands', async () => {
  const { win } = await page([tier(80, 101, 'preventive'), tier(30, 80, 'rehab'), tier(0, 30, 'reconstruction')]);
  assert.deepEqual(badges(win, [85, 75, 45, 35, 25]),
    ['pci-good', 'pci-fair', 'pci-fair', 'pci-fair', 'pci-poor']);
});

test('a PCI in a gap between bands takes the nearest band', async () => {
  const { win } = await page([tier(70, 101, 'preventive'), tier(0, 40, 'reconstruction')]);
  assert.equal(win.tierForPCI(65).label, 'preventive');
  assert.equal(win.tierForPCI(45).label, 'reconstruction');
  assert.deepEqual(badges(win, [65, 45]), ['pci-good', 'pci-poor']);
});

test('the default three tiers keep green, amber and red', async () => {
  const { win } = await page();
  assert.deepEqual(win.tierLegendLabels().map((l) => [l.text, l.fillStyle]),
    [['Preventive', GREEN], ['Rehab', AMBER], ['Reconstruction', RED]]);
});

test('custom tier labels get their own legend entries and colours', async () => {
  // Listed worst-first on purpose: rank comes from the PCI bands, not the order.
  const custom = [tier(0, 25, 'rebuild'), tier(25, 50, 'mill'), tier(50, 75, 'overlay'), tier(75, 101, 'seal')];
  const scenarios = readFixture('scenarios.json');
  for (const scope of ['city', 'all']) {
    for (const s of scenarios[scope]) {
      for (const y of s.years) y.cost_tier = bandLabel(custom, y.pci);
    }
  }
  const { win } = await page(custom, scenarios);

  const chart = win.__charts.find((c) => c.options?.plugins?.legend?.labels?.generateLabels);
  assert.ok(chart, 'no tier-coloured chart rendered');
  const legend = chart.options.plugins.legend.labels.generateLabels();
  assert.deepEqual(legend.map((l) => l.text), ['Seal', 'Overlay', 'Mill', 'Rebuild']);
  assert.equal(legend[0].fillStyle, GREEN, 'the best tier is green');
  assert.equal(legend.at(-1).fillStyle, RED, 'the worst tier is red');
  assert.equal(new Set(legend.map((l) => l.fillStyle)).size, 4, 'every tier has its own colour');

  // The bars are drawn in the legend's colours, never the unknown-tier grey.
  const byLabel = new Map(legend.map((l) => [l.text.toLowerCase(), l.fillStyle]));
  const baseline = scenarios.city[0];
  const bars = Array.from(chart.data.datasets[0].backgroundColor);
  assert.deepEqual(bars, baseline.years.map((y) => byLabel.get(y.cost_tier)));
  assert.ok(!bars.includes(GREY));
});

/** The label of the band holding pci, for building a scenario in a custom schedule. */
function bandLabel(tiers, pci) {
  return tiers.find((t) => pci >= t.min_pci && pci < t.max_pci).label;
}

test('a material tier keeps its default colour under a custom cost schedule', async () => {
  const { win } = await page([tier(60, 101, 'light'), tier(0, 60, 'heavy')]);
  assert.equal(win.tierColor('light'), GREEN);
  assert.equal(win.tierColor('heavy'), RED);
  assert.equal(win.tierColor('rehab'), AMBER, 'material tiers use the default labels');
  assert.equal(win.tierColor('no-such-tier'), GREY);
});
