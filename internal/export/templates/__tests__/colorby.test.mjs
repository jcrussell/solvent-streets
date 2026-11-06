// Generic "Color by" (solvent-streets-59hj): every hex coloring is a registry
// metric. The pavement colorings are DERIVED specs shipped in
// PVMT_CONFIG.hex_metrics (rendered from the real Go registry by the harness)
// and computed client-side over the toggled resources; stored metrics come
// from hex-metrics.json; hexgrid carries geometry-only hexes so stored
// metrics can paint hexes without pavement.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON } from './fixtures.mjs';

const plain = (v) => JSON.parse(JSON.stringify(v));

function hexFC(features) {
  return {
    type: 'FeatureCollection',
    v: 5,
    features: features.map((props, i) => ({
      type: 'Feature',
      geometry: { type: 'Polygon', coordinates: [[[0, 0], [0, 1], [1, 1], [0, 0]]] },
      properties: { id: `hex:0:${i}`, ...props },
    })),
  };
}

const grid = hexFC([
  { all: { roads: { area: 1000, share: 0.4 }, parking: { area: 3000, share: 0.7, spaces: 120 } }, city_same: 1 },
  { all: { roads: { area: 500, share: 0.2 } }, city_same: 1 },
  {}, // a grid hex with no pavement rows
]);

const costSummary = {
  all: { roads: { total_area: 1500, year1_need: 15000 }, parking: { total_area: 3000, year1_need: 6000 } },
  city: { roads: { total_area: 1500, year1_need: 15000 }, parking: { total_area: 3000, year1_need: 6000 } },
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
  assert.deepEqual(opts, ['coverage_share', 'est_cost_usd', 'parking_spaces', 'paved_area_m2']);
  assert.equal(get('hexMetric'), 'coverage_share');
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

  win.selectHexMetric('coverage_share');
  assert.equal(byId(win.buildBlendedHexFC())['hex:0:0'].metric_v, 1, 'coverage is capped at a share of 1');
});

test('legend labels follow the metric format', async () => {
  const { win, $$ } = await loadGrid();
  win.selectHexMetric('parking_spaces');
  const labels = $$('#legend .legend-labels span').map((s) => s.textContent);
  assert.equal(labels[0], '≤ 10');
  assert.equal(labels[labels.length - 1], '> 250');
});

// Area band edges are metric; converted to sq ft they are never round, and
// at full precision ("5,382 sq ft–10,764 sq ft") the legend outgrew the map.
// Compact edges, with the unit named once on the last label.
test('area legend edges are compact and name the unit once', async () => {
  const { win, $$ } = await loadGrid();
  win.selectHexMetric('paved_area_m2');
  const labels = $$('#legend .legend-labels span').map((s) => s.textContent);
  const unit = win.areaUnit();
  const sqft = unit !== 'm²' && unit !== 'sq m';
  assert.deepEqual(labels, sqft
    ? ['≤ 5.4K', '5.4K–11K', '11K–22K', '22K–43K', '43K–86K', '> 86K ' + unit]
    : ['≤ 500', '500–1K', '1K–2K', '2K–4K', '4K–8K', '> 8K ' + unit]);

  // Dollar edges keep a decimal: fmtCost would round $2,500 to "$3K".
  win.selectHexMetric('est_cost_usd');
  assert.deepEqual($$('#legend .legend-labels span').map((s) => s.textContent),
    ['≤ $1K', '$1K–$2.5K', '$2.5K–$5K', '$5K–$10K', '$10K–$20K', '> $20K']);
});

test('density follows the unit system: per km² stored, per sq mi shown in imperial', async () => {
  const { win } = await loadGrid();
  const big = win.areaVeryLargeUnit();
  const want = big === 'sq mi' ? '2,590 / sq mi' : '1,000 / sq km';
  assert.equal(win.fmtMetric(1000, 'density'), want);
});

