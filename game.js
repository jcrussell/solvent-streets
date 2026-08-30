        // ---- Template-injected payload (same wiring as index.html.tmpl) -------
        // Config injected by the template as window.PVMT_CONFIG (see
        // TemplateData.GameConfigJSON in internal/export/meta.go).
        const PVMT_CONFIG = window.PVMT_CONFIG;
        const CENTER = PVMT_CONFIG.center;
        const FORECAST_SEED = PVMT_CONFIG.forecastSeed;
        // Single-city: data files are at data/<file> (DATA_PREFIX ''). Multi-city:
        // the live server injects the active city's slug (it re-renders per
        // ?city=<slug>); a static host leaves ActiveSlug empty, so the block below
        // resolves the city client-side from ?city= against the selector options.
        let DATA_PREFIX = PVMT_CONFIG.dataPrefix;
        // Back-to-map target: absolute on the live server, relative on a static
        // host so navigation stays inside the exported tree wherever it is
        // mounted (e.g. a project subpath like /solvent-streets/).
        const MAP_HREF = PVMT_CONFIG.mapHref;

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

        // City dropdown (multi-city only). Resolve the active city from ?city= on
        // boot (validated against the selector's own options), then reload to
        // <current-path>?city=<slug> on change — a full reload is the clean reset
        // for the stateful board (MapLibre source + WASM game state + timers), and
        // using location.href keeps us on play.html (static) or /play (server).
        {
            const sel = selectById('city-select');
            if (sel) {
                const valid = new Set(Array.from(sel.options).map(o => o.value));
                const want = new URLSearchParams(location.search).get('city');
                if (want && valid.has(want)) {
                    DATA_PREFIX = 'cities/' + want + '/';
                    sel.value = want;
                } else if (!DATA_PREFIX && sel.value) {
                    // Static multi-city with no valid ?city=: default to the first
                    // option (a multi-city export has no root data/ dir).
                    DATA_PREFIX = 'cities/' + sel.value + '/';
                }
                sel.addEventListener('change', () => {
                    const u = new URL(location.href);
                    u.searchParams.set('city', sel.value);
                    location.assign(u);
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
        const BAND_COUNT = PVMT_CONFIG.bandCount;
        const BAND_COLORS = ['#b2182b', '#ef8a62', '#fddbc7', '#a6d96a', '#66bd63', '#1a9850'];
        const GRAVEL_COLOR = '#8d8074';
        const HEX_SOURCE = 'play-hexes';
        const HEX_FILL = 'play-fill';

        // ---- Error surfacing -------------------------------------------------
        function showError(label, detail) {
            console.error('[play]', label, detail);
            const banner = document.getElementById('error-banner');
            const list = document.getElementById('error-banner-list');
            const li = document.createElement('li');
            li.textContent = label + (detail ? ' (' + detail + ')' : '');
            list.appendChild(li);
            banner.style.display = 'block';
        }

        // callWasm wraps a window.gameXxx function: stringify-free args in,
        // parsed state out. Surfaces {error} via the banner and returns null.
        function callWasm(fnName, ...args) {
            // window is indexed by a runtime string, so one honest `any` hop here;
            // WasmFn (globals.d.ts) then carries a real call signature through the
            // rest, and the guard narrows away the undefined case.
            /** @type {WasmFn|undefined} */
            const fn = /** @type {any} */ (window)[fnName];
            if (typeof fn !== 'function') {
                showError(fnName, 'WASM function not registered');
                return null;
            }
            let raw;
            try {
                raw = fn(...args);
            } catch (err) {
                showError(fnName, err.message || String(err));
                return null;
            }
            let parsed;
            try {
                parsed = JSON.parse(raw);
            } catch (err) {
                showError(fnName, 'invalid response: ' + (err.message || String(err)));
                return null;
            }
            if (parsed && parsed.error) {
                showError(fnName, parsed.error);
                return null;
            }
            return parsed;
        }

        async function loadJSON(url) {
            try {
                const resp = await fetch(url);
                if (!resp.ok) { showError(url, 'HTTP ' + resp.status); return null; }
                return await resp.json();
            } catch (err) {
                showError(url, err.message || String(err));
                return null;
            }
        }

        // ---- Formatting ------------------------------------------------------
        function fmtMoney(n) {
            if (n == null || isNaN(n)) return '$0';
            const neg = n < 0;
            const abs = Math.abs(n);
            let s;
            if (abs >= 1e9) s = '$' + (abs / 1e9).toFixed(2) + 'B';
            else if (abs >= 1e6) s = '$' + (abs / 1e6).toFixed(2) + 'M';
            else s = '$' + Math.round(abs).toLocaleString();
            return (neg ? '-' : '') + s;
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
            if (state.backlog != null && state.backlog > peakBacklog) peakBacklog = state.backlog;
            const endless = !!(gameConfig && gameConfig.endless);
            const horizon = gameConfig ? gameConfig.horizon_years : ((FORECAST_SEED && FORECAST_SEED.years) || 0);
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
            document.getElementById('hud-backlog').textContent = fmtMoney(state.backlog);
        }

        function onStatusChange(status) {
            if (status === 'running') return;
            stopLoop();
            const s = lastState || {};
            const hexCount = (gameConfig && gameConfig.hexes) ? gameConfig.hexes.length : 0;
            const gravelPct = hexCount ? (100 * (s.closed_count || 0) / hexCount) : 0;
            const won = status === 'won';
            const row = (k, v) => '<div class="row"><span class="k">' + k + '</span><span class="v">' + v + '</span></div>';
            const banner = document.getElementById('banner');
            banner.className = won ? 'won' : 'lost';
            banner.innerHTML =
                '<div class="headline">' + (won ? 'You stayed solvent!' : 'Network insolvent.') + '</div>' +
                '<div class="sub">' + (won ? 'The network held through the horizon.' : 'Decay outran the budget — game over.') + '</div>' +
                '<div class="banner-stats">' +
                    row('Years', (s.year != null ? s.year.toFixed(1) : '—')) +
                    row('Final PCI', (s.network_pci != null ? s.network_pci.toFixed(1) : '—')) +
                    row('Total spent', fmtMoney(s.spent)) +
                    row('Treatments', (s.treatments || 0).toLocaleString()) +
                    row('Gravel', gravelPct.toFixed(0) + '%') +
                    row('Peak backlog', fmtMoney(peakBacklog)) +
                '</div>' +
                '<div class="banner-actions">' +
                    '<button class="btn" type="button" id="play-again">Play again</button>' +
                    '<a class="btn" href="' + MAP_HREF + '">&larr; Map</a>' +
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
            // gameConfig.starting_budget, so gameInit already seeds the engine at
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
        function setInGameUI(inGame) {
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
            seedGame();
        }

        // showStartScreen returns to the pre-game start overlay (first boot, and
        // "Play again" after a game ends) so the horizon can be (re)chosen.
        function showStartScreen() {
            stopLoop();
            clearBanner();
            setInGameUI(false);
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
            if (gameStatus !== 'running' || !brushStroke) return;
            const ids = [];
            for (const id of brushHexesAt(pt)) {
                const key = String(id);
                if (id != null && !brushStroke.has(key)) { brushStroke.add(key); ids.push(key); }
            }
            if (!ids.length) return; // all hexes under the brush already painted this stroke
            const state = callWasm('gameTreatBatch', JSON.stringify(ids), selectedTier);
            if (state) applyState(state);
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

        // wireControls binds the in-game controls (reset, speed, brush size). Called
        // from boot once gameConfig exists (reset mutates it).
        function wireControls() {
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
        // condition; budget is the fixed funding rate baked into starting_budget),
        // and each change refreshes the projected-insolvency preview. The Start
        // button launches play. Called from boot once gameConfig exists;
        // defaultBudget is the forecast-derived rate (pickStartingBudget).
        function wireStartOverlay(defaultBudget) {
            const hwrap = document.getElementById('horizon-buttons');
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

            // Annual-budget slider: 0..2x the forecast-derived default (same range as
            // the old in-game slider). Sets gameConfig.starting_budget and refreshes
            // the preview; there is no in-game budget slider anymore.
            const slider = inputById('start-budget-slider');
            const out = document.getElementById('start-budget-value');
            const max = Math.max(1, defaultBudget * 2);
            slider.min = '0';
            slider.max = String(max);
            slider.step = String(Math.max(1, Math.round(max / 200)));
            slider.value = String(defaultBudget);
            out.textContent = fmtMoney(defaultBudget);
            slider.addEventListener('input', () => {
                const rate = parseFloat(slider.value);
                gameConfig.starting_budget = rate;
                out.textContent = fmtMoney(rate);
                refreshProjection();
            });

            refreshProjection(); // initial preview for the default budget + horizon
            document.getElementById('start-btn').addEventListener('click', startGame);
        }

        // ---- Pregame insolvency projection preview ---------------------------
        // refreshProjection updates the start-overlay "at this budget" line by
        // running the macro insolvency forecast in WASM for the current budget and
        // horizon, WITHOUT a board: the slim payload omits hexes so a 5k-hex city
        // isn't serialized on every slider tick. The forecast is cohort-based and
        // hex-count-independent, so it is cheap; the short debounce just coalesces
        // rapid drags. null insolvency_year => solvent through the horizon (mirrors
        // the old HUD convention).
        let projectionTimer = null;
        function refreshProjection() {
            if (!gameConfig) return;
            if (projectionTimer) clearTimeout(projectionTimer);
            projectionTimer = setTimeout(() => {
                const slim = {
                    cohorts: gameConfig.cohorts,
                    initial_pci: gameConfig.initial_pci,
                    cost_tiers: gameConfig.cost_tiers,
                    starting_budget: gameConfig.starting_budget,
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
        function pickStartingBudget(forecast) {
            // Prefer the ROADS entry's current_budget; else sum across resources;
            // else a sane default.
            if (Array.isArray(forecast)) {
                const roads = forecast.find(f => f.resource_type === 'roads' && f.current_budget);
                if (roads && roads.current_budget) return roads.current_budget;
                let sum = 0;
                for (const f of forecast) if (f.current_budget) sum += f.current_budget;
                if (sum > 0) return sum;
            }
            return 1000000; // fallback: $1M/yr
        }

        async function boot() {
            // Palette ↔ Go band-count guard (see BAND_COLORS): surface a loud
            // error instead of silently graying out out-of-range bands.
            if (BAND_COLORS.length !== BAND_COUNT) {
                showError('band palette', 'BAND_COLORS has ' + BAND_COLORS.length +
                    ' entries but Go game.BandCount is ' + BAND_COUNT);
                return;
            }

            // 1. Fetch board geometry + the authoritative playable-hex set + budget.
            //    Also fetch the PER-CITY forecast seed (cohorts/cost_tiers/etc.) so
            //    it comes from the SAME city as the board and budget below; the
            //    template-embedded FORECAST_SEED is a region-wide aggregate in
            //    multi-city static export and would mismatch the per-city budget.
            const [hexgrid, playHexes, forecast, fetchedSeed] = await Promise.all([
                loadJSON(DATA_PREFIX + 'data/hexgrid.geojson'),
                loadJSON(DATA_PREFIX + 'data/play-hexes.json'),
                loadJSON(DATA_PREFIX + 'data/forecast.json'),
                loadJSON(DATA_PREFIX + 'data/forecast_seed.json')
            ]);
            if (!hexgrid || !hexgrid.features || !playHexes) {
                showError('board data', 'missing hexgrid or play-hexes');
                return;
            }

            // 2. Join: keep hexgrid features whose id is in the play-hex set;
            //    play-hexes supplies road_area + k for the gameInit config.
            const playById = new Map();
            for (const ph of playHexes) playById.set(ph.id, ph);
            const features = [];
            for (const f of hexgrid.features) {
                const id = f.properties && f.properties.id;
                if (id == null || !playById.has(id)) continue;       // out-of-bounds / non-playable
                features.push({ type: 'Feature', id: id, geometry: f.geometry, properties: { id: id } });
            }
            if (features.length === 0) {
                showError('board data', 'no playable hexes after join');
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

            // 5. Build the gameInit config from real data and seed the game.
            //    Prefer the per-city seed fetched above so cohorts/cost_tiers/
            //    initial_pci/years/treatment_cycle_years/growth_rate all come from
            //    the SAME city as starting_budget (pickStartingBudget below). Fall
            //    back to the embedded scalar (single-city equals it; also covers a
            //    fetch error). loadJSON returns null on failure, so the || chain
            //    picks the embedded seed only when the fetch didn't yield one.
            const seed = fetchedSeed || FORECAST_SEED || {};
            const startingBudget = pickStartingBudget(forecast);
            // The game must simulate exactly the hexes it renders: build cfg.hexes
            // from the joined `features` (those present in BOTH the grid and the
            // play-hex set), not the raw play-hex list. Otherwise a play-hex
            // missing from the served grid becomes an invisible, untreatable hex
            // that still decays, closes, and drives the loss conditions.
            const gameHexes = features.map(f => {
                const ph = playById.get(f.id);
                return { id: ph.id, road_area: ph.road_area, k: ph.k };
            });
            // Macro insolvency headline needs cohorts. City-scope cohorts are the
            // first choice, but `city_cohorts` is omitempty — fall back to the
            // all-scope cohorts so the headline isn't permanently "solvent" (and
            // the budget slider isn't inert) for cities lacking city-scope stats.
            const macroCohorts =
                (seed.city_cohorts && seed.city_cohorts.length) ? seed.city_cohorts :
                (seed.cohorts || []);
            // Hoisted to module scope so reset() and the horizon buttons can
            // re-seed the game from the same config.
            gameConfig = {
                hexes: gameHexes,
                initial_pci: seed.initial_pci != null ? seed.initial_pci : 85,
                pci_jitter: 8,
                cost_tiers: seed.cost_tiers || [],
                starting_budget: startingBudget,
                horizon_years: seed.years || 20,
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
            wireStartOverlay(startingBudget);

            document.getElementById('loading').style.display = 'none';
            setInGameUI(false);
        }

        // ---- WASM boot, then board boot --------------------------------------
        function onWasmReady() {
            // Wait for the map style to be ready before adding game sources/layers.
            if (map.isStyleLoaded()) boot();
            else map.once('load', boot);
        }

        if (typeof WebAssembly !== 'undefined' && typeof Go !== 'undefined') {
            const go = new Go();
            WebAssembly.instantiateStreaming(fetch(PVMT_CONFIG.wasmPrefix + 'pvmt.wasm'), go.importObject)
                .then(result => { go.run(result.instance); onWasmReady(); })
                .catch(err => showError('forecast WASM', err.message || String(err)));
        } else {
            showError('WASM', 'WebAssembly or Go runtime unavailable');
        }
