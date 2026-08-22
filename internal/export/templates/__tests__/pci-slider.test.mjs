// The Initial PCI slider's seeding (solvent-streets-q48z.9). Config validation
// accepts initial_pci anywhere in 0-100 and NormalizeForecast only replaces <=0
// or >100, so a city configured below the markup's 50 floor reaches the seed
// intact. Clamping it there ran the interactive line — and the End PCI /
// N-Year Spend / N-Year Deficit tiles it overwrites — from a starting condition
// the city never configured, while forecast.json had been built from the real
// one.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON } from './fixtures.mjs';

function withPCI(pci) {
  return { ...fullData, 'forecast_seed.json': { ...seedJSON, initial_pci: pci } };
}

async function ready(data) {
  const h = load({ data });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  return h;
}

test('a seed below the markup floor widens the track instead of clamping', async () => {
  const { $, win } = await ready(withPCI(45));

  assert.equal(parseInt($('#pci-slider').value), 45,
    'the slider clamped the seed to the markup floor');
  assert.ok(parseFloat($('#pci-slider').min) <= 45,
    'slider min must cover the seeded value');
  assert.equal($('#pci-value').textContent, '45',
    'the label disagrees with the seeded PCI');
  assert.equal(win.__simCalls.at(-1).initial_pci, 45,
    'the bridge simulated from a starting condition the city never configured');
});

test('an ordinary in-range seed leaves the shipped track alone', async () => {
  const { $ } = await ready(withPCI(85));
  assert.equal(parseFloat($('#pci-slider').min), 50);
  assert.equal(parseFloat($('#pci-slider').max), 100);
  assert.equal(parseInt($('#pci-slider').value), 85);
});

test('a widened track is restored on the next city', async () => {
  // The leak this guards: widening is cumulative unless something puts the
  // shipped track back, so one low-PCI city would leave every city after it
  // draggable down to 45.
  const data = withPCI(45);
  const h = await ready(data);
  assert.equal(parseFloat(h.$('#pci-slider').min), 45);

  data['forecast_seed.json'] = { ...seedJSON, initial_pci: 85 };
  await h.win.loadFinancials();          // the refetch path a city switch takes
  await h.flush();

  assert.equal(parseFloat(h.$('#pci-slider').min), 50,
    'the previous city\'s widened track leaked into this one');
  assert.equal(parseInt(h.$('#pci-slider').value), 85);
});

test('the ARIA value tracks the seed and the drag, not the shipped default', async () => {
  // AT reads aria-valuenow in preference to the native value, and nothing kept
  // it in step: it stayed at the markup's 85 whatever the slider held. The
  // bounds have to follow the widened track for the same reason.
  const { $, win } = await ready(withPCI(45));
  const slider = $('#pci-slider');

  assert.equal(slider.getAttribute('aria-valuenow'), '45',
    'a screen reader would announce the shipped default, not the seeded PCI');
  assert.equal(slider.getAttribute('aria-valuemin'), '45',
    'the announced range does not cover the widened track');

  slider.value = '70';
  slider.dispatchEvent(new win.Event('input', { bubbles: true }));
  assert.equal(slider.getAttribute('aria-valuenow'), '70',
    'the announced value went stale as soon as the slider moved');
});
