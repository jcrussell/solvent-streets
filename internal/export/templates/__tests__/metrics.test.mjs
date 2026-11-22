// "Color by" per-hex metrics (hex-metrics.json): the
// selector lists the city's metrics, choosing one swaps the paint expression
// to discrete bands guarded against hexes without a value, redraws the legend
// from the metric's scale, shows the points of service, rides the URL only on
// the Map tab, and falls back to coverage when the city lacks the metric.
import { test } from 'node:test';
import assert from 'node:assert/strict';
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

// fireRows reads the fire metric's stats card as its [label, value] rows, in
// order, and its caveat. A metric card carries no key: it is the one titled
// with the label its meta.json summary gives it.
/** @param {any} h @param {any} [meta] */
function fireRows(h, meta = metaJSON) {
  const card = h.$$('#stats .metric-card').find((c) => c.querySelector('h3').textContent === meta.metrics.fire_response_s.label);
  assert.ok(card, 'no metric card rendered from meta.metrics');
  return {
    rows: Array.from(card.querySelectorAll('.stat-row:not(.stat-note)'), (r) => ({
      label: r.querySelector('.stat-label').textContent,
      value: r.querySelector('.stat-value').textContent,
    })),
    caveat: card.querySelector('.points-caveat'),
  };
}

async function loadWithMetrics(url) {
  const h = load({ data, url });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  return h;
}

test('the Color-by selector lists the available derived metrics plus each stored metric, and the stats card summarises it', async () => {
  const h = await loadWithMetrics();
  const { $, $$, win, get } = h;
  const opts = $$('#hex-metric option').map((o) => o.value);
  // Grouped in display order: pavement, then services (spaces and year-1
  // need are absent: the fixture has no parking or cost summary).
  assert.deepEqual(opts, ['maintained_share', 'maintained_area', 'fire_response_s']);
  // One heading per registry group the city has a metric in, in the page's
  // group order, each option under its own metric's group.
  const headings = get('METRIC_GROUPS');
  assert.deepEqual($$('#hex-metric optgroup').map((g) => g.label), [headings.pavement, headings.services]);
  assert.ok(headings.pavement && headings.services && headings.pavement !== headings.services);
  for (const o of $$('#hex-metric option')) {
    assert.equal(o.parentElement.label, headings[win.lookupMetric(o.value).group], o.value);
    assert.equal(o.textContent, win.lookupMetric(o.value).label, o.value);
  }
  assert.equal($('#map-color-by').hidden, false);
  // The card's rows, in order: the share within the threshold, the
  // unreachable hexes, the median, the range, the points of service. (A time
  // has no total.)
  const s = metaJSON.metrics.fire_response_s;
  const { rows, caveat } = fireRows(h);
  assert.equal(rows.length, 5);
  const [, , median, range, points] = rows;
  assert.equal(median.value, '8:20', 'median must render in m:ss');
  assert.equal(median.value, win.fmtMetric(s.median, s.format));
  assert.equal(range.value, win.fmtLegend(s.min, s.format, false) + ' – ' + win.fmtLegend(s.max, s.format, true));
  // The points row is named as the summary names its points, and counts them;
  // the fixture station is inside the city, so nothing is set apart.
  assert.equal(points.label, s.point_label);
  assert.equal(points.value, String(s.point_count));
  assert.equal(caveat, null, 'a city with its station inside carries no caveat');
});

// The stations row says how many serve the city
// from outside it, and a city whose station list looks incomplete says so.
test('the stations row counts outside stations, and an incomplete list carries the caveat', async () => {
  const fireSummary = metaJSON.metrics.fire_response_s;
  assert.ok(fireSummary.point_label, 'Go names the points');
  assert.equal(fireSummary.outside_point_count, 0);
  const caveat = 'Station list from OpenStreetMap may be incomplete: no station inside the city serves it';
  const flagged = {
    ...metaJSON,
    metrics: {
      ...metaJSON.metrics,
      fire_response_s: { ...fireSummary, point_count: 3, outside_point_count: 3, points_caveat: caveat },
    },
  };
  const h = load({ data: { ...data, 'meta.json': flagged } });
  await h.flush();
  await h.win.loadCityData();
  await h.flush();
  const { rows, caveat: shown } = fireRows(h, flagged);
  const points = rows.at(-1);
  assert.equal(points.label, fireSummary.point_label);
  // Three stations, and the three of them outside: both counts, told apart.
  assert.match(points.value, /^3\D+3\D*$/);
  assert.equal(shown?.textContent, caveat);
});

