// The tab bar's accessibility tree and its phone scroll fades:
// the tablist holds only tabs
// (the Play link sits beside it in the same scrolling strip), the gear is a
// disclosure rather than a menu, since it holds selects and toggles, not
// menu items.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { metaJSON } from './fixtures.mjs';

async function page() {
  const h = load({ data: { 'meta.json': metaJSON } });
  await h.flush();
  return h;
}

test('the tablist owns only tabs, and the Play link is outside it', async () => {
  const { $, $$ } = await page();
  const list = $('[role="tablist"]');
  assert.ok(list);
  assert.equal(list.getAttribute('aria-label'), 'Sections');
  const kids = Array.from(list.children);
  assert.ok(kids.length > 0);
  for (const k of kids) assert.equal(k.getAttribute('role'), 'tab', `non-tab child in the tablist: ${k.outerHTML}`);
  const play = $('a.nav-link');
  assert.ok(play, 'the Play link is gone');
  assert.equal(play.closest('[role="tablist"]'), null, 'the Play link is inside the tablist');
  assert.ok(play.closest('.tab-list'), 'the Play link left the scrolling tab strip');
  assert.equal($$('[role="tablist"]').length, 1);
});

test('the gear is a disclosure, not a menu', async () => {
  const { $ } = await page();
  const btn = $('#gear-btn');
  const menu = $('#gear-menu');
  assert.equal(menu.getAttribute('role') === 'menu', false, 'role=menu holds form controls, not menuitems');
  assert.equal(btn.hasAttribute('aria-haspopup'), false, 'aria-haspopup announces a menu that is not there');
  assert.equal(btn.getAttribute('aria-controls'), 'gear-menu');
  assert.equal(btn.getAttribute('aria-expanded'), 'false');
  btn.click();
  assert.equal(btn.getAttribute('aria-expanded'), 'true');
  assert.equal(menu.hidden, false);
});

// stubScroll gives the strip a layout jsdom lacks: `content` px of tabs in a
// `box` px viewport, scrolled to `left`.
function stubScroll(win, el, content, box, left) {
  Object.defineProperty(el, 'scrollWidth', { configurable: true, value: content });
  Object.defineProperty(el, 'clientWidth', { configurable: true, value: box });
  Object.defineProperty(el, 'scrollLeft', { configurable: true, writable: true, value: left });
  el.dispatchEvent(new win.Event('scroll'));
}
const fades = (el) => ['fade-left', 'fade-right'].filter((c) => el.classList.contains(c));

test('the phone tab strip fades only the edges with more tabs beyond them', async () => {
  const { win, $ } = await page();
  const strip = $('.tab-list');
  stubScroll(win, strip, 600, 360, 0);
  assert.deepEqual(fades(strip), ['fade-right'], 'at the start only the right edge fades');
  stubScroll(win, strip, 600, 360, 120);
  assert.deepEqual(fades(strip), ['fade-left', 'fade-right'], 'mid-scroll both edges fade');
  stubScroll(win, strip, 600, 360, 240);
  assert.deepEqual(fades(strip), ['fade-left'], 'at the end Play must not stay faded');
  // A wide screen fits every tab: no fade at all.
  stubScroll(win, strip, 600, 600, 0);
  assert.deepEqual(fades(strip), []);
  // A resize re-checks without a scroll.
  Object.defineProperty(strip, 'clientWidth', { configurable: true, value: 300 });
  win.dispatchEvent(new win.Event('resize'));
  assert.deepEqual(fades(strip), ['fade-right']);
});
