// Play page guidance (solvent-streets-ern4.30): the Map links carry the city
// and tag filters back to the dashboard, a lost game's banner names what
// ended it, and a first-run hint says how to treat roads for the input in
// hand.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, seedJSON, hexgridJSON } from './fixtures.mjs';

const mapLinks = ($$) => $$('a.map-link').map((a) => a.getAttribute('href'));

test('play: the Map link carries the active city and tag filters', async () => {
  const { $$, flush } = load({ page: 'play', data: {},
    url: 'https://example.test/play.html?city=beta-ca&tag=state:CA' });
  await flush();
  assert.deepEqual(mapLinks($$), ['index.html?city=beta-ca&tag=state%3ACA']);
});

test('play: the Map link follows a filter change that keeps the city', async () => {
  const { win, $, $$, flush } = load({ page: 'play', data: {},
    url: 'https://example.test/play.html?city=beta-ca' });
  await flush();
  assert.deepEqual(mapLinks($$), ['index.html?city=beta-ca']);
  const f = $('select.tag-filter[data-dim="group"]');
  f.value = 'Top 50';
  f.dispatchEvent(new win.Event('change'));
  await flush();
  assert.deepEqual(mapLinks($$), ['index.html?city=beta-ca&tag=group%3ATop+50']);
});

test('play (live server): the Map link names the served city', () => {
  const { $$ } = load({ page: 'play-live', data: {}, navigate: () => {},
    url: 'https://example.test/play?city=gamma-or' });
  assert.deepEqual(mapLinks($$), ['/?city=gamma-or']);
});

// endBanner shows the end-of-game banner for a final state.
function endBanner(state, status = 'lost') {
  const h = load({ page: 'play', data: {}, url: 'https://example.test/play.html?city=beta-ca' });
  h.win.__finalState = state;
  h.get('lastState = window.__finalState');
  h.win.onStatusChange(status);
  return h;
}

test('play: a backlog loss with the treasury untouched says it went unspent', () => {
  const { $, $$ } = endBanner({ loss_reason: 'backlog', treasury: 8e6, budget_rate: 1e6, treatment_count: 0, year: 8.1 });
  const sub = $('#banner .sub').textContent;
  assert.match(sub, /went unspent and no roads were treated/);
  assert.match(sub, /drag over the map to treat/);
  assert.doesNotMatch(sub, /outran/);
  assert.equal($('#banner .headline').textContent, 'Network insolvent.');
  assert.ok(mapLinks($$).includes('index.html?city=beta-ca'), 'the banner Map link must carry the city');
});

test('play: a backlog loss with a year of budget banked says it went unspent', () => {
  const { $ } = endBanner({ loss_reason: 'backlog', treasury: 3e6, budget_rate: 1e6, treatment_count: 40 });
  const sub = $('#banner .sub').textContent;
  assert.match(sub, /went unspent,/);
  assert.doesNotMatch(sub, /no roads were treated/);
});

// Peer review: at a $0 budget there was never money to spend, so the banner
// must not blame the player for leaving it unspent.
test('play: a backlog loss at a $0 budget does not call $0 unspent', () => {
  const { $ } = endBanner({ loss_reason: 'backlog', treasury: 0, budget_rate: 0, treatment_count: 0 });
  const sub = $('#banner .sub').textContent;
  assert.doesNotMatch(sub, /unspent/);
  assert.match(sub, /outran the budget/);
});

// Peer review: Play again re-seeds a 'running' engine behind the start
// overlay, so painting must also require the in-game UI to be up.
test('play: the board does not take treatments behind the start overlay', () => {
  const h = load({ page: 'play', data: {}, url: 'https://example.test/play.html?city=beta-ca' });
  h.win.setInGameUI(true);
  assert.equal(h.get('playing'), true);
  h.win.setInGameUI(false);
  assert.equal(h.get('playing'), false, 'the start screen must stop the brush');
});

test('play: a backlog loss with the budget spent says decay outran it', () => {
  const { $ } = endBanner({ loss_reason: 'backlog', treasury: 2e5, budget_rate: 1e6, treatment_count: 40 });
  assert.match($('#banner .sub').textContent, /outran the budget/);
});

test('play: PCI-floor and closure losses name their cause', () => {
  assert.match(endBanner({ loss_reason: 'pci_floor', treasury: 0, budget_rate: 1 }).$('#banner .sub').textContent, /PCI fell below/);
  assert.match(endBanner({ loss_reason: 'closures', treasury: 0, budget_rate: 1 }).$('#banner .sub').textContent, /closed to gravel/);
});

