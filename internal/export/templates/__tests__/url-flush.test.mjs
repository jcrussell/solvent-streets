// Financials sliders write the URL once input pauses (Router.replaceSoon).
// A finished edit must still reach the history entry
// when the reader leaves at once: by leaving the page, or by Back right
// after letting go of the slider (peer review of 4hzr).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData } from './fixtures.mjs';

async function ready() {
  const h = load({ data: fullData, url: 'https://example.test/index.html?tab=financials-tab' });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  return h;
}
const pciParam = (win) => new URLSearchParams(win.location.search).get('pci');

// drag sets the PCI slider to a value it does not already hold and fires the
// input events a drag fires, without the release.
function drag(win) {
  const el = win.document.querySelector('#pci-slider');
  const v = String(Number(el.value) === 60 ? 61 : 60);
  el.value = v;
  el.dispatchEvent(new win.Event('input', { bubbles: true }));
  return { el, v };
}

test('leaving the page inside the debounce window writes the last edit', async () => {
  const { win } = await ready();
  const { v } = drag(win);
  win.dispatchEvent(new win.Event('pagehide'));
  assert.equal(pciParam(win), v);
});

test('Back right after releasing a slider keeps the edit on the entry left', async () => {
  const { win, flush } = await ready();
  win.selectTab('map-tab');
  win.selectTab('financials-tab');
  const { el, v } = drag(win);
  el.dispatchEvent(new win.Event('pointerup', { bubbles: true }));
  win.history.back();
  await flush();
  win.history.forward();
  await flush();
  assert.equal(pciParam(win), v);
  assert.equal(win.document.querySelector('#pci-slider').value, v);
});

test('a key press on a slider writes its edit when the key is released', async () => {
  const { win } = await ready();
  const { el, v } = drag(win);
  el.dispatchEvent(new win.KeyboardEvent('keyup', { key: 'ArrowRight', bubbles: true }));
  assert.equal(pciParam(win), v);
});
