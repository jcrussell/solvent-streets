// The snapshot picker on `pvmt serve`'s dashboard (index-live). Its default
// option is what the server pinned: the city's newest snapshot, or for a city
// with no snapshot its latest compute. "Latest compute (not snapshotted)"
// requests ?snapshot=latest, and meta.json's latest_compute shows the badge.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, readFixture } from './fixtures.mjs';

const snapshots = [
  { id: 5, name: 'v0.6.0', created_at: '2026-11-10T12:00:00Z', compute_epoch: 2, config_hash: 'abcdef0123456789', hex_edge_m: 100, parts: [] },
  { id: 3, name: 'v0.5.0', created_at: '2026-10-01T08:30:00Z', compute_epoch: 1, config_hash: '0123456789abcdef', hex_edge_m: 150, parts: [] },
];

const options = (sel) => Array.from(sel.options).map((o) => [o.value, o.textContent]);

test('a city with snapshots defaults to the newest, and offers the latest compute', async () => {
  const { $, flush } = load({ page: 'index-live', data: { ...fullData, snapshots } });
  await flush();
  const sel = $('#snapshot-picker');
  assert.deepEqual(options(sel), [
    ['', 'Newest (v0.6.0, 2026-11-10)'],
    ['latest', 'Latest compute (not snapshotted)'],
    ['v0.6.0', 'v0.6.0 2026-11-10 12:00'],
    ['v0.5.0', 'v0.5.0 2026-10-01 08:30'],
  ]);
  assert.equal(sel.value, '');
});

test('a server pinned to an older snapshot defaults to it', async () => {
  const pinned = [snapshots[0], { ...snapshots[1], served: true }];
  const { $, flush } = load({ page: 'index-live', data: { ...fullData, snapshots: pinned } });
  await flush();
  assert.deepEqual(options($('#snapshot-picker'))[0], ['', 'Served (v0.5.0, 2026-10-01)']);
});

test('choosing a snapshot requests it by name', async () => {
  const { win, $, flush, fetchURLs } = load({ page: 'index-live', data: { ...fullData, snapshots } });
  await flush();
  win.selectSnapshot('v0.5.0');
  await flush();
  assert.equal($('#snapshot-picker').value, 'v0.5.0');
  assert.ok(fetchURLs().some((u) => u.includes('meta.json?snapshot=v0.5.0')),
    'no meta.json request carried ?snapshot=v0.5.0: ' + fetchURLs().join(', '));
});

test('choosing the latest compute requests ?snapshot=latest', async () => {
  const { win, $, flush, fetchURLs } = load({ page: 'index-live', data: { ...fullData, snapshots } });
  await flush();
  win.selectSnapshot('latest');
  await flush();
  assert.equal($('#snapshot-picker').value, 'latest');
  assert.ok(fetchURLs().some((u) => u.includes('meta.json?snapshot=latest')),
    'no meta.json request carried ?snapshot=latest: ' + fetchURLs().join(', '));
});

test('a city with no snapshot defaults to its latest compute', async () => {
  const { $, flush } = load({ page: 'index-live', data: { ...fullData, snapshots: [] } });
  await flush();
  assert.deepEqual(options($('#snapshot-picker')), [['', 'Latest compute (not snapshotted)']]);
});

test('meta.json latest_compute shows the badge; a snapshot hides it', async () => {
  const latest = { ...readFixture('meta.json'), latest_compute: true };
  const shown = load({ page: 'index-live', data: { ...fullData, 'meta.json': latest, snapshots: [] } });
  await shown.flush();
  shown.fireMap('load'); // the page loads the city's data once the map is ready
  await shown.flush();
  assert.equal(shown.$('#latest-compute-badge').hidden, false);

  const snap = { ...readFixture('meta.json'), snapshot: 'v0.6.0' };
  const hidden = load({ page: 'index-live', data: { ...fullData, 'meta.json': snap, snapshots } });
  await hidden.flush();
  hidden.fireMap('load');
  await hidden.flush();
  hidden.win.selectSnapshot('latest'); // still a snapshot's meta: stays hidden
  await hidden.flush();
  assert.equal(hidden.$('#latest-compute-badge').hidden, true);
});

