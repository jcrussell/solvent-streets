// Data fixtures for the app.js specs — READ FROM DISK, not hand-written.
//
// These are the real files a `pvmt export` produces: internal/export's TestJS
// builds them with the actual exporter entry points (BuildMeta,
// BuildForecastsForCity, BuildScenariosData, BuildForecastSeed) over the same
// deterministic DB-free fixture the golden tests use, and drops them in
// PVMT_FIXTURE_DIR next to the rendered index.html. See
// js_harness_test.go:writeJSFixtureTree.
//
// Hand-written literals would drift from the Go json tags with nothing to
// notice, and a spec reading a misnamed key passes while the chart it covers
// never renders. Reading the exporter's own output makes that class of drift
// impossible: rename a json tag on the Go side and these specs fail.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fixtureDir } from './harness.mjs';

// readFixture returns a FRESH parse each call, so a spec can mutate what it
// gets back without leaking into the next one.
export function readFixture(name) {
  return JSON.parse(readFileSync(join(fixtureDir(), name), 'utf8'));
}

export const metaJSON = readFixture('meta.json');
export const scenariosJSON = readFixture('scenarios.json');
export const forecastJSON = readFixture('forecast.json');
export const seedJSON = readFixture('forecast_seed.json');

// The /play board: the exporter's hexgrid.geojson for a city with per-hex road
// results and decay rates (js_harness_test.go:writeJSFixtureBoard). Its board
// hexes are the features carrying k.
export const hexgridJSON = readFixture('hexgrid.geojson');

// fullData is everything a city needs for the Financials tab to render.
export const fullData = {
  'meta.json': metaJSON,
  'scenarios.json': scenariosJSON,
  'forecast.json': forecastJSON,
  'forecast_seed.json': seedJSON,
};
