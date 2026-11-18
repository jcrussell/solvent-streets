// Financials -> Advanced options cost-tier table (solvent-streets-8ade): bad
// input is caught inline, in display units, before it reaches the simulator,
// and the table reads cleanly (rounded costs, 100 for the open top bound,
// labelled inputs).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON } from './fixtures.mjs';

async function ready() {
  const h = load({ data: fullData });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  return h;
}

const input = ($, i, field) => $('#tier-inputs input[data-tier="' + i + '"][data-field="' + field + '"]');

function type(win, el, value) {
  el.value = String(value);
  el.dispatchEvent(new win.Event('input'));
}

test('a negative cost is flagged inline and never reaches the simulator', async () => {
  const { win, $, flush } = await ready();
  const calls = win.__simCalls.length;
  type(win, input($, 0, 'cost_per_sqm'), -5);
  await flush();
  assert.equal(win.__simCalls.length, calls, 'invalid input must not be simulated');
  assert.equal($('#tier-errors').hidden, false);
  assert.match($('#tier-errors').textContent, /cost must be more than \$0\/sq ft/, 'the message uses the units on screen');
  assert.equal(input($, 0, 'cost_per_sqm').getAttribute('aria-invalid'), 'true');
  assert.equal($('#error-banner').hidden, true, 'an input error is not a load failure');

  // A free tier is refused too: the simulator rejects a zero cost.
  type(win, input($, 0, 'cost_per_sqm'), 0);
  await flush();
  assert.equal(win.__simCalls.length, calls, 'a $0 tier must not be simulated');
  assert.equal($('#tier-errors').hidden, false);

  // Correcting it clears the message and runs again.
  type(win, input($, 0, 'cost_per_sqm'), 1);
  await flush();
  assert.equal($('#tier-errors').hidden, true);
  assert.ok(win.__simCalls.length > calls, 'a corrected value runs the simulator');
});

// Invalid tiers stop the forecast, so the headline figures above still show
// the last valid run: they are dimmed until the inputs are fixed
// (solvent-streets-ern4.36).
test('the forecast figures dim while the tier inputs are invalid', async () => {
  const { win, $, flush } = await ready();
  const ids = ['solvency-headline', 'headlines', 'cohort-section', 'charts'];
  const stale = () => ids.filter((id) => $('#' + id).classList.contains('forecast-stale'));
  assert.deepEqual(stale(), [], 'valid inputs: nothing dimmed');

  type(win, input($, 0, 'cost_per_sqm'), -5);
  await flush();
  assert.deepEqual(stale(), ids, 'every forecast figure is dimmed while the run is refused');
  assert.match($('#headlines').getAttribute('title'), /Not updated/);

  type(win, input($, 0, 'cost_per_sqm'), 1);
  await flush();
  assert.deepEqual(stale(), [], 'a corrected value runs again and undims');
  assert.equal($('#headlines').getAttribute('title'), null);
});

test('an inverted or overlapping range is flagged', async () => {
  const { win, $, flush } = await ready();
  type(win, input($, 0, 'min_pci'), 90);
  type(win, input($, 0, 'max_pci'), 20);
  await flush();
  assert.match($('#tier-errors').textContent, /minimum PCI must be below its maximum/);

  type(win, input($, 0, 'min_pci'), 0);
  type(win, input($, 0, 'max_pci'), 100);
  await flush();
  assert.match($('#tier-errors').textContent, /overlap/, 'two tiers covering the same PCI must be flagged');
});

test('costs show rounded, the open top shows 100, inputs are labelled', async () => {
  const { $ } = await ready();
  const tiers = seedJSON.cost_tiers;
  tiers.forEach((t, i) => {
    const cost = input($, i, 'cost_per_sqm').value;
    assert.ok(!/\.\d{3,}/.test(cost), `tier ${i} cost shows unrounded: ${cost}`);
    assert.ok(parseFloat(input($, i, 'max_pci').value) <= 100, `tier ${i} shows the internal 101 sentinel`);
    for (const f of ['min_pci', 'max_pci', 'cost_per_sqm']) {
      assert.ok(input($, i, f).getAttribute('aria-label'), `tier ${i} ${f} has no label`);
    }
  });
});

test('rounding the display does not move the simulated costs', async () => {
  const { win, $, flush } = await ready();
  type(win, input($, 0, 'min_pci'), seedJSON.cost_tiers[0].min_pci); // any valid edit re-runs
  await flush();
  const sent = Array.from(win.__simCalls.at(-1).cost_tiers, (t) => [t.cost_per_sqm, t.max_pci]);
  assert.deepEqual(sent, seedJSON.cost_tiers.map((t) => [t.cost_per_sqm, t.max_pci]),
    'the sim must get the exact seeded $/sq m and the 101 sentinel back');
});
