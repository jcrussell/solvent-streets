// "Color by" per-hex metrics (hex-metrics.json, solvent-streets-ql2w): the
// selector lists the city's metrics, choosing one swaps the paint expression
// to discrete bands guarded against hexes without a value, redraws the legend
// from the metric's scale, shows the points of service, rides the URL only on
// the Map tab, and falls back to coverage when the city lacks the metric.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { load } from './harness.mjs';
import { metaJSON, fullData, readFixture } from './fixtures.mjs';

const plain = (v) => JSON.parse(JSON.stringify(v));
const params = (win) => new URLSearchParams(win.location.search);

// REAL exporter output (BuildHexMetrics / BuildMeta over the harness fixture,
// see js_harness_test.go), not hand-written: rename a json tag on the Go side
// and these specs fail instead of passing over a stale literal.
const metricsFile = readFixture('hex-metrics.json');
const fire = metricsFile.metrics.fire_response_s;
// The fixture's hexes, real ids of its drawn grid: the two the fire run
// timed, fastest first, then one it did not (the unreachable hex has no value).
const [h0, h1] = Object.keys(fire.values).sort((a, b) => fire.values[a] - fire.values[b]);
const h2 = 'hex:unreached';
const hexIDs = [h0, h1, h2];

function hexFC(features) {
  return {
    type: 'FeatureCollection',
    v: 6,
    features: features.map((props, i) => ({
      type: 'Feature',
      geometry: { type: 'Polygon', coordinates: [[[0, 0], [0, 1], [1, 1], [0, 0]]] },
      properties: { id: hexIDs[i], ...props },
    })),
  };
}

const data = {
  ...fullData,
  'meta.json': metaJSON,
  'hexgrid.geojson': hexFC([
    { all: { roads: { area: 100, share: 0.1 } }, city_same: 1 },
    { all: { roads: { area: 100, share: 0.1 } }, city_same: 1 },
    { all: { roads: { area: 100, share: 0.1 } }, city_same: 1 }, // no metric value
  ]),
  'hex-metrics.json': metricsFile,
};

async function loadWithMetrics(url) {
  const h = load({ data, url });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  return h;
}

test('the Color-by selector lists the available derived metrics plus each stored metric, and the stats card summarises it', async () => {
  const { $, $$ } = await loadWithMetrics();
  const opts = $$('#hex-metric option').map((o) => o.value);
  // Derived pavement colorings first (default first, then registry order;
  // spaces and est. cost are absent: the fixture has no parking or cost
  // summary), then stored metrics.
  assert.deepEqual(opts, ['coverage_share', 'paved_area_m2', 'fire_response_s']);
  assert.equal($('#hex-metric').hidden, false);
  const card = $$('#stats .stat-card').find((c) => c.querySelector('h3').textContent === 'Fire response');
  assert.ok(card, 'no metric card rendered from meta.metrics');
  assert.match(card.textContent, /Median per hex\s*11:40/, 'median must render in m:ss, marked per hex');
  assert.match(card.textContent, /Range per hex/);
  assert.match(card.textContent, /Stations serving the city\s*1/);
  assert.doesNotMatch(card.textContent, /outside the city/, 'the fixture station is inside the city');
  assert.equal(card.querySelector('.points-caveat'), null, 'a city with its station inside carries no caveat');
});

// solvent-streets-ern4.28: the stations row says how many serve the city
// from outside it, and a city whose station list looks incomplete says so.
test('the stations row counts outside stations, and an incomplete list carries the caveat', async () => {
  const fireSummary = metaJSON.metrics.fire_response_s;
  assert.equal(fireSummary.point_label, 'Stations serving the city', 'Go names the points');
  assert.equal(fireSummary.inside_point_count, 1);
  assert.equal(fireSummary.outside_point_count, 0);
  const caveat = 'Station list from OpenStreetMap may be incomplete: no station inside the city serves it';
  const flagged = {
    ...metaJSON,
    metrics: {
      ...metaJSON.metrics,
      fire_response_s: { ...fireSummary, point_count: 3, inside_point_count: 0, outside_point_count: 3, points_incomplete: true, points_caveat: caveat },
    },
  };
  const h = load({ data: { ...data, 'meta.json': flagged } });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  const card = h.$$('#stats .stat-card').find((c) => c.querySelector('h3').textContent === 'Fire response');
  assert.ok(card, 'no metric card rendered');
  assert.match(card.textContent, /Stations serving the city\s*3 \(3 outside the city\)/);
  assert.equal(card.querySelector('.points-caveat')?.textContent, caveat);
});

