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

// Fire response from two stations, so the popup has a Services row.
const fire = {
  label: 'Fire response', unit: 's', format: 'minutes', source: 'test', threshold_s: 240,
  scale: { kind: 'sequential', transform: 'linear', bands: [240, 480] },
  values: { 'hex:0:0': 205, 'hex:0:1': 300 },
  refs: { 'hex:0:0': 0, 'hex:0:1': 1 },
  points: [
    { id: 'n/1', name: 'Station 4', position: [0, 0], source: 'osm' },
    { id: 'n/2', name: 'Station 7', position: [1, 1], source: 'osm' },
  ],
};

async function loadGrid() {
  const h = load({ data: {
    'meta.json': metaJSON,
    'hexgrid.geojson': grid,
    'hex-cost-summary.json': costSummary,
    'hex-metrics.json': { v: 2, metrics: { population, fire_response_s: fire } },
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
  assert.equal(p.querySelector('.hex-detail').dataset.hex, 'hex:0:0');
  assert.deepEqual(hexRows(h), ['parking', 'roads']);
  assert.match(p.textContent, /Est\. parking spaces\s*120/);
  assert.deepEqual(h.win.__popups[0].lngLat, { lng: 0.25, lat: 0.75 }, 'the popup is not at the clicked point');
  assert.equal(p.getAttribute('role'), 'dialog');
  assert.equal(p.getAttribute('aria-labelledby'), 'hex-popup-title');
  assert.match(h.$('#hex-popup-status').textContent, /^Hex details: /, 'the popup is not announced');

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
  assert.equal(popup(h).querySelector('.hex-detail').dataset.hex, 'hex:0:1');
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
  assert.equal(statValue(h, 'Year-1 need'), '$21.0K');
  // One name for the metric: the panel row reads as the "Color by" option
  // does.
  assert.equal(h.win.PVMT_CONFIG.hex_metrics.find((m) => m.key === 'year1_need').label, 'Year-1 need');

  untick(h, 'parking');
  assert.ok(h.$('#stats .region-detail'), 'the toggle closed the region panel');
  assert.equal(statValue(h, 'Year-1 need'), '$15.0K', 'the region panel kept the toggled-off resource');

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

// groupRows is one popup group's rows as [label, value] pairs.
const groupRows = (h, title) => {
  const g = Array.from(popup(h).querySelectorAll('.hex-group')).find((el) => el.querySelector('h4').textContent === title);
  return g ? Array.from(g.querySelectorAll('.stat-row')).map((r) => [r.querySelector('.stat-label').textContent, r.querySelector('.stat-value').textContent]) : null;
};

test('the hex popup groups pavement, people and services under friendly labels', async () => {
  const h = await loadGrid();
  h.win.selectScope('all', { push: false });
  clickHex(h, 'hex:0:0');
  const p = popup(h);
  assert.equal(p.querySelector('h3').textContent, 'Hex details');
  assert.doesNotMatch(p.textContent, /hex:0:0/, 'the popup shows the internal hex id');
  // Land is the first listed resource's area over its share: 3000 m² of
  // parking at 0.7 is a 4,286 m² hex, 1.1 acres. Roads and parking
  // together cover all of it.
  assert.equal(p.querySelector('.hex-sub').textContent, '1.1 acres · 100% paved');
  assert.deepEqual(hexRows(h), ['parking', 'roads']);
  assert.deepEqual(groupRows(h, 'Pavement'), [['Est. parking spaces', '120']]);
  assert.match(p.querySelector('.total-row').textContent, /\$16\.0K/, 'the cost summary\'s year-1 cost is missing');
  // 4,000 m² and $16.0K over 50 residents.
  assert.deepEqual(groupRows(h, 'People'), [
    ['Maintained area per resident', '861 sq ft'], ['Year-1 need per resident', '$320'], ['Population', '50'],
  ]);
  assert.deepEqual(groupRows(h, 'Services'), [['Fire response', '3:25 min · Station 4']]);
});

test('a hex with no stored metrics shows no People or Services group', async () => {
  const h = load({ data: { 'meta.json': metaJSON, 'hexgrid.geojson': grid, 'hex-cost-summary.json': costSummary } });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  clickHex(h, 'hex:0:1');
  assert.equal(groupRows(h, 'People'), null);
  assert.equal(groupRows(h, 'Services'), null);
  assert.deepEqual(hexRows(h), ['roads']);
});

// selectAll drags a region over both hexes.
function selectAll(h) {
  h.win._map.unproject = ([x]) => (x === 0 ? { lng: -1, lat: -1 } : { lng: 2, lat: 2 });
  h.$('#region-select-btn').click();
  const drag = (ev, x) => { for (const fn of h.win.__mapHandlers.get(ev)) fn({ originalEvent: { clientX: x, clientY: x } }); };
  drag('mousedown', 0);
  drag('mouseup', 10);
}
const highlighted = (h) => {
  const src = h.win._map.getSource('region-highlight');
  return src && h.win._map.getLayer('region-highlight') ? Array.from(src.data.features, (f) => f.properties.hex_id).sort() : null;
};

// cardValue is a stat row's value inside the region card titled `title`.
const cardValue = (h, title, label) => {
  const card = h.$$('#stats .stat-card').find((c) => c.querySelector('h3').firstChild.textContent === title);
  const row = card && Array.from(card.querySelectorAll('.stat-row')).find((r) => r.querySelector('.stat-label').textContent === label);
  return row && row.querySelector('.stat-value').textContent;
};

test('the region shows the city panel\'s cards: summary, each resource, each metric', async () => {
  const h = await loadGrid();
  h.win.selectScope('all', { push: false });
  h.win.selectHexMetric('fire_response_s', { push: false });
  selectAll(h);
  assert.equal(statValue(h, 'Hexes selected'), '2');
  // A 4,286 m² hex (parking's 3000 m² at 0.7) and a 2,500 m² one (roads'
  // 500 m² at 0.2), with 4,500 m² paved between them: the share is paved
  // over land, not the paved-area-weighted mean of the hexes' shares.
  assert.equal(statValue(h, 'Land area (acres)'), '1.7');
  assert.equal(statValue(h, 'Paved area (acres)'), '1.1');
  assert.equal(statValue(h, '% paved (of land)'), '66.3%');
  assert.equal(statValue(h, 'Year-1 need'), '$21.0K');
  const titles = h.$$('#stats .stat-card h3').map((e) => e.firstChild.textContent);
  assert.deepEqual(titles.slice(0, 3), ['Selected Region', 'parking', 'roads']);
  assert.equal(cardValue(h, 'parking', 'Est. spaces'), '120');
  assert.equal(cardValue(h, 'roads', 'Year-1 need'), '$15.0K');
  // The median of 205 s and 300 s, as the city's fire card shows it.
  assert.equal(cardValue(h, 'Fire response', 'Median per hex'), '4:13');
  assert.equal(cardValue(h, 'Population', 'Median per hex'), '66');
});

test('the region\'s hexes are highlighted until the selection is cleared or the city changes', async () => {
  const h = await loadGrid();
  h.win.selectScope('all', { push: false });
  selectAll(h);
  assert.deepEqual(highlighted(h), ['hex:0:0', 'hex:0:1']);

  untick(h, 'parking');
  assert.deepEqual(highlighted(h), ['hex:0:0', 'hex:0:1'], 'a toggle dropped the highlight');

  h.$('#clear-region').click();
  assert.equal(h.$('#stats .region-detail'), null, 'Clear selection left the region card open');
  assert.equal(highlighted(h), null, 'Clear selection left the highlight on the map');

  selectAll(h);
  h.$('#close-region').click();
  assert.equal(highlighted(h), null, 'closing the card left the highlight on the map');

  selectAll(h);
  await h.win.loadCityData();
  await h.flush();
  assert.equal(highlighted(h), null, 'a city change left the old region highlighted');
});
