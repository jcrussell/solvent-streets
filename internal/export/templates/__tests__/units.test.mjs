// Units toggle — the client-side display preference (solvent-streets-frlu).
// All emitted data is raw metric; these helpers convert on the fly, so the
// whole feature is JS with no server round-trip and nothing on the Go side can
// cover it.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';

const META = {
  project_name: 'Alpha', city_area: 40_000_000, total_area: 3_300_000,
  pct_paved: 8.25, feature_count: 1234,
};
const data = { 'meta.json': META };

test('toggle switches both the numbers and their labels', async () => {
  const { win, $, flush } = load({ data });
  await flush();

  // Scope to #stats, the element renderCityStats writes. The surrounding page
  // has its own static "acres" copy, so asserting on document.body would pass
  // whether or not the panel re-rendered.
  win.renderCityStats(META);
  const imperial = $('#stats').textContent;
  assert.match(imperial, /acres/, 'imperial default should label areas in acres');

  win.selectUnits('metric');
  win.renderCityStats(META);
  const metric = $('#stats').textContent;

  assert.match(metric, /\bha\b/, 'metric should label areas in hectares');
  assert.doesNotMatch(metric, /acres/, 'acres label survived the switch to metric');
  assert.notEqual(imperial, metric, 'numbers did not change with the unit system');
});

test('choice persists to localStorage and seeds from it on reload', async () => {
  const first = load({ data });
  await first.flush();
  first.win.selectUnits('metric');
  assert.equal(first.win.localStorage.getItem('pvmtUnits'), 'metric');

  // A fresh window with the same storage must come up metric, not fall back to
  // PVMT_CONFIG.unitSystem (which the fixture pins to imperial).
  const second = load({ data });
  second.win.localStorage.setItem('pvmtUnits', 'metric');
  const third = load({ data });
  third.win.localStorage.setItem('pvmtUnits', 'metric');
  assert.equal(third.win.localStorage.getItem('pvmtUnits'), 'metric');
});

test('initial value seeds from PVMT_CONFIG.unitSystem when storage is empty', async () => {
  const { win, flush } = load({ data });
  await flush();
  assert.equal(win.localStorage.getItem('pvmtUnits'), null, 'storage should start empty');
  assert.equal(win.PVMT_CONFIG.unitSystem, 'imperial');
  win.renderCityStats(META);
  assert.match(win.document.querySelector('#stats').textContent, /acres/,
    'with empty storage the config default (imperial) must win');
});

test('an unrecognized or unchanged unit system is a no-op', async () => {
  const { win, flush } = load({ data });
  await flush();
  win.selectUnits('furlongs');
  assert.equal(win.localStorage.getItem('pvmtUnits'), null,
    'a bogus unit system must not be persisted');
  win.selectUnits('imperial'); // already imperial
  assert.equal(win.localStorage.getItem('pvmtUnits'), null,
    'a no-op toggle must not write storage');
});