test('an unreachable hex has no value on the map but counts against the within-threshold share', async () => {
  const { win, $$ } = await loadWithMetrics();
  const card = $$('#stats .stat-card').find((c) => c.querySelector('h3').textContent === 'Fire response');
  // The fixture's fire run reached two hexes (300 s within 5:20, 700 s not)
  // and recorded a third, on the network, as unreachable.
  assert.equal(metaJSON.metrics.fire_response_s.unreachable_count, 1);
  assert.match(card.textContent, /Unreachable hexes\s*1/);
  assert.match(card.textContent, /Within 5:20\s*33\.3%/, 'the share is of all three hexes on the network');
  assert.equal(fire.values[h2], undefined, 'an unreachable hex carries no time');
  win.selectHexMetric('fire_response_s');
  const fc = plain(win.buildBlendedHexFC());
  const byId = Object.fromEntries(fc.features.map((f) => [f.properties.hex_id, f.properties]));
  assert.equal(byId[h2].metric_v, undefined, 'so it is painted as a hex without a value');
});

test('selecting a metric paints discrete bands guarded for missing values, redraws the legend, and shows points', async () => {
  const { win, $, $$, get } = await loadWithMetrics();
  assert.equal(plain(win.hexFillColor())[2][0], 'interpolate', 'coverage paints the continuous ramp');

  win.selectHexMetric('fire_response_s');
  const paint = plain(win.hexFillColor());
  assert.equal(paint[0], 'case');
  assert.deepEqual(paint[1], ['has', 'metric_v'], 'a hex without a value must not hit the step expression');
  assert.equal(paint[2][0], 'step');
  assert.equal(paint[2].length, 3 + 2 * fire.scale.bands.length, 'one colour per band');

  const fc = plain(win.buildBlendedHexFC());
  const byId = Object.fromEntries(fc.features.map((f) => [f.properties.hex_id, f.properties]));
  assert.equal(byId[h0].metric_v, 300);
  assert.equal(byId[h0].metric_ref, 'Station 9');
  assert.equal(byId[h1].metric_v, 700);
  assert.equal(byId[h2].metric_v, undefined, 'a hex the metric does not cover carries no value');

  assert.match($('#legend .legend-row label').textContent, /Color by/);
  // One label per band, centred under its colour block: "≤ first", ranges,
  // "> last"; the exact strings follow the fixture's resolved bands.
  const labels = $$('#legend .legend-labels span').map((s) => s.textContent);
  assert.equal(labels.length, fire.scale.bands.length + 1, 'one label per band');
  assert.ok(labels[0].startsWith('≤ '), labels[0]);
  assert.ok(labels[labels.length - 1].startsWith('> '), labels[labels.length - 1]);
  assert.match(labels[1], /^\d+:\d\d–\d+:\d\d$/, 'inner labels name the band range');
  assert.equal($('#legend .legend-labels').style.gridTemplateColumns, 'repeat(' + labels.length + ', 1fr)');
  assert.ok($('#legend .legend-bar').classList.contains('discrete'));
  assert.ok(get('map').__sources.has('metric-points'), 'the points layer source must exist for a metric with points');
  assert.ok(get('map').__layers.has('metric-points'));

  win.selectHexMetric('coverage_share');
  assert.equal(plain(win.hexFillColor())[2][0], 'interpolate');
  assert.equal(get('map').__sources.has('metric-points'), false, 'switching back to coverage drops the points layer');
  assert.equal($$('#legend .legend-labels span')[0].textContent, '0%');
});