// --- first-run hint ---

const boardData = { ...fullData, 'forecast_seed.json': seedJSON, 'hexgrid.geojson': hexgridJSON };

// running is a minimal live engine state for the stubbed WASM exports.
const running = (extra = {}) => JSON.stringify({ status: 'running', year: 0, treasury: 1e6, budget_rate: 1e6, changed: [], treatment_count: 0, ...extra });

// startBoard boots the board with stub engine exports and presses Start.
async function startBoard({ coarse = false, before } = {}) {
  const h = load({ page: 'play', wasm: true, data: boardData, before(win) {
    win.matchMedia = (q) => ({ matches: coarse && q === '(pointer: coarse)', media: q });
    win.gameInit = () => running();
    win.gameTick = () => running();
    win.gameTreatBatch = () => running({ treatment_count: 1 });
    if (before) before(win);
  } });
  await h.flush();
  h.fireMap('load');
  await h.flush(12);
  assert.ok(h.get('gameConfig'), 'the board did not boot');
  h.$('#start-btn').click();
  // Start runs the clock on requestAnimationFrame, which would keep the
  // window (and the test run) alive; these specs need no ticks.
  h.win.stopLoop();
  return h;
}

test('play: the first-run hint shows on Start, worded for a mouse', async () => {
  const { $ } = await startBoard();
  assert.equal($('#onboard-hint').hidden, false);
  assert.match($('#onboard-text').textContent, /Right-drag to pan/);
  assert.match($('#brush-hint').textContent, /left-drag to treat/);
});

test('play: on a touch screen the hint says drag a finger and pinch', async () => {
  const { $ } = await startBoard({ coarse: true });
  const text = $('#onboard-text').textContent;
  assert.match(text, /Drag a finger/);
  assert.match(text, /Pinch to zoom/);
  assert.doesNotMatch(text, /right-drag/i);
  assert.doesNotMatch($('#brush-hint').textContent, /right-drag/);
});

test('play: dismissing the hint hides it and remembers that', async () => {
  const { $, win } = await startBoard();
  $('#onboard-dismiss').click();
  assert.equal($('#onboard-hint').hidden, true);
  assert.equal(win.localStorage.getItem('pvmtPlayHintSeen'), '1');
});

test('play: a remembered dismissal keeps the hint hidden', async () => {
  const { $ } = await startBoard({ before: (win) => win.localStorage.setItem('pvmtPlayHintSeen', '1') });
  assert.equal($('#onboard-hint').hidden, true);
});

test('play: the first treatment dismisses the hint', async () => {
  const { $, get, win } = await startBoard();
  get('map.queryRenderedFeatures = () => [{ id: "h1" }]');
  get('brushStroke = new Set()');
  win.brushPaint({ x: 0, y: 0 });
  assert.equal($('#onboard-hint').hidden, true);
  assert.equal(win.localStorage.getItem('pvmtPlayHintSeen'), '1');
});

// solvent-streets-ern4.36: the finished game's board and HUD must not linger
// behind the start screen after Play again.
test('play: Play again shows a fresh board and HUD behind the start screen', async () => {
  const { $, get, win } = await startBoard();
  let inits = 0;
  win.gameInit = () => { inits++; return running({ year: 0, deferred_backlog: 0 }); };
  win.__end = { status: 'lost', loss_reason: 'backlog', year: 8.1, treasury: 8e6, budget_rate: 1e6, deferred_backlog: 5e6, changed: [] };
  get('applyState(window.__end)');
  assert.match($('#hud-year').textContent, /^8\.1/);
  $('#play-again').click();
  assert.equal(inits, 1, 'Play again must re-seed the engine');
  assert.match($('#hud-year').textContent, /^0\.0/, 'the HUD must show the fresh game');
  assert.equal(get('peakBacklog'), 0);
  assert.equal($('#start-overlay').style.display, 'block');
  assert.equal($('#hud').style.display, 'none');
});

test('play: the hint still works when storage throws', async () => {
  const { $ } = await startBoard({ before(win) {
    Object.defineProperty(win, 'localStorage', { get() { throw new Error('SecurityError'); } });
  } });
  assert.equal($('#onboard-hint').hidden, false);
  $('#onboard-dismiss').click();
  assert.equal($('#onboard-hint').hidden, true);
});
