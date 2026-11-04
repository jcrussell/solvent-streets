// Generic "Color by" (solvent-streets-59hj): every hex coloring is a registry
// metric. The pavement colorings are DERIVED specs shipped in
// PVMT_CONFIG.hexMetrics (rendered from the real Go registry by the harness)
// and computed client-side over the toggled resources; stored metrics come
// from hex-metrics.json; hexgrid v4 carries geometry-only hexes so stored
// metrics can paint hexes without pavement.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON } from './fixtures.mjs';

const plain = (v) => JSON.parse(JSON.stringify(v));

function hexFC(features) {
  return {
    type: 'FeatureCollection',
    v: 4,
    features: features.map((props, i) => ({
      type: 'Feature',
      geometry: { type: 'Polygon', coordinates: [[[0, 0], [0, 1], [1, 1], [0, 0]]] },
      properties: { id: `hex:0:${i}`, ...props },
    })),
  };
}

const grid = hexFC([
  { bbox: { roads: { area: 1000, pct: 40 }, parking: { area: 3000, pct: 70, spaces: 120 } }, city_same: 1 },
  { bbox: { roads: { area: 500, pct: 20 } }, city_same: 1 },
  {}, // v4: a grid hex with no pavement rows
]);

const costSummary = {
  bbox: { roads: { total_area: 1500, year1_cost: 15000 }, parking: { total_area: 3000, year1_cost: 6000 } },
  city: { roads: { total_area: 1500, year1_cost: 15000 }, parking: { total_area: 3000, year1_cost: 6000 } },
};

const population = {
  label: 'Population', unit: '', format: 'int', source: 'test',
  scale: { kind: 'sequential', transform: 'linear', bands: [10, 100] },
  values: { 'hex:0:0': 50, 'hex:0:1': 2, 'hex:0:2': 80 },
};

async function loadGrid(extra = {}) {
  const data = { 'meta.json': metaJSON, 'hexgrid.geojson': grid, 'hex-cost-summary.json': costSummary, ...extra };
  const h = load({ data });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  return h;
}

const byId = (fc) => Object.fromEntries(plain(fc).features.map((f) => [f.properties.hex_id, f.properties]));

test('the derived pavement colorings come from the registry and are all offered when their inputs exist', async () => {
  const { $$, get } = await loadGrid();
  const opts = $$('#hex-metric option').map((o) => o.value);
  assert.deepEqual(opts, ['coverage_pct', 'est_cost_usd', 'parking_spaces', 'paved_area_m2']);
  assert.equal(get('hexMetric'), 'coverage_pct');
});

test('a derived sum paints its registry bands from the blended toggled resources', async () => {
  const { win } = await loadGrid();
  win.selectHexMetric('paved_area_m2');
  const paint = plain(win.hexFillColor());
  assert.equal(paint[2][0], 'step');
  const fc = byId(win.buildBlendedHexFC());
  assert.equal(fc['hex:0:0'].metric_v, 4000, 'area sums roads + parking');
  assert.equal(fc['hex:0:1'].metric_v, 500);
  assert.equal(fc['hex:0:2'], undefined, 'a derived pavement coloring does not paint pavement-free hexes');

  win.selectHexMetric('est_cost_usd');
  const cost = byId(win.buildBlendedHexFC());
  // roads: 1000/1500 × 15000 + parking: 3000/3000 × 6000
  assert.equal(cost['hex:0:0'].metric_v, 16000);

  win.selectHexMetric('coverage_pct');
  assert.equal(byId(win.buildBlendedHexFC())['hex:0:0'].metric_v, 100, 'coverage is capped at 100');
});

test('legend labels follow the metric format', async () => {
  const { win, $$ } = await loadGrid();
  win.selectHexMetric('parking_spaces');
  const labels = $$('#legend .legend-labels span').map((s) => s.textContent);
  assert.equal(labels[0], '≤ 10');
  assert.equal(labels[labels.length - 1], '> 250');
});

test('untoggling the only resource with spaces falls back from Parking spaces', async () => {
  const { win, $, $$, get } = await loadGrid();
  win.selectHexMetric('parking_spaces');
  const cb = $('#toggles input[data-type="parking"]');
  cb.checked = false;
  cb.dispatchEvent(new win.Event('change'));
  assert.equal(get('hexMetric'), 'coverage_pct');
  assert.ok(!$$('#hex-metric option').map((o) => o.value).includes('parking_spaces'));
});

test('a stored metric paints pavement-free v4 hexes too', async () => {
  const { win } = await loadGrid({ 'hex-metrics.json': { v: 1, metrics: { population } } });
  win.selectHexMetric('population');
  const fc = byId(win.buildBlendedHexFC());
  assert.equal(fc['hex:0:2'].metric_v, 80, 'the geometry-only hex carries the stored value');
  assert.equal(fc['hex:0:2'].pct_covered, 0);
});

test('a ratio divides a blended numerator by a stored denominator above min_den', async () => {
  const h = await loadGrid({ 'hex-metrics.json': { v: 1, metrics: { population } } });
  h.get('derivedMetrics').road_m2_per_resident = {
    key: 'road_m2_per_resident', label: 'Road m² per resident', unit: 'm2', format: 'float1', source: 'test',
    scale: { kind: 'sequential', transform: 'linear', bands: [1, 10] },
    derive: { op: 'ratio', prop: 'area', den: 'population', min_den: 5 },
  };
  h.win.selectHexMetric('road_m2_per_resident');
  assert.equal(h.get('hexMetric'), 'road_m2_per_resident');
  const fc = byId(h.win.buildBlendedHexFC());
  assert.equal(fc['hex:0:0'].metric_v, 80, '4000 m² / 50 residents');
  assert.equal(fc['hex:0:1'].metric_v, undefined, 'below min_den the ratio is left empty');
  assert.equal(fc['hex:0:2'].metric_v, 0, 'a populated hex with no pavement has 0 m² per resident');
});

test('a ratio is not offered without its stored denominator', async () => {
  const h = await loadGrid();
  h.get('derivedMetrics').road_m2_per_resident = {
    key: 'road_m2_per_resident', label: 'x', unit: '', format: 'float1', source: 'test',
    scale: { kind: 'sequential', transform: 'linear', bands: [1] },
    derive: { op: 'ratio', prop: 'area', den: 'population' },
  };
  assert.equal(h.win.metricAvailable('road_m2_per_resident'), false);
});
