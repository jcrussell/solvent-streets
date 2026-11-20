// common.js is the plumbing both pages load before their own script:
// one money formatter, one error banner, one JSON
// fetch wrapper, and one way to boot and call the forecast WASM. These specs
// run it inside both rendered pages.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, forecastJSON, seedJSON, hexgridJSON } from './fixtures.mjs';

const PAGES = ['index', 'play'];

test('both pages load common.js before the selector and page scripts', () => {
  for (const [page, script] of [['index', 'app.js'], ['play', 'game.js']]) {
    const { $$ } = load({ page });
    const srcs = $$('body script[src]').map((s) => s.getAttribute('src'));
    assert.deepEqual(srcs, ['common.js', 'selectors.js', script], page);
  }
});

test('fmtMoney: one rounding rule per magnitude, on both pages', () => {
  const cases = [
    [0, '$0'],
    [789.4, '$789'],
    [999.4, '$999'],
    [999.6, '$1.0K'],
    [1_000, '$1.0K'],
    [2_500, '$2.5K'],
    [45_649, '$45.6K'],
    [999_949, '$999.9K'],
    [999_960, '$1.0M'],
    [12_345_678, '$12.3M'],
    [999_960_000, '$1.00B'],
    [1_254_000_000, '$1.25B'],
    [-1_234_567, '-$1.2M'],
    [-0.2, '$0'],
    [null, '$0'],
    [undefined, '$0'],
    [NaN, '$0'],
  ];
  for (const page of PAGES) {
    const { win } = load({ page });
    for (const [n, want] of cases) {
      assert.equal(win.PvmtCommon.fmtMoney(n), want, page + ': fmtMoney(' + n + ')');
    }
  }
});

test('fmtMoney full: whole dollars with en-US separators at any size', () => {
  const { win } = load();
  const full = (n) => win.PvmtCommon.fmtMoney(n, { full: true });
  assert.equal(full(12_345_678.4), '$12,345,678');
  assert.equal(full(999.6), '$1,000');
  assert.equal(full(-4_200), '-$4,200');
  assert.equal(full(0), '$0');
});

// The complaint behind the shared formatter: the same hold-steady budget read
// "$12.3M" on Financials and "$12.35M" on Play.
test('the hold-steady budget reads the same on Financials and on Play', async () => {
  const forecast = structuredClone(forecastJSON);
  const roads = forecast.find((f) => f.resource_type === 'roads');
  delete roads.current_budget; // both pages then lead with the hold-steady budget
  roads.break_even_budget = 12_345_678;
  const data = {
    ...fullData,
    'forecast.json': forecast,
    'forecast_seed.json': seedJSON,
    'hexgrid.geojson': hexgridJSON,
  };

  const index = load({ data });
  await index.flush(12);
  assert.match(index.$('#solvency-headline .lead').textContent, /\$12\.3M\/yr/);
  // Visitors read this: it says what is missing, not which config key to set.
  const detail = index.$('#solvency-headline .detail').textContent;
  assert.match(detail, /No cited street budget for .+ is on file/);
  assert.doesNotMatch(index.$('#solvency-headline').innerHTML, /current_budget|<code>/);

  const play = load({ page: 'play', wasm: true, data });
  await play.flush();
  play.fireMap('load');
  await play.flush(12);
  assert.equal(play.$('#start-budget-value').textContent, '$12.3M');
});

test('the error banner is the same markup on both pages, hidden until an error', () => {
  const shape = (h) => {
    const b = h.$('#error-banner');
    return {
      role: b.getAttribute('role'),
      live: b.getAttribute('aria-live'),
      heading: b.querySelector('strong').textContent,
      hidden: b.hidden,
      items: h.$$('#error-banner-list li').length,
    };
  };
  const want = { role: 'alert', live: 'assertive', heading: 'Something went wrong:', hidden: true, items: 0 };
  // The index page never reaches the WASM in this harness; on Play the boot is
  // held so its 404 has not been reported yet.
  assert.deepEqual(shape(load()), want);
  assert.deepEqual(shape(load({ page: 'play', hold: ['pvmt.wasm'] })), want);
});

test('showError lists the failure and reveals the banner; Dismiss hides it again', () => {
  for (const page of PAGES) {
    const { win, $, $$ } = load({ page, hold: ['pvmt.wasm'] });
    win.PvmtCommon.showError('data/x.json', 'HTTP 500');
    assert.equal($('#error-banner').hidden, false, page);
    assert.deepEqual($$('#error-banner-list li').map((li) => li.textContent), ['data/x.json (HTTP 500)'], page);

    $('#error-banner-dismiss').click();
    assert.equal($('#error-banner').hidden, true, page + ': Dismiss left the banner up');

    win.PvmtCommon.showError('board data');
    assert.equal($('#error-banner').hidden, false, page + ': a later error did not bring the banner back');
    assert.equal($$('#error-banner-list li').at(-1).textContent, 'board data', page);
  }
});

