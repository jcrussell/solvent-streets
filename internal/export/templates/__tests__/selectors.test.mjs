// Unit tests for the shared selectors.js namespace: tag normalization, ?tag=
// parsing, AND-matching across dimensions, and the client-side #city-select
// rebuild. These need no rendered fixture — selectors.js is pure DOM logic —
// so they build their own jsdom window.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';

const HERE = dirname(fileURLToPath(import.meta.url));
const SELECTORS_JS = readFileSync(join(HERE, '..', 'selectors.js'), 'utf8');

// Objects and arrays built inside the jsdom window belong to that realm, so
// strict deep-equality (which compares prototypes) rejects them; round-trip
// through JSON to compare structure only.
const plain = (v) => JSON.parse(JSON.stringify(v));

function loadSelectors(html = '<body></body>') {
  const dom = new JSDOM(`<!doctype html><html>${html}</html>`, { runScripts: 'outside-only' });
  dom.window.eval(SELECTORS_JS);
  return { win: dom.window, doc: dom.window.document, S: dom.window.PvmtSelectors };
}

const dims = [
  { name: 'group', label: 'Group', values: [{ tag: 'Bay Area', label: 'Bay Area' }, { tag: 'Top 50', label: 'Top 50' }] },
  { name: 'state', label: 'State', values: [{ tag: 'state:CA', label: 'CA' }, { tag: 'state:TX', label: 'TX' }] },
];
const cities = [
  { slug: 'san-jose-ca', name: 'San Jose, CA', bbox: [1, 2, 3, 4], center: [1.5, 2.5], tags: ['Bay Area', 'Top 50', 'state:CA'] },
  { slug: 'oakland-ca', name: 'Oakland, CA', tags: ['Bay Area', 'state:CA'] },
  { slug: 'austin-tx', name: 'Austin, TX', tags: ['Top 50', 'state:TX'] },
  { slug: 'reno-nv', name: 'Reno, NV' },
];

test('normalizeTag prefixes bare labels with the default dimension', () => {
  const { S } = loadSelectors();
  assert.equal(S.normalizeTag('Bay Area'), 'group:Bay Area');
  assert.equal(S.normalizeTag('state:CA'), 'state:CA');
  assert.equal(S.dimensionOf('Bay Area'), 'group');
  assert.equal(S.dimensionOf('state:CA'), 'state');
});

test('parseTags accepts normalized and legacy bare ?tag= values and ignores unknown ones', () => {
  const { S } = loadSelectors();
  const p = new URLSearchParams('tag=group:Bay%20Area&tag=state:TX&tag=state:ZZ&tag=Nope');
  assert.deepEqual(plain(S.parseTags(p, dims)), { group: 'Bay Area', state: 'state:TX' });
  assert.deepEqual(plain(S.parseTags(new URLSearchParams('tag=Top%2050'), dims)), { group: 'Top 50' },
    'a legacy bare label must resolve to the default dimension');
  assert.deepEqual(plain(S.parseTags(new URLSearchParams(''), dims)), {});
  assert.deepEqual(plain(S.parseTags(new URLSearchParams('tag=x'), undefined)), {});
  // A dimension with a single value renders no filter, so a tag in it is
  // ignored rather than applied with no way to clear it.
  const one = dims.concat([{ name: 'metro', label: 'Metro', values: [{ tag: 'metro:X', label: 'X' }] }]);
  assert.deepEqual(plain(S.parseTags(new URLSearchParams('tag=metro:X&tag=state:CA'), one)), { state: 'state:CA' });
});

test('urlTags serializes in dimension order using the normalized form', () => {
  const { S } = loadSelectors();
  assert.deepEqual(plain(S.urlTags({ state: 'state:CA', group: 'Bay Area' }, dims)), ['group:Bay Area', 'state:CA']);
  assert.deepEqual(plain(S.urlTags({}, dims)), []);
});

