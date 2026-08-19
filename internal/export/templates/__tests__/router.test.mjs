// URL state: ?tab= / ?city= / ?scope= / ?tag= must round-trip and survive
// back/forward. Router was previously verified only by a one-off Node harness
// during the consolidate-tagged-site audit (solvent-streets-8je6) — nothing in
// the repo ran it.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData } from './fixtures.mjs';

const params = (win) => new URLSearchParams(win.location.search);

test('selecting a city writes it to the URL', async () => {
  const { win, flush } = load({ data: fullData });
  await flush();

  win.selectCity('beta-ca');
  await flush();

  assert.equal(params(win).get('city'), 'beta-ca');
});

test('a deep-linked city is restored on load', async () => {
  const { $, flush } = load({
    data: fullData,
    url: 'https://example.test/index.html?city=gamma-ca',
  });
  await flush();

  assert.equal($('#city-select').value, 'gamma-ca',
    'the city selector did not adopt ?city=');
});

test('a deep-linked tab is restored on load', async () => {
  const { $, flush } = load({
    data: fullData,
    url: 'https://example.test/index.html?tab=financials-tab',
  });
  await flush();

  const active = $('.tab-btn.active');
  assert.ok(active, 'no active tab');
  assert.equal(active.dataset.tab, 'financials-tab',
    'the active tab did not adopt ?tab=');
});

test('tag scope round-trips through the URL', async () => {
  const { win, $, flush } = load({ data: fullData });
  await flush();

  // #tag-scope only serializes while it is visible — it is shown on the
  // Compare/Aggregate tabs, and pinning ?tag= elsewhere would set it where it
  // does nothing.
  win.selectTab('aggregate-tab');
  await flush();
  const tagEl = $('#tag-scope');
  assert.equal(tagEl.hidden, false, '#tag-scope should be visible on the Aggregate tab');

  tagEl.value = 'Metro';
  win.onTagScopeChange('Metro');
  await flush();

  assert.equal(params(win).get('tag'), 'Metro', 'tag did not reach the URL');
});

test('a tag is not serialized on a tab where it does nothing', async () => {
  const { win, $, get, flush } = load({ data: fullData });
  await flush();

  win.selectTab('map-tab');
  await flush();
  assert.equal($('#tag-scope').hidden, true, '#tag-scope should be hidden on the Map tab');

  get('Router.push()');
  assert.equal(params(win).get('tag'), null,
    'a hidden tag selector must not pin ?tag=');
});

test('back/forward restores prior state', async () => {
  const { win, $, flush } = load({ data: fullData });
  await flush();

  win.selectCity('beta-ca');
  await flush();
  win.selectCity('gamma-ca');
  await flush();
  assert.equal(params(win).get('city'), 'gamma-ca');

  win.history.back();
  await flush();
  // jsdom fires popstate asynchronously; app.js's handler re-reads the URL.
  assert.equal($('#city-select').value, 'beta-ca',
    'going back did not restore the previous city');
});

test('scope selection round-trips only while the scope row is visible', async () => {
  const { win, doc, get, flush } = load({ data: fullData });
  await flush();

  const row = doc.getElementById('gear-scope-row');
  assert.ok(row, '#gear-scope-row missing from the template');

  row.hidden = true;
  get('Router.push()');
  assert.equal(params(win).get('scope'), null,
    'a hidden scope row must not pin ?scope=');

  row.hidden = false;
  win.selectScope('bbox', { push: true });
  await flush();
  assert.equal(params(win).get('scope'), 'bbox');
});
