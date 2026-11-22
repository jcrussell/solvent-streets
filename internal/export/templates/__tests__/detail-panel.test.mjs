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
  label: 'Fire response', unit: 's', format: 'minutes', source: 'test', group: 'services', order: 1, threshold_s: 240,
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

// hexRows names the resources the open hex popup lists: the pavement table's
// rows but the total.
const hexRows = (h) => Array.from(popup(h).querySelectorAll('.hex-detail tr:not(.total-row) td:first-child'))
  .map((td) => td.textContent);

// The rows of the panel and the popup carry no metric key, only a label. A
// row that shows a registry or stored metric is found by that metric's own
// label (lookupMetric: PVMT_CONFIG.hex_metrics, then hex-metrics.json), and
// read back to its key the same way, so no label is spelled out here.
const metricLabel = (h, key) => h.win.lookupMetric(key).label;
const metricKey = (h, label) => {
  const key = [...h.win.PVMT_CONFIG.hex_metrics.map((m) => m.key), ...Object.keys(h.get('hexMetrics'))]
    .find((k) => metricLabel(h, k) === label);
  assert.ok(key, `the row "${label}" matches no metric`);
  return key;
};

// The per-resident card's rows are the page's PER_RESIDENT list; the
// maintained area is the entry that divides `area`.
const perResidentArea = (h) => {
  const label = h.get('PER_RESIDENT').find((f) => f.prop === 'area').label;
  const row = h.$$('#stats .ratio-card .stat-row')
    .find((r) => r.querySelector('.stat-label').textContent === label);
  return row && row.querySelector('.stat-value').textContent;
};

// regionSummary reads the Selected Region card by position: hexes, land
// area, maintained area, the share of land maintained, then the year-1 need
// when there is one.
const regionSummary = (h) => {
  const rows = h.$$('#stats .region-detail .summary-card .stat-row');
  const [hexes, land, maintained, share, need] = rows.map((r) => r.querySelector('.stat-value').textContent);
  return { hexes, land, maintained, share, need, labels: rows.map((r) => r.querySelector('.stat-label').textContent) };
};

// pavementGroup is the popup group holding the per-resource table.
const pavementGroup = (h) => Array.from(popup(h).querySelectorAll('.hex-group')).find((g) => g.querySelector('table'));

test('a hex click opens a popup with its details and keeps the city summary in the panel', async () => {
  const h = await loadGrid();
  h.win.selectScope('all', { push: false });
  const summary = h.$('#stats').innerHTML;
  clickHex(h, 'hex:0:0', { lng: 0.25, lat: 0.75 });

  const p = popup(h);
  assert.ok(p, 'the click opened no popup');
  assert.equal(p.querySelector('.hex-detail').dataset.hex, 'hex:0:0');
  assert.deepEqual(hexRows(h), ['parking', 'roads']);
  // The hex's 120 parking spaces sit under its pavement table, named as the
  // Color by option for them is.
  assert.deepEqual(Array.from(pavementGroup(h).querySelectorAll('.stat-row'), (r) =>
    [metricKey(h, r.querySelector('.stat-label').textContent), r.querySelector('.stat-value').textContent]),
  [['parking_spaces', '120']]);
  assert.deepEqual(h.win.__popups[0].lngLat, { lng: 0.25, lat: 0.75 }, 'the popup is not at the clicked point');
  assert.equal(p.getAttribute('role'), 'dialog');
  assert.equal(p.getAttribute('aria-labelledby'), 'hex-popup-title');
  // The live region announces the popup: its heading and what it leads with.
  const announced = h.$('#hex-popup-status').textContent;
  assert.ok(announced.includes(p.querySelector('#hex-popup-title').textContent), 'the popup is not announced: ' + announced);
  assert.ok(announced.includes(p.querySelector('.hex-sub').textContent), 'the announcement does not describe the hex: ' + announced);

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
  // The pavement table's area cell carries the unit.
  const area = () => popup(h).querySelector('.hex-detail tr td:nth-child(2)').textContent;
  const imperial = h.win.areaUnit();
  assert.ok(area().endsWith(imperial), area());
  h.win.selectUnits('metric');
  assert.ok(popup(h), 'switching units closed the popup');
  assert.notEqual(h.win.areaUnit(), imperial);
  assert.ok(area().endsWith(h.win.areaUnit()), 'the open popup kept the old units: ' + area());
  assert.ok(!popup(h).textContent.includes(imperial), 'the open popup kept the old units');
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
  assert.equal(regionSummary(h).need, '$21.0K');
  // One name for the metric: the panel row reads as the "Color by" option
  // does.
  assert.equal(regionSummary(h).labels.at(-1), metricLabel(h, 'year1_need'));

  untick(h, 'parking');
  assert.ok(h.$('#stats .region-detail'), 'the toggle closed the region panel');
  assert.equal(regionSummary(h).need, '$15.0K', 'the region panel kept the toggled-off resource');

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
    const n = regionSummary(h).hexes;
    h.$('#close-region').click();
    return n;
  };
  assert.equal(regionCount(), '2');
  h.win.selectHexMetric('population');
  assert.equal(regionCount(), '2', 'a stored metric added its pavement-less hex to the count');
});

