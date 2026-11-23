// Which scenario is "the baseline": Financials,
// Materials and Compare all pick it by findBaseline, so a set that does not
// list the baseline first still reads the baseline everywhere. And the Play
// legend is drawn from the colours the Play map paints with.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, scenariosJSON, readFixture } from './fixtures.mjs';

const baseline = scenariosJSON.city.find((s) => s.scenario.name === 'baseline');
const funded = scenariosJSON.city.find((s) => s.scenario.name === 'fund-100pct');

/** scenarios.json with the baseline moved to the end of each scope's set. */
function baselineLast() {
  const scenarios = readFixture('scenarios.json');
  for (const scope of ['city', 'all']) scenarios[scope].push(scenarios[scope].shift());
  return scenarios;
}

test('the fixture lists the baseline first and funding changes the backlog', () => {
  assert.equal(scenariosJSON.city[0].scenario.name, 'baseline');
  assert.notEqual(baseline.years.at(-1).deferred_backlog, funded.years.at(-1).deferred_backlog);
});

test('Compare reads the baseline even when the set does not list it first', async () => {
  const h = load({ data: fullData });
  await h.flush();
  const city = (name) => ({
    slug: name.toLowerCase(), name,
    meta: readFixture('meta.json'), scenarios: baselineLast(),
    seed: readFixture('forecast_seed.json'), forecast: readFixture('forecast.json'),
  });
  h.win.renderCompare([city('Alpha'), city('Beta')]);

  const chart = h.chartsOn('#compare-cost-chart').at(-1);
  assert.ok(chart, 'the cost-trajectory chart did not render');
  assert.deepEqual(Array.from(chart.data.datasets[0].data), baseline.years.map((y) => y.deferred_backlog));
});

test('Financials reads the same baseline from the same set', async () => {
  const h = load({ data: { ...fullData, 'scenarios.json': baselineLast() } });
  await h.flush();
  // The backlog chart's baseline line is the do-nothing run, wherever the
  // set lists it.
  const backlog = h.win.__charts.find((c) => c.data?.datasets?.some((d) => d.fill === true));
  assert.ok(backlog, 'no backlog chart with a filled baseline');
  const line = backlog.data.datasets.find((d) => d.fill === true);
  assert.equal(line.label, baseline.scenario.label);
  assert.deepEqual(Array.from(line.data), baseline.years.map((y) => y.deferred_backlog));
});

test('compare.json\'s stripped set is its only entry; a set with no baseline has none', async () => {
  const { win } = load({ data: fullData });
  const stripped = [{ years: baseline.years }];
  assert.equal(win.findBaseline(stripped), stripped[0]);
  assert.equal(win.findBaseline(scenariosJSON.city.filter((s) => s !== baseline)), null);
  assert.equal(win.findBaseline([]), null);
  assert.equal(win.findBaseline(null), null);
});

test('play: the legend ramp and gravel swatch are the map\'s colours', async () => {
  const { $, get } = load({ page: 'play', data: {} });
  const colors = get('BAND_COLORS');
  assert.equal(colors.length, 6);

  const ramp = $('#legend .legend-bar').style.backgroundImage.toLowerCase().replace(/\s+/g, '');
  assert.match(ramp, /^linear-gradient\(90deg,/);
  // jsdom may hand colours back as rgb(); compare each stop through the
  // same normalisation.
  const probe = $('#legend .gravel-swatch').ownerDocument.createElement('span');
  const norm = (c) => { probe.style.color = c; return probe.style.color.replace(/\s+/g, ''); };
  const stops = ramp.slice('linear-gradient(90deg,'.length, -1).match(/#[0-9a-f]{6}|rgb\([^)]*\)/g);
  assert.deepEqual(stops.map(norm), Array.from(colors, norm));
  assert.equal(norm($('#legend .gravel-swatch').style.backgroundColor), norm(get('GRAVEL_COLOR')));
});