test('loadJSON: parsed JSON on success; a failure is reported and resolves to null', async () => {
  for (const page of PAGES) {
    const { win, $$ } = load({ page, hold: ['pvmt.wasm'], data: { 'data/ok.json': { a: 1 } } });
    // Only this spec's own requests: the page's boot loads report theirs too.
    const lines = () => $$('#error-banner-list li').map((li) => li.textContent).filter((l) => l.startsWith('data/'));

    assert.deepEqual(JSON.parse(JSON.stringify(await win.PvmtCommon.loadJSON('data/ok.json'))), { a: 1 }, page);
    assert.deepEqual(lines(), [], page);

    assert.equal(await win.PvmtCommon.loadJSON('data/optional.json', { silent404: true }), null, page);
    assert.deepEqual(lines(), [], page + ': a silent 404 was reported');

    assert.equal(await win.PvmtCommon.loadJSON('data/missing.json'), null, page);
    assert.deepEqual(lines(), ['data/missing.json (HTTP 404)'], page);
  }
});

test('callWasm unwraps the result and reports an error envelope, a throw or a missing export', () => {
  for (const page of PAGES) {
    const { win, $$ } = load({ page, hold: ['pvmt.wasm'] });
    const lines = () => $$('#error-banner-list li').map((li) => li.textContent);
    const call = win.PvmtCommon.callWasm;

    win.gameTick = (dt) => JSON.stringify({ year: dt * 2 });
    assert.equal(call('gameTick', [1.5]).year, 3, page);
    assert.deepEqual(lines(), [], page);

    win.gameTick = () => JSON.stringify({ error: 'no game' });
    assert.equal(call('gameTick', [1]), null, page);
    win.gameTick = () => { throw new Error('boom'); };
    assert.equal(call('gameTick', [1], 'the clock'), null, page);
    win.gameTick = () => 'not json';
    assert.equal(call('gameTick', [1]), null, page);
    assert.equal(call('gameNope', []), null, page);

    const got = lines();
    assert.equal(got[0], 'gameTick (no game)', page);
    assert.equal(got[1], 'the clock (boom)', page);
    assert.match(got[2], /^gameTick \(invalid response: /, page);
    assert.equal(got[3], 'gameNope (WASM function not registered)', page);
  }
});

test('the dashboard reports a simulator error under one name', async () => {
  const h = load({ data: fullData, simulate: () => ({ error: 'bad tiers' }) });
  await h.flush();
  h.win.onWasmReady();
  await h.flush();
  assert.ok(h.$$('#error-banner-list li').some((li) => li.textContent === 'forecast simulator (bad tiers)'));
});

test('loadWasm fetches the Go runtime itself when the page has not loaded it', async () => {
  for (const page of PAGES) {
    const { win, $$, flush } = load({ page, goRuntime: false });
    await flush();
    // The dashboard boots the simulator when a tab that runs it opens.
    if (page === 'index') { win.selectTab('financials-tab'); await flush(); }
    assert.equal($$('head script[src="wasm_exec.js"]').length, 1, page);
    // A second caller joins the same boot rather than starting another.
    win.PvmtCommon.loadWasm();
    assert.equal($$('head script[src="wasm_exec.js"]').length, 1, page);
  }
});

test('loadWasm boots the WASM once and hands over to the page', async () => {
  const index = load({ data: fullData, goRuntime: true, wasm: true });
  await index.flush();
  index.win.selectTab('financials-tab');
  await index.flush();
  assert.equal(index.get('wasmReady'), true, 'the dashboard never learned the simulator was up');
  await index.win.PvmtCommon.loadWasm();
  assert.equal(index.win.__goRuns, 1);

  const play = load({ page: 'play', wasm: true });
  await play.flush();
  assert.equal(play.win.__goRuns, 1);
  assert.equal(play.$('#error-banner').hidden, true);
});

test('a WASM that fails to load is reported on both pages', async () => {
  for (const page of PAGES) {
    // The runtime is there but pvmt.wasm 404s through the fetch stub.
    const { win, $, $$, flush } = load({ page, goRuntime: true });
    await flush();
    if (page === 'index') { win.selectTab('financials-tab'); await flush(); }
    assert.equal($('#error-banner').hidden, false, page);
    assert.ok($$('#error-banner-list li').some((li) => li.textContent.startsWith('forecast WASM (')), page);
  }
});

// The dashboard's Map tab never runs the forecast simulator, so opening on it
// leaves pvmt.wasm unfetched until the map has drawn (its 'idle' event, then
// the browser's idle time) or a tab that runs it opens.
test('the dashboard fetches pvmt.wasm after the map draws or a forecast tab opens', async () => {
  const wasmFetched = (h) => h.fetches().some((p) => p.endsWith('pvmt.wasm'));

  const onMap = load({ data: fullData, goRuntime: true, wasm: true });
  await onMap.flush();
  assert.equal(wasmFetched(onMap), false, 'the Map tab fetched pvmt.wasm before the map drew');
  onMap.fireMap('idle');
  await new Promise((r) => setTimeout(r, 10));
  await onMap.flush();
  assert.equal(onMap.get('wasmReady'), true, 'the simulator did not boot once the map was idle');

  const onTab = load({ data: fullData, goRuntime: true, wasm: true });
  await onTab.flush();
  onTab.win.selectTab('financials-tab');
  await onTab.flush();
  assert.equal(onTab.get('wasmReady'), true, 'opening Financials did not boot the simulator');

  // A deep link straight to a forecast tab boots it on first load, without
  // waiting for the map.
  for (const tab of ['financials-tab', 'materials-tab', 'compare-tab', 'aggregate-tab']) {
    const deep = load({ data: fullData, goRuntime: true, wasm: true, url: 'https://example.test/index.html?tab=' + tab });
    await deep.flush();
    assert.equal(deep.get('wasmReady'), true, 'a ?tab=' + tab + ' deep link did not boot the simulator');
  }
});
