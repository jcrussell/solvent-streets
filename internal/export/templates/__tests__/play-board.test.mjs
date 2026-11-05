// Play page board boot (game.js boot()): once the WASM is up and the map has
// loaded, the board is built from the city's own files. These replace Go greps
// of game.js for the wiring (solvent-streets-yvlv.16): the board must seed from
// the PER-CITY forecast_seed.json in preference to the region-wide seed
// embedded in the page, and fit the map to the board rather than the region.
// The engine is never called here; game.js only reaches it on Start.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON, hexgridJSON, playHexesJSON } from './fixtures.mjs';

// bootBoard loads the play page with a working WASM and the board files, then
// fires the map 'load' event boot() waits for.
async function bootBoard(data) {
  const h = load({ page: 'play', wasm: true, data });
  await h.flush();
  h.fireMap('load');
  await h.flush(12);
  return h;
}

const boardData = (seed) => ({
  ...fullData,
  'forecast_seed.json': seed,
  'hexgrid.geojson': hexgridJSON,
  'play-hexes.json': playHexesJSON,
});

test('play: the board seeds from the per-city forecast_seed.json, not the embedded region seed', async () => {
  // Mark the fetched seed so it cannot be confused with the embedded one.
  const perCity = { ...structuredClone(seedJSON), initial_pci: 61.25, years: 17 };
  const { get } = await bootBoard(boardData(perCity));

  const embedded = get('FORECAST_SEED');
  assert.ok(embedded, 'the page embeds no region seed, so the preference is untested');
  assert.notEqual(embedded.initial_pci, 61.25);

  const cfg = get('gameConfig');
  assert.ok(cfg, 'the board did not boot');
  assert.equal(cfg.initial_pci, 61.25, 'the board used the embedded seed instead of the per-city one');
  assert.equal(cfg.horizon_years, 17);
  assert.equal(cfg.hexes.length, playHexesJSON.length, 'every playable hex should be on the board');
});

test('play: without a per-city seed the board falls back to the embedded one', async () => {
  const data = boardData(undefined);
  delete data['forecast_seed.json'];
  const { get } = await bootBoard(data);

  const cfg = get('gameConfig');
  assert.ok(cfg, 'the board did not boot');
  assert.equal(cfg.initial_pci, get('FORECAST_SEED').initial_pci);
});

test('play: the map is fitted to the board, not left on the region', async () => {
  const { win } = await bootBoard(boardData(seedJSON));

  const playable = new Set(playHexesJSON.map((p) => p.id));
  const coords = hexgridJSON.features
    .filter((f) => playable.has(f.properties.id))
    .flatMap((f) => f.geometry.coordinates.flat(f.geometry.type === 'MultiPolygon' ? 2 : 1));
  const lngs = coords.map((c) => c[0]);
  const lats = coords.map((c) => c[1]);
  const want = [[Math.min(...lngs), Math.min(...lats)], [Math.max(...lngs), Math.max(...lats)]];

  // JSON round-trip: the recorded bounds are arrays from the page's realm,
  // which strict deepEqual would reject on prototype alone.
  const last = JSON.parse(JSON.stringify(win.__fitBounds.at(-1) ?? null));
  assert.deepEqual(last, want, 'the last fit was not to the board');
});