test('?metric= rides the URL only on the Map tab and restores on load', async () => {
  const { win, get } = await loadWithMetrics();
  win.selectHexMetric('fire_response_s');
  assert.equal(params(win).get('metric'), 'fire_response_s');
  win.selectTab('financials-tab');
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(params(win).get('metric'), null, 'a metric must not pin ?metric= on a tab where it does nothing');
  win.selectTab('map-tab');
  assert.equal(params(win).get('metric'), 'fire_response_s');
  get('Router.push()');

  const deep = await loadWithMetrics('https://example.test/index.html?metric=fire_response_s');
  assert.equal(deep.get('hexMetric'), 'fire_response_s');
  assert.equal(deep.$('#hex-metric').value, 'fire_response_s');
  assert.equal(plain(deep.win.hexFillColor())[0], 'case', 'a deep-linked metric must paint on first load');
});

test('a city without the requested metric falls back to coverage, leaves it off the selector, and drops ?metric=', async () => {
  const noMetrics = { ...fullData, 'meta.json': { ...metaJSON, metrics: undefined } };
  const h = load({ data: noMetrics, url: 'https://example.test/index.html?metric=fire_response_s' });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  assert.equal(h.get('hexMetric'), 'coverage_share');
  // The derived pavement colorings exist for every city, so the selector
  // shows whenever more than one is available, and never offers the absent
  // stored metric.
  const opts = h.$$('#hex-metric option').map((o) => o.value);
  assert.ok(!opts.includes('fire_response_s'), 'an absent stored metric must not be offered');
  assert.equal(h.$('#hex-metric').hidden, opts.length <= 1);
  assert.match(h.$('#legend .legend-row label').textContent, opts.length <= 1 ? /Coverage %/ : /Color by/);
  assert.equal(params(h.win).get('metric'), null, 'the address bar must stop claiming the metric');
});

test('fmtMetric rounds to whole seconds before splitting minutes', async () => {
  const { win } = await loadWithMetrics();
  assert.equal(win.fmtMetric(359.7, 'minutes'), '6:00');
  assert.equal(win.fmtMetric(300, 'minutes'), '5:00');
  assert.equal(win.fmtMetric(61.4, 'minutes'), '1:01');
  assert.equal(win.fmtMetric(1234.5, 'int'), '1,235');
});

test('the Color-by select is named by its visible label (solvent-streets-jjcz)', async () => {
  const { $ } = await loadWithMetrics();
  assert.equal($('#hex-metric').getAttribute('aria-label'), null, 'an aria-label overrides the visible "Color by" (WCAG 2.5.3)');
  assert.equal($('label[for="hex-metric"]').textContent, 'Color by');
});

test('an unknown hex-metrics.json version is refused with the error banner (solvent-streets-jjcz)', async () => {
  const saved = data['hex-metrics.json'];
  data['hex-metrics.json'] = { ...saved, v: 99 };
  try {
    const { $, $$ } = await loadWithMetrics();
    assert.equal($('#error-banner').hidden, false, 'the refusal was silent');
    assert.ok($$('#error-banner-list li').some((li) => li.textContent.includes('hex-metrics.json')));
    assert.ok(!$$('#hex-metric option').some((o) => o.value === 'fire_response_s'), 'the refused metrics were still listed');
  } finally {
    data['hex-metrics.json'] = saved;
  }
});

test('a prototype key in ?metric= does not resolve to a metric', async () => {
  const h = await loadWithMetrics('https://example.test/index.html?metric=constructor');
  assert.equal(h.get('hexMetric'), 'coverage_share');
  assert.equal(plain(h.win.hexFillColor())[2][0], 'interpolate');
});

test('Back across a city change restores the metric for the returned-to city', async () => {
  const h = await loadWithMetrics();
  h.win.selectHexMetric('fire_response_s');
  // The fetch stub reads `data` live: hide the metrics file while the second
  // city loads so it lacks the metric, then restore it before going Back.
  const saved = data['hex-metrics.json'];
  data['hex-metrics.json'] = { v: 2, metrics: {} };
  h.win.selectCity('beta-ca');
  await h.flush();
  assert.equal(h.get('hexMetric'), 'coverage_share', 'the city without the metric falls back');
  data['hex-metrics.json'] = saved;
  h.win.history.back();
  await h.flush();
  await h.flush();
  assert.equal(params(h.win).get('metric'), 'fire_response_s');
  assert.equal(h.get('hexMetric'), 'fire_response_s', 'the metric must be restored, not collapsed to coverage by the empty map mid-load');
});

