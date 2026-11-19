// When the dashboard starts loading the city's map data (solvent-streets-h2pa).
//
// MapLibre's 'load' waits for the style AND every basemap tile, and it fires
// only from inside a render. A tile that fails with anything but a 404 (a
// blocked or rate-limited tile server, an offline laptop) marks itself errored
// without asking for another frame, so once the last tile errors the map is
// loaded() with no render left to announce it: 'load' never fires, and a
// dashboard gated on it never fetched meta.json, the boundary or the hexes.
// The data needs only the style, so the page starts on 'style.load'.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData } from './fixtures.mjs';

const metaFetches = (fetches) => fetches().filter((k) => k.endsWith('/meta.json') && k.startsWith('cities/')).length;

test('the city data loads once the style is in, without waiting for the basemap tiles', async () => {
  const { flush, fetches, fireMap } = load({ data: fullData });
  await flush();
  assert.equal(metaFetches(fetches), 0, 'the city data loaded before the map had a style');

  fireMap('style.load');
  await flush();
  assert.equal(metaFetches(fetches), 1, "the city data waited for MapLibre's 'load'");
  assert.ok(fetches().some((k) => k.endsWith('boundary.geojson')), 'the boundary was not requested');
});

test("'load' after 'style.load' does not load the city a second time", async () => {
  const { flush, fetches, fireMap } = load({ data: fullData });
  await flush();
  fireMap('style.load');
  await flush();
  fireMap('load');
  await flush();
  assert.equal(metaFetches(fetches), 1);
});
