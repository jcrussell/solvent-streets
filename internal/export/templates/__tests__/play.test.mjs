// Play page (game.js) selector boot: the same ?tag= filters as the index page
// narrow its city selector, a deep-linked ?city= wins over a conflicting
// filter, and a filter change that keeps the current city only rewrites the
// URL (a change that switches city navigates, which jsdom cannot follow, so it
// is not exercised here — the shared rebuild logic is covered in
// selectors.test.mjs and router.test.mjs).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';

const params = (win) => new URLSearchParams(win.location.search);
const filter = ($, dim) => $('select.tag-filter[data-dim="' + dim + '"]');
// Distinct slugs in selector order (a multi-tag city appears under each of its groups).
const cityOptions = ($) => Array.from(new Set(Array.from($('#city-select').querySelectorAll('option')).map((o) => o.value)));

test('play: ?tag= narrows the city selector and the deep-linked city is kept', async () => {
  const { get, $, flush } = load({
    page: 'play',
    data: {},
    url: 'https://example.test/play.html?city=beta-ca&tag=state:CA',
  });
  await flush();

  assert.deepEqual(cityOptions($), ['alpha-ca', 'beta-ca'], 'Gamma, OR must be filtered out');
  assert.equal($('#city-select').value, 'beta-ca');
  // game.js declares DATA_PREFIX with `let`, so read it in script scope.
  assert.equal(get('DATA_PREFIX'), 'cities/beta-ca/');
  assert.equal(filter($, 'state').value, 'state:CA');
});

test('play: a deep-linked city outside the filters wins', async () => {
  const { get, $, flush } = load({
    page: 'play',
    data: {},
    url: 'https://example.test/play.html?city=gamma-or&tag=state:CA',
  });
  await flush();

  assert.equal($('#city-select').value, 'gamma-or');
  assert.equal(get('DATA_PREFIX'), 'cities/gamma-or/');
  assert.equal(filter($, 'state').value, '', 'the conflicting filter is dropped');
  assert.equal(cityOptions($).length, 3);
});

test('play: a filter change that keeps the current city only rewrites the URL', async () => {
  const { win, $, flush } = load({
    page: 'play',
    data: {},
    url: 'https://example.test/play.html?city=beta-ca',
  });
  await flush();

  filter($, 'group').value = 'Top 50';
  filter($, 'group').dispatchEvent(new win.Event('change'));
  await flush();

  assert.equal($('#city-select').value, 'beta-ca', 'Beta carries Top 50 and must stay selected');
  assert.deepEqual(cityOptions($), ['beta-ca']);
  assert.deepEqual(params(win).getAll('tag'), ['group:Top 50']);
  assert.equal(params(win).get('city'), 'beta-ca');
});

test('play (live server): the served city stays in charge when filters keep it', async () => {
  const gone = [];
  const { $, get } = load({ page: 'play-live', data: {}, navigate: (u) => gone.push(String(u)),
    url: 'https://example.test/play?city=gamma-or&tag=state:OR' });
  assert.equal($('#city-select').value, 'gamma-or');
  assert.equal(get('DATA_PREFIX'), 'cities/gamma-or/', 'the injected prefix must not be clobbered');
  assert.deepEqual(gone, [], 'no re-render needed');
});

test('play (live server): a filter that excludes the served city re-renders for the shown one', async () => {
  const gone = [];
  load({ page: 'play-live', data: {}, navigate: (u) => gone.push(String(u)),
    url: 'https://example.test/play?tag=state:CA' });
  assert.equal(gone.length, 1, 'must navigate exactly once');
  const q = new URL(gone[0]).searchParams;
  assert.equal(q.get('city'), 'alpha-ca', 'the first visible CA city');
  assert.deepEqual(q.getAll('tag'), ['state:CA'], 'the filters ride along');
});

test('play (live server): an unknown ?city= with no filters keeps the served city', async () => {
  const gone = [];
  const { $, get } = load({ page: 'play-live', data: {}, navigate: (u) => gone.push(String(u)),
    url: 'https://example.test/play?city=zzz' });
  assert.equal($('#city-select').value, 'gamma-or');
  assert.equal(get('DATA_PREFIX'), 'cities/gamma-or/');
  assert.deepEqual(gone, []);
});
