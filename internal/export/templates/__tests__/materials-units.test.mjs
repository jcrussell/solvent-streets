// The Materials tab under the unit toggle: every
// figure, header and rate follows the toggle, whether the tab is open when it
// flips or is opened afterwards, and a round trip lands back where it began.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, scenariosJSON } from './fixtures.mjs';

const headers = ($$) => $$('#materials-tier-table thead th').map((th) => th.textContent);
const firstRow = ($$) => [...$$('#materials-tier-table tbody tr')[0].querySelectorAll('td')].map((td) => td.textContent);

// The units each system shows: the mass a headline is in, the mass a rate is
// per, the tier table's intensity, and the area its oil column is per.
const UNITS = {
  imperial: { mass: 'tons', per: 'ton', intensity: 'lb/sq ft', oilPer: '10,000 sq ft' },
  metric: { mass: 't', per: 'tonne', intensity: 'kg/m²', oilPer: '1000 m²' },
};
const word = (/** @type {string} */ unit) => new RegExp('(^|[\\s(/])' + unit.replace(/[/]/g, '\\/') + '($|[\\s)/])');

// inUnits asserts every unit-bearing figure on the tab is in `system`'s units.
/** @param {any} h @param {'imperial' | 'metric'} system */
function inUnits({ win, $, $$ }, system) {
  const u = UNITS[system];
  assert.equal(win.massUnitSingular(), u.per);
  assert.equal(win.intensityUnit(), u.intensity);
  const h = headers($$);
  assert.equal(h.length, 5);
  for (const at of [1, 3]) assert.ok(h[at].includes('(' + u.intensity + ')'), `${system}: column ${at} reads ${h[at]}`);
  assert.ok(h[4].includes(u.oilPer), `${system}: the oil column reads ${h[4]}`);
  assert.match($('#hl-mix').textContent, word(u.mass), system);
  assert.match($('#hl-binder').textContent, word(u.mass), system);
  assert.match($('#hl-oil-sub').textContent, word(u.per), system);
}

test('a unit toggle on the open Materials tab re-renders it, and back again', async () => {
  const h = load({ data: fullData });
  const { win, $, $$, flush } = h;
  await flush();
  win.selectTab('materials-tab');
  await flush(12);
  // The fixture page is imperial.
  const imperial = { h: headers($$), r: firstRow($$), sub: $('#hl-oil-sub').textContent };
  inUnits(h, 'imperial');

  win.selectUnits('metric');
  await flush(12);
  inUnits(h, 'metric');
  assert.notDeepEqual(firstRow($$), imperial.r, 'the tier figures were not converted');

  win.selectUnits('imperial');
  await flush(12);
  assert.deepEqual(headers($$), imperial.h);
  assert.deepEqual(firstRow($$), imperial.r);
  assert.equal($('#hl-oil-sub').textContent, imperial.sub);
  inUnits(h, 'imperial');
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
  // Doing nothing lays no material, so the baseline draws no line.
  const baseline = scenariosJSON.city.find((s) => s.scenario.strategy === 'do_nothing').scenario.label;
  assert.ok(!mixChart().data.datasets.some((d) => d.label.startsWith(baseline)),
    `the do-nothing baseline draws a material line: ${mixChart().data.datasets.map((d) => d.label)}`);
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
  const h = load({ data: fullData });
  const { win, flush } = h;
  await flush();
  win.selectTab('materials-tab'); await flush(12);
  win.selectTab('financials-tab'); await flush(12);
  win.selectUnits('metric'); await flush(12);
  win.selectTab('materials-tab'); await flush(12);
  inUnits(h, 'metric');

  win.selectTab('financials-tab'); await flush(12);
  win.selectUnits('imperial'); await flush(12);
  win.selectTab('materials-tab'); await flush(12);
  inUnits(h, 'imperial');
});
