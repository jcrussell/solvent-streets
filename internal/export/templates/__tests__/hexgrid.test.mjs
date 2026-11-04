// The hexgrid.geojson reader. This is the B6 change (solvent-streets-pav7.5)
// that would have silently dropped 69.7% of city-scope features while the map
// kept rendering — the exact class of failure eslint and tsc cannot see.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON } from './fixtures.mjs';

// Four per-feature property shapes exist in a v4 file, and all four have to
// roll up correctly:
//   city_same: 1   -> city scope reuses the bbox coverage (the dedup, ~70% of features)
//   city: {...}    -> city scope differs and carries its own object
//   bbox only      -> the hex has no city-scope rows at all
//   id only        -> a grid hex with no pavement rows (v4): geometry for
//                     stored metrics, nothing for the pavement colorings
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

async function loadHexes(hexgrid) {
  const h = load({ data: { 'meta.json': metaJSON, 'hexgrid.geojson': hexgrid } });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  return h;
}

test('all three per-feature scope shapes roll up correctly', async () => {
  const { get } = await loadHexes(hexFC([
    { bbox: { roads: { area: 100, pct: 10 } }, city_same: 1 },              // dedup
    { bbox: { roads: { area: 200, pct: 20 } }, city: { roads: { area: 50, pct: 5 } } },
    { bbox: { roads: { area: 300, pct: 30 } } },                            // bbox only
  ]));

  const byScope = get('hexDataByScope');
  assert.equal(byScope.bbox.roads.length, 3, 'every hex has bbox coverage');
  assert.equal(byScope.city.roads.length, 2,
    'city scope should hold the city_same hex and the explicit-city hex, and only those');

  // Array.from, not .map: arrays produced inside the jsdom realm have a
  // different Array.prototype, and deepStrictEqual compares prototypes. Build
  // the array in this realm instead.
  const cityAreas = Array.from(byScope.city.roads, (f) => f.properties.area).sort((a, b) => a - b);
  assert.deepEqual(cityAreas, [50, 100],
    'city_same must reuse the bbox measurement (100), not drop it or read the city object');
});

test('a city_same hex is not silently dropped from city scope', async () => {
  // The regression this exists for: reading f.properties.city directly means
  // every city_same feature has no city entry and vanishes. With 70% of a real
  // file deduped, the map still draws and the totals are quietly ~1/3 right.
  const { get } = await loadHexes(hexFC(
    Array.from({ length: 10 }, () => ({ bbox: { roads: { area: 100, pct: 10 } }, city_same: 1 })),
  ));

  assert.equal(get('hexDataByScope').city.roads.length, 10,
    'every city_same hex must appear in city scope');
});

test('a non-v4 hexgrid is refused rather than under-reported', async () => {
  // Failing loudly is the point: an older client reading a newer file finds
  // keys it does not know (or misses ones it expects) and under-reports with no
  // error anywhere.
  const stale = hexFC([{ bbox: { roads: { area: 100, pct: 10 } } }]);
  delete stale.v;
  const { get, doc } = await loadHexes(stale);

  assert.equal(Object.keys(get('hexDataByScope').city).length, 0,
    'a version-less file must not populate any scope');
  const banner = doc.getElementById('load-error') || doc.querySelector('.load-error, #error-banner');
  const text = banner ? banner.textContent : doc.body.textContent;
  assert.match(text, /hexgrid|stale|v4/i, 'no error surfaced for a stale hexgrid format');
});

test('multiple resource types stay separated per scope', async () => {
  const { get } = await loadHexes(hexFC([
    {
      bbox: { roads: { area: 100, pct: 10 }, sidewalks: { area: 20, pct: 2 } },
      city_same: 1,
    },
  ]));

  const city = get('hexDataByScope').city;
  assert.equal(city.roads.length, 1);
  assert.equal(city.sidewalks.length, 1);
  assert.equal(city.sidewalks[0].properties.area, 20);
});

test('parking spaces ride the per-resource map into the blended hex layer', async () => {
  const { get, win } = await loadHexes(hexFC([
    { bbox: { roads: { area: 100, pct: 10 }, parking: { area: 60, pct: 6, spaces: 12 } }, city_same: 1 },
    { bbox: { roads: { area: 100, pct: 10 } }, city_same: 1 },
  ]));
  const parking = get('hexDataByScope').bbox.parking;
  assert.equal(parking.length, 1);
  assert.equal(parking[0].properties.spaces, 12, 'spaces must be read from the parking map');
  assert.equal(get('hexDataByScope').bbox.roads[0].properties.spaces, 0, 'resources without the key read as 0');

  const fc = win.buildBlendedHexFC();
  const byId = {};
  for (const f of fc.features) byId[f.properties.hex_id] = f.properties;
  assert.equal(byId['hex:0:0'].spaces, 12, 'the blended layer must carry the hex spaces for the tooltip');
  assert.equal(byId['hex:0:1'].spaces, 0);
});
