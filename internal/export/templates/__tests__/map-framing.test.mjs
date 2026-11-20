// How the dashboard frames a city on its map.
//
// The stats panel floats over the map: down the right edge on a wide screen,
// docked along the bottom on a phone. A fit with a uniform 40 px padding put
// the city's east side (or, on a phone, its south side) under the panel,
// so the fit pads the side the panel covers.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData } from './fixtures.mjs';

const square = (w, s, e, n) => [[[w, s], [e, s], [e, n], [w, n], [w, s]]];
const boundary = (coordinates) => ({
  type: 'FeatureCollection',
  features: [{ type: 'Feature', properties: {}, geometry: { type: 'MultiPolygon', coordinates } }],
});

// rect builds a DOMRect-like box from its left/top corner and size.
const rect = (left, top, width, height) => ({ left, top, width, height, right: left + width, bottom: top + height, x: left, y: top });

// layout pins the map's and the panel's boxes, which jsdom leaves at zero.
function layout(win, mapBox, panelBox) {
  win.document.getElementById('map').getBoundingClientRect = () => mapBox;
  win.document.getElementById('panel').getBoundingClientRect = () => panelBox;
}

const plain = (v) => JSON.parse(JSON.stringify(v ?? null));

async function fitAfterLoad(arrange, coordinates = [square(-122.3, 37.7, -122.2, 37.8)]) {
  const page = load({ data: { ...fullData, 'boundary.geojson': boundary(coordinates) } });
  arrange(page.win);
  await page.flush();
  page.fireMap('style.load');
  await page.flush(12);
  return { page, bounds: plain(page.win.__fitBounds.at(-1)), opts: plain(page.win.__fitOptions.at(-1)) };
}

test('desktop: the fit pads the right side by the panel beside the map', async () => {
  const { opts } = await fitAfterLoad((win) => layout(win, rect(0, 0, 1440, 800), rect(1070, 10, 360, 590)));
  assert.deepEqual(opts.padding, { top: 40, right: 410, bottom: 40, left: 40 });
});

test('phone: the fit pads the bottom by the panel docked under the map', async () => {
  const { opts } = await fitAfterLoad((win) => layout(win, rect(0, 100, 390, 560), rect(8, 400, 374, 252)));
  // The panel reaches 660 - 400 = 260 px up from the map's bottom edge.
  assert.deepEqual(opts.padding, { top: 40, right: 40, bottom: 300, left: 40 });
});

test('a panel with no box (the Map tab hidden) leaves the plain padding', async () => {
  const { opts } = await fitAfterLoad(() => {});
  assert.deepEqual(opts.padding, { top: 40, right: 40, bottom: 40, left: 40 });
});

test('a panel too big to leave room for the city leaves the plain padding', async () => {
  // A 300 px map with a 280 px-wide panel down its side: padding for it would
  // leave no map, and MapLibre refuses a fit it cannot make.
  const { opts } = await fitAfterLoad((win) => layout(win, rect(0, 0, 300, 800), rect(10, 10, 280, 780)));
  assert.deepEqual(opts.padding, { top: 40, right: 40, bottom: 40, left: 40 });
});

test('switching cities pads the provisional fit the same way', async () => {
  const { page } = await fitAfterLoad((win) => layout(win, rect(0, 0, 1440, 800), rect(1070, 10, 360, 590)));
  const before = page.win.__fitOptions.length;
  page.win.selectCity('beta-ca');
  assert.ok(page.win.__fitOptions.length > before, 'selectCity did not fit the map');
  assert.deepEqual(plain(page.win.__fitOptions[before]).padding, { top: 40, right: 410, bottom: 40, left: 40 });
});

// San Francisco's boundary carries the Farallon Islands, 40 km out to sea:
// part of the city and county, but framing them drew the city as a sliver at
// the map's edge. The fit frames the parts that make
// up nearly all of the area; the islands stay in the boundary and the hexes.
test('the fit leaves out a far-off islet with a sliver of the area', async () => {
  const { bounds } = await fitAfterLoad(() => {}, [
    square(-122.5, 37.7, -122.4, 37.8),
    square(-123.01, 37.69, -123.0, 37.70),
  ]);
  assert.deepEqual(bounds, [[-122.5, 37.7], [-122.4, 37.8]]);
});

test('the fit keeps every part that holds a real share of the city', async () => {
  // Alameda-like: the main island and a second piece a sixth its size.
  const { bounds } = await fitAfterLoad(() => {}, [
    square(-122.33, 37.75, -122.23, 37.80),
    square(-122.26, 37.72, -122.23, 37.745),
  ]);
  assert.deepEqual(bounds, [[-122.33, 37.72], [-122.23, 37.8]]);
});

test('a single-polygon boundary is framed whole', async () => {
  const page = load({ data: { ...fullData, 'boundary.geojson': {
    type: 'FeatureCollection',
    features: [{ type: 'Feature', properties: {}, geometry: { type: 'Polygon', coordinates: square(-121.85, 37.63, -121.7, 37.73) } }],
  } } });
  await page.flush();
  page.fireMap('style.load');
  await page.flush(12);
  assert.deepEqual(plain(page.win.__fitBounds.at(-1)), [[-121.85, 37.63], [-121.7, 37.73]]);
});