test('matches ANDs across dimensions', () => {
  const { S } = loadSelectors();
  assert.equal(S.matches(['Bay Area', 'state:CA'], {}), true);
  assert.equal(S.matches(['Bay Area', 'state:CA'], { group: 'Bay Area' }), true);
  assert.equal(S.matches(['Bay Area', 'state:CA'], { group: 'Bay Area', state: 'state:TX' }), false);
  assert.equal(S.matches(undefined, { group: 'Bay Area' }), false);
  assert.deepEqual(plain(S.filterCities(cities, { state: 'state:CA' }).map((c) => c.slug)), ['san-jose-ca', 'oakland-ca']);
  assert.equal(S.sameFilters({ group: 'Bay Area' }, { group: 'Bay Area' }), true);
  assert.equal(S.sameFilters({ group: 'Bay Area' }, { group: 'Top 50' }), false);
  assert.equal(S.sameFilters({ group: '' }, {}), true, 'an empty value counts as unselected');
});

test('rebuildCitySelect mirrors the server-side optgroup layout and keeps data attributes', () => {
  const { S, doc } = loadSelectors('<body><select id="city-select"></select></body>');
  const sel = doc.getElementById('city-select');
  const picked = S.rebuildCitySelect(sel, cities, dims, {});
  assert.equal(picked, 'oakland-ca', 'first option of the first group (sorted by name) wins when nothing was selected');
  const groups = Array.from(sel.querySelectorAll('optgroup')).map((g) => g.label);
  assert.deepEqual(plain(groups), ['Bay Area', 'Top 50']);
  const bayArea = Array.from(sel.querySelectorAll('optgroup')[0].querySelectorAll('option')).map((o) => o.value);
  assert.deepEqual(plain(bayArea), ['oakland-ca', 'san-jose-ca'], 'cities sort by name within a group');
  assert.equal(sel.querySelectorAll('option[value="san-jose-ca"]').length, 2, 'multi-tag city appears once per group');
  const bare = Array.from(sel.children).filter((el) => el.tagName === 'OPTION').map((o) => o.value);
  assert.deepEqual(plain(bare), ['reno-nv'], 'untagged city renders as a bare option after the groups');
  const sj = sel.querySelector('option[value="san-jose-ca"]');
  assert.equal(sj.dataset.bbox, '1,2,3,4');
  assert.equal(sel.querySelector('option[value="reno-nv"]').dataset.bbox, undefined);
});

test('rebuildCitySelect keeps a surviving selection, else falls to the first visible city, else "No cities match"', () => {
  const { S, doc } = loadSelectors('<body><select id="city-select"></select></body>');
  const sel = doc.getElementById('city-select');
  S.rebuildCitySelect(sel, cities, dims, {});
  sel.value = 'austin-tx';
  assert.equal(S.rebuildCitySelect(sel, cities, dims, { group: 'Top 50' }), 'austin-tx', 'kept');
  assert.equal(S.rebuildCitySelect(sel, cities, dims, { state: 'state:CA' }), 'oakland-ca', 'filtered out → first visible');
  assert.equal(S.rebuildCitySelect(sel, cities, dims, { state: 'state:CA' }, 'san-jose-ca'), 'san-jose-ca', 'prefer wins');
  assert.equal(S.rebuildCitySelect(sel, cities, dims, { group: 'Top 50', state: 'state:TX' }), 'austin-tx');
  assert.equal(S.rebuildCitySelect(sel, cities, dims, { group: 'Bay Area', state: 'state:TX' }), '');
  assert.equal(sel.options.length, 1);
  assert.equal(sel.options[0].disabled, true);
  assert.equal(sel.options[0].textContent, 'No cities match');
});

test('readFilters/writeFilters round-trip through .tag-filter selects', () => {
  const html = `<body>
    <select class="tag-filter" data-dim="group"><option value="">Any</option><option value="Bay Area">Bay Area</option></select>
    <select class="tag-filter" data-dim="state"><option value="">Any</option><option value="state:CA">CA</option></select>
  </body>`;
  const { S, doc } = loadSelectors(html);
  assert.deepEqual(plain(S.readFilters(doc)), {});
  S.writeFilters({ group: 'Bay Area', state: 'state:ZZ' }, doc);
  assert.deepEqual(plain(S.readFilters(doc)), { group: 'Bay Area' }, 'an unknown value resets that dimension to Any');
  S.writeFilters({}, doc);
  assert.deepEqual(plain(S.readFilters(doc)), {});
});