test('an unreachable hex has no value on the map but counts against the within-threshold share', async () => {
  const h = await loadWithMetrics();
  const { win } = h;
  // The fixture's fire run reached two hexes (300 s within 5:20, 700 s not)
  // and recorded a third, on the network, as unreachable.
  assert.equal(metaJSON.metrics.fire_response_s.unreachable_count, 1);
  // The card leads with the share within the threshold (the row names the
  // threshold), then the unreachable count.
  const [within, unreachable] = fireRows(h).rows;
  assert.equal(win.fmtMetric(metaJSON.metrics.fire_response_s.threshold_s, 'minutes'), '5:20');
  assert.ok(within.label.includes('5:20'), within.label);
  assert.equal(within.value, '33.3%', 'the share is of all three hexes on the network');
  assert.equal(unreachable.value, '1');
  assert.equal(fire.values[h2], undefined, 'an unreachable hex carries no time');
  win.selectHexMetric('fire_response_s');
  const fc = plain(win.buildBlendedHexFC());
  const byId = Object.fromEntries(fc.features.map((f) => [f.properties.hex_id, f.properties]));
  assert.equal(byId[h2].metric_v, undefined, 'so it is painted as a hex without a value');
});

test('selecting a metric paints discrete bands guarded for missing values, redraws the legend, and shows points', async () => {
  const { win, $, $$, get } = await loadWithMetrics();
  assert.equal(plain(win.hexFillColor())[2][0], 'step', '% Paved paints discrete bands');

  win.selectHexMetric('fire_response_s');
  const paint = plain(win.hexFillColor());
  assert.equal(paint[0], 'case');
  assert.deepEqual(paint[1], ['has', 'metric_v'], 'a hex without a value must not hit the step expression');
  assert.equal(paint[2][0], 'step');
  assert.equal(paint[2].length, 3 + 2 * fire.scale.bands.length, 'one colour per band');

  const fc = plain(win.buildBlendedHexFC());
  const byId = Object.fromEntries(fc.features.map((f) => [f.properties.hex_id, f.properties]));
  assert.equal(byId[h0].metric_v, 300);
  assert.equal(byId[h0].metric_ref, fire.points[fire.refs[h0]].name);
  assert.ok(byId[h0].metric_ref, 'fixture: the station has a name');
  assert.equal(byId[h1].metric_v, 700);
  assert.equal(byId[h2].metric_v, undefined, 'a hex the metric does not cover carries no value');

  assert.equal($('#legend-title').textContent, $('#hex-metric').selectedOptions[0].textContent,
    'the legend is titled by the metric it keys');
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

  win.selectHexMetric('maintained_share');
  assert.equal(plain(win.hexFillColor())[2][0], 'step');
  assert.equal(get('map').__sources.has('metric-points'), false, 'switching back to % Paved drops the points layer');
  assert.equal($$('#legend .legend-labels span')[0].textContent, '≤ 5%');
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
  assert.equal(h.get('hexMetric'), 'maintained_share');
  // The derived pavement colorings exist for every city, so the selector
  // shows whenever more than one is available, and never offers the absent
  // stored metric.
  const opts = h.$$('#hex-metric option').map((o) => o.value);
  assert.ok(!opts.includes('fire_response_s'), 'an absent stored metric must not be offered');
  assert.equal(h.$('#map-color-by').hidden, opts.length <= 1);
  assert.equal(h.$('#legend-title').textContent, h.win.lookupMetric('maintained_share').label, 'the fallback metric titles the legend');
  assert.ok(h.$('#legend-title').textContent.trim().length > 0);
  assert.equal(params(h.win).get('metric'), null, 'the address bar must stop claiming the metric');
});

