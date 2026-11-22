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

test('the filters are in the top bar beside the city selector, not the gear menu', async () => {
  const h = load({ data: fullData });
  await h.flush();
  assert.ok(h.$('.tab-bar #topbar-filters'), 'no filters in the top bar');
  assert.equal(h.$('#gear-menu select.tag-filter'), null, 'the gear menu still holds a filter');
  assert.equal(h.$('#topbar-filters').nextElementSibling, h.$('#city-select'));
  assert.equal(filter(h, 'group').options[0].textContent, 'Any Group');
  assert.equal(filter(h, 'state').options[0].textContent, 'Any State');
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
  assert.equal(status.querySelector('.filter-status-text').textContent, 'Filtered from 3 cities');
  assert.equal(h.$('#aggregate-tab .filter-status .filter-status-text').textContent, 'Filtered from 3 cities');
  assert.equal(h.$('#compare-scope').textContent, 'How the 2 cities in Metro compare.');
  assert.equal(h.$('#aggregate-scope').textContent,
    'The 2 cities in Metro as one fiscal entity: what does it cost to keep them solvent?');
});

test('Clear on the status line drops every filter', async () => {
  const h = load({ data: fullData, url: 'https://example.test/index.html?tab=compare-tab&tag=group:Metro&tag=state:CA' });
  await h.flush(12);
  const status = h.$('#compare-tab .filter-status');
  assert.equal(status.querySelector('.filter-status-text').textContent, 'Filtered from 3 cities');
  assert.equal(h.$('#compare-scope').textContent, 'How the 2 cities in Metro, CA compare.');

  status.querySelector('.filter-clear').click();
  await h.flush(12);
  assert.equal(status.hidden, true);
  assert.equal(h.$('#compare-scope').textContent, 'How all 3 cities compare.');
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
