// The city tag filters sit in the top bar beside the city selector (behind
// a Filter button on a narrow screen), not in the gear menu, and Compare and
// Aggregate open with the cities they cover ("How the 2 cities in Metro
// compare") and, when filtered, a "Filtered from M cities" line with a
// Clear. The fixture's three cities carry a "group" dimension (Metro,
// Top 50) and a "state" one (CA, OR).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData } from './fixtures.mjs';

const filter = (h, dim) => h.$(`#topbar-filters select.tag-filter[data-dim="${dim}"]`);
const cityOptions = (h) => Array.from(new Set(h.$$('#city-select option').map((o) => o.value)));
const tags = (h) => new URLSearchParams(h.win.location.search).getAll('tag');

function choose(h, dim, value) {
  const sel = filter(h, dim);
  sel.value = value;
  sel.dispatchEvent(new h.win.Event('change'));
}

// count matches a text that carries n as a whole number.
const count = (/** @type {number} */ n) => new RegExp('(^|\\D)' + n + '(\\D|$)');
// chosen is the label of the value a filter is set to, as its option shows it.
const chosen = (h, dim) => filter(h, dim).selectedOptions[0].textContent;

test('the filters are in the top bar beside the city selector, not the gear menu', async () => {
  const h = load({ data: fullData });
  await h.flush();
  assert.ok(h.$('.tab-bar #topbar-filters'), 'no filters in the top bar');
  assert.equal(h.$('#gear-menu select.tag-filter'), null, 'the gear menu still holds a filter');
  assert.equal(h.$('#topbar-filters').nextElementSibling, h.$('#city-select'));
  // Each filter opens on its unfiltered option, which carries no tag.
  for (const dim of ['group', 'state']) {
    assert.equal(filter(h, dim).selectedIndex, 0, dim);
    assert.equal(filter(h, dim).options[0].value, '', dim);
    assert.ok(filter(h, dim).options[0].textContent.trim().length > 0, dim);
  }
});

test('a filter narrows the city selector and the Compare tiles, and reaches the URL', async () => {
  const h = load({ data: fullData, url: 'https://example.test/index.html?tab=compare-tab' });
  await h.flush(12);
  assert.equal(h.$$('#solvency-cards .score-card').length, 3);
  assert.equal(h.$('#compare-tab .filter-status').hidden, true, 'the status line shows with no filter set');

  choose(h, 'group', 'Metro');
  await h.flush(12);
  assert.deepEqual(cityOptions(h), ['alpha-ca', 'beta-ca']);
  assert.deepEqual(h.$$('#solvency-cards .score-card').map((c) => c.dataset.slug).sort(), ['alpha-ca', 'beta-ca']);
  assert.deepEqual(tags(h), ['group:Metro']);
  assert.ok(filter(h, 'group').classList.contains('active'));

  const status = h.$('#compare-tab .filter-status');
  assert.equal(status.hidden, false);
  // The status line counts the whole site; the tab leads count the cities
  // left and name the filter that left them.
  assert.match(status.querySelector('.filter-status-text').textContent, count(3));
  assert.equal(h.$('#aggregate-tab .filter-status .filter-status-text').textContent,
    status.querySelector('.filter-status-text').textContent);
  for (const id of ['compare-scope', 'aggregate-scope']) {
    const lead = h.$('#' + id).textContent;
    assert.match(lead, count(2), id);
    assert.ok(lead.includes(chosen(h, 'group')), id + ': ' + lead);
  }
});

test('Clear on the status line drops every filter', async () => {
  const h = load({ data: fullData, url: 'https://example.test/index.html?tab=compare-tab&tag=group:Metro&tag=state:CA' });
  await h.flush(12);
  const status = h.$('#compare-tab .filter-status');
  assert.match(status.querySelector('.filter-status-text').textContent, count(3));
  const filtered = h.$('#compare-scope').textContent;
  assert.match(filtered, count(2));
  // Both filters are named, in the order of the bar.
  const [group, state] = [chosen(h, 'group'), chosen(h, 'state')];
  assert.ok(filtered.includes(group) && filtered.includes(state), filtered);
  assert.ok(filtered.indexOf(group) < filtered.lastIndexOf(state), filtered);

  status.querySelector('.filter-clear').click();
  await h.flush(12);
  assert.equal(status.hidden, true);
  assert.match(h.$('#compare-scope').textContent, count(3));
  assert.ok(!h.$('#compare-scope').textContent.includes(group), 'the lead still names the cleared filter');
  assert.deepEqual(tags(h), []);
  assert.equal(filter(h, 'group').value, '');
  assert.equal(filter(h, 'state').value, '');
  assert.deepEqual(cityOptions(h).sort(), ['alpha-ca', 'beta-ca', 'gamma-or']);
  assert.equal(h.$$('#solvency-cards .score-card').length, 3);
});

test('the Filter button counts the active filters and opens and closes the popover', async () => {
  const h = load({ data: fullData, url: 'https://example.test/index.html?tag=state:CA' });
  await h.flush();
  const btn = h.$('#filter-btn');
  const bar = h.$('#topbar-filters');
  assert.equal(btn.getAttribute('aria-controls'), 'tag-filter-pop');
  assert.equal(h.$('#filter-count').hidden, false);
  assert.equal(h.$('#filter-count').textContent, '1');

  btn.click();
  assert.ok(bar.classList.contains('open'));
  assert.equal(btn.getAttribute('aria-expanded'), 'true');
  h.doc.dispatchEvent(new h.win.KeyboardEvent('keydown', { key: 'Escape' }));
  assert.ok(!bar.classList.contains('open'));
  assert.equal(btn.getAttribute('aria-expanded'), 'false');

  btn.click();
  h.doc.body.click();
  assert.ok(!bar.classList.contains('open'), 'an outside click left the popover open');

  choose(h, 'state', '');
  await h.flush();
  assert.equal(h.$('#filter-count').hidden, true);
});