// metricRows is the popup's metric rows as one [[metric key, value, point of
// service]] list per group, the groups in the order shown, without the
// pavement table's. The value is the row's own text, apart from the point of
// service set under it (.stat-ref).
const metricRows = (h) => Array.from(popup(h).querySelectorAll('.hex-group')).filter((g) => g !== pavementGroup(h))
  .map((g) => Array.from(g.querySelectorAll('.stat-row'), (r) => {
    const value = r.querySelector('.stat-value');
    const ref = value.querySelector('.stat-ref');
    return [metricKey(h, r.querySelector('.stat-label').textContent), value.firstChild.textContent, ref ? ref.textContent : null];
  }));

test('the hex popup groups pavement, people and services', async () => {
  const h = await loadGrid();
  h.win.selectScope('all', { push: false });
  clickHex(h, 'hex:0:0');
  const p = popup(h);
  assert.ok(p.querySelector('h3').textContent.trim().length > 0, 'the popup has no heading');
  assert.equal(p.querySelector('h3').id, p.getAttribute('aria-labelledby'), 'the dialog is not labelled by its heading');
  assert.doesNotMatch(p.textContent, /hex:0:0/, 'the popup shows the internal hex id');
  // Land is the first listed resource's area over its share: 3000 m² of
  // parking at 0.7 is a 4,286 m² hex, 1.1 acres. Roads and parking
  // together cover all of it.
  const sub = p.querySelector('.hex-sub').textContent;
  assert.ok(sub.includes('1.1 ' + h.win.areaLargeUnit()), sub);
  assert.match(sub, /\b100%/);
  assert.deepEqual(hexRows(h), ['parking', 'roads']);
  // Three groups, each under a heading of its own, pavement first.
  const groups = Array.from(p.querySelectorAll('.hex-group'));
  assert.equal(groups.length, 3);
  assert.equal(groups[0], pavementGroup(h));
  const headings = groups.map((g) => g.querySelector('h4').textContent.trim());
  assert.ok(headings.every((t) => t.length > 0) && new Set(headings).size === 3, 'the groups are not told apart: ' + headings);
  assert.match(p.querySelector('.total-row').textContent, /\$16\.0K/, 'the cost summary\'s year-1 cost is missing');
  // The people living there (per-resident ratios are city and region
  // figures, not per hex), then the services that reach them: 205 s from the
  // hex's nearest station, which is named under the time.
  assert.deepEqual(metricRows(h), [
    [['population', '50', null]],
    [['fire_response_s', '3:25 min', fire.points[fire.refs['hex:0:0']].name]],
  ]);
  // Each metric sits under the heading the page files its registry group under.
  assert.equal(h.win.lookupMetric('fire_response_s').group, 'services');
  assert.deepEqual(headings.slice(1), ['population', 'fire_response_s'].map((k) => h.win.metricGroup(h.win.lookupMetric(k))));
});

test('a hex with no stored metrics shows no People or Services group', async () => {
  const h = load({ data: { 'meta.json': metaJSON, 'hexgrid.geojson': grid, 'hex-cost-summary.json': costSummary } });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  clickHex(h, 'hex:0:1');
  assert.deepEqual(Array.from(popup(h).querySelectorAll('.hex-group')), [pavementGroup(h)]);
  assert.deepEqual(metricRows(h), []);
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

// cardValues are the stat values, in order, of the region card titled
// `title`: a resource type, or a stored metric's label.
const cardValues = (h, title) => {
  const card = h.$$('#stats .region-detail .stat-card').find((c) => c.querySelector('h3').firstChild.textContent === title);
  assert.ok(card, `no region card is titled "${title}"`);
  return Array.from(card.querySelectorAll('.stat-row .stat-value'), (v) => v.textContent);
};

test('the region shows the city panel\'s cards: summary, each resource, each metric', async () => {
  const h = await loadGrid();
  h.win.selectScope('all', { push: false });
  h.win.selectHexMetric('fire_response_s', { push: false });
  selectAll(h);
  // A 4,286 m² hex (parking's 3000 m² at 0.7) and a 2,500 m² one (roads'
  // 500 m² at 0.2), with 4,500 m² paved between them: the share is paved
  // over land, not the paved-area-weighted mean of the hexes' shares.
  const summary = regionSummary(h);
  assert.deepEqual([summary.hexes, summary.land, summary.maintained, summary.share, summary.need],
    ['2', '1.7', '1.1', '66.3%', '$21.0K']);
  // The two areas are in the large unit, and say so.
  assert.equal(h.win.areaLargeUnit(), 'acres');
  for (const at of [1, 2]) assert.ok(summary.labels[at].includes('acres'), summary.labels[at]);
  // The summary first, then a card per resource in the layer order.
  const cards = h.$$('#stats .region-detail .stat-card');
  assert.ok(cards[0].classList.contains('summary-card'));
  assert.deepEqual(cards.slice(1, 3).map((c) => c.querySelector('h3').textContent), ['parking', 'roads']);
  // A resource card: two areas, the spaces where it has any, the year-1 need.
  const parking = cardValues(h, 'parking');
  assert.equal(parking.length, 4);
  assert.equal(parking[2], '120');
  const roads = cardValues(h, 'roads');
  assert.equal(roads.length, 3, 'roads have no parking spaces to list');
  assert.equal(roads.at(-1), '$15.0K');
  // A metric card is titled as the city's card for the metric is (the
  // meta.json summary's label, else the metric's own) and ends with the
  // median and the range per hex: the median of 205 s and 300 s.
  const fireTitle = metaJSON.metrics.fire_response_s.label;
  assert.notEqual(fireTitle, fire.label, 'fixture: the summary and the metrics file label fire alike');
  assert.equal(cardValues(h, fireTitle).at(-2), '4:13');
  assert.equal(cardValues(h, population.label).at(-2), '66');
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