test('fmtMetric rounds to whole seconds before splitting minutes', async () => {
  const { win } = await loadWithMetrics();
  assert.equal(win.fmtMetric(359.7, 'minutes'), '6:00');
  assert.equal(win.fmtMetric(300, 'minutes'), '5:00');
  assert.equal(win.fmtMetric(61.4, 'minutes'), '1:01');
  assert.equal(win.fmtMetric(1234.5, 'int'), '1,235');
});

test('the Color-by select is named by its visible label', async () => {
  const { $ } = await loadWithMetrics();
  assert.equal($('#hex-metric').getAttribute('aria-label'), null, 'an aria-label overrides the visible "Color by" (WCAG 2.5.3)');
  assert.ok($('label[for="hex-metric"]').textContent.trim().length > 0, 'the select has no visible label');
});

test('an unknown hex-metrics.json version is refused with the error banner', async () => {
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
  assert.equal(h.get('hexMetric'), 'maintained_share');
  assert.equal(plain(h.win.hexFillColor())[2][0], 'step');
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
  assert.equal(h.get('hexMetric'), 'maintained_share', 'the city without the metric falls back');
  data['hex-metrics.json'] = saved;
  h.win.history.back();
  await h.flush();
  await h.flush();
  assert.equal(params(h.win).get('metric'), 'fire_response_s');
  assert.equal(h.get('hexMetric'), 'fire_response_s', 'the metric must be restored, not collapsed to coverage by the empty map mid-load');
});

// Goldens: % Paved and fire paint and label through the same band path.
// Colours resolve to '' in jsdom (no stylesheet), so the expression SHAPE
// and stops are what is pinned. Every pavement hex carries metric_v ===
// share, so the has-value guard never fires for % Paved.
test('golden: % Paved paint bands, legend labels and definition', async () => {
  const { win, $, $$ } = await loadWithMetrics();
  const paint = plain(win.hexFillColor());
  assert.deepEqual(paint.slice(0, 2), ['case', ['has', 'metric_v']]);
  const step = paint[2];
  assert.deepEqual(step.slice(0, 2), ['step', ['get', 'metric_v']]);
  assert.deepEqual(step.slice(3).filter((_, i) => i % 2 === 0), [0.05, 0.1, 0.2, 0.3, 0.5]);
  for (const f of plain(win.buildBlendedHexFC()).features) {
    assert.equal(f.properties.metric_v, f.properties.share, 'every pavement hex paints its own share');
  }
  assert.deepEqual($$('#legend .legend-labels span').map((s) => s.textContent), ['≤ 5%', '5%–10%', '10%–20%', '20%–30%', '30%–50%', '> 50%']);
  assert.equal($('#legend-title').textContent, win.lookupMetric('maintained_share').label);
  // The legend defines the coloring in the registry's words (its source),
  // and the line follows the metric chosen.
  const source = (key) => win.lookupMetric(key).source;
  assert.ok(source('maintained_share'), 'fixture: the registry gives the coloring no definition');
  assert.equal($('#legend-def').textContent, source('maintained_share'), 'the legend defines the coloring');
  assert.equal($('#legend-def').hidden, false);
  win.selectHexMetric('fire_response_s');
  assert.notEqual(source('fire_response_s'), source('maintained_share'));
  assert.equal($('#legend-def').textContent, source('fire_response_s'));
  // A metric with no definition hides the line.
  win.lookupMetric('fire_response_s').source = '';
  win.renderLegend();
  assert.equal($('#legend-def').hidden, true);
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
// fractional housing units, and "Range 0 – 6.3" read as fractional homes.
// Large counts still compact.
test('a count metric formats its range and legend edges as whole numbers', async () => {
  const { win } = await loadWithMetrics();
  assert.equal(win.fmtLegend(6.3, 'int', true), '6');
  assert.equal(win.fmtLegend(0.4, 'int', false), '0');
  assert.equal(win.fmtLegend(250, 'int', false), '250');
  assert.equal(win.fmtLegend(1234, 'int', false), '1.2K');
});
