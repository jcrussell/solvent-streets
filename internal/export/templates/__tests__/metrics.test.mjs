// "Color by" per-hex metrics (hex-metrics.json, solvent-streets-ql2w): the
// selector lists the city's metrics, choosing one swaps the paint expression
// to discrete bands guarded against hexes without a value, redraws the legend
// from the metric's scale, shows the points of service, rides the URL only on
// the Map tab, and falls back to coverage when the city lacks the metric.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON, fullData } from './fixtures.mjs';

const plain = (v) => JSON.parse(JSON.stringify(v));
const params = (win) => new URLSearchParams(win.location.search);

function hexFC(features) {
  return {
    type: 'FeatureCollection',
    v: 3,
    features: features.map((props, i) => ({
      type: 'Feature',
      geometry: { type: 'Polygon', coordinates: [[[0, 0], [0, 1], [1, 1], [0, 0]]] },
      properties: { id: `hex:0:${i}`, ...props },
    })),
  };
}

const metricsFile = {
  v: 1,
  metrics: {
    fire_response_s: {
      label: 'Fire response', unit: 's', format: 'minutes', source: 'test',
      scale: { kind: 'sequential', transform: 'linear', bands: [320, 440, 560, 680] },
      values: { 'hex:0:0': 300, 'hex:0:1': 700 },
      refs: { 'hex:0:0': 0 },
      points: [{ id: 'osm:node:9', name: 'Station 9', lon: 0.5, lat: 0.5, source: 'osm' }],
    },
  },
};

const data = {
  ...fullData,
  'meta.json': { ...metaJSON, metrics: { fire_response_s: { label: 'Fire response', unit: 's', format: 'minutes', hexes: 2, min: 300, median: 700, max: 700, points: 1 } } },
  'hexgrid.geojson': hexFC([
    { bbox: { roads: { area: 100, pct: 10 } }, city_same: 1 },
    { bbox: { roads: { area: 100, pct: 10 } }, city_same: 1 },
    { bbox: { roads: { area: 100, pct: 10 } }, city_same: 1 }, // no metric value
  ]),
  'hex-metrics.json': metricsFile,
};

async function loadWithMetrics(url) {
  const h = load({ data, url });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  return h;
}

test('the Color-by selector lists coverage plus each metric and the stats card summarises it', async () => {
  const { $, $$ } = await loadWithMetrics();
  const opts = $$('#hex-metric option').map((o) => o.value);
  assert.deepEqual(opts, ['coverage', 'fire_response_s']);
  assert.equal($('#hex-metric').hidden, false);
  const card = $$('#stats .stat-card').find((c) => c.querySelector('h3').textContent === 'Fire response');
  assert.ok(card, 'no metric card rendered from meta.metrics');
  assert.match(card.textContent, /Median\s*11:40/, 'median must render in m:ss');
  assert.match(card.textContent, /Points of service\s*1/);
});

test('selecting a metric paints discrete bands guarded for missing values, redraws the legend, and shows points', async () => {
  const { win, $, $$, get } = await loadWithMetrics();
  assert.equal(plain(win.hexFillColor())[0], 'interpolate', 'coverage paints the continuous ramp');

  win.selectHexMetric('fire_response_s');
  const paint = plain(win.hexFillColor());
  assert.equal(paint[0], 'case');
  assert.deepEqual(paint[1], ['has', 'metric_v'], 'a hex without a value must not hit the step expression');
  assert.equal(paint[2][0], 'step');
  assert.equal(paint[2].length, 3 + 2 * 4, 'one colour per band: 4 edges -> 5 colours');

  const fc = plain(win.buildBlendedHexFC());
  const byId = Object.fromEntries(fc.features.map((f) => [f.properties.hex_id, f.properties]));
  assert.equal(byId['hex:0:0'].metric_v, 300);
  assert.equal(byId['hex:0:0'].metric_ref, 'Station 9');
  assert.equal(byId['hex:0:1'].metric_v, 700);
  assert.equal(byId['hex:0:2'].metric_v, undefined, 'a hex the metric does not cover carries no value');

  assert.match($('#legend .legend-row label').textContent, /Color by/);
  const labels = $$('#legend .legend-labels span').map((s) => s.textContent);
  assert.deepEqual(labels, ['≤ 5:20', '7:20', '9:20', '11:20', '> 11:20']);
  assert.ok($('#legend .legend-bar').classList.contains('discrete'));
  assert.ok(get('map').__sources.has('metric-points'), 'the points layer source must exist for a metric with points');
  assert.ok(get('map').__layers.has('metric-points'));

  win.selectHexMetric('coverage');
  assert.equal(plain(win.hexFillColor())[0], 'interpolate');
  assert.equal(get('map').__sources.has('metric-points'), false, 'switching back to coverage drops the points layer');
  assert.equal($$('#legend .legend-labels span')[0].textContent, '0%');
});

test('?metric= rides the URL only on the Map tab and restores on load', async () => {
  const { win, get } = await loadWithMetrics();
  win.selectHexMetric('fire_response_s');
  assert.equal(params(win).get('metric'), 'fire_response_s');
  win.selectTab('financials-tab');
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(params(win).get('metric'), null, 'a metric must not pin ?metric= on a tab where it does nothing');
  win.selectTab('map-tab');
  assert.equal(params(win).get('metric'), 'fire_response_s');
  get('Router.push()');

  const deep = await loadWithMetrics('https://example.test/index.html?metric=fire_response_s');
  assert.equal(deep.get('hexMetric'), 'fire_response_s');
  assert.equal(deep.$('#hex-metric').value, 'fire_response_s');
  assert.equal(plain(deep.win.hexFillColor())[0], 'case', 'a deep-linked metric must paint on first load');
});

test('a city without the requested metric falls back to coverage and hides the selector', async () => {
  const h = load({ data: fullData, url: 'https://example.test/index.html?metric=fire_response_s' });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  assert.equal(h.get('hexMetric'), 'coverage');
  assert.equal(h.$('#hex-metric').hidden, true, 'no metrics -> no selector');
  assert.match(h.$('#legend .legend-row label').textContent, /Coverage %/);
});