test('untoggling the only resource with spaces falls back from Parking spaces', async () => {
  const { win, $, $$, get } = await loadGrid();
  win.selectHexMetric('parking_spaces');
  const cb = $('#toggles input[data-type="parking"]');
  cb.checked = false;
  cb.dispatchEvent(new win.Event('change'));
  assert.equal(get('hexMetric'), 'coverage_share');
  assert.ok(!$$('#hex-metric option').map((o) => o.value).includes('parking_spaces'));
});

test('a stored metric paints pavement-free hexes too', async () => {
  const { win } = await loadGrid({ 'hex-metrics.json': { v: 2, metrics: { population } } });
  win.selectHexMetric('population');
  const fc = byId(win.buildBlendedHexFC());
  assert.equal(fc['hex:0:2'].metric_v, 80, 'the geometry-only hex carries the stored value');
  assert.equal(fc['hex:0:2'].share, 0);
});

// The population ratios are the REAL registry specs (internal/population),
// rendered into PVMT_CONFIG.hex_metrics by the harness: min_den is 10.
test('a ratio divides a blended numerator by a stored denominator above min_den', async () => {
  const h = await loadGrid({ 'hex-metrics.json': { v: 2, metrics: { population } } });
  h.win.selectHexMetric('road_m2_per_resident');
  assert.equal(h.get('hexMetric'), 'road_m2_per_resident');
  const fc = byId(h.win.buildBlendedHexFC());
  assert.equal(fc['hex:0:0'].metric_v, 80, '4000 m² / 50 residents');
  assert.equal(fc['hex:0:1'].metric_v, undefined, 'below min_den the ratio is left empty');
  assert.equal(fc['hex:0:2'].metric_v, 0, 'a populated hex with no pavement has 0 m² per resident');
});

test('metric cards lead with a total for additive counts and never list hexes reached', async () => {
  const meta = { ...metaJSON, metrics: {
    population: { label: 'Population', unit: '', format: 'int', hex_count: 3, min: 2, median: 50, max: 80, total: 132 },
    population_per_km2: { label: 'Population density (per km²)', unit: '1/km2', format: 'int', hex_count: 3, min: 70, median: 1900, max: 3000 },
  } };
  const h = await loadGrid({ 'meta.json': meta });
  const card = (title) => h.$$('#stats .metric-card').find((c) => c.querySelector('h3').textContent === title);
  assert.match(card('Population').textContent, /Total\s*132/);
  assert.doesNotMatch(card('Population density (per km²)').textContent, /Total/, 'a density has no meaningful sum');
  assert.doesNotMatch(h.$('#stats').textContent, /Hexes reached/);
});

test('a ratio is not offered without its stored denominator', async () => {
  const h = await loadGrid();
  assert.equal(h.win.metricAvailable('road_m2_per_resident'), false);
  assert.equal(h.$('#stats .ratio-card'), null, 'no per-resident card without population');
});

test('the per-resident card is a city-wide sum over sum and follows the toggles', async () => {
  const h = await loadGrid({ 'hex-metrics.json': { v: 2, metrics: { population } } });
  const rows = () => Object.fromEntries(h.$$('#stats .ratio-card .stat-row').map((r) =>
    [r.querySelector('.stat-label').textContent, r.querySelector('.stat-value').textContent]));
  // 4500 m² paved / 132 residents (34.1 m², shown in sq ft); $21,000 year-1 need / 132 residents.
  assert.deepEqual(rows(), { 'Paved area per resident': '367 sq ft', 'Year-1 need per resident': '$159' });
  const cb = h.$('#toggles input[data-type="parking"]');
  cb.checked = false;
  cb.dispatchEvent(new h.win.Event('change'));
  // Roads only: 1500 m² and $15,000 over the same 132 residents.
  assert.deepEqual(rows(), { 'Paved area per resident': '122 sq ft', 'Year-1 need per resident': '$114' });
});
