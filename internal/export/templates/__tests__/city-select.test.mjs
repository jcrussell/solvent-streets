// The city <select> has one shape: the city_select
// partial renders it on both pages, and selectors.js rebuildCitySelect
// rebuilds the same markup when a tag filter changes. These specs read the
// server-rendered pages before any script runs and compare them, attribute for
// attribute, with each other and with the client-side rebuild.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { JSDOM } from 'jsdom';
import { load, fixtureDir } from './harness.mjs';

// attrs is an element's attributes as a plain sorted list of [name, value].
const attrs = (el) => Array.from(el.attributes).map((a) => [a.name, a.value]).sort();
const option = (o) => ({ attrs: attrs(o), text: o.textContent });

// shape describes a select: its own attributes, then each child in order, an
// optgroup with its label and options or a bare option.
function shape(sel) {
  return {
    attrs: attrs(sel),
    children: Array.from(sel.children).map((c) => (c.tagName === 'OPTGROUP'
      ? { group: attrs(c), options: Array.from(c.children).map(option) }
      : option(c))),
  };
}

// served is the selector as the Go template rendered it: the page file parsed
// with no script run.
function served(file) {
  const dom = new JSDOM(readFileSync(join(fixtureDir(), file), 'utf8'));
  return shape(dom.window.document.getElementById('city-select'));
}

// rebuilt is what rebuildCitySelect makes of the page's own cities, on a
// select carrying the served one's attributes.
function rebuilt(page, prefer) {
  const { win, doc } = load({ page });
  const sel = doc.createElement('select');
  for (const a of doc.getElementById('city-select').attributes) sel.setAttribute(a.name, a.value);
  const cfg = win.PVMT_CONFIG;
  win.PvmtSelectors.rebuildCitySelect(sel, cfg.cities, cfg.tag_dimensions, {}, prefer);
  return shape(sel);
}

test('the fixture selector has optgroups, a multi-group city and bare options', () => {
  const s = served('index.html');
  const groups = s.children.filter((c) => c.group);
  assert.ok(groups.length >= 2, 'the comparison needs more than one optgroup');
  assert.ok(s.children.some((c) => !c.group), 'the comparison needs an untagged city');
  const values = groups.flatMap((g) => g.options.map((o) => o.attrs.find((a) => a[0] === 'value')[1]));
  assert.ok(new Set(values).size < values.length, 'the comparison needs a city listed under two groups');
});

test('both pages render the same city selector', () => {
  assert.deepEqual(served('play.html'), served('index.html'));
});

test('every option carries value and data-bbox, and exactly one is selected', () => {
  for (const file of ['index.html', 'play.html', 'play-live.html']) {
    const options = served(file).children.flatMap((c) => (c.group ? c.options : [c]));
    for (const o of options) {
      const names = o.attrs.map((a) => a[0]).filter((n) => n !== 'selected');
      assert.deepEqual(names, ['data-bbox', 'value'], file + ': ' + o.text);
      assert.match(o.attrs.find((a) => a[0] === 'data-bbox')[1], /^-?[\d.]+(,-?[\d.]+){3}$/, file);
    }
    assert.equal(options.filter((o) => o.attrs.some((a) => a[0] === 'selected')).length, 1, file);
  }
});

test('the page opens on its first option, and the live play page on the served city', () => {
  const selected = (file) => {
    const options = served(file).children.flatMap((c) => (c.group ? c.options : [c]));
    const at = options.findIndex((o) => o.attrs.some((a) => a[0] === 'selected'));
    return { at, value: options[at].attrs.find((a) => a[0] === 'value')[1] };
  };
  assert.equal(selected('index.html').at, 0);
  assert.equal(selected('play.html').at, 0);
  assert.equal(selected('play-live.html').value, 'gamma-or');
});

test('rebuildCitySelect produces the markup the server rendered', () => {
  assert.deepEqual(rebuilt('index'), served('index.html'));
  assert.deepEqual(rebuilt('play'), served('play.html'));
  assert.deepEqual(rebuilt('play-live', 'gamma-or'), served('play-live.html'));
});

test('a city listed under two groups is selected once, at its first listing', () => {
  // Beta is in both Metro and Top 50.
  const s = rebuilt('index', 'beta-ca');
  const options = s.children.flatMap((c) => (c.group ? c.options : [c]));
  const isBeta = (o) => o.attrs.some((a) => a[0] === 'value' && a[1] === 'beta-ca');
  const marked = options.map((o, i) => (o.attrs.some((a) => a[0] === 'selected') ? i : -1)).filter((i) => i >= 0);
  assert.equal(options.filter(isBeta).length, 2, 'fixture: Beta should be listed twice');
  assert.deepEqual(marked, [options.findIndex(isBeta)]);
});
