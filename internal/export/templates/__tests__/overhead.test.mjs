// The program-overhead slider (solvent-streets-tzsr). Turns the bare
// construction cost tiers into the all-in program cost a city budgets:
// +ADA curb ramps, +design/inspection, +contingency.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON } from './fixtures.mjs';

function withSeedOverhead(oh) {
  return { ...fullData, 'forecast_seed.json': { ...seedJSON, cost_overhead: oh } };
}

async function ready(data) {
  const h = load({ data });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  return h;
}

test('the slider seeds from forecast_seed.json', async () => {
  const { $ } = await ready(withSeedOverhead(1.75));
  assert.equal(parseFloat($('#overhead-slider').value), 1.75);
  assert.match($('#overhead-value').textContent, /1\.75/);
});

test('a stale seed with no cost_overhead falls back to bare, not to $0', async () => {
  // The failure this guards: a partially-rebuilt site pairs a new app.js and
  // pvmt.wasm with an old per-city forecast_seed.json that has no
  // cost_overhead. Decoding that as 0 and multiplying would price the entire
  // page at $0. Bare is wrong by ~1.5x; blank is wrong by everything.
  const stale = { ...seedJSON };
  delete stale.cost_overhead;
  const { $, win } = await ready({ ...fullData, 'forecast_seed.json': stale });

  assert.equal(parseFloat($('#overhead-slider').value), 1,
    'a seed without cost_overhead must fall back to 1.0 (bare)');
  const sent = win.__simCalls.at(-1);
  assert.equal(sent.cost_overhead, 1, 'the bridge must receive 1.0, never 0');
});

test('the overhead reaches the WASM bridge on both runs of a simulation', async () => {
  const { win, resetSimCalls } = await ready(withSeedOverhead(1.5));
  resetSimCalls();
  win.runCustomScenario();

  // Two calls: the year-1 baseline inside getControlValues, then the scenario.
  // BOTH must carry the same overhead — the Budget Level slider is a percentage
  // of that year-1 need, so pricing them differently would silently scale the
  // budget by the overhead.
  assert.equal(win.__simCalls.length, 2);
  for (const call of win.__simCalls) {
    assert.equal(call.cost_overhead, 1.5,
      'every simulation in a run must price at the same overhead');
  }
});

test('moving the slider re-runs the simulation at the new overhead', async () => {
  const { win, $, resetSimCalls } = await ready(withSeedOverhead(1.5));
  resetSimCalls();

  const slider = $('#overhead-slider');
  slider.value = '2';
  slider.dispatchEvent(new win.Event('input', { bubbles: true }));

  assert.ok(win.__simCalls.length > 0, 'moving the slider did not re-simulate');
  assert.equal(win.__simCalls.at(-1).cost_overhead, 2);
  assert.match($('#overhead-value').textContent, /2/);
});

test('cost tiers reach the bridge UNSCALED, so the overhead is applied once', async () => {
  // The double-application trap: if the seed shipped pre-scaled tiers AND a
  // separate overhead, the bridge would price everything at overhead squared.
  // Tiers and overhead travel separately and combine exactly once, in
  // TieredCostProjector.
  const { win } = await ready(withSeedOverhead(1.5));
  const sent = win.__simCalls.at(-1);

  const seeded = seedJSON.cost_tiers.map((t) => t.cost_per_sqm);
  const sentCosts = Array.from(sent.cost_tiers, (t) => t.cost_per_sqm);
  assert.deepEqual(sentCosts, seeded,
    'tiers were scaled before reaching the bridge; the overhead would be applied twice');
});

test('the headline names the cost basis the figures are on', async () => {
  // At the calibrated default the claim is that these dollars are comparable to
  // a published pavement budget — that is the claim a reader needs in order to
  // use the number at all.
  const dflt = await ready(withSeedOverhead(1));
  assert.match(dflt.doc.getElementById('solvency-headline').textContent,
    /comparable to a published pavement budget/i,
    'the default basis must state its comparability, or the figure is unusable');

  // A city that overrode it must say so, or its numbers silently differ from
  // every other city on the same site.
  const scaled = await ready(withSeedOverhead(1.5));
  assert.match(scaled.doc.getElementById('solvency-headline').textContent,
    /1\.5.*cost multiplier/i,
    'an overridden multiplier must be disclosed');
});
