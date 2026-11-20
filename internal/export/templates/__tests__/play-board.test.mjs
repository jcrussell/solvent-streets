// Play page board boot (game.js boot()): once the WASM is up and the map has
// loaded, the board is built from the city's own files. These replace Go greps
// of game.js for the wiring: the board must seed from
// the PER-CITY forecast_seed.json in preference to the region-wide seed
// embedded in the page, and fit the map to the board rather than the region.
// The engine is never called here; game.js only reaches it on Start.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON, forecastJSON, hexgridJSON } from './fixtures.mjs';

// bootBoard loads the play page with a working WASM and the board files, then
// fires the map 'load' event boot() waits for.
async function bootBoard(data) {
  const h = load({ page: 'play', wasm: true, data });
  await h.flush();
  h.fireMap('load');
  await h.flush(12);
  return h;
}

// boardFeatures is the fixture hexgrid's board: the features carrying k (the
// city-jurisdiction road hexes).
const boardFeatures = () => hexgridJSON.features.filter((f) => f.properties.k > 0);

// cityRoadsArea is a feature's city-scope roads area as the map reads it:
// city_same refers back to "all".
const cityRoadsArea = (p) => (p.city_same ? p.all : p.city).roads.area;

const boardData = (seed) => ({
  ...fullData,
  'forecast_seed.json': seed,
  'hexgrid.geojson': hexgridJSON,
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
  assert.equal(cfg.hexes.length, boardFeatures().length, 'every playable hex should be on the board');
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

  const coords = boardFeatures()
    .flatMap((f) => f.geometry.coordinates.flat(f.geometry.type === 'MultiPolygon' ? 2 : 1));
  const lngs = coords.map((c) => c[0]);
  const lats = coords.map((c) => c[1]);
  const want = [[Math.min(...lngs), Math.min(...lats)], [Math.max(...lngs), Math.max(...lats)]];

  // JSON round-trip: the recorded bounds are arrays from the page's realm,
  // which strict deepEqual would reject on prototype alone.
  const last = JSON.parse(JSON.stringify(win.__fitBounds.at(-1) ?? null));
  assert.deepEqual(last, want, 'the last fit was not to the board');
});

// The game prices work at the overhead the forecast priced the budget at, on
// the board and in the start screen's projection alike.
test('play: the board and the projection carry the seed cost overhead', async () => {
  const seed = { ...structuredClone(seedJSON), cost_overhead: 1.35 };
  const h = await bootBoard(boardData(seed));
  assert.equal(h.get('gameConfig').cost_overhead, 1.35);
  const sent = [];
  h.win.gameProjectInsolvency = (json) => { sent.push(JSON.parse(json)); return '{}'; };
  h.get('refreshProjection()');
  await new Promise((r) => setTimeout(r, 100));
  assert.equal(sent.at(-1).cost_overhead, 1.35, 'the projection priced work bare');
});

// The start screen opens at the cited street budget,
// else at the roads hold-steady budget, as the Financials slider does.
const roadsForecast = forecastJSON.find((f) => f.resource_type === 'roads');

test('play: the start budget is the cited street budget', async () => {
  assert.ok(roadsForecast.current_budget > 0, 'fixture must cite a budget');
  const { get, $ } = await bootBoard(boardData(seedJSON));
  assert.equal(get('gameConfig').annual_budget, roadsForecast.current_budget);
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
  assert.equal(get('gameConfig').annual_budget, roadsForecast.break_even_budget);
  assert.equal($('#start-budget-value').textContent, get('PvmtCommon.fmtMoney')(roadsForecast.break_even_budget));
});

// The board is read from hexgrid.geojson alone: each board hex's road area is
// the map's city-scope roads area (through city_same or a "city" object) and
// its k is the feature's k, and hexes without k are not on the board.
test('play: board hexes take road area and k from hexgrid', async () => {
  const want = boardFeatures();
  assert.ok(want.some((f) => f.properties.city_same) && want.some((f) => f.properties.city),
    'fixture must hold board hexes of both city encodings');
  assert.ok(hexgridJSON.features.length > want.length, 'fixture must hold hexes off the board');

  const { get } = await bootBoard(boardData(seedJSON));
  const got = JSON.parse(JSON.stringify(get('gameConfig').hexes));
  assert.deepEqual(got, want.map((f) => ({
    id: f.properties.id,
    roads_area: cityRoadsArea(f.properties),
    decay_rate: f.properties.k,
  })));
});

// The map rounds areas to 2dp, so a hex clipping only the tip of a road can
// read 0.00; k says it holds road, so it stays on the board as it reads.
test('play: a road hex the map rounds to zero area stays on the board', async () => {
  const grid = structuredClone(hexgridJSON);
  const sliver = grid.features.find((f) => f.properties.k > 0 && f.properties.city);
  sliver.properties.city.roads.area = 0;
  const { get } = await bootBoard({ ...boardData(seedJSON), 'hexgrid.geojson': grid });
  const hex = get('gameConfig').hexes.find((h) => h.id === sliver.properties.id);
  assert.equal(hex.roads_area, 0);
});

test('play: a hexgrid of another format is refused', async () => {
  const grid = structuredClone(hexgridJSON);
  grid.v = 5;
  const { get, doc } = await bootBoard({ ...boardData(seedJSON), 'hexgrid.geojson': grid });
  assert.equal(get('gameConfig'), null, 'the board booted from an unsupported hexgrid');
  assert.match(doc.body.textContent, /unsupported hexgrid format/);
});

// MapLibre fires 'load' only once every first-view
// basemap tile has settled, so a tile server that errors or never answers
// held "Loading board…" up for good. The board needs only the style.
test('play: the board boots on style readiness when the basemap tiles never load', async () => {
  const h = load({ page: 'play', wasm: true, data: boardData(seedJSON) });
  await h.flush();
  // The tile source errors and 'load' never comes; only the style is ready.
  h.fireMap('error');
  h.fireMap('style.load');
  await h.flush(12);

  assert.ok(h.get('gameConfig'), 'the board did not boot without the map load event');
  assert.equal(h.doc.getElementById('loading').style.display, 'none', '"Loading board…" is still up');
});

test('play: a map load after style.load does not boot the board twice', async () => {
  const h = load({ page: 'play', wasm: true, data: boardData(seedJSON) });
  await h.flush();
  h.fireMap('style.load');
  await h.flush(12);
  h.fireMap('load');
  await h.flush(12);
  assert.ok(h.get('gameConfig'), 'the board did not boot');
  assert.equal(h.win.__fitBounds.length, 1, 'boot ran more than once');
});
