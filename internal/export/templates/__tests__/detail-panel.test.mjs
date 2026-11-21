// The map's detail views. Clicking a hex opens a popup on the map with that
// hex's breakdown and leaves the city summary in the stats panel; dragging a
// region replaces #stats with its totals and a close button that restores
// the summary. A resource toggle, scope switch or units switch must leave an
// open popup or panel working, describing what the map now shows, and
// closing the region panel must land on a summary that reflects the change.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON } from './fixtures.mjs';

// hex:0:0 has parking only outside the city limits, so the scope switch
// changes what its panel lists; hex:0:1 is identical in both scopes.
const grid = {
  type: 'FeatureCollection',
  v: 6,
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
  all: { roads: { area: 1500, year1_need: 15000 }, parking: { area: 3000, year1_need: 6000 } },
  city: { roads: { area: 1500, year1_need: 15000 } },
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

// clickHex fires the map's click handlers for a click on a hex, as MapLibre
// would: the map-wide one (which closes an open popup) and the hex-fill one
// (which opens it at the clicked point).
function clickHex(h, id, lngLat = { lng: 0.5, lat: 0.5 }) {
  for (const fn of h.win.__mapHandlers.get('click')) fn({ features: [{ properties: { hex_id: id } }], lngLat });
}

// clickMap fires them for a click off the hexes: no features.
function clickMap(h) {
  for (const fn of h.win.__mapHandlers.get('click')) fn({ lngLat: { lng: 5, lat: 5 } });
}

function untick(h, type) {
  const cb = h.$(`#toggles input[data-type="${type}"]`);
  cb.checked = false;
  cb.dispatchEvent(new h.win.Event('change'));
}

const popup = (h) => h.$('.maplibregl-popup.hex-popup');

// hexRows names the resources the open hex popup lists.
const hexRows = (h) => Array.from(popup(h).querySelectorAll('.hex-detail tr td:first-child'))
  .map((td) => td.textContent).filter((t) => t !== 'Total');

const perResidentArea = (h) => {
  const row = h.$$('#stats .ratio-card .stat-row')
    .find((r) => r.querySelector('.stat-label').textContent === 'Maintained area per resident');
  return row && row.querySelector('.stat-value').textContent;
};

const statValue = (h, label) => {
  const row = h.$$('#stats .stat-row').find((r) => r.querySelector('.stat-label').textContent === label);
  return row && row.querySelector('.stat-value').textContent;
};

test('a hex click opens a popup with its details and keeps the city summary in the panel', async () => {
  const h = await loadGrid();
  h.win.selectScope('all', { push: false });
  const summary = h.$('#stats').innerHTML;
  clickHex(h, 'hex:0:0', { lng: 0.25, lat: 0.75 });

  const p = popup(h);
  assert.ok(p, 'the click opened no popup');
  assert.equal(p.querySelector('h3').textContent, 'Hex hex:0:0');
  assert.deepEqual(hexRows(h), ['parking', 'roads']);
  assert.match(p.textContent, /Est\. Parking Spaces\s*120/);
  assert.deepEqual(h.win.__popups[0].lngLat, { lng: 0.25, lat: 0.75 }, 'the popup is not at the clicked point');
  assert.equal(p.getAttribute('role'), 'dialog');
  assert.equal(p.getAttribute('aria-labelledby'), 'hex-popup-title');
  assert.match(h.$('#hex-popup-status').textContent, /^Hex hex:0:0: /, 'the popup is not announced');

  assert.equal(h.$('#stats').innerHTML, summary, 'the click replaced the city summary');
  assert.equal(h.$('#stats .hex-detail'), null);

  p.querySelector('.maplibregl-popup-close-button').click();
  assert.equal(popup(h), null, 'the close button left the popup open');
  assert.equal(h.$('#hex-popup-status').textContent, '');
});

test('another hex moves the popup; a click off the hexes or Escape closes it', async () => {
  const h = await loadGrid();
  clickHex(h, 'hex:0:0');
  clickHex(h, 'hex:0:1', { lng: 0.9, lat: 0.1 });
  assert.equal(h.$$('.maplibregl-popup').length, 1);
  assert.equal(h.win.__popups.length, 1, 'a second popup was made rather than the first moved');
  assert.equal(popup(h).querySelector('h3').textContent, 'Hex hex:0:1');
  assert.deepEqual(h.win.__popups[0].lngLat, { lng: 0.9, lat: 0.1 });

  clickMap(h);
  assert.equal(popup(h), null, 'a click off the hexes left the popup open');

  clickHex(h, 'hex:0:0');
  h.doc.dispatchEvent(new h.win.KeyboardEvent('keydown', { key: 'Escape' }));
  assert.equal(popup(h), null, 'Escape left the popup open');
});

test('the popup closes on a city change and a Color by change', async () => {
  const h = await loadGrid();
  clickHex(h, 'hex:0:0');
  await h.win.loadCityData();
  await h.flush();
  assert.equal(popup(h), null, 'a city load left the old city\'s hex open');

  clickHex(h, 'hex:0:0');
  h.win.selectHexMetric('population', { push: false });
  assert.equal(popup(h), null, 'a Color by change left the popup open');
});

test('the hover tooltip stays off the hex the popup shows', async () => {
  const h = await loadGrid();
  const hover = (id) => {
    for (const fn of h.win.__mapHandlers.get('mousemove')) {
      fn({ features: [{ properties: { hex_id: id, share: 0.4, area: 1000 } }], point: { x: 10, y: 10 }, originalEvent: { clientX: 10, clientY: 10 } });
    }
  };
  const tip = h.$('#hex-tooltip');
  hover('hex:0:0');
  assert.equal(tip.style.display, 'block');
  clickHex(h, 'hex:0:0');
  assert.equal(tip.style.display, 'none', 'the tooltip stayed up over the new popup');
  hover('hex:0:0');
  assert.equal(tip.style.display, 'none', 'the popup\'s own hex showed a tooltip over it');
});

test('the hex popup follows a resource toggle', async () => {
  const h = await loadGrid();
  h.win.selectScope('all', { push: false });
  clickHex(h, 'hex:0:0');
  untick(h, 'parking');
  assert.deepEqual(hexRows(h), ['roads'], 'the open popup still lists the toggled-off resource');
  // Roads only: 1500 m² over 132 residents.
  assert.equal(perResidentArea(h), '122 sq ft', 'the summary did not follow the toggle');
});

test('the hex popup follows a scope switch', async () => {
  const h = await loadGrid();
  clickHex(h, 'hex:0:0');
  assert.deepEqual(hexRows(h), ['roads'], 'city scope has no parking in this hex');
  h.win.selectScope('all', { push: false });
  assert.deepEqual(hexRows(h), ['parking', 'roads'], 'the open popup kept the old scope');
});

test('the hex popup stays open across a units switch and follows it', async () => {
  const h = await loadGrid();
  clickHex(h, 'hex:0:0');
  assert.match(popup(h).textContent, /sq ft/);
  h.win.selectUnits('metric');
  assert.ok(popup(h), 'switching units closed the popup');
  assert.doesNotMatch(popup(h).textContent, /sq ft/, 'the open popup kept the old units');
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
  assert.equal(statValue(h, 'Est. year-1 cost'), '$21.0K');
  // One name for the metric: the panel row reads as the "Color by" option
  // does.
  assert.equal(h.win.PVMT_CONFIG.hex_metrics.find((m) => m.key === 'year1_need').label, 'Est. year-1 cost');

  untick(h, 'parking');
  assert.ok(h.$('#stats .region-detail'), 'the toggle closed the region panel');
  assert.equal(statValue(h, 'Est. year-1 cost'), '$15.0K', 'the region panel kept the toggled-off resource');

  h.$('#close-region').click();
  assert.equal(h.$('#stats .region-detail'), null, 'the close button stopped working after the toggle');
  assert.equal(perResidentArea(h), '122 sq ft');
});

test('the region hex count does not depend on Color by', async () => {
  // A third hex with residents but no pavement: a stored metric brings it
  // onto the map, but it holds nothing to total.
  const empty = { type: 'Feature', geometry: grid.features[0].geometry, properties: { id: 'hex:0:2', all: {} } };
  const pop = { ...population, values: { ...population.values, 'hex:0:2': 7 } };
  const h = load({ data: {
    'meta.json': metaJSON,
    'hexgrid.geojson': { ...grid, features: [...grid.features, empty] },
    'hex-cost-summary.json': costSummary,
    'hex-metrics.json': { v: 2, metrics: { population: pop } },
  } });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  h.win._map.unproject = ([x]) => (x === 0 ? { lng: -1, lat: -1 } : { lng: 2, lat: 2 });
  const drag = (ev, x) => { for (const fn of h.win.__mapHandlers.get(ev)) fn({ originalEvent: { clientX: x, clientY: x } }); };
  const regionCount = () => {
    h.$('#region-select-btn').click();
    drag('mousedown', 0);
    drag('mouseup', 10);
    const n = statValue(h, 'Hexes selected');
    h.$('#close-region').click();
    return n;
  };
  assert.equal(regionCount(), '2');
  h.win.selectHexMetric('population');
  assert.equal(regionCount(), '2', 'a stored metric added its pavement-less hex to the count');
});
