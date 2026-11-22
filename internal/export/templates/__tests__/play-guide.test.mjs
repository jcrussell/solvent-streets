// Play page guidance: the Map links carry the city
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

// The banner's explanation follows the cause Go reports and, for a backlog
// loss, whether money was left unspent. The plain backlog loss (the budget
// spent, decay outran it) is the reference the others are told apart from.
const SPENT = { loss_reason: 'backlog', treasury: 2e5, annual_budget: 1e6, treatment_count: 40 };
const subOf = (h) => h.$('#banner .sub').textContent;
const headlineOf = (h) => h.$('#banner .headline').textContent;

test('play: a backlog loss with the treasury untouched says it went unspent', () => {
  const state = { loss_reason: 'backlog', treasury: 8e6, annual_budget: 1e6, treatment_count: 0, year: 8.1 };
  const h = endBanner(state);
  const sub = subOf(h);
  // It names the money left over, and is not the plain loss's explanation.
  assert.ok(sub.includes(h.win.PvmtCommon.fmtMoney(8e6)), sub);
  assert.notEqual(sub, subOf(endBanner(SPENT)));
  // With nothing treated it says more than when some roads were.
  const treated = subOf(endBanner({ ...state, treatment_count: 40 }));
  assert.ok(sub.length > treated.length, 'the banner does not say no roads were treated');
  // The same kind of loss, so the same headline.
  assert.ok(headlineOf(h).trim().length > 0);
  assert.equal(headlineOf(h), headlineOf(endBanner(SPENT)));
  assert.ok(mapLinks(h.$$).includes('index.html?city=beta-ca'), 'the banner Map link must carry the city');
});

test('play: a backlog loss with a year of budget banked says it went unspent', () => {
  const state = { loss_reason: 'backlog', treasury: 3e6, annual_budget: 1e6, treatment_count: 40 };
  const h = endBanner(state);
  const sub = subOf(h);
  assert.ok(sub.includes(h.win.PvmtCommon.fmtMoney(3e6)), sub);
  assert.notEqual(sub, subOf(endBanner(SPENT)));
  // Roads were treated, so it does not say none were.
  assert.ok(sub.length < subOf(endBanner({ ...state, treatment_count: 0 })).length);
});

// Peer review: at a $0 budget there was never money to spend, so the banner
// must not blame the player for leaving it unspent.
test('play: a backlog loss at a $0 budget does not call $0 unspent', () => {
  const sub = subOf(endBanner({ loss_reason: 'backlog', treasury: 0, annual_budget: 0, treatment_count: 0 }));
  assert.doesNotMatch(sub, /\$/, 'the banner names an unspent amount');
  assert.equal(sub, subOf(endBanner(SPENT)), 'a $0 budget did not read as the plain backlog loss');
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
  // Less than a year's budget left and roads treated: nothing went unspent,
  // so no amount is named, whatever small sum remains.
  const sub = subOf(endBanner(SPENT));
  assert.ok(sub.trim().length > 0);
  assert.doesNotMatch(sub, /\$/);
  assert.equal(sub, subOf(endBanner({ ...SPENT, treasury: 0 })));
});

// The banner's stat rows carry no key: they are read by position.
const STAT = { years: 0, pci: 1, spent: 2, treatments: 3, gravel: 4, peakBacklog: 5 };
const stat = (h, at) => {
  const rows = h.$$('#banner .banner-stats .row');
  assert.equal(rows.length, Object.keys(STAT).length, 'the banner stats changed; update STAT');
  return rows[at].querySelector('.v').textContent;
};

test('play: the banner reports the closed share of road area as gravel', () => {
  const h = endBanner({ loss_reason: 'closures', treasury: 0, annual_budget: 1, closed_share: 0.537 });
  assert.equal(stat(h, STAT.gravel), '54%');
  assert.equal(stat(endBanner({ loss_reason: 'closures', treasury: 0, annual_budget: 1 }), STAT.gravel), '0%');
});

test('play: PCI-floor and closure losses name their cause', () => {
  const banners = ['pci_floor', 'closures', 'backlog'].map((loss_reason) => endBanner({ loss_reason, treasury: 0, annual_budget: 1 }));
  const subs = banners.map(subOf);
  const headlines = banners.map(headlineOf);
  assert.ok(subs.every((s) => s.trim().length > 0) && headlines.every((s) => s.trim().length > 0));
  assert.equal(new Set(subs).size, 3, 'two causes share an explanation: ' + subs.join(' | '));
  assert.equal(new Set(headlines).size, 3, 'two causes share a headline: ' + headlines.join(' | '));
  // A won game reads as none of them.
  const won = endBanner({ treasury: 0, annual_budget: 1 }, 'won');
  assert.ok(!subs.includes(subOf(won)) && !headlines.includes(headlineOf(won)));
  assert.ok(won.$('#banner').classList.contains('won'));
  assert.ok(banners.every((b) => b.$('#banner').classList.contains('lost')));
});

