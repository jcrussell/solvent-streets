        // ---- Template-injected payload (same wiring as index.html.tmpl) -------
        // Config injected by the template as window.PVMT_CONFIG (see
        // TemplateData.GameConfigJSON in internal/export/meta.go).
        const PVMT_CONFIG = window.PVMT_CONFIG;
        const CENTER = PVMT_CONFIG.center;
        // The Go package's forecast defaults, for a value the city's seed
        // omits. The seed itself is the city's forecast_seed.json, fetched at
        // boot; every forecast setting the board uses comes from it.
        const FORECAST_DEFAULTS = PVMT_CONFIG.forecast_defaults;
        // Single-city: data files are at data/<file> (DATA_PREFIX ''). Multi-city:
        // the live server injects the active city's slug (it re-renders per
        // ?city=<slug>); a static host leaves ActiveSlug empty, so the block below
        // resolves the city client-side from ?city= against the selector options.
        let DATA_PREFIX = PVMT_CONFIG.data_prefix;
        // Back-to-map target: absolute on the live server, relative on a static
        // host so navigation stays inside the exported tree wherever it is
        // mounted (e.g. a project subpath like /solvent-streets/).
        const MAP_HREF = PVMT_CONFIG.map_href;
        // mapHref is MAP_HREF carrying the active city and tag filters, so
        // the Map links (top bar and end banner) land on the dashboard's view
        // of the same selection — the reverse of the dashboard's Play link
        // (syncPlayLink in app.js). syncMapLinks keeps both in step.
        let mapHref = MAP_HREF;
        /** @param {string} slug @param {string[]} tags dimension:value pairs */
        function syncMapLinks(slug, tags) {
            const p = new URLSearchParams();
            if (slug) p.set('city', slug);
            tags.forEach(t => p.append('tag', t));
            const qs = p.toString();
            mapHref = MAP_HREF + (qs ? '?' + qs : '');
            for (const a of document.querySelectorAll('a.map-link')) a.setAttribute('href', mapHref);
        }
        // Money formatting, the error banner and the JSON fetch wrapper are the
        // shared ones from common.js, which the page loads before this script.
        const { fmtMoney, showError, loadJSON } = PvmtCommon;

        // Typed DOM accessors — same pattern as app.js: one audited type-assertion
        // each so the call sites below read straight (el.value / el.dataset / …)
        // under tsc --checkJs instead of casting inline. Non-null by design; the
        // few optional nodes keep their own runtime `if (el)` guards.
        /** @param {string} id @returns {HTMLInputElement} */
        function inputById(id) { return /** @type {HTMLInputElement} */ (document.getElementById(id)); }
        /** @param {string} id @returns {HTMLSelectElement} */
        function selectById(id) { return /** @type {HTMLSelectElement} */ (document.getElementById(id)); }
        /** @param {string} sel @param {ParentNode} [root] @returns {HTMLElement[]} */
        function queryAll(sel, root) { return /** @type {HTMLElement[]} */ (Array.from((root || document).querySelectorAll(sel))); }

        // City dropdown (multi-city only). Apply the ?tag= filters first (the
        // shared PvmtSelectors logic from selectors.js rebuilds the selector to
        // the matching cities; a deep-linked ?city= outside the filters wins and
        // the filters are dropped), then resolve the active city from ?city=
        // (validated against the selector's own options), then reload to
        // <current-path>?city=<slug> on change — a full reload is the clean reset
        // for the stateful board (MapLibre source + WASM game state + timers), and
        // using location.href keeps us on play.html (static) or /play (server).
        // A filter change that keeps the current city only rewrites the URL.
        // navigateTo is the one place the play page leaves for another URL; a
        // function declaration so the jsdom specs can observe it (jsdom cannot
        // navigate).
        /** @param {URL} u */
        function navigateTo(u) {
            // Test seam: jsdom cannot navigate, so the harness installs
            // window.__pvmtNavigate to observe where the page would have gone.
            if (typeof window.__pvmtNavigate === 'function') { window.__pvmtNavigate(u); return; }
            location.assign(u);
        }
        {
            const sel = selectById('city-select');
            if (sel) {
                const cities = PVMT_CONFIG.cities || [];
                const dims = PVMT_CONFIG.tag_dimensions;
                const params = new URLSearchParams(location.search);
                const want = params.get('city');
                let filters = PvmtSelectors.parseTags(params, dims);
                const linked = want ? cities.find(c => c.slug === want) : null;
                if (linked && !PvmtSelectors.matches(linked.tags, filters)) filters = {};
                PvmtSelectors.writeFilters(filters);
                // On the live server the page was rendered FOR one city: its
                // data prefix, HUD title and seed are baked in. Keep that city
                // selected through the rebuild; if the filters (or an unknown
                // ?city=) leave the dropdown on a different city, the only
                // consistent state is a re-render, so navigate to it.
                const served = DATA_PREFIX.startsWith('cities/') ? DATA_PREFIX.slice('cities/'.length, -1) : '';
                // Keep a KNOWN ?city= selected through the rebuild; an unknown one
                // defers to the served city (or the first option on a static host).
                const prefer = (want && cities.some(c => c.slug === want)) ? want : (served || sel.value);
                if (cities.length > 0) PvmtSelectors.rebuildCitySelect(sel, cities, dims, filters, prefer);
                // With no city matching, the board behind the overlay is a city
                // the filters exclude, so Start stays disabled until one matches.
                const syncNoMatch = () => {
                    const empty = cities.length > 0 && !sel.value;
                    const btn = /** @type {HTMLButtonElement | null} */ (document.getElementById('start-btn'));
                    if (btn) btn.disabled = empty;
                    const note = document.getElementById('start-no-match');
                    if (note) note.hidden = !empty;
                };
                syncNoMatch();
                /** @param {string} slug @param {Record<string, string>} selected */
                const urlFor = (slug, selected) => {
                    const u = new URL(location.href);
                    u.searchParams.delete('tag');
                    PvmtSelectors.urlTags(selected, dims).forEach(t => u.searchParams.append('tag', t));
                    if (slug) u.searchParams.set('city', slug);
                    else u.searchParams.delete('city');
                    return u;
                };
                /** @param {string} slug @param {Record<string, string>} selected */
                const syncLinks = (slug, selected) => syncMapLinks(slug, PvmtSelectors.urlTags(selected, dims));
                const valid = new Set(Array.from(sel.options).map(o => o.value));
                if (served) {
                    if (sel.value && sel.value !== served) navigateTo(urlFor(sel.value, filters));
                } else if (want && valid.has(want)) {
                    DATA_PREFIX = 'cities/' + want + '/';
                    sel.value = want;
                } else if (!DATA_PREFIX && sel.value) {
                    // Static multi-city with no valid ?city=: default to the first
                    // option (a multi-city export has no root data/ dir).
                    DATA_PREFIX = 'cities/' + sel.value + '/';
                }
                // The heading names the city the board shows; on a static host
                // that can be a ?city= other than the one the page was built
                // to open on.
                const heading = document.getElementById('hud-city');
                const shown = sel.selectedOptions[0];
                if (heading && shown && shown.value) heading.textContent = shown.textContent;
                syncLinks(sel.value, filters);
                sel.addEventListener('change', () => {
                    if (!sel.value) return;
                    navigateTo(urlFor(sel.value, PvmtSelectors.readFilters()));
                });
                queryAll('select.tag-filter').forEach(f => {
                    f.addEventListener('change', () => {
                        const selected = PvmtSelectors.readFilters();
                        const before = sel.value;
                        const picked = cities.length > 0
                            ? PvmtSelectors.rebuildCitySelect(sel, cities, dims, selected, before)
                            : before;
                        syncNoMatch();
                        syncLinks(picked, selected);
                        if (picked && picked !== before) navigateTo(urlFor(picked, selected));
                        // An empty match names no city, so a reload lands on the
                        // same empty state rather than a ?city= that would win
                        // and drop the filters (the dashboard's rule too).
                        else history.replaceState(null, '', urlFor(picked, selected));
                    });
                });
            }
        }

        // ---- Tunables --------------------------------------------------------
        // Real-time → sim-time conversion: 1 sim-year ~ 3 real seconds.
        const SIM_YEARS_PER_SEC = 1 / 3;
        // Throttle WASM ticks to ~10/s regardless of frame rate.
        const TICK_INTERVAL_MS = 100;
        // Cap catch-up after the tab is backgrounded (rAF pauses, so the first
        // frame back reports a huge elapsed). Without this, one giant Tick
        // over-accrues backlog and can trip the loss ceiling — an instant,
        // spurious "game over" purely from tabbing away.
        const MAX_FRAME_MS = 250;
        // Red→green PCI ramp, band 0 (worst) … band N-1 (best); Go owns the
        // band assignment (game.BandForPCI), JS only maps band→color here. The
        // palette length must match Go's game.BandCount, injected below —
        // checked at boot so a Go-side change can't silently gray out bands.
        const BAND_COUNT = PVMT_CONFIG.band_count;
        const BAND_COLORS = ['#b2182b', '#ef8a62', '#fddbc7', '#a6d96a', '#66bd63', '#1a9850'];
        const GRAVEL_COLOR = '#8d8074';
        // The legend's ramp and gravel swatch are drawn from the same two
        // values the map paints with.
        function paintLegend() {
            for (const bar of queryAll('#legend .legend-bar')) {
                bar.style.backgroundImage = 'linear-gradient(90deg, ' + BAND_COLORS.join(', ') + ')';
            }
            for (const swatch of queryAll('#legend .gravel-swatch')) {
                swatch.style.backgroundColor = GRAVEL_COLOR;
            }
        }
        paintLegend();
        const HEX_SOURCE = 'play-board';
        const HEX_FILL = 'play-fill';

        // callWasm calls a window.gameXxx export of the forecast WASM and returns
        // its parsed state; a failure is shown in the error banner and returns
        // null.
        /** @param {string} fnName @param {...any} args */
        function callWasm(fnName, ...args) {
            return PvmtCommon.callWasm(fnName, args);
        }

        // ---- Map setup -------------------------------------------------------
        const map = new maplibregl.Map({
            container: 'map',
            style: {
                version: 8,
                sources: { osm: { type: 'raster', tiles: ['https://tile.openstreetmap.org/{z}/{x}/{y}.png'], tileSize: 256, attribution: '&copy; OpenStreetMap' } },
                layers: [{ id: 'osm', type: 'raster', source: 'osm', minzoom: 0, maxzoom: 19 }]
            },
            center: CENTER,
            zoom: 13
        });
        map.addControl(new maplibregl.NavigationControl(), 'top-right');
        // styleReady resolves once the style is parsed, which is all the board
        // needs (addSource/addLayer). 'load' alone is not enough: it waits for
        // every first-view basemap tile, so a tile server that hangs or is
        // unreachable would leave "Loading board…" up for good.
        // Registered here, at construction, so neither event can fire unheard.
        const styleReady = new Promise(resolve => {
            map.once('style.load', resolve);
            map.once('load', resolve);
        });

        // ---- Game state (JS holds only view/loop state, never rules) ---------
        // Treatments are always automatic: the engine picks the right tier per hex by
        // its condition. There is no manual tier selector, so this is fixed at 'auto'.
        const selectedTier = 'auto';
        let gameStatus = 'running';
        let rafId = null;
        let lastFrameTs = 0;
        let tickAccumMs = 0;
        // simSpeed multiplies sim-time per tick (1×/2×/4×); a view preference, so
        // it is NOT part of gameConfig and survives reset/horizon changes.
        let simSpeed = 1;
        // gameConfig is the last config handed to gameInit; reset()/horizon reuse
        // it. lastState/peakBacklog feed the end-of-game summary.
        let gameConfig = null;
        let lastState = null;
        let peakBacklog = 0;
        // Paint-brush view state. The brush is always on: left-drag paints, so
        // dragPan is disabled and right-drag pans instead (see boot). brushSize is
        // the pixel radius; brushStroke dedupes hexes within one drag so each is
        // treated at most once. panning/panLast track an in-progress right-drag pan.
        let brushSize = 24;
        let painting = false;
        let brushStroke = null;
        let panning = false;
        let panLast = null;

        // applyState: O(changed) repaint via setFeatureState + HUD refresh.
        // Colors come entirely from Go's band output (see BAND_COLORS / paint).
        function applyState(state) {
            if (!state) return;
            lastState = state;
            if (Array.isArray(state.changed)) {
                for (const c of state.changed) {
                    map.setFeatureState(
                        { source: HEX_SOURCE, id: c.id },
                        { band: c.band, closed: !!c.closed }
                    );
                }
            }
            updateHUD(state);
            if (state.status && state.status !== gameStatus) {
                gameStatus = state.status;
                onStatusChange(gameStatus);
            }
        }

        function updateHUD(state) {
            if (state.deferred_backlog != null && state.deferred_backlog > peakBacklog) peakBacklog = state.deferred_backlog;
            const endless = !!(gameConfig && gameConfig.endless);
            const horizon = gameConfig ? gameConfig.horizon_years : FORECAST_DEFAULTS.horizon_years;
            const yr = state.year != null ? state.year.toFixed(1) : '0';
            document.getElementById('hud-year').textContent =
                endless ? (yr + ' (Endless)') : (yr + (horizon ? ' / ' + horizon : ''));
            const treasuryEl = document.getElementById('hud-treasury');
            treasuryEl.textContent = fmtMoney(state.treasury);
            // Out of funds: can't afford the cheapest treatment anywhere (Go computes
            // this). Flag the treasury red and show the standing warning.
            const broke = !!state.out_of_funds;
            treasuryEl.classList.toggle('broke', broke);
            document.getElementById('hud-funds-warn').style.display = broke ? 'block' : 'none';
            document.getElementById('hud-pci').textContent =
                state.network_pci != null ? state.network_pci.toFixed(1) : '—';
            document.getElementById('hud-backlog').textContent = fmtMoney(state.deferred_backlog);
        }

        /**
         * lossText is the end banner's headline and explanation for a lost
         * game, from the condition Go reports as loss_reason. A backlog loss
         * with a treasury still holding a year's budget (or with nothing ever
         * treated) is money left unspent, not a budget decay outran: the
         * treasury only pays for the roads the player treats.
         * @param {any} s the final game state
         * @returns {{headline: string, sub: string}}
         */
        function lossText(s) {
            if (s.loss_reason === 'pci_floor') {
                return { headline: 'Roads failed.', sub: 'Network PCI fell below the failing floor — game over.' };
            }
            if (s.loss_reason === 'closures') {
                return { headline: 'Roads closed.', sub: 'More than half the network has closed to gravel — game over.' };
            }
            const treated = s.treatment_count || 0;
            // Only money actually left over is "unspent": at a $0 budget there
            // was never any to spend.
            const unspent = s.treasury > 0 && (treated === 0 || s.treasury >= s.annual_budget);
            if (s.loss_reason === 'backlog' && unspent) {
                return {
                    headline: 'Network insolvent.',
                    sub: fmtMoney(s.treasury) + ' went unspent' +
                        (treated === 0 ? ' and no roads were treated' : '') +
                        ', so deferred repairs piled up. The treasury only pays for roads you treat: drag over the map to treat them.'
                };
            }
            return { headline: 'Network insolvent.', sub: 'Deferred repairs outran the budget — game over.' };
        }

        function onStatusChange(status) {
            if (status === 'running') return;
            stopLoop();
            const s = lastState || {};
            // The share of the board's road area that closed, as the
            // closures loss measures it.
            const gravelPct = 100 * (s.closed_share || 0);
            const won = status === 'won';
            const row = (k, v) => '<div class="row"><span class="k">' + k + '</span><span class="v">' + v + '</span></div>';
            const text = won
                ? { headline: 'You stayed solvent!', sub: 'The network held through the horizon.' }
                : lossText(s);
            const banner = document.getElementById('banner');
            banner.className = won ? 'won' : 'lost';
            banner.innerHTML =
                '<div class="headline">' + text.headline + '</div>' +
                '<div class="sub">' + text.sub + '</div>' +
                '<div class="banner-stats">' +
                    row('Years', (s.year != null ? s.year.toFixed(1) : '—')) +
                    row('Final PCI', (s.network_pci != null ? s.network_pci.toFixed(1) : '—')) +
                    row('Total spent', fmtMoney(s.total_spend)) +
                    row('Treatments', (s.treatment_count || 0).toLocaleString()) +
                    row('Gravel', gravelPct.toFixed(0) + '%') +
                    row('Peak backlog', fmtMoney(peakBacklog)) +
                '</div>' +
                '<div class="banner-actions">' +
                    '<button class="btn" type="button" id="play-again">Play again</button>' +
                    '<a class="btn map-link" href="' + mapHref + '">&larr; Map</a>' +
                '</div>';
            document.getElementById('play-again').addEventListener('click', showStartScreen);
        }

        // seedGame (re)initializes the engine from gameConfig (gameInit returns every
        // hex, so the board fully repaints) and starts the clock. Shared by reset()
        // (in-game restart) and startGame() (first run from the start screen). No
        // engine reset export is needed — gameInit replaces the game. Returns false
        // if gameInit failed (error already surfaced).
        function seedGame() {
            const initState = callWasm('gameInit', JSON.stringify(gameConfig));
            if (!initState) return false;
            gameStatus = 'running';
            peakBacklog = 0;
            // Normalize the segmented Speed control to the running speed (clears any
            // paused highlight, re-marks the active speed).
            setRate(simSpeed);
            stopLoop();
            applyState(initState); // changed == every hex → full repaint
            // The annual budget is chosen on the start overlay and baked into
            // gameConfig.annual_budget, so gameInit already seeds the engine at
            // the right rate — no post-seed budget sync needed.
            startLoop();
            return true;
        }

        function clearBanner() {
            const banner = document.getElementById('banner');
            banner.className = ''; banner.innerHTML = '';
        }

        // setInGameUI toggles between the pre-game overlay and the in-game
        // panels. New panels belong here so startGame/showStartScreen stay in
        // sync.
        // playing is true while the in-game UI is up: the start overlay hides
        // the board between games, and Play again re-seeds a 'running' engine
        // behind it, so painting checks this as well as the game status.
        let playing = false;
        function setInGameUI(inGame) {
            playing = inGame;
            document.getElementById('start-overlay').style.display = inGame ? 'none' : 'block';
            for (const id of ['hud', 'controls', 'legend'])
                document.getElementById(id).style.display = inGame ? 'block' : 'none';
        }

        // reset is the in-game Reset button: a fast same-horizon restart. Clears the
        // win/lose banner and re-seeds without returning to the start screen.
        function reset() {
            if (!gameConfig) return;
            clearBanner();
            seedGame();
        }

        // startGame launches play from the start screen: reveal the HUD/controls and
        // seed the engine with the chosen horizon.
        function startGame() {
            if (!gameConfig) return;
            setInGameUI(true);
            showHint();
            seedGame();
        }

        // showStartScreen returns to the pre-game start overlay ("Play again"
        // after a game ends) so the horizon can be (re)chosen. The finished
        // game's board and HUD would otherwise linger behind the overlay, so
        // the engine is re-seeded (clock stopped) to show a fresh board.
        function showStartScreen() {
            stopLoop();
            clearBanner();
            setInGameUI(false);
            document.getElementById('brush-cursor').style.display = 'none';
            if (gameConfig && gameStatus !== 'running') {
                const fresh = callWasm('gameInit', JSON.stringify(gameConfig));
                if (fresh) { peakBacklog = 0; applyState(fresh); }
            }
        }

        // ---- Clock -----------------------------------------------------------
        function frame(ts) {
            if (!lastFrameTs) lastFrameTs = ts;
            const elapsedMs = Math.min(ts - lastFrameTs, MAX_FRAME_MS);
            lastFrameTs = ts;
            tickAccumMs += elapsedMs;
            if (tickAccumMs >= TICK_INTERVAL_MS) {
                const dtYears = (tickAccumMs / 1000) * SIM_YEARS_PER_SEC * simSpeed;
                tickAccumMs = 0;
                const state = callWasm('gameTick', dtYears);
                if (state) applyState(state);
                if (gameStatus !== 'running') return; // applyState already stopped the loop
            }
            rafId = requestAnimationFrame(frame);
        }
        function startLoop() {
            if (rafId != null || gameStatus !== 'running') return;
            lastFrameTs = 0; tickAccumMs = 0;
            rafId = requestAnimationFrame(frame);
        }
        function stopLoop() {
            if (rafId != null) { cancelAnimationFrame(rafId); rafId = null; }
        }

        // setRate drives the segmented Pause/1×/2×/4× control: rate 0 pauses, any
        // other value sets the sim-speed multiplier and (re)starts the clock. The
        // four states are mutually exclusive, so exactly one button is highlighted.
        function setRate(rate) {
            if (rate === 0) {
                stopLoop();
            } else {
                simSpeed = rate;
                startLoop(); // no-op if the game is over or already looping
            }
            queryAll('#speed-buttons .btn')
                .forEach(b => b.classList.toggle('active', Number(b.dataset.speed) === rate));
        }

        // ---- Paint brush -----------------------------------------------------
        // The brush is always on: left-drag paints treatments (dragPan is disabled in
        // boot, right-drag pans instead). Each move treats every hex under a screen
        // box of radius brushSize via one gameTreatBatch call; a per-stroke Set keeps
        // each hex treated at most once.
        function brushHexesAt(pt) {
            const r = brushSize;
            const box = (r <= 1) ? pt : [[pt.x - r, pt.y - r], [pt.x + r, pt.y + r]];
            return map.queryRenderedFeatures(box, { layers: [HEX_FILL] }).map(f => f.id);
        }
        function brushPaint(pt) {
            if (!playing || gameStatus !== 'running' || !brushStroke) return;
            const ids = [];
            for (const id of brushHexesAt(pt)) {
                const key = String(id);
                if (id != null && !brushStroke.has(key)) { brushStroke.add(key); ids.push(key); }
            }
            if (!ids.length) return; // all hexes under the brush already painted this stroke
            const state = callWasm('gameTreatBatch', JSON.stringify(ids), selectedTier);
            if (state) applyState(state);
            if (state && state.treatment_count > 0 && !hintDismissed) dismissHint();
        }
        function brushCursorAt(pt) {
            const cur = document.getElementById('brush-cursor');
            const rect = map.getCanvas().getBoundingClientRect();
            const d = Math.max(10, brushSize * 2);
            cur.style.width = d + 'px';
            cur.style.height = d + 'px';
            cur.style.left = (rect.left + pt.x) + 'px';
            cur.style.top = (rect.top + pt.y) + 'px';
            cur.style.display = 'block';
        }

        // ---- First-run hint --------------------------------------------------
        // The only instructions the board needs: how to treat roads, worded
        // for the input in hand (a coarse pointer is a finger: one-finger
        // drag paints, pinch zooms; a mouse left-drags to paint and
        // right-drags to pan). Shown on Start until it is dismissed or the
        // first road is treated, and not again once either has happened
        // (remembered in localStorage when storage works).
        const HINT_KEY = 'pvmtPlayHintSeen';
        const TOUCH = typeof window.matchMedia === 'function' && window.matchMedia('(pointer: coarse)').matches;
        function hintSeen() {
            try { return localStorage.getItem(HINT_KEY) === '1'; } catch (e) { return false; }
        }
        let hintDismissed = hintSeen();
        function dismissHint() {
            hintDismissed = true;
            document.getElementById('onboard-hint').hidden = true;
            try { localStorage.setItem(HINT_KEY, '1'); } catch (e) { /* storage disabled */ }
        }
        function wireHint() {
            document.getElementById('onboard-text').textContent = TOUCH
                ? 'Drag a finger over the roads to treat them; the treasury only pays for roads you treat. Pinch to zoom.'
                : 'Drag over the roads to treat them; the treasury only pays for roads you treat. Right-drag to pan, scroll to zoom.';
            document.getElementById('brush-hint').textContent = TOUCH
                ? '(drag to treat, pinch to zoom)'
                : '(left-drag to treat, right-drag to pan)';
            document.getElementById('onboard-dismiss').addEventListener('click', dismissHint);
        }
        function showHint() {
            document.getElementById('onboard-hint').hidden = hintDismissed;
        }

        // wireControls binds the in-game controls (reset, speed, brush size). Called
        // from boot once gameConfig exists (reset mutates it).
        function wireControls() {
            wireHint();
            document.getElementById('reset-btn').addEventListener('click', reset);
            // Segmented Pause/1×/2×/4× control.
            queryAll('#speed-buttons .btn').forEach(b =>
                b.addEventListener('click', () => setRate(Number(b.dataset.speed))));
            const size = inputById('brush-size');
            brushSize = parseInt(size.value, 10) || 0;
            size.addEventListener('input', () => { brushSize = parseInt(size.value, 10) || 0; });
        }

        // wireStartOverlay binds the pre-game start screen: the horizon buttons and
        // the annual-budget slider mutate gameConfig (horizon feeds the win
        // condition; budget is the fixed funding rate baked into annual_budget),
        // and each change refreshes the projected-insolvency preview. The Start
        // button launches play. Called from boot once gameConfig exists;
        // defaultBudget is the forecast-derived rate (pickStartingBudget).
        /**
         * @param {number} defaultBudget
         * @param {number} holdSteady roads break-even budget, or 0 when unknown
         */
        function wireStartOverlay(defaultBudget, holdSteady) {
            const hwrap = document.getElementById('horizon-buttons');
            offerHorizon(hwrap, gameConfig.horizon_years);
            const markActive = () => queryAll('.btn', hwrap).forEach(b => {
                const h = b.dataset.horizon;
                const on = (h === 'endless') ? !!gameConfig.endless
                    : (!gameConfig.endless && Number(h) === gameConfig.horizon_years);
                b.classList.toggle('active', on);
            });
            queryAll('.btn', hwrap).forEach(b => b.addEventListener('click', () => {
                const h = b.dataset.horizon;
                if (h === 'endless') gameConfig.endless = true;
                else { gameConfig.endless = false; gameConfig.horizon_years = Number(h); }
                markActive();
                refreshProjection(); // horizon changes the projected insolvency year
            }));
            markActive(); // highlight the default horizon on boot

            // Annual-budget slider: 0..2x the larger of the forecast-derived default
            // and the hold-steady (break-even) budget, so a solvent budget is always
            // reachable even when today's budget is far below it. Sets
            // gameConfig.annual_budget and refreshes the preview; there is no
            // in-game budget slider.
            const slider = inputById('start-budget-slider');
            const out = document.getElementById('start-budget-value');
            const max = Math.max(1, 2 * Math.max(defaultBudget, holdSteady || 0));
            slider.min = '0';
            slider.max = String(max);
            slider.step = String(Math.max(1, Math.round(max / 200)));
            slider.value = String(defaultBudget);
            out.textContent = fmtMoney(defaultBudget);
            slider.addEventListener('input', () => {
                const rate = parseFloat(slider.value);
                gameConfig.annual_budget = rate;
                out.textContent = fmtMoney(rate);
                refreshProjection();
            });

            refreshProjection(); // initial preview for the default budget + horizon
            document.getElementById('start-btn').addEventListener('click', startGame);
        }

        /**
         * offerHorizon adds a button for the city's own horizon when the
         * rendered start screen lacks one: the page renders its horizons from
         * the page's settings, which on a multi-city site are the region's.
         * The button goes in ascending order, before Endless.
         * @param {HTMLElement} wrap
         * @param {number} years
         */
        function offerHorizon(wrap, years) {
            const buttons = queryAll('.btn', wrap);
            if (!(years > 0) || buttons.some(b => Number(b.dataset.horizon) === years)) return;
            const btn = document.createElement('button');
            btn.className = 'btn';
            btn.type = 'button';
            btn.dataset.horizon = String(years);
            btn.textContent = years + 'y';
            const after = buttons.find(b => b.dataset.horizon === 'endless' || Number(b.dataset.horizon) > years);
            wrap.insertBefore(btn, after || null);
        }

        // ---- Pregame insolvency projection preview ---------------------------
        // refreshProjection updates the start-overlay "at this budget" line by
        // running the macro insolvency forecast in WASM for the current budget and
        // horizon, WITHOUT a board: the slim payload omits hexes so a 5k-hex city
        // isn't serialized on every slider tick. The forecast is cohort-based and
        // hex-count-independent, so it is cheap; the short debounce just coalesces
        // rapid drags. No insolvency_year => solvent through the horizon.
        let projectionTimer = null;
        function refreshProjection() {
            if (!gameConfig) return;
            if (projectionTimer) clearTimeout(projectionTimer);
            projectionTimer = setTimeout(() => {
                const slim = {
                    cohorts: gameConfig.cohorts,
                    initial_pci: gameConfig.initial_pci,
                    cost_tiers: gameConfig.cost_tiers,
                    cost_overhead: gameConfig.cost_overhead,
                    annual_budget: gameConfig.annual_budget,
                    horizon_years: gameConfig.horizon_years,
                    treatment_cycle_years: gameConfig.treatment_cycle_years,
                    growth_rate: gameConfig.growth_rate
                };
                const res = callWasm('gameProjectInsolvency', JSON.stringify(slim));
                const el = document.getElementById('start-projection');
                if (!el) return;
                if (!res) { el.className = ''; el.textContent = ''; return; }
                const yr = res.insolvency_year;
                if (yr == null) {
                    el.className = 'ok';
                    // Endless play has no fixed horizon, so "solvent through the
                    // horizon" would misdescribe it — the projection only covers a
                    // finite preview window (horizon_years). Word it as a floor.
                    el.textContent = gameConfig.endless
                        ? 'Solvent for at least ' + gameConfig.horizon_years + ' years at this budget'
                        : 'Solvent through the horizon at this budget';
                } else {
                    el.className = 'bad';
                    el.innerHTML = 'Projected insolvent by <span class="num">yr ' + yr + '</span> at this budget';
                }
            }, 60);
        }

        // ---- Board build + boot ----------------------------------------------
        /**
         * The roads forecast's hold-steady (break-even) budget, or 0.
         * @param {any} forecast
         * @returns {number}
         */
        function pickHoldSteady(forecast) {
            if (!Array.isArray(forecast)) return 0;
            const roads = forecast.find(f => f.resource_type === 'roads');
            return (roads && roads.break_even_budget > 0) ? roads.break_even_budget : 0;
        }

        /**
         * The budget the start screen opens at: the city's cited annual street
         * budget (forecast.json carries current_budget on the roads entry
         * only), else the roads hold-steady budget, which is where the
         * Financials budget slider opens for a city with no cited budget.
         * @param {any} forecast
         * @returns {number}
         */
        function pickStartingBudget(forecast) {
            const roads = Array.isArray(forecast) ? forecast.find(f => f.resource_type === 'roads') : null;
            if (roads && roads.current_budget > 0) return roads.current_budget;
            return pickHoldSteady(forecast);
        }

        // HEXGRID_VERSION is the hexgrid.geojson "v" this page reads
        // (export.HexGridFileVersion, injected); app.js gates on the same value.
        const HEXGRID_VERSION = PVMT_CONFIG.hexgrid_version;
        /**
         * playHex reads one hexgrid feature's properties as a board hex
         * {id, roads_area, decay_rate}, or null when it is not on the board. A
         * board hex carries k, its decay rate (city-jurisdiction roads); its
         * roads area is the city-scope roads area, read through city_same the
         * way the map reads it.
         * @param {any} props
         * @returns {{id: string, roads_area: number, decay_rate: number} | null}
         */
        function playHex(props) {
            if (!props || props.id == null || !(props.k > 0)) return null;
            const city = props.city_same ? props.all : props.city;
            const roads = city && city.roads;
            if (!roads || !(roads.area >= 0)) return null;
            return { id: props.id, roads_area: roads.area, decay_rate: props.k };
        }

        async function boot() {
            // Palette ↔ Go band-count guard (see BAND_COLORS): surface a loud
            // error instead of silently graying out out-of-range bands.
            if (BAND_COLORS.length !== BAND_COUNT) {
                showError('band palette', 'BAND_COLORS has ' + BAND_COLORS.length +
                    ' entries but Go game.BandCount is ' + BAND_COUNT);
                return;
            }

            // 1. Fetch the board (hexgrid.geojson: geometry, road area, k), the
            //    budget (forecast.json) and the forecast inputs
            //    (forecast_seed.json), all three from the SAME city. The page
            //    carries no seed of its own: on a multi-city site the only one it
            //    could carry is the region's, whose cohorts sum every city and
            //    would price this city's budget against the whole region.
            const [hexgrid, forecast, seed] = await Promise.all([
                loadJSON(DATA_PREFIX + 'data/hexgrid.geojson'),
                loadJSON(DATA_PREFIX + 'data/forecast.json'),
                loadJSON(DATA_PREFIX + 'data/forecast_seed.json')
            ]);
            if (!hexgrid || !hexgrid.features) {
                showError('board data', 'missing hexgrid');
                return;
            }
            // loadJSON has already said which file failed; with no forecast
            // inputs there is no game to start.
            if (!seed) return;
            // Same format gate as the map (app.js): a hexgrid of another shape
            // would read as an empty or wrong board with no error anywhere.
            if (hexgrid.v !== HEXGRID_VERSION) {
                showError('board data', 'unsupported hexgrid format (expected v' + HEXGRID_VERSION +
                    ') — re-export this site with a current pvmt');
                return;
            }

            // 2. The board is the hexgrid's city-jurisdiction road hexes: those
            //    carrying k (their decay rate). Road area is the map's own
            //    city-scope roads area, so Play bills exactly what the map shows.
            const playById = new Map();
            const features = [];
            for (const f of hexgrid.features) {
                const ph = playHex(f.properties);
                if (!ph) continue;                                    // out-of-bounds / non-playable
                playById.set(ph.id, ph);
                features.push({ type: 'Feature', id: ph.id, geometry: f.geometry, properties: { id: ph.id } });
            }
            if (features.length === 0) {
                showError('board data', 'no city road hexes in hexgrid');
                return;
            }

            // 3. Add the board source (promoteId so feature ids == properties.id
            //    for setFeatureState) and a single fill layer driven by Go bands.
            map.addSource(HEX_SOURCE, {
                type: 'geojson',
                data: { type: 'FeatureCollection', features },
                promoteId: 'id'
            });
            map.addLayer({
                id: HEX_FILL, type: 'fill', source: HEX_SOURCE,
                paint: {
                    'fill-color': [
                        'case',
                        ['==', ['feature-state', 'closed'], true], GRAVEL_COLOR,
                        // match arms built from BAND_COLORS so the paint rule
                        // can't drift from the palette.
                        ['match', ['feature-state', 'band'],
                            ...BAND_COLORS.flatMap((color, band) => [band, color]),
                            '#555c6a' // default before first paint
                        ]
                    ],
                    'fill-opacity': 0.72
                }
            });
            map.addLayer({
                id: HEX_FILL + '-outline', type: 'line', source: HEX_SOURCE,
                paint: { 'line-color': '#1a1f29', 'line-width': 0.4, 'line-opacity': 0.45 }
            });

            // Fit the map to the ACTUAL board rather than the embedded region
            // CENTER (which, in multi-city static export, is the region centroid —
            // off-screen for wide configs). The initial center/zoom-13 is just a
            // pre-fetch placeholder; this overrides it once the real board is known.
            // features is non-empty here (the empty case returned above).
            {
                const coords = features.flatMap(f => {
                    const g = f.geometry;
                    if (!g) return [];
                    if (g.type === 'Polygon') return g.coordinates.flatMap(ring => ring);
                    if (g.type === 'MultiPolygon') return g.coordinates.flatMap(poly => poly.flatMap(ring => ring));
                    return [];
                });
                if (coords.length > 0) {
                    const lngs = coords.map(c => c[0]);
                    const lats = coords.map(c => c[1]);
                    map.fitBounds([[Math.min(...lngs), Math.min(...lats)], [Math.max(...lngs), Math.max(...lats)]], { padding: 40 });
                }
            }

            // 4. Interaction: the brush is always on. Disable dragPan/dragRotate so
            //    left-drag paints (instead of panning) and right-drag pans the map;
            //    suppress the context menu so the right-drag doesn't pop one. Touch:
            //    one-finger paints, pinch zooms — one-finger touch-pan is dropped.
            map.dragPan.disable();
            map.dragRotate.disable();
            map.getCanvas().addEventListener('contextmenu', e => e.preventDefault());

            // startStroke routes by button: right (2) begins a pan, left (0)/touch
            // begins a paint stroke (resetting the per-stroke dedupe Set). moveStroke
            // tracks the cursor ring and either pans by the pixel delta or paints.
            const startStroke = e => {
                const btn = (e.originalEvent && typeof e.originalEvent.button === 'number') ? e.originalEvent.button : 0;
                if (btn === 2) { panning = true; panLast = e.point; return; }
                if (btn !== 0) return; // ignore the middle button
                painting = true; brushStroke = new Set(); brushPaint(e.point);
            };
            const moveStroke = e => {
                brushCursorAt(e.point);
                if (panning && panLast) {
                    map.panBy([panLast.x - e.point.x, panLast.y - e.point.y], { duration: 0 });
                    panLast = e.point;
                    return;
                }
                if (painting) brushPaint(e.point);
            };
            const endStroke = () => { painting = false; panning = false; panLast = null; };
            map.on('mousedown', startStroke);
            map.on('mousemove', moveStroke);
            map.on('touchstart', startStroke);
            map.on('touchmove', moveStroke);
            // End the stroke on release ANYWHERE: map.on('mouseup') only fires over
            // the canvas, so releasing over a panel (HUD/controls overlap the map)
            // would otherwise leave the brush "stuck on" and keep painting/panning.
            window.addEventListener('mouseup', endStroke);
            window.addEventListener('touchend', endStroke);
            window.addEventListener('touchcancel', endStroke);
            map.getCanvas().addEventListener('mouseleave', () => { document.getElementById('brush-cursor').style.display = 'none'; });

            // 5. Build the gameInit config from the city's seed fetched above, so
            //    cohorts/cost_tiers/initial_pci/years/treatment_cycle_years/
            //    growth_rate all come from the SAME city as annual_budget
            //    (pickStartingBudget below).
            const startingBudget = pickStartingBudget(forecast);
            // The game must simulate exactly the hexes it renders: build cfg.hexes
            // from `features`, so no hex decays, closes or drives the loss
            // conditions without a cell on the map to treat it.
            const gameHexes = features.map(f => {
                const ph = playById.get(f.id);
                return { id: ph.id, roads_area: ph.roads_area, decay_rate: ph.decay_rate };
            });
            // Macro insolvency headline needs cohorts. City-scope cohorts are the
            // first choice, but `city_cohorts` is omitempty — fall back to the
            // all-scope cohorts so the headline isn't permanently "solvent" (and
            // the budget slider isn't inert) for cities lacking city-scope stats.
            // Roads only: the board is road area, and the Financials insolvency
            // year it must agree with is the roads forecast's. The seed's cohort
            // lists also carry the parking and sidewalks classes, which billed
            // the road budget for lots and sidewalks and put Play's insolvency a
            // year early.
            /** @param {any[]} list */
            const roadCohorts = list => (list || []).filter(c => c.resource_type === 'roads');
            const cityRoads = roadCohorts(seed.city_cohorts);
            const macroCohorts = cityRoads.length ? cityRoads : roadCohorts(seed.cohorts);
            // Hoisted to module scope so reset() and the horizon buttons can
            // re-seed the game from the same config.
            gameConfig = {
                hexes: gameHexes,
                initial_pci: seed.initial_pci != null ? seed.initial_pci : FORECAST_DEFAULTS.initial_pci,
                cost_tiers: seed.cost_tiers || [],
                // The loaded-cost multiplier the forecast priced the budget
                // at; a seed without one is bare (0 reads as 1.0).
                cost_overhead: seed.cost_overhead || 0,
                annual_budget: startingBudget,
                horizon_years: seed.horizon_years || FORECAST_DEFAULTS.horizon_years,
                endless: false,
                treatment_cycle_years: seed.treatment_cycle_years || 0,
                growth_rate: seed.growth_rate || 0,
                cohorts: macroCohorts
            };

            // 6. Wire the in-game controls and the start screen (horizon + annual
            //    budget). Do NOT seed the engine or start the clock yet — the player
            //    picks the horizon and budget on the start overlay and presses Start
            //    (startGame() seeds). The budget default is the forecast-derived rate.
            wireControls();
            wireStartOverlay(startingBudget, pickHoldSteady(forecast));

            document.getElementById('loading').style.display = 'none';
            setInGameUI(false);
        }

        // ---- WASM boot, then board boot --------------------------------------
        function onWasmReady() {
            // Wait for the map style (not its tiles) before adding the board.
            return styleReady.then(boot);
        }

        PvmtCommon.loadWasm().then(onWasmReady)
            .catch(err => showError('forecast WASM', PvmtCommon.errorText(err)));