// Goldens for the generic-coloring refactor (solvent-streets-59hj): the
// coverage ramp and the fire bands must paint and label exactly as they did
// when each had its own code path. Colours resolve to '' in jsdom (no
// stylesheet), so the expression SHAPE and stops are what is pinned.
//
// Coverage now paints metric_v (its registry value) under the generic
// has-value guard; every pavement hex carries metric_v === share, so the guard
// never fires and the ramp interpolates over 0/0.1/0.25/0.5/0.75/1.
test('golden: coverage paint ramp and legend labels', async () => {
  const { win, $$ } = await loadWithMetrics();
  const paint = plain(win.hexFillColor());
  assert.deepEqual(paint.slice(0, 2), ['case', ['has', 'metric_v']]);
  const ramp = paint[2];
  assert.deepEqual(ramp.slice(0, 3), ['interpolate', ['linear'], ['get', 'metric_v']]);
  assert.deepEqual(ramp.slice(3).filter((_, i) => i % 2 === 0), [0, 0.1, 0.25, 0.5, 0.75, 1]);
  for (const f of plain(win.buildBlendedHexFC()).features) {
    assert.equal(f.properties.metric_v, f.properties.share, 'every coverage hex paints its own share');
  }
  assert.deepEqual($$('#legend .legend-labels span').map((s) => s.textContent), ['0%', '25%', '50%', '75%', '100%']);
  // Each tick sits at its value's position, so 100% is labelled where the
  // bar ends (solvent-streets-ern4.36), not spread past it.
  assert.deepEqual($$('#legend .legend-labels span').map((s) => s.style.left), ['0%', '25%', '50%', '75%', '100%']);
});

// The coverage ramp's top two stops must read as different colours in both
// themes, or the bar looks as if it stops at 75% (solvent-streets-ern4.36).
test('coverage ramp: the 75% and 100% stops are distinct in both themes', () => {
  const theme = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '..', 'theme.html.tmpl'), 'utf8');
  const stops = (name) => [...theme.matchAll(new RegExp('--' + name + ':\\s*(#[0-9a-f]{6})', 'gi'))].map((m) => m[1]);
  const top = stops('legend-6');
  const below = stops('legend-5');
  assert.equal(top.length, 2, 'a light and a dark --legend-6');
  const lum = (hex) => {
    const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
      .map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
    return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
  };
  for (let i = 0; i < 2; i++) {
    const [a, b] = [lum(top[i]), lum(below[i])].sort((x, y) => y - x);
    assert.ok((a + 0.05) / (b + 0.05) >= 1.4, `--legend-5 ${below[i]} and --legend-6 ${top[i]} are too close`);
  }
});

test('golden: fire paint bands and legend labels', async () => {
  const { win, $$ } = await loadWithMetrics();
  win.selectHexMetric('fire_response_s');
  const paint = plain(win.hexFillColor());
  const step = paint[2];
  assert.deepEqual(step.slice(0, 2), ['step', ['get', 'metric_v']]);
  assert.deepEqual(step.slice(3).filter((_, i) => i % 2 === 0), fire.scale.bands);
  const labels = $$('#legend .legend-labels span').map((s) => s.textContent);
  assert.deepEqual(labels, goldenBandLabels(fire.scale.bands, (v) => win.fmtMetric(v, 'minutes')));
});

function goldenBandLabels(bands, fmt) {
  const out = ['≤ ' + fmt(bands[0])];
  for (let i = 1; i < bands.length; i++) out.push(fmt(bands[i - 1]) + '–' + fmt(bands[i]));
  out.push('> ' + fmt(bands[bands.length - 1]));
  return out;
}

// A count is shown in whole units: apportioning census blocks to hexes gives
// fractional housing units, and "Range 0 – 6.3" read as fractional homes
// (solvent-streets-ern4.36). Large counts still compact.
test('a count metric formats its range and legend edges as whole numbers', async () => {
  const { win } = await loadWithMetrics();
  assert.equal(win.fmtLegend(6.3, 'int', true), '6');
  assert.equal(win.fmtLegend(0.4, 'int', false), '0');
  assert.equal(win.fmtLegend(250, 'int', false), '250');
  assert.equal(win.fmtLegend(1234, 'int', false), '1.2K');
});
