// Overlapping data loads: switching cities while the previous city's files are
// still in flight must leave the NEWER city on screen. loadCityData and
// loadFinancials each bump a generation counter and bail after every await if
// a newer load has started (solvent-streets-yvlv.22). These specs used to be
// Go greps of app.js for the counter tokens; they now make the race happen —
// the outgoing city's response is held until after the incoming city's lands.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, metaJSON, scenariosJSON } from './fixtures.mjs';

// perCity gives alpha-ca and beta-ca their own copy of file, tagged so a spec
// can tell whose data is on screen. Per-city keys come FIRST: the fetch stub
// matches by suffix and takes the first key that matches, so a bare
// 'meta.json' listed earlier would answer for every city.
function perCity(file, base, tag) {
  return {
    ['cities/alpha-ca/data/' + file]: tag(structuredClone(base), 'alpha'),
    ['cities/beta-ca/data/' + file]: tag(structuredClone(base), 'beta'),
  };
}

test('a slow load for the previous city cannot overwrite the city switched to', async () => {
  const data = {
    ...perCity('meta.json', metaJSON, (m, who) => ({ ...m, project_name: who + ' project' })),
    ...fullData,
  };
  // The page loads CITIES[0] (Alpha) once the map is ready; hold Alpha's
  // meta.json so that load is still in flight when the user switches to Beta.
  const { win, get, flush, release, fireMap } = load({ data, hold: ['cities/alpha-ca/data/meta.json'] });
  const shown = () => get('lastCityMeta && lastCityMeta.project_name');
  await flush();
  fireMap('load');
  await flush();
  assert.equal(shown(), null, 'Alpha\'s held load should not have rendered yet');

  win.selectCity('beta-ca');
  await flush();
  assert.equal(shown(), 'beta project', 'the Beta load did not render');

  // Alpha's response finally arrives. It belongs to a superseded load and must
  // be dropped rather than replace Beta's stats (or re-add its map sources,
  // which the MapLibre-faithful stub would throw on).
  release('cities/alpha-ca/data/meta.json');
  await flush(12);
  assert.equal(shown(), 'beta project', 'a stale Alpha response overwrote Beta');
});

test('a slow financials load for the previous city cannot replace the new one', async () => {
  const data = {
    ...perCity('scenarios.json', scenariosJSON, (s, who) => ({ ...s, __city: who })),
    ...fullData,
  };
  const { win, get, flush, release } = load({ data, hold: ['cities/alpha-ca/data/scenarios.json'] });
  await flush();

  win.selectCity('beta-ca');
  await flush();
  assert.equal(get('scenarioData && scenarioData.__city'), 'beta', 'the Beta financials did not load');

  release('cities/alpha-ca/data/scenarios.json');
  await flush(12);
  assert.equal(get('scenarioData && scenarioData.__city'), 'beta', 'a stale Alpha scenarios.json replaced Beta');
});
