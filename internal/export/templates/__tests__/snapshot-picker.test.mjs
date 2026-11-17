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

  const snap = { ...readFixture('meta.json'), snapshot_id: 5 };
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