// --- first-run hint ---

const boardData = { ...fullData, 'forecast_seed.json': seedJSON, 'hexgrid.geojson': hexgridJSON };

// running is a minimal live engine state for the stubbed WASM exports.
const running = (extra = {}) => JSON.stringify({ status: 'running', year: 0, treasury: 1e6, annual_budget: 1e6, changed: [], treatment_count: 0, ...extra });

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

// The hint and the brush caption are worded for the pointer in hand, so a
// mouse and a touch screen read differently. What each says is the page's
// copy; the one thing held is that a touch screen is not told to right-drag,
// which a finger cannot do.
const hintText = ({ $ }) => ({ hint: $('#onboard-text').textContent, brush: $('#brush-hint').textContent });

test('play: the first-run hint shows on Start, worded for a mouse', async () => {
  const mouse = await startBoard();
  assert.equal(mouse.$('#onboard-hint').hidden, false);
  const text = hintText(mouse);
  assert.ok(text.hint.trim().length > 0 && text.brush.trim().length > 0);
  const touch = hintText(await startBoard({ coarse: true }));
  assert.notEqual(text.hint, touch.hint, 'a mouse got the touch hint');
  assert.notEqual(text.brush, touch.brush, 'a mouse got the touch caption');
});

test('play: on a touch screen the hint is worded for a finger', async () => {
  const h = await startBoard({ coarse: true });
  assert.equal(h.$('#onboard-hint').hidden, false);
  const text = hintText(h);
  assert.ok(text.hint.trim().length > 0 && text.brush.trim().length > 0);
  assert.doesNotMatch(text.hint, /right-drag/i);
  assert.doesNotMatch(text.brush, /right-drag/i);
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

// The finished game's board and HUD must not linger
// behind the start screen after Play again.
test('play: Play again shows a fresh board and HUD behind the start screen', async () => {
  const { $, get, win } = await startBoard();
  let inits = 0;
  win.gameInit = () => { inits++; return running({ year: 0, deferred_backlog: 0 }); };
  win.__end = { status: 'lost', loss_reason: 'backlog', year: 8.1, treasury: 8e6, annual_budget: 1e6, deferred_backlog: 5e6, changed: [] };
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

// The site shows PCI as a whole number (shownPCI), on the play page too.
test('play: the HUD and the end banner show PCI as a whole number', async () => {
  const { $, get, win } = await startBoard();
  win.__s = { status: 'running', year: 1, treasury: 1e6, annual_budget: 1e6, network_pci: 71.6, deferred_backlog: 0, changed: [] };
  get('applyState(window.__s)');
  assert.equal($('#hud-pci').textContent, '72');

  const finalPCI = (state) => stat(endBanner(state), STAT.pci);
  assert.equal(finalPCI({ loss_reason: 'backlog', treasury: 0, annual_budget: 1, network_pci: 24.6 }), '25');
  // Below the floor, the banner floors: 29.6 must not read as a floor of 30.
  assert.equal(finalPCI({ loss_reason: 'pci_floor', treasury: 0, annual_budget: 1, network_pci: 29.6 }), '29');
});

// A tick the engine refuses is reported once and pauses the clock, rather
// than adding a banner line every tick.
test('play: a failed tick stops the clock and reports one error', async () => {
  const { $, $$, win } = await startBoard();
  const frames = [];
  win.requestAnimationFrame = (cb) => { frames.push(cb); return frames.length; };
  win.gameTick = () => JSON.stringify({ error: 'boom' });
  win.setRate(1);
  for (let i = 0, ts = 0; i < 20 && frames.length > 0; i++, ts += 200) frames.shift()(ts);

  assert.equal(frames.length, 0, 'the loop is still scheduled after a failed tick');
  const lines = $$('#error-banner-list li').map((li) => li.textContent).filter((t) => /boom/.test(t));
  assert.equal(lines.length, 1, 'want one banner line, got: ' + lines.join(' | '));
  assert.ok($('#pause-btn').classList.contains('active'), 'the speed control does not show the pause');
});
