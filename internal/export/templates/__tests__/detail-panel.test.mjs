// The detail panels over the stats panel: clicking a hex or dragging a region
// replaces #stats with a breakdown and a close button that restores the city
// summary. A resource toggle, scope switch or units switch must leave the
// open panel working — still closable, describing what the map now shows —
// and closing it must land on a summary that reflects the same change
// (solvent-streets-ern4.4).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON } from './fixtures.mjs';

// hex:0:0 has parking only outside the city limits, so the scope switch
// changes what its panel lists; hex:0:1 is identical in both scopes.
const grid = {
  type: 'FeatureCollection',
  v: 5,
  features: [
    {
      all: { roads: { area: 1000, share: 0.4 }, parking: { area: 3000, share: 0.7, spaces: 120 } },
      city: { roads: { area: 1000, share: 0.4 } },
    },
    { all: { roads: { area: 500, share: 0.2 } }, city_same: 1 },
  ].map((props, i) => ({
    type: 'Feature',
    geometry: { type: 'Polygon', coordinates: [[[0, 0], [0, 1], [1, 1], [0, 0]]] },
    properties: { id: `hex:0:${i}`, ...props },
  })),
};

const costSummary = {
  all: { roads: { total_area: 1500, year1_need: 15000 }, parking: { total_area: 3000, year1_need: 6000 } },
  city: { roads: { total_area: 1500, year1_need: 15000 } },
};

// 132 residents, so the per-resident card shows in the summary.
const population = {
  label: 'Population', unit: '', format: 'int', source: 'test',
  scale: { kind: 'sequential', transform: 'linear', bands: [10, 100] },
  values: { 'hex:0:0': 50, 'hex:0:1': 82 },
};

async function loadGrid() {
  const h = load({ data: {
    'meta.json': metaJSON,
    'hexgrid.geojson': grid,
    'hex-cost-summary.json': costSummary,
    'hex-metrics.json': { v: 2, metrics: { population } },
  } });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  return h;
}

// clickHex fires the map's hex-fill click handler, as MapLibre would.
function clickHex(h, id) {
  for (const fn of h.win.__mapHandlers.get('click')) fn({ features: [{ properties: { hex_id: id } }] });
}

function untick(h, type) {
  const cb = h.$(`#toggles input[data-type="${type}"]`);
  cb.checked = false;
  cb.dispatchEvent(new h.win.Event('change'));
}

// hexRows names the resources the open hex panel lists.
const hexRows = (h) => h.$$('#stats .hex-detail tr td:first-child')
  .map((td) => td.textContent).filter((t) => t !== 'Total');

const perResidentArea = (h) => {
  const row = h.$$('#stats .ratio-card .stat-row')
    .find((r) => r.querySelector('.stat-label').textContent === 'Paved area per resident');
  return row && row.querySelector('.stat-value').textContent;
};

const statValue = (h, label) => {
  const row = h.$$('#stats .stat-row').find((r) => r.querySelector('.stat-label').textContent === label);
  return row && row.querySelector('.stat-value').textContent;
};

test('the hex panel stays closable after a resource toggle and follows it', async () => {
  const h = await loadGrid();
  h.win.selectScope('all', { push: false });
  clickHex(h, 'hex:0:0');
  assert.deepEqual(hexRows(h), ['parking', 'roads']);

  untick(h, 'parking');
  assert.deepEqual(hexRows(h), ['roads'], 'the open panel still lists the toggled-off resource');

  h.$('#close-hex').click();
  assert.equal(h.$('#stats .hex-detail'), null, 'the close button stopped working after the toggle');
  // Roads only: 1500 m² over 132 residents.
  assert.equal(perResidentArea(h), '122 sq ft', 'closing restored the summary from before the toggle');
});

test('the hex panel stays closable after a scope switch and follows it', async () => {
  const h = await loadGrid();
  clickHex(h, 'hex:0:0');
  assert.deepEqual(hexRows(h), ['roads'], 'city scope has no parking in this hex');

  h.win.selectScope('all', { push: false });
  assert.deepEqual(hexRows(h), ['parking', 'roads'], 'the open panel kept the old scope');

  h.$('#close-hex').click();
  assert.equal(h.$('#stats .hex-detail'), null, 'the close button stopped working after the scope switch');
  // All scope: 4500 m² over 132 residents.
  assert.equal(perResidentArea(h), '367 sq ft', 'closing restored the city-scope summary');
});

test('the hex panel stays open and closable across a units switch', async () => {
  const h = await loadGrid();
  clickHex(h, 'hex:0:0');
  const imperial = h.$('#stats .hex-detail').textContent;
  assert.match(imperial, /sq ft/);

  h.win.selectUnits('metric');
  const panel = h.$('#stats .hex-detail');
  assert.ok(panel, 'switching units closed the hex panel');
  assert.doesNotMatch(panel.textContent, /sq ft/, 'the open panel kept the old units');

  h.$('#close-hex').click();
  assert.equal(h.$('#stats .hex-detail'), null);
  assert.match(h.$('#stats').textContent, /\bha\b/, 'closing restored the summary in the old units');
});

test('the region panel stays open and closable after a resource toggle and follows it', async () => {
  const h = await loadGrid();
  h.win.selectScope('all', { push: false });
  // A drag from (0,0) to (10,10) covering both hexes.
  h.win._map.unproject = ([x]) => (x === 0 ? { lng: -1, lat: -1 } : { lng: 2, lat: 2 });
  h.$('#region-select-btn').click();
  const drag = (ev, x) => { for (const fn of h.win.__mapHandlers.get(ev)) fn({ originalEvent: { clientX: x, clientY: x } }); };
  drag('mousedown', 0);
  drag('mouseup', 10);
  assert.ok(h.$('#stats .region-detail'), 'the drag did not open the region panel');
  assert.equal(statValue(h, 'Est. Annual Cost'), '$21K');

  untick(h, 'parking');
  assert.ok(h.$('#stats .region-detail'), 'the toggle closed the region panel');
  assert.equal(statValue(h, 'Est. Annual Cost'), '$15K', 'the region panel kept the toggled-off resource');

  h.$('#close-region').click();
  assert.equal(h.$('#stats .region-detail'), null, 'the close button stopped working after the toggle');
  assert.equal(perResidentArea(h), '122 sq ft');
});