test('the static export has neither picker nor badge', () => {
  const { $ } = load({ data: fullData });
  assert.equal($('#snapshot-picker'), null);
  assert.equal($('#latest-compute-badge'), null);
});

// solvent-streets-a1ud: going back across a snapshot change reloads the city's
// data, which empties the metric list; the entry's Color-by must survive that
// reload rather than collapse to coverage while the URL keeps ?metric=.
test('back across a snapshot change keeps the entry\'s Color-by', async () => {
  const population = {
    label: 'Population', unit: '', format: 'int', source: 'test',
    scale: { kind: 'sequential', transform: 'linear', bands: [10, 100] },
    values: { 'hex:0:0': 50 },
  };
  const data = { ...fullData, snapshots, 'hex-metrics.json': { v: 2, metrics: { population } } };
  const { win, get, flush, fireMap } = load({ page: 'index-live', data, url: 'https://example.test/index-live.html?tab=map-tab' });
  await flush();
  fireMap('load');
  await flush();
  win.selectHexMetric('population');
  assert.equal(get('hexMetric'), 'population', 'the fixture does not offer the stored metric');
  win.selectSnapshot('v0.5.0');
  await flush();
  win.selectHexMetric('coverage_share');
  await flush();
  // Two entries back: the snapshot AND the metric differ from the one in view.
  win.history.go(-2);
  await flush();
  assert.equal(win.document.getElementById('snapshot-picker').value, '');
  assert.equal(get('hexMetric'), 'population', 'the Color-by fell back to coverage');
  assert.equal(new URLSearchParams(win.location.search).get('metric'), 'population');
});

test('a cold ?snapshot= deep link selects that snapshot and keeps it in the URL (solvent-streets-6wyp)', async () => {
  const { win, $, flush, fetchURLs } = load({ page: 'index-live', data: { ...fullData, snapshots },
    url: 'https://example.test/index-live.html?snapshot=v0.5.0' });
  await flush(12);
  assert.equal($('#snapshot-picker').value, 'v0.5.0');
  assert.equal(new URLSearchParams(win.location.search).get('snapshot'), 'v0.5.0');
  assert.ok(fetchURLs().some((u) => u.includes('snapshot=v0.5.0')), 'no data request named the snapshot');
});

test('a late snapshot list for the city just left does not replace the new city\'s (solvent-streets-9cnm)', async () => {
  const alphaSnaps = [{ id: 9, name: 'alpha-only', created_at: '2026-11-01T00:00:00Z', parts: [] }];
  const betaSnaps = [{ id: 7, name: 'beta-only', created_at: '2026-11-02T00:00:00Z', parts: [] }];
  const h = load({ page: 'index-live', data: { 'alpha-ca/snapshots': alphaSnaps, 'beta-ca/snapshots': betaSnaps, ...fullData },
    url: 'https://example.test/index-live.html?city=alpha-ca', hold: ['alpha-ca/snapshots'] });
  await h.flush();
  h.win.selectCity('beta-ca'); await h.flush(8);
  h.release('alpha-ca/snapshots'); await h.flush(8);
  const opts = Array.from(h.$('#snapshot-picker').options).map((o) => o.value);
  assert.ok(opts.includes('beta-only') && !opts.includes('alpha-only'), 'picker holds ' + opts);
});

test('back across a city switch restores the entry\'s snapshot (solvent-streets-6wyp)', async () => {
  const { win, $, flush } = load({ page: 'index-live', data: { ...fullData, snapshots },
    url: 'https://example.test/index-live.html?city=alpha-ca' });
  await flush(12);
  win.selectSnapshot('v0.5.0'); await flush(8);
  win.selectCity('beta-ca'); await flush(8);
  win.history.back(); await flush(12);
  assert.equal(new URLSearchParams(win.location.search).get('city'), 'alpha-ca');
  assert.equal($('#snapshot-picker').value, 'v0.5.0', 'the picker lost the entry\'s snapshot');
});
