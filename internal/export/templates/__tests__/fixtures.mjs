// Data fixtures for the app.js specs — READ FROM DISK, not hand-written.
//
// These are the real files a `pvmt export` produces: internal/export's TestJS
// builds them with the actual exporter entry points (BuildMeta,
// BuildForecastsForCity, BuildScenariosData, BuildForecastSeed) over the same
// deterministic DB-free fixture the golden tests use, and drops them in
// PVMT_FIXTURE_DIR next to the rendered index.html. See
// js_harness_test.go:writeJSFixtureTree.
//
// They used to be JS object literals, and they had drifted from the Go json
// tags with nothing to notice (solvent-streets-q48z.8): the seed said `area`
// where Go emits `total_area`, scenarios were named `do-nothing`/`maintain`
// where the exporter emits `baseline`/`fund-25pct`/..., and meta.json put
// total_area at the top level instead of inside stats[]. Three of the four
// Financials charts never rendered under test and the suite stayed green — so
// it would also have stayed green through a real regression. Reading the
// exporter's own output is what makes that class of drift impossible: rename a
// json tag on the Go side and these specs fail.
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

// fullData is everything a city needs for the Financials tab to render.
export const fullData = {
  'meta.json': metaJSON,
  'scenarios.json': scenariosJSON,
  'forecast.json': forecastJSON,
  'forecast_seed.json': seedJSON,
};
