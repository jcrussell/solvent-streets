// URL state: ?tab= / ?city= / ?scope= / ?tag= (repeated, dim:value) must round-trip and survive
// back/forward.
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
    url: 'https://example.test/index.html?city=gamma-or',
  });
  await flush();

  assert.equal($('#city-select').value, 'gamma-or',
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

// Objects built inside the jsdom window belong to that realm; compare by
// structure only.
const plain = (v) => JSON.parse(JSON.stringify(v));
const filter = ($, dim) => $('select.tag-filter[data-dim="' + dim + '"]');
// Distinct slugs in selector order (a multi-tag city appears under each of its groups).
const cityOptions = ($) => Array.from(new Set(Array.from($('#city-select').querySelectorAll('option')).map((o) => o.value)));

test('tag filters round-trip through the URL on every tab', async () => {
  const { win, $, flush } = load({ data: fullData });
  await flush();

  // The filters live in the gear menu and apply everywhere, so they
  // serialize on the Map tab too. Both dimensions are present: "group" from
  // the explicit tags, "state" derived from the ", XX" name suffix.
  win.selectTab('map-tab');
  await flush();
  assert.equal($('#tag-scope'), null, 'tag filtering lives in the per-dimension filters, not a #tag-scope selector');
  assert.equal(filter($, 'group').hidden, false);
  assert.equal(filter($, 'state').hidden, false);

  filter($, 'group').value = 'Metro';
  win.onTagFilterChange();
  await flush();
  assert.deepEqual(params(win).getAll('tag'), ['group:Metro'], 'the filter did not reach the URL in dim:value form');

  filter($, 'state').value = 'state:CA';
  win.onTagFilterChange();
  await flush();
  assert.deepEqual(params(win).getAll('tag'), ['group:Metro', 'state:CA']);
  assert.deepEqual(plain(win.selectedTags), { group: 'Metro', state: 'state:CA' });
});

test('a ?tag= in the default dimension narrows the selector', async () => {
  const { win, $, flush } = load({
    data: fullData,
    url: 'https://example.test/index.html?tag=group:Metro',
  });
  await flush();

  assert.deepEqual(plain(win.selectedTags), { group: 'Metro' });
  assert.equal(filter($, 'group').value, 'Metro');
  assert.deepEqual(cityOptions($), ['alpha-ca', 'beta-ca'], 'Gamma (no Metro tag) must be filtered out');
  assert.ok($('#city-select option[value="alpha-ca"]').dataset.bbox, 'rebuilt options must keep data-bbox for selectCity');
});

test('without ?city=, the data in view follows the selector default, not CITIES[0]', async () => {
  // A ?tag= filter that excludes CITIES[0] (Alpha, flat-alphabetical first)
  // leaves the dropdown defaulting to Gamma. DATA_PREFIX was seeded from
  // CITIES[0], so without the boot-time alignment the page would load Alpha's
  // data under a dropdown naming Gamma (solvent-streets-yvlv.24).
  const { win, $, flush } = load({
    data: fullData,
    url: 'https://example.test/index.html?tag=state:OR',
  });
  await flush();

  assert.deepEqual(cityOptions($), ['gamma-or'], 'the filter should leave only Gamma');
  assert.equal($('#city-select').value, 'gamma-or');
  assert.equal(win.DATA_PREFIX, 'cities/gamma-or/', 'the data prefix must follow the shown city');
});

test('filtering out the city in view switches to the first visible city', async () => {
  const { win, $, flush } = load({ data: fullData });
  await flush();

  win.selectCity('gamma-or');
  await flush();
  assert.equal(params(win).get('city'), 'gamma-or');

  filter($, 'state').value = 'state:CA';
  win.onTagFilterChange();
  await flush();
  assert.equal($('#city-select').value, 'alpha-ca');
  assert.equal(params(win).get('city'), 'alpha-ca', 'the URL must follow the switched city');
  assert.equal(win.DATA_PREFIX, 'cities/alpha-ca/', 'the data prefix must follow the switched city');
});

test('an empty match keeps the current city and renders a disabled placeholder', async () => {
  const { win, $, flush } = load({ data: fullData });
  await flush();
  win.selectCity('gamma-or');
  await flush();

  filter($, 'group').value = 'Top 50';
  filter($, 'state').value = 'state:OR';
  win.onTagFilterChange();
  await flush();
  const opts = Array.from($('#city-select').options);
  assert.equal(opts.length, 1);
  assert.equal(opts[0].disabled, true);
  assert.equal(win.DATA_PREFIX, 'cities/gamma-or/', 'no match must not switch the data in view');
});

test('a deep-linked city outside the filters wins and the filters are dropped', async () => {
  const { win, $, flush } = load({
    data: fullData,
    url: 'https://example.test/index.html?city=gamma-or&tag=state:CA',
  });
  await flush();

  assert.equal($('#city-select').value, 'gamma-or');
  assert.equal(win.DATA_PREFIX, 'cities/gamma-or/');
  assert.equal(filter($, 'state').value, '', 'the conflicting filter must be reset');
  assert.deepEqual(params(win).getAll('tag'), [], 'replaceState must record the dropped filter');
});

test('scopeCityData ANDs the selected dimensions', async () => {
  const { win, flush } = load({ data: fullData });
  await flush();

  const rows = [{ slug: 'alpha-ca' }, { slug: 'beta-ca' }, { slug: 'gamma-or' }];
  win.applyTagFilters({ group: 'Metro', state: 'state:CA' });
  assert.deepEqual(plain(win.scopeCityData(rows)).map((r) => r.slug), ['alpha-ca', 'beta-ca']);
  win.applyTagFilters({ group: 'Top 50', state: 'state:CA' });
  assert.deepEqual(plain(win.scopeCityData(rows)).map((r) => r.slug), ['beta-ca']);
  win.applyTagFilters({});
  assert.equal(win.scopeCityData(rows).length, 3);
});

test('the Play link carries the city and the filters', async () => {
  const { win, $, flush } = load({ data: fullData });
  await flush();

  filter($, 'group').value = 'Metro';
  win.onTagFilterChange();
  await flush();
  const href = $('a.nav-link').getAttribute('href');
  const q = new URLSearchParams(href.slice(href.indexOf('?') + 1));
  assert.equal(q.get('city'), $('#city-select').value);
  assert.deepEqual(q.getAll('tag'), ['group:Metro']);
});

test('back/forward restores the filter set', async () => {
  const { win, $, flush } = load({ data: fullData });
  await flush();

  filter($, 'group').value = 'Metro';
  win.onTagFilterChange();
  await flush();
  filter($, 'group').value = '';
  win.onTagFilterChange();
  await flush();
  assert.deepEqual(params(win).getAll('tag'), []);

  win.history.back();
  await flush();
  assert.deepEqual(params(win).getAll('tag'), ['group:Metro']);
  assert.deepEqual(plain(win.selectedTags), { group: 'Metro' }, 'going back did not restore the filter');
  assert.equal(filter($, 'group').value, 'Metro', 'the select did not follow the restored filter');
  assert.deepEqual(cityOptions($), ['alpha-ca', 'beta-ca'], 'the selector was not rebuilt for the restored filter');
});

test('back/forward restores prior state', async () => {
  const { win, $, flush } = load({ data: fullData });
  await flush();

  win.selectCity('beta-ca');
  await flush();
  win.selectCity('gamma-or');
  await flush();
  assert.equal(params(win).get('city'), 'gamma-or');

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
  win.selectScope('all', { push: true });
  await flush();
  assert.equal(params(win).get('scope'), 'all');
});

test('back to a different tab of the same city does not reload the city', async () => {
  // Deep-link a city so every history entry carries ?city=: the bug was gating
  // the reload on the entry having a city at all (yvlv.21), which a same-city
  // back navigation always does.
  const { win, fetches, fireMap, flush } = load({
    data: fullData,
    url: 'https://example.test/index.html?city=beta-ca',
  });
  await flush();
  fireMap('load');
  await flush();
  const cityLoads = () => fetches().filter((f) => f.endsWith('/meta.json')).length;
  const before = cityLoads();
  assert.ok(before > 0, 'the deep-linked city never loaded');
  const startTab = params(win).get('tab');

  win.selectTab('financials-tab');
  await flush();
  assert.notEqual(params(win).get('tab'), startTab, 'selectTab did not push a new tab');
  win.history.back();
  await flush();

  assert.equal(params(win).get('tab'), startTab, 'going back did not return to the earlier tab');
  assert.equal(cityLoads(), before, 'a tab-only back navigation reloaded the city');
});
