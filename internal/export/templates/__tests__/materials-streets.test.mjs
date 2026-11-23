// Once the simulator is up, the Materials tab covers streets only, as the
// Financials tab does: the funded streets-only reference bound plus the run
// at the city's cited budget as Today's budget, each with the material the
// simulator says its spending lays. Until then it shows the exported set.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load, defaultSimulate } from './harness.mjs';
import { fullData, scenariosJSON, forecastJSON } from './fixtures.mjs';

const TODAY = forecastJSON.find((f) => f.resource_type === 'roads').current_budget;
const types = (cohorts) => [...new Set(Array.from(cohorts ?? [], (c) => c.resource_type))].sort();

// laid gives each year the material its spending lays, as the simulator
// does: a tonne per $1,000 spent, all of it rehab.
const laid = (years) => years.map((y) => ({
  ...y,
  materials: { mix_tonnes: y.annual_spend / 1000, binder_tonnes: y.annual_spend / 20000, oil_barrels: y.annual_spend / 5000 },
  materials_by_tier: y.annual_spend > 0 ? [{ tier: 'rehab', mix_tonnes: y.annual_spend / 1000 }] : [],
}));

// sim answers the reference-set request with the exported set's scenarios
// over simulated years, and the solvency probes with a hold-steady budget.
function sim(input) {
  const out = defaultSimulate(input);
  if (input.solvency) {
    out.break_even_budget = 30_000_000;
    return out;
  }
  out.years = laid(out.years);
  if (input.scenarios) {
    out.scenarios = scenariosJSON.city.map((s) => ({
      ...s, years: laid(defaultSimulate({ ...input, annual_budget: s.scenario.strategy === 'do_nothing' ? 0 : 1e7 }).years),
    }));
  }
  return out;
}

async function ready({ wasm = true } = {}) {
  const h = load({ data: fullData, simulate: sim, url: 'https://example.test/index.html?tab=materials' });
  await h.flush();
  if (wasm) h.win.onWasmReady();
  await h.flush();
  await h.win.loadMaterials();
  await h.flush();
  return h;
}

// The mix chart is the line chart on the Materials tab.
const mixChart = (h) => h.chartsOn('#materials-charts').filter((c) => c.config.type === 'line').at(0);

// The streets-only set is drawn from this city's Financials data. When the
// simulator is up before that data lands, the tab redraws once it does,
// rather than staying on the exported all-asphalt set.
test('Materials redraws over the streets when Financials lands after the simulator', async () => {
  const h = load({ data: fullData, simulate: sim, url: 'https://example.test/index.html?tab=materials', hold: ['forecast.json'] });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  assert.equal(h.$('#materials-roads-note').hidden, true, 'fixture: the streets set drew before Financials landed');
  h.release('forecast.json');
  await h.flush(12);
  assert.equal(h.$('#materials-roads-note').hidden, false, 'Materials stayed on the exported set');
  assert.equal(mixChart(h).data.datasets.at(-1).label, h.win.eval('TODAY_LINE'));
});

test('before the simulator the tab shows the exported set and no streets note', async () => {
  const h = await ready({ wasm: false });
  assert.equal(h.$('#materials-roads-note').hidden, true);
  const labels = mixChart(h).data.datasets.map((d) => d.label);
  assert.ok(!labels.includes(h.win.eval('TODAY_LINE')), 'a Today line without the simulator');
});

test('with the simulator the tab covers streets and draws Today\'s budget', async () => {
  assert.ok(TODAY > 0, 'fixture must cite a current budget');
  const h = await ready();
  assert.equal(h.$('#materials-roads-note').hidden, false, 'the streets-only note is missing');
  const ref = h.win.__simCalls.filter((c) => c.scenarios).at(-1);
  assert.ok(ref, 'no streets-only reference set was simulated');
  assert.deepEqual(types(ref.cohorts), ['roads']);
  const today = h.win.__simCalls.filter((c) => !c.solvency && !c.scenarios && c.annual_budget === TODAY);
  assert.ok(today.length > 0, 'no run at today\'s budget');
  assert.deepEqual(types(today.at(-1).cohorts), ['roads']);

  const lines = mixChart(h).data.datasets;
  assert.equal(lines.at(-1).label, h.win.eval('TODAY_LINE'));
  assert.equal(lines.length, 2, 'full funding and Today\'s budget: ' + lines.map((d) => d.label));
  // Today's line is the material its run's spending lays.
  const todayYears = laid(defaultSimulate(today.at(-1)).years);
  assert.deepEqual(Array.from(lines.at(-1).series, (r) => r.mixTonnes), todayYears.map((y) => y.materials.mix_tonnes));
  // The tier chart stacks today's mix by tier.
  const bars = h.chartsOn('#materials-charts').find((c) => c.config.type === 'bar');
  const rehab = bars.data.datasets.find((d) => d.label === 'rehab');
  assert.ok(rehab && rehab.data.some((v) => v > 0), 'the rehab stack is empty');
});
