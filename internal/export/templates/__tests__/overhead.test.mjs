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

// assertReachable pins the property that keeps a browser from moving the seeded
// value: it must lie on the step ladder the input advertises (which starts at
// min, per the HTML step-base rule), or the input must be off-step entirely.
//
// This is asserted rather than observed because jsdom implements only the
// invalid-value, underflow and overflow clauses of the range sanitization
// algorithm — it has NO step-mismatch branch. Setting 1.33 on min=1 step=0.05
// reads back "1.33" in jsdom and 1.35 in a real browser. Reading .value can
// therefore never catch the snap; the ladder can.
function assertReachable(slider, v) {
  const step = parseFloat(slider.step);
  if (!Number.isFinite(step)) return;      // step="any" reaches everything
  const n = (v - parseFloat(slider.min)) / step;
  assert.ok(Math.abs(n - Math.round(n)) < 1e-6,
    `${v} is off the step ladder (min=${slider.min} step=${slider.step}); a browser would snap it`);
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
  // page at $0. Bare is off by whatever the configured overhead was (nothing
  // at all under the current 1.0 default); blank is wrong by everything.
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

test('a seed above the markup range widens the slider instead of clamping', async () => {
  // Config validation accepts cost_overhead up to 5, but the markup ships a
  // 1-2.5 track. Clamping a 3.0 seed to 2.5 priced the interactive line — and
  // the headline tiles it overwrites — 17% below the static export lines on the
  // same chart, while the disclosure text still read "3x". The seed is the
  // source of truth; the track has to accommodate it.
  const { $, win } = await ready(withSeedOverhead(3));

  assert.equal(parseFloat($('#overhead-slider').value), 3,
    'the slider clamped the seed instead of widening to admit it');
  assert.ok(parseFloat($('#overhead-slider').max) >= 3,
    'slider max must cover the seeded value');
  assert.equal(win.__simCalls.at(-1).cost_overhead, 3,
    'the bridge must price at the seeded multiplier, not the clamped one');
  assert.match($('#overhead-value').textContent, /3/);

  // And the disclosure must agree with what was actually simulated.
  assert.match(win.document.getElementById('solvency-headline').textContent,
    /3.*cost multiplier/i);
});

test('a sub-1 seed widens the slider downward rather than rounding up', async () => {
  const { $, win } = await ready(withSeedOverhead(0.8));
  assert.equal(parseFloat($('#overhead-slider').value), 0.8);
  assert.ok(parseFloat($('#overhead-slider').min) <= 0.8);
  assert.equal(win.__simCalls.at(-1).cost_overhead, 0.8);
});

test('an ordinary in-range seed leaves the shipped track alone', async () => {
  // The widening must be conditional: a 1.5 seed must not move min/max, or the
  // slider's feel changes city to city for no reason.
  const { $ } = await ready(withSeedOverhead(1.5));
  assert.equal(parseFloat($('#overhead-slider').min), 1);
  assert.equal(parseFloat($('#overhead-slider').max), 2.5);
});

test('a step-mismatched seed is admitted exactly, not snapped to the ladder', async () => {
  // A config-legal cost_overhead of 1.33 has step candidates 1.30 and 1.35 on
  // the shipped step=0.05 track; 1.35 is nearer, so a browser hands the sim
  // 1.35 — ~1.5% above what forecast.json was priced at — while costRegimeText
  // (which reads the seed, not the slider) keeps printing "1.33x".
  const { $, win } = await ready(withSeedOverhead(1.33));

  assertReachable($('#overhead-slider'), 1.33);
  assert.equal(parseFloat($('#overhead-slider').value), 1.33);
  assert.equal(win.__simCalls.at(-1).cost_overhead, 1.33,
    'the bridge priced at something other than the seeded multiplier');
});

test('a widened track is restored on the next city', async () => {
  // Widening is cumulative unless something puts the shipped track back: one
  // city seeded at 4x would leave every city after it draggable to 4x, on a
  // track whose scale silently changed under the user.
  const data = withSeedOverhead(4);
  const h = await ready(data);
  assert.equal(parseFloat(h.$('#overhead-slider').max), 4);

  data['forecast_seed.json'] = { ...seedJSON, cost_overhead: 1.5 };
  await h.win.loadFinancials();        // the refetch path a city switch takes
  await h.flush();

  assert.equal(parseFloat(h.$('#overhead-slider').max), 2.5,
    'the previous city\'s widened ceiling leaked into this one');
  assert.equal(h.$('#overhead-slider').getAttribute('aria-valuemax'), '2.5',
    'the announced ceiling still describes the previous city');
  assert.equal(parseFloat(h.$('#overhead-slider').value), 1.5);
});

test('a downward-widened track does not make the default unreachable', async () => {
  // The nastiest form of the leak. Seeding 0.83 moves min to 0.83, and with
  // step=0.05 the ladder becomes 0.83/0.88/.../0.98/1.03 — 1.0 is not on it.
  // The next city, seeded at the default 1.0, lands on 0.98 in a real browser:
  // a city that configured nothing gets priced 2% light.
  const data = withSeedOverhead(0.83);
  const h = await ready(data);
  assert.equal(parseFloat(h.$('#overhead-slider').min), 0.83);

  data['forecast_seed.json'] = { ...seedJSON, cost_overhead: 1 };
  await h.win.loadFinancials();
  await h.flush();

  const slider = h.$('#overhead-slider');
  assert.equal(parseFloat(slider.min), 1, 'the widened floor leaked into this city');
  assertReachable(slider, 1);
  assert.equal(parseFloat(slider.value), 1);
  assert.equal(h.win.__simCalls.at(-1).cost_overhead, 1);
});
