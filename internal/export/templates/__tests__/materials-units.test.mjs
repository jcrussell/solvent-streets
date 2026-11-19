// The Materials tab under the unit toggle (solvent-streets-ern4.35): every
// figure, header and rate follows the toggle, whether the tab is open when it
// flips or is opened afterwards, and a round trip lands back where it began.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData } from './fixtures.mjs';

const headers = ($$) => $$('#materials-tier-table thead th').map((th) => th.textContent);
const firstRow = ($$) => [...$$('#materials-tier-table tbody tr')[0].querySelectorAll('td')].map((td) => td.textContent);

test('a unit toggle on the open Materials tab re-renders it, and back again', async () => {
  const { win, $, $$, flush } = load({ data: fullData });
  await flush();
  win.selectTab('materials-tab');
  await flush(12);
  // The fixture page is imperial.
  const imperial = { h: headers($$), r: firstRow($$), sub: $('#hl-oil-sub').textContent };
  assert.match($('#hl-mix').textContent, / tons\/yr$/);
  assert.match($('#hl-binder').textContent, / tons\/yr$/);
  assert.match(imperial.sub, /per ton binder$/);

  win.selectUnits('metric');
  await flush(12);
  assert.deepEqual(headers($$), ['Tier', 'Mix (kg/m²)', 'Binder %', 'Binder (kg/m²)', 'Oil (bbl/1000 m²)']);
  assert.match($('#hl-mix').textContent, / t\/yr$/);
  assert.match($('#hl-binder').textContent, / t\/yr$/);
  assert.match($('#hl-oil-sub').textContent, /per tonne binder$/);

  win.selectUnits('imperial');
  await flush(12);
  assert.deepEqual(headers($$), imperial.h);
  assert.deepEqual(firstRow($$), imperial.r);
  assert.equal($('#hl-oil-sub').textContent, imperial.sub);
  assert.match($('#hl-mix').textContent, / tons\/yr$/);
});

test('a unit toggle on another tab shows on the next Materials open', async () => {
  const { win, $, $$, flush } = load({ data: fullData });
  await flush();
  win.selectTab('materials-tab'); await flush(12);
  win.selectTab('financials-tab'); await flush(12);
  win.selectUnits('metric'); await flush(12);
  win.selectTab('materials-tab'); await flush(12);
  assert.equal(headers($$)[1], 'Mix (kg/m²)');
  assert.match($('#hl-oil-sub').textContent, /per tonne binder$/);

  win.selectTab('financials-tab'); await flush(12);
  win.selectUnits('imperial'); await flush(12);
  win.selectTab('materials-tab'); await flush(12);
  assert.equal(headers($$)[1], 'Mix (lb/sq ft)');
  assert.match($('#hl-oil-sub').textContent, /per ton binder$/);
});
