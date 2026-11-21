// The stats panel's show/hide toggle: it collapses the panel to a tab and
// expands it again, says which with aria-expanded, and remembers the
// viewer's choice in localStorage, which may be unavailable.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON } from './fixtures.mjs';

const KEY = 'pvmtPanelCollapsed';

function collapsed(h) {
  const btn = h.$('#panel-toggle');
  const shut = h.$('#panel').classList.contains('collapsed');
  // One state, three views of it: they must agree.
  assert.equal(h.$('#panel-body').hidden, shut);
  assert.equal(btn.getAttribute('aria-expanded'), String(!shut));
  return shut;
}

test('the toggle collapses and expands the panel and remembers the choice', async () => {
  const h = load({ data: { 'meta.json': metaJSON } });
  await h.flush();
  const btn = h.$('#panel-toggle');
  assert.equal(btn.getAttribute('aria-controls'), 'panel-body');
  assert.ok(h.$('#panel-body #stats'), 'the toggle does not control the stats');
  assert.equal(collapsed(h), false, 'the panel starts collapsed');

  btn.click();
  assert.equal(collapsed(h), true);
  assert.ok(h.$('#panel .panel-head h2'), 'the collapsed tab lost the city name');
  assert.equal(h.win.localStorage.getItem(KEY), '1');

  btn.click();
  assert.equal(collapsed(h), false);
  assert.equal(h.win.localStorage.getItem(KEY), '0');
});

test('a stored collapsed panel starts collapsed', async () => {
  const h = load({ data: { 'meta.json': metaJSON }, before: (win) => win.localStorage.setItem(KEY, '1') });
  await h.flush();
  assert.equal(collapsed(h), true);
  h.$('#panel-toggle').click();
  assert.equal(collapsed(h), false);
});

test('the panel works without storage', async () => {
  const h = load({
    data: { 'meta.json': metaJSON },
    before: (win) => Object.defineProperty(win, 'localStorage', { get() { throw new Error('SecurityError'); } }),
  });
  await h.flush();
  assert.equal(collapsed(h), false);
  h.$('#panel-toggle').click();
  assert.equal(collapsed(h), true, 'the toggle stopped working without storage');
  h.$('#panel-toggle').click();
  assert.equal(collapsed(h), false);
});
