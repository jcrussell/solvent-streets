// The Materials tab under the unit toggle: every
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

// The mass charts plot display units, so Chart.js picks round ticks in tons
// as well as tonnes: the imperial series are the metric ones converted, and
// the ticks print the value as it is. The do-nothing line is named for what
// it plots: the material the decaying network would need.
test('the Materials mass charts plot display units, ticks unconverted', async () => {
  const { win, flush } = load({ data: fullData });
  await flush();
  const mixChart = () => win.__charts.filter((c) => c.config.type === 'line').at(-2);

  win.selectUnits('metric');
  win.selectTab('materials-tab');
  await flush(12);
  const metric = mixChart().data.datasets.map((d) => d.data);
  assert.ok(metric.length > 0 && metric[0].length > 0, 'the mix chart plotted nothing');
  assert.ok(mixChart().data.datasets.some((d) => d.label.endsWith(' (material need)')),
    `no do-nothing line named for its need: ${mixChart().data.datasets.map((d) => d.label)}`);
  assert.equal(mixChart().options.scales.y.ticks.callback(250000), '250,000 t');

  win.selectUnits('imperial');
  await flush(12);
  const imperial = mixChart().data.datasets.map((d) => d.data);
  imperial.forEach((series, i) => series.forEach((v, j) => {
    assert.ok(Math.abs(v - metric[i][j] / 0.90718474) < 1e-6, `series ${i} year ${j}: ${v} tons for ${metric[i][j]} t`);
  }));
  assert.equal(mixChart().options.scales.y.ticks.callback(250000), '250,000 tons');
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
