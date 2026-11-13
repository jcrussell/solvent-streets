// Play page board boot (game.js boot()): once the WASM is up and the map has
// loaded, the board is built from the city's own files. These replace Go greps
// of game.js for the wiring (solvent-streets-yvlv.16): the board must seed from
// the PER-CITY forecast_seed.json in preference to the region-wide seed
// embedded in the page, and fit the map to the board rather than the region.
// The engine is never called here; game.js only reaches it on Start.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON, forecastJSON, hexgridJSON, playHexesJSON } from './fixtures.mjs';

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
  const perCity = { ...structuredClone(seedJSON), initial_pci: 61.25, horizon_years: 17 };
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

// solvent-streets-lu32.22: the start screen opens at the cited street budget,
// else at the roads hold-steady budget, as the Financials slider does.
const roadsForecast = forecastJSON.find((f) => f.resource_type === 'roads');

test('play: the start budget is the cited street budget', async () => {
  assert.ok(roadsForecast.current_budget > 0, 'fixture must cite a budget');
  const { get, $ } = await bootBoard(boardData(seedJSON));
  assert.equal(get('gameConfig').starting_budget, roadsForecast.current_budget);
  assert.equal($('#start-budget-slider').value, String(roadsForecast.current_budget));
});

test('play: with no cited budget the start budget is the hold-steady budget', async () => {
  assert.ok(roadsForecast.break_even_budget > 0, 'fixture must carry a hold-steady budget');
  const forecast = structuredClone(forecastJSON);
  for (const f of forecast) {
    if (f.resource_type === 'roads') delete f.current_budget;
    // A budget on another resource is not a street budget.
    else f.current_budget = 123_456;
  }
  const { get, $ } = await bootBoard({ ...boardData(seedJSON), 'forecast.json': forecast });
  assert.equal(get('gameConfig').starting_budget, roadsForecast.break_even_budget);
  assert.equal($('#start-budget-value').textContent, get('fmtMoney')(roadsForecast.break_even_budget));
});
