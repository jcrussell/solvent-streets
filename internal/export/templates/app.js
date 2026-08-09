        // Config injected by the template as window.PVMT_CONFIG (see
        // TemplateData.IndexConfigJSON in internal/export/meta.go).
        const PVMT_CONFIG = window.PVMT_CONFIG;

        // Multi-city data prefix
        var CITIES = PVMT_CONFIG.cities;
        var DATA_PREFIX = CITIES.length > 0 ? 'cities/' + CITIES[0].slug + '/' : '';

        // Typed DOM accessors: one audited type-assertion each so the call sites
        // below read straight (el.value / el.dataset / …) under tsc --checkJs
        // instead of casting inline at every lookup. Non-null by design — matches
        // this file's assume-present style; the few genuinely-optional nodes
        // (e.g. snapshot-picker, absent in static export) keep their own runtime
        // `if (el)` guards, which stay correct even though the type says non-null.
        /** @param {string} id @returns {HTMLInputElement} */
        function inputById(id) { return /** @type {HTMLInputElement} */ (document.getElementById(id)); }
        /** @param {string} id @returns {HTMLSelectElement} */
        function selectById(id) { return /** @type {HTMLSelectElement} */ (document.getElementById(id)); }
        /** @param {string} id @returns {HTMLButtonElement} */
        function buttonById(id) { return /** @type {HTMLButtonElement} */ (document.getElementById(id)); }
        /** @param {string} id @returns {HTMLCanvasElement} */
        function canvasById(id) { return /** @type {HTMLCanvasElement} */ (document.getElementById(id)); }
        /** @param {string} sel @param {ParentNode} [root] @returns {HTMLElement} */
        function queryEl(sel, root) { return /** @type {HTMLElement} */ ((root || document).querySelector(sel)); }
        /** @param {string} sel @param {ParentNode} [root] @returns {HTMLInputElement} */
        function queryInput(sel, root) { return /** @type {HTMLInputElement} */ ((root || document).querySelector(sel)); }
        /** @param {string} sel @param {ParentNode} [root] @returns {HTMLElement[]} */
        function queryAll(sel, root) { return /** @type {HTMLElement[]} */ (Array.from((root || document).querySelectorAll(sel))); }

        // URL state router: city, tab, scope, snapshot. DOM is the source of
        // truth; the router serializes DOM → URL and applies URL → DOM via
        // the same selectX helpers the event handlers use. The snapshot slot
        // is reserved for the snapshot-picker (solvent-streets-pv8).
        const Router = {
            read() {
                const p = new URLSearchParams(location.search);
                return {
                    city:     p.get('city'),
                    tab:      p.get('tab'),
                    tag:      p.get('tag'),
                    scope:    p.get('scope'),
                    snapshot: p.get('snapshot'),
                };
            },
            current() {
                const tabBtn   = queryEl('.tab-btn.active');
                const scopeBtn = queryEl('#scope-toggle button.active');
                // Scope visibility is driven by #gear-scope-row.hidden (see the
                // reveal logic that toggles it), not #scope-toggle's inline
                // display — so read the row's .hidden to decide serialization.
                const scopeRow = document.getElementById('gear-scope-row');
                const scopeVis = scopeRow && !scopeRow.hidden;
                const snapEl   = selectById('snapshot-picker');
                const sel      = selectById('city-select');
                // Same visibility rule as scope: #tag-scope is only shown on
                // the Compare/Aggregate tabs, so serializing it elsewhere would
                // pin a ?tag= to a tab where it does nothing.
                const tagEl    = selectById('tag-scope');
                const tagVis   = tagEl && !tagEl.hidden;
                return {
                    city:     sel ? sel.value : null,
                    tab:      tabBtn ? tabBtn.dataset.tab : null,
                    tag:      tagVis ? tagEl.value : null,
                    scope:    (scopeVis && scopeBtn) ? scopeBtn.dataset.scope : null,
                    snapshot: snapEl ? snapEl.value : null,
                };
            },
            push() {
                const s = this.current();
                const p = new URLSearchParams();
                for (const k of ['city', 'tab', 'tag', 'scope', 'snapshot']) {
                    if (s[k]) p.set(k, s[k]);
                }
                const qs = p.toString();
                history.pushState(s, '', location.pathname + (qs ? '?' + qs : ''));
            },
        };

        // Scope state. Declared above applyInitialState so the URL-state IIFE
        // can read/write it before the map state block runs.
        let currentScope = 'city';

        // Compare/Aggregate tab state. These MUST be initialized above the
        // applyInitialState IIFE: for a deep link (?tab=aggregate-tab/compare-tab)
        // the IIFE synchronously calls loadAggregateData()/loadCompareData(), which
        // set the *Loaded guards and create allCityDataPromise. If their `var ... =`
        // initializers stayed further down in the script, evaluation would re-run
        // those initializers in place and wipe the guards + discard the in-flight
        // promise cache (var hoists the declaration but runs the initializer in
        // place), defeating the dedupe and leaving stale scope figures.
        var compareLoaded = false;
        var compareCharts = [];
        var aggregateLoaded = false;
        var aggregateCharts = [];
        // Materials tab chart handles + stale-load guard. Declared here (like the
        // compare/aggregate arrays above) so a boot-time deep link ?tab=materials-tab
        // — which calls loadMaterials() from the applyInitialState IIFE below,
        // before the Materials function block executes — sees an initialized
        // generation counter rather than a hoisted-undefined `var` (++undefined is
        // NaN, which would make the load bail its own stale-guard).
        var materialsCharts = [];
        var materialsLoadGen = 0;
        var allCityDataPromise = null;
        // Tag scope for the Compare/Aggregate tabs: '' means all cities, else a
        // tag label from #tag-scope. Declared here (alongside the other tab
        // guards) so a deep link does not re-run the initializer and reset it.
        var selectedTag = '';

        // updateTagScopeVisibility shows #tag-scope only on the Compare/Aggregate
        // tabs, and only when there is more than the "All cities" option (i.e.
        // at least one real tag exists). Called from both selectTab and the
        // deep-link initializer so a ?tab= link reveals it too.
        function updateTagScopeVisibility(tabId) {
            var tagScope = selectById('tag-scope');
            if (!tagScope) return;
            var onScopedTab = tabId === 'compare-tab' || tabId === 'aggregate-tab';
            tagScope.hidden = !(onScopedTab && tagScope.options.length > 1);
        }

        // Apply URL state synchronously, before first render, to avoid flicker.
        // currentScope is set here (default 'city') and is the sole source of
        // truth thereafter — selectScope is the only function that changes it.
        (function applyInitialState() {
            const init = Router.read();
            if (init.city && CITIES.some(c => c.slug === init.city)) {
                DATA_PREFIX = 'cities/' + init.city + '/';
                const sel = selectById('city-select');
                if (sel) sel.value = init.city;
            } else {
                // No ?city= deep link: the browser default-selects the first DOM <option>,
                // which follows CitiesByTag (tag groups first) and can differ from
                // CITIES[0] (flat alphabetical) that seeded DATA_PREFIX above. Align the
                // data prefix with the shown selection so we never load one city's data
                // while the dropdown names another (and replaceState records the truth).
                const sel = selectById('city-select');
                if (sel && sel.value) DATA_PREFIX = 'cities/' + sel.value + '/';
            }
            // Seed the tag filter BEFORE the tab block below: that block calls
            // loadCompareData()/loadAggregateData(), which read selectedTag via
            // scopeCityData, so seeding afterwards would render the unfiltered
            // set on a ?tag= deep link. Unknown labels are ignored rather than
            // filtering everything away. The else-branch matters too: browsers
            // that restore form state across reload/bfcache (Firefox) would
            // otherwise show a tag in #tag-scope while the data shows all cities.
            const initTagEl = selectById('tag-scope');
            if (initTagEl) {
                const known = init.tag && Array.from(initTagEl.options).some(o => o.value === init.tag);
                selectedTag = known ? init.tag : '';
                initTagEl.value = selectedTag;
            }
            if (init.tab && document.getElementById(init.tab)) {
                queryAll('.tab-btn').forEach(b => b.classList.toggle('active', b.dataset.tab === init.tab));
                queryAll('.tab-content').forEach(c => c.classList.toggle('active', c.id === init.tab));
                syncTabAria();
                // Toggling classes does NOT trigger the per-tab data load that
                // a selectTab() click would, so a deep-linked ?tab= for a
                // lazy-loaded tab would otherwise hang on its loading spinner.
                if (init.tab === 'aggregate-tab') loadAggregateData();
                else if (init.tab === 'compare-tab') loadCompareData();
                else if (init.tab === 'materials-tab') loadMaterials();
                // Class-toggling above bypasses selectTab, so reveal #tag-scope
                // here too — otherwise a deep-linked Compare/Aggregate tab shows
                // no tag selector until the user clicks a tab.
                updateTagScopeVisibility(init.tab);
            }
            if (init.scope === 'city' || init.scope === 'bbox') {
                currentScope = init.scope;
                queryAll('#scope-toggle button').forEach(b =>
                    b.classList.toggle('active', b.dataset.scope === currentScope));
            }
            history.replaceState(Router.current(), '');
        })();

        window.addEventListener('popstate', (e) => {
            const s = e.state || Router.read();
            const sel = selectById('city-select');
            const cityChanged = !!(s.city && sel && sel.value !== s.city);
            // Set the snapshot picker value BEFORE selectCity so its data
            // fetches use the right snapshot. dataURL reads the picker live.
            const snapEl = selectById('snapshot-picker');
            const targetSnap = s.snapshot == null ? '' : String(s.snapshot);
            const snapshotChanged = !!(snapEl && snapEl.value !== targetSnap);
            if (snapEl) snapEl.value = targetSnap;
            // Update currentScope from URL state before kicking off reloads
            // (selectCity → loadCityData → buildBlendedHexFC reads currentScope).
            if (s.scope === 'city' || s.scope === 'bbox') currentScope = s.scope;
            queryAll('#scope-toggle button').forEach(b =>
                b.classList.toggle('active', b.dataset.scope === currentScope));
            // Only fire selectCity when the city actually changed. selectCity
            // resets the snapshot picker (snapshots are per-city), so calling it
            // on a same-city snapshot-only navigation would clobber targetSnap
            // (set above) and reload against Latest. The snapshot-only branch
            // below handles that case explicitly.
            // Restore the tag filter before the tab switch below: selectTab
            // kicks off loadCompareData/loadAggregateData, which read
            // selectedTag via scopeCityData. Clearing compareLoaded here (not
            // after) is what lets that one-shot loader re-render; otherwise the
            // leaderboard keeps the previous filter's rows while the URL claims
            // otherwise.
            const tagEl = selectById('tag-scope');
            const targetTag = s.tag == null ? '' : String(s.tag);
            const tagChanged = selectedTag !== targetTag;
            if (tagEl) tagEl.value = targetTag;
            if (tagChanged) {
                selectedTag = targetTag;
                compareLoaded = false;
            }
            if (cityChanged) selectCity(s.city, { push: false });
            if (s.tab)  selectTab(s.tab,   { push: false });
            // A hand-edited URL can carry ?tag= with no ?tab=, so selectTab
            // never fired and nothing re-rendered. Router.push always writes
            // both, so this only guards that case.
            if (tagChanged && !s.tab) applyTagScope(targetTag);
            // Snapshot-only change (no city change) needs an explicit reload —
            // selectCity didn't fire so loadCityData/loadFinancials weren't called.
            if (!cityChanged && snapshotChanged) {
                loadCityData();
                loadFinancials();
                reloadMaterialsIfActive();
            }
            // No city or snapshot change → the underlying data didn't reload,
            // but currentScope did change. Repaint via selectScope.
            if (!cityChanged && !snapshotChanged) {
                selectScope(currentScope, { push: false });
            }
        });

        // dataURL builds a /data/{file} request URL, appending ?snapshot=<id>
        // if the snapshot-picker has a non-empty value. The picker only exists
        // in live-server mode; in static export this returns the bare URL.
        function dataURL(prefix, file) {
            const sel = selectById('snapshot-picker');
            const snap = sel && sel.value ? sel.value : '';
            return prefix + 'data/' + file + (snap ? '?snapshot=' + encodeURIComponent(snap) : '');
        }

        // Single fetch wrapper: reports network/HTTP failures via the visible
        // error banner and console.error so a silent empty-chart never looks
        // like "still loading" (solvent-streets-y68). Returns parsed JSON on
        // success, or null on failure — callers fall back gracefully.
        // opts.silent404 swallows a 404 without surfacing — used for the
        // hexgrid file, which is legitimately absent for cities with no
        // hex_stats rows at all.
        async function loadJSON(url, opts) {
            try {
                const resp = await fetch(url);
                if (!resp.ok) {
                    if (!(opts && opts.silent404 && resp.status === 404)) {
                        showLoadError(url, 'HTTP ' + resp.status);
                    }
                    return null;
                }
                return await resp.json();
            } catch (err) {
                showLoadError(url, err.message || String(err));
                return null;
            }
        }
        function showLoadError(url, detail) {
            console.error('failed to load', url, detail);
            const banner = document.getElementById('error-banner');
            const list = document.getElementById('error-banner-list');
            if (!banner || !list) return;
            const li = document.createElement('li');
            li.textContent = url + ' (' + detail + ')';
            list.appendChild(li);
            banner.hidden = false;
        }
        // safeSimulate wraps window.simulateForecast so a WASM-side panic
        // surfaces in the error banner instead of silently freezing the
        // chart. Returns null when the simulation throws or reports an
        // error; callers must check for null and bail.
        function safeSimulate(input) {
            let raw;
            try {
                raw = window.simulateForecast(JSON.stringify(input));
            } catch (err) {
                showLoadError('forecast simulator', err.message || String(err));
                return null;
            }
            let parsed;
            try {
                parsed = JSON.parse(raw);
            } catch (err) {
                showLoadError('forecast simulator', 'invalid response: ' + (err.message || String(err)));
                return null;
            }
            if (parsed && parsed.error) {
                showLoadError('forecast simulator', parsed.error);
                return null;
            }
            return parsed;
        }

        // Tab switching. Keeps .active class, aria-selected, and roving
        // tabindex in lockstep so screen readers and keyboard users see the
        // same selection state as sighted users.
        function syncTabAria() {
            queryAll('.tab-btn').forEach(b => {
                const active = b.classList.contains('active');
                b.setAttribute('aria-selected', active ? 'true' : 'false');
                b.setAttribute('tabindex', active ? '0' : '-1');
            });
        }
        function selectTab(tabId, opts) {
            const btn = queryEl('.tab-btn[data-tab="' + tabId + '"]');
            const content = document.getElementById(tabId);
            if (!btn || !content) return;
            queryAll('.tab-btn').forEach(b => b.classList.remove('active'));
            queryAll('.tab-content').forEach(c => c.classList.remove('active'));
            btn.classList.add('active');
            content.classList.add('active');
            syncTabAria();
            if (opts && opts.focus) btn.focus();
            if (tabId === 'map-tab' && window._map) window._map.resize();
            if (tabId === 'compare-tab') loadCompareData();
            if (tabId === 'aggregate-tab') loadAggregateData();
            if (tabId === 'materials-tab') loadMaterials();
            updateTagScopeVisibility(tabId);
            // The aggregate honors the region-wide scope, so re-evaluate the
            // scope-toggle reveal whenever the active tab changes.
            maybeRevealScopeToggle();
            if (!opts || opts.push !== false) Router.push();
        }
        queryAll('.tab-btn').forEach(btn => {
            btn.addEventListener('click', () => selectTab(btn.dataset.tab));
        });
        // WAI-ARIA tablist keyboard pattern: arrows move focus & selection,
        // Home/End jump to first/last.
        (function bindTabKeyboardNav() {
            const tabs = queryAll('.tab-btn');
            if (tabs.length === 0) return;
            tabs.forEach((tab, idx) => {
                tab.addEventListener('keydown', (e) => {
                    let target = null;
                    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
                        target = tabs[(idx + 1) % tabs.length];
                    } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
                        target = tabs[(idx - 1 + tabs.length) % tabs.length];
                    } else if (e.key === 'Home') {
                        target = tabs[0];
                    } else if (e.key === 'End') {
                        target = tabs[tabs.length - 1];
                    }
                    if (target) {
                        e.preventDefault();
                        selectTab(target.dataset.tab, { focus: true });
                    }
                });
            });
        })();

        // Map
        const CENTER = PVMT_CONFIG.center;
        const layerColors = PVMT_CONFIG.layerColors;
        const UNIT_SYSTEM = PVMT_CONFIG.unitSystem;

        // Unit conversion helpers
        const SQM_TO_SQFT = 10.763910417;
        const SQM_TO_ACRE = SQM_TO_SQFT / 43560;
        const SQM_TO_SQMI = SQM_TO_ACRE / 640;
        const SQM_TO_HA = 1 / 10000;
        const SQM_TO_SQKM = 1 / 1000000;

        function toAreaLarge(sqm) {
            return UNIT_SYSTEM === 'metric' ? sqm * SQM_TO_HA : sqm * SQM_TO_ACRE;
        }
        function toAreaVeryLarge(sqm) {
            return UNIT_SYSTEM === 'metric' ? sqm * SQM_TO_SQKM : sqm * SQM_TO_SQMI;
        }
        function areaLargeUnit() {
            return UNIT_SYSTEM === 'metric' ? 'ha' : 'acres';
        }
        function areaVeryLargeUnit() {
            return UNIT_SYSTEM === 'metric' ? 'sq km' : 'sq mi';
        }
        function fmtArea(sqm) {
            if (UNIT_SYSTEM === 'metric') return fmtNum(sqm) + ' sq m';
            return fmtNum(sqm * SQM_TO_SQFT) + ' sq ft';
        }

        const map = new maplibregl.Map({
            container: 'map',
            style: {
                version: 8,
                sources: { 'osm': { type: 'raster', tiles: ['https://tile.openstreetmap.org/{z}/{x}/{y}.png'], tileSize: 256, attribution: '&copy; OpenStreetMap' } },
                layers: [{ id: 'osm', type: 'raster', source: 'osm', minzoom: 0, maxzoom: 19 }]
            },
            center: CENTER,
            zoom: 13
        });
        window._map = map;

        // Hex interactivity state. Scope-keyed so the same map view can
        // repaint between city-only and full-bbox without re-fetching;
        // currentScope (declared above applyInitialState) is the single
        // source of truth for which bucket buildBlendedHexFC / the click
        // handler read from.
        let hexDataByScope = { city: {}, bbox: {} };       // { scope: { type: [features] } }
        let costSummaryByScope = { city: {}, bbox: {} };   // { scope: { type: {year1_cost,total_area} } }
        let activeTypes = new Set(Object.keys(layerColors));
        let savedPanelHTML = '';
        // Monotonic load-generation tokens. loadCityData and loadFinancials each
        // await several fetches before mutating map sources/layers and module
        // globals; overlapping invocations (e.g. rapid city/snapshot switches or a
        // back-to-back popstate) would otherwise both pass their synchronous
        // teardown and then race their async writes (duplicate addSource throws;
        // last-writer-wins globals). Each function bumps its own counter at entry
        // and bails after every await if a newer load has started. Separate
        // counters — the two are called as a pair, so a shared one would let one
        // abort the other.
        let cityLoadGen = 0;
        let financialsLoadGen = 0;
        // True while the user is in drag-to-select-region mode. Read by the
        // hex-click handler (suppressed mid-selection) and the region-select
        // pointer handlers below.
        let selecting = false;

        function fmtNum(n) {
            return Math.round(n).toLocaleString();
        }

        // Escape config-sourced strings (city names, cost-tier labels) before
        // concatenating them into innerHTML. html/template's JS-string escaping at
        // page-generation time protects only the script literal, not the runtime
        // DOM insertion, so a name/label containing markup would otherwise execute.
        function esc(s) {
            return String(s == null ? '' : s)
                .replace(/&/g, '&amp;')
                .replace(/</g, '&lt;')
                .replace(/>/g, '&gt;')
                .replace(/"/g, '&quot;')
                .replace(/'/g, '&#39;');
        }

        function fmtCost(n) {
            if (n >= 1e9) return '$' + (n/1e9).toFixed(2) + 'B';
            if (n >= 1e6) return '$' + (n/1e6).toFixed(1) + 'M';
            if (n >= 1e3) return '$' + (n/1e3).toFixed(0) + 'K';
            return '$' + Math.round(n).toLocaleString();
        }

        // Build a blended hex FeatureCollection from the active resource
        // types under the current scope. Reads module state (currentScope,
        // hexDataByScope, costSummaryByScope, activeTypes) — no params, so
        // the scope-swap path (selectScope → updateHexGrid) and the
        // type-toggle path (checkbox → updateHexGrid) share one call site.
        function buildBlendedHexFC() {
            const hexDataByType = hexDataByScope[currentScope] || {};
            const costSummary = costSummaryByScope[currentScope] || {};
            const byHex = {};
            for (const type of activeTypes) {
                const features = hexDataByType[type] || [];
                for (const f of features) {
                    const hid = f.properties.hex_id;
                    if (!byHex[hid]) {
                        byHex[hid] = { geometry: f.geometry, area: 0, pct_covered: 0, est_cost: 0 };
                    }
                    byHex[hid].area += f.properties.area || 0;
                    byHex[hid].pct_covered += f.properties.pct_covered || 0;
                }
            }

            for (const type of activeTypes) {
                const cs = costSummary[type];
                if (!cs || !cs.total_area) continue;
                const features = hexDataByType[type] || [];
                for (const f of features) {
                    const hid = f.properties.hex_id;
                    if (!byHex[hid]) continue;
                    byHex[hid].est_cost += ((f.properties.area || 0) / cs.total_area) * cs.year1_cost;
                }
            }

            const features = [];
            for (const [hid, data] of Object.entries(byHex)) {
                features.push({
                    type: 'Feature',
                    geometry: data.geometry,
                    properties: {
                        hex_id: hid,
                        area: data.area,
                        pct_covered: Math.min(data.pct_covered, 100),
                        est_cost: data.est_cost
                    }
                });
            }
            return { type: 'FeatureCollection', features };
        }

        function updateHexGrid() {
            const src = map.getSource('hexgrid');
            if (!src) return;
            src.setData(buildBlendedHexFC());
        }

        async function loadCityData() {
            const gen = ++cityLoadGen; // bail stale loads after each await
            const prefix = DATA_PREFIX; // snapshot to avoid mid-await mutation
            // Remove existing layers and sources
            ['hex-outline', 'hex-fill', 'boundary-line'].concat(Object.keys(layerColors).map(t => t + '-fill')).forEach(id => {
                if (map.getLayer(id)) map.removeLayer(id);
            });
            ['hexgrid', 'boundary'].concat(Object.keys(layerColors)).forEach(id => {
                if (map.getSource(id)) map.removeSource(id);
            });
            hexDataByScope = { city: {}, bbox: {} };
            costSummaryByScope = { city: {}, bbox: {} };
            activeTypes = new Set(Object.keys(layerColors));

            // Reload meta.json for stats panel
            {
                const meta = await loadJSON(dataURL(prefix, 'meta.json'));
                if (gen !== cityLoadGen) return; // a newer load superseded us
                if (meta) {
                    document.querySelector('#panel h2').textContent = meta.project_name;
                    const subtitleEl = document.querySelector('#panel .subtitle');
                    if (subtitleEl && meta.snapshot_date) {
                        subtitleEl.textContent = 'Pavement & Parking Analysis | Last computed ' + meta.snapshot_date;
                    }
                    const statsEl = document.getElementById('stats');
                    let statsHTML = '';
                    if (meta.city_area && meta.city_area > 0) {
                        statsHTML +=
                            '<div class="stat-card summary-card"><h3>City Summary</h3>' +
                            '<div class="stat-row"><span class="stat-label">City Area (' + areaLargeUnit() + ')</span><span class="stat-value">' + toAreaLarge(meta.city_area).toFixed(1) + '</span></div>' +
                            '<div class="stat-row"><span class="stat-label">City Area (' + areaVeryLargeUnit() + ')</span><span class="stat-value">' + toAreaVeryLarge(meta.city_area).toFixed(2) + '</span></div>' +
                            '<div class="stat-row"><span class="stat-label">Total Paved (' + areaLargeUnit() + ')</span><span class="stat-value">' + toAreaLarge(meta.total_paved || 0).toFixed(1) + '</span></div>' +
                            '<div class="stat-row"><span class="stat-label">% Paved</span><span class="stat-value">' + (meta.pct_paved || 0).toFixed(1) + '%</span></div>' +
                            '</div>';
                    }
                    (meta.stats || []).forEach(s => {
                        const areaLargeVal = toAreaLarge(s.total_area);
                        statsHTML +=
                            '<div class="stat-card"><h3>' + s.type + '</h3>' +
                            '<div class="stat-row"><span class="stat-label">Features</span><span class="stat-value">' + s.feature_count + '</span></div>' +
                            '<div class="stat-row"><span class="stat-label">Area (' + areaLargeUnit() + ')</span><span class="stat-value">' + areaLargeVal.toFixed(1) + '</span></div>' +
                            '<div class="stat-row"><span class="stat-label">Area (' + areaVeryLargeUnit() + ')</span><span class="stat-value">' + toAreaVeryLarge(s.total_area).toFixed(2) + '</span></div>' +
                            '</div>';
                    });
                    statsEl.innerHTML = statsHTML;
                    savedPanelHTML = statsHTML;
                }
            }

            // Load and render boundary polygon
            {
                const data = await loadJSON(dataURL(prefix, 'boundary.geojson'));
                if (gen !== cityLoadGen) return; // a newer load superseded us
                if (data) {
                    if (!map.getSource('boundary')) {
                        map.addSource('boundary', { type: 'geojson', data });
                        map.addLayer({
                            id: 'boundary-line', type: 'line', source: 'boundary',
                            paint: { 'line-color': '#333', 'line-width': 2, 'line-opacity': 0.8, 'line-dasharray': [3, 2] }
                        });
                    }
                    // Fit map to boundary bbox
                    const coords = data.features.flatMap(f => {
                        const g = f.geometry;
                        if (g.type === 'Polygon') return g.coordinates[0];
                        if (g.type === 'MultiPolygon') return g.coordinates.flat(2).length ? g.coordinates.flatMap(p => p[0]) : [];
                        return [];
                    });
                    if (coords.length > 0) {
                        const lngs = coords.map(c => c[0]);
                        const lats = coords.map(c => c[1]);
                        map.fitBounds([[Math.min(...lngs), Math.min(...lats)], [Math.max(...lngs), Math.max(...lats)]], { padding: 40 });
                    }
                }
            }

            let toggles = '';

            // Resource toggles — filter the blended hex grid by type. There is
            // no separate per-resource fill layer anymore (it was a cosmetic
            // overlay duplicating the hex grid); empty data-layers skips the
            // visibility-setter while data-type still drives activeTypes and
            // updateHexGrid().
            for (const type of Object.keys(layerColors)) {
                toggles += '<label><input type="checkbox" checked data-layers="" data-type="' + type + '"><span class="legend-dot" style="background:' + layerColors[type] + '"></span>' + type + '</label>';
            }

            // Load hex cost summary: map[scope]map[resource]{...}, scope in
            // {city, bbox}. "city" is omitted when the city has no :city rows.
            {
                const cs = await loadJSON(dataURL(prefix, 'hex-cost-summary.json'));
                if (gen !== cityLoadGen) return; // a newer load superseded us
                if (cs) {
                    costSummaryByScope.city = cs.city || {};
                    costSummaryByScope.bbox = cs.bbox || {};
                }
            }

            // Load the hex grid — a single multi-scope file, one feature per
            // hex with nested {bbox, city?} coverage keyed by resource. Expand
            // it back into the per-scope/per-resource feature arrays the render
            // path consumes. A feature whose properties lack "city" carries no
            // city-scope data, so a city with no such objects leaves
            // hexDataByScope.city empty and the scope toggle stays hidden.
            const hexData = await loadJSON(dataURL(prefix, 'hexgrid.geojson'), { silent404: true });
            if (gen !== cityLoadGen) return; // a newer load superseded us
            if (hexData && hexData.features) {
                for (const f of hexData.features) {
                    const hid = f.properties.id;
                    for (const scope of ['bbox', 'city']) {
                        const byRes = f.properties[scope];
                        if (!byRes) continue;
                        for (const rt of Object.keys(byRes)) {
                            const m = byRes[rt];
                            if (!hexDataByScope[scope][rt]) hexDataByScope[scope][rt] = [];
                            hexDataByScope[scope][rt].push({
                                geometry: f.geometry,
                                properties: {
                                    hex_id: hid,
                                    resource_type: rt,
                                    area: m.area,
                                    pct_covered: m.pct,
                                },
                            });
                        }
                    }
                }
            }

            // Fall back if currentScope's data is empty. The button state
            // and URL are kept in sync via selectScope (don't push, this is
            // an internal repair, not a user action).
            const cityEmpty = Object.keys(hexDataByScope.city).length === 0;
            const bboxEmpty = Object.keys(hexDataByScope.bbox).length === 0;
            if (currentScope === 'city' && cityEmpty && !bboxEmpty) {
                currentScope = 'bbox';
                queryAll('#scope-toggle button').forEach(b =>
                    b.classList.toggle('active', b.dataset.scope === currentScope));
            } else if (currentScope === 'bbox' && bboxEmpty && !cityEmpty) {
                currentScope = 'city';
                queryAll('#scope-toggle button').forEach(b =>
                    b.classList.toggle('active', b.dataset.scope === currentScope));
            }
            maybeRevealScopeToggle();

            {
                const haveAnyHex = Object.keys(hexDataByScope.city).length > 0 ||
                                    Object.keys(hexDataByScope.bbox).length > 0;
                if (haveAnyHex) {
                    {
                        const blended = buildBlendedHexFC();
                        if (!map.getSource('hexgrid')) {
                            map.addSource('hexgrid', { type: 'geojson', data: blended });
                            map.addLayer({
                                id: 'hex-fill', type: 'fill', source: 'hexgrid',
                                paint: {
                                    'fill-color': hexFillColor(),
                                    'fill-opacity': 0.6
                                }
                            });
                            map.addLayer({
                                id: 'hex-outline', type: 'line', source: 'hexgrid',
                                paint: { 'line-color': '#333', 'line-width': 0.5, 'line-opacity': 0.3 }
                            });
                        }
                        toggles += '<label><input type="checkbox" checked data-layers="hex-fill,hex-outline"><span class="legend-gradient" style="background:linear-gradient(90deg,var(--legend-2),var(--legend-6))"></span>hex grid</label>';
                        // Hover/click handlers for the hex-fill layer are registered
                        // ONCE at startup (registerHexHandlers), not here. MapLibre
                        // layer-scoped listeners are keyed by layer id and survive
                        // removeLayer/addLayer, so registering them inside loadCityData
                        // (which re-runs on every city/snapshot switch) stacked one set
                        // per switch. The registered handlers read live module state.
                    }
                }
            }

            document.getElementById('toggles').innerHTML = toggles;
            queryAll('#toggles input').forEach(cb => {
                cb.addEventListener('change', e => {
                    const input = e.target;
                    if (!(input instanceof HTMLInputElement)) return;
                    const vis = input.checked ? 'visible' : 'none';
                    const layers = input.dataset.layers ? input.dataset.layers.split(',') : [];
                    layers.forEach(id => map.setLayoutProperty(id, 'visibility', vis));

                    // Update activeTypes and reblend hex grid
                    const type = input.dataset.type;
                    if (type) {
                        if (input.checked) {
                            activeTypes.add(type);
                        } else {
                            activeTypes.delete(type);
                        }
                        updateHexGrid();
                    }
                });
            });

            // City data is now loaded; the region-select control can operate on
            // this city's blended hex FC. Enable it (it starts disabled).
            {
                const rsBtn = buttonById('region-select-btn');
                if (rsBtn) rsBtn.disabled = false;
            }
        }

        // Register the hex-fill hover/click handlers exactly once. They read live
        // module state (hexDataByScope/costSummaryByScope/currentScope/savedPanelHTML),
        // so re-running loadCityData with new data is reflected without re-binding.
        // Registered before the layer exists is fine: MapLibre layer-scoped listeners
        // simply don't fire until a layer with that id is present.
        function registerHexHandlers() {
            const tooltip = document.getElementById('hex-tooltip');
            map.on('mousemove', 'hex-fill', e => {
                map.getCanvas().style.cursor = 'pointer';
                if (e.features && e.features.length > 0) {
                    const p = e.features[0].properties;
                    tooltip.innerHTML =
                        '<strong>Coverage:</strong> ' + (p.pct_covered != null ? Math.round(p.pct_covered) : 0) + '%<br>' +
                        '<strong>Area:</strong> ' + fmtArea(p.area || 0) + '<br>' +
                        '<strong>Est. Annual Cost:</strong> ' + fmtCost(p.est_cost || 0);
                    tooltip.style.display = 'block';
                    tooltip.style.left = (e.point.x + 12) + 'px';
                    tooltip.style.top = (e.point.y - 12) + 'px';
                }
            });
            map.on('mouseleave', 'hex-fill', () => {
                map.getCanvas().style.cursor = '';
                tooltip.style.display = 'none';
            });
            map.on('click', 'hex-fill', e => {
                if (selecting) return; // region-select drag in progress; don't open hex panel
                if (!e.features || e.features.length === 0) return;
                const p = e.features[0].properties;
                const hexId = p.hex_id;

                // Save current stats panel
                const statsEl = document.getElementById('stats');
                if (!statsEl.querySelector('.hex-detail')) {
                    savedPanelHTML = statsEl.innerHTML;
                }

                // Build per-type breakdown — read from the active scope's buckets
                // so the click panel matches what the user just saw on the heatmap.
                const hexDataByType = hexDataByScope[currentScope] || {};
                const costSummary = costSummaryByScope[currentScope] || {};
                let rows = '';
                let totalArea = 0, totalPct = 0, totalCost = 0;
                for (const type of Object.keys(layerColors)) {
                    const features = hexDataByType[type] || [];
                    const match = features.find(f => f.properties.hex_id === hexId);
                    if (!match) continue;
                    const area = match.properties.area || 0;
                    const pct = match.properties.pct_covered || 0;
                    let cost = 0;
                    const cs = costSummary[type];
                    if (cs && cs.total_area) {
                        cost = (area / cs.total_area) * cs.year1_cost;
                    }
                    totalArea += area;
                    totalPct += pct;
                    totalCost += cost;
                    rows += '<tr><td style="text-transform:capitalize">' + type + '</td><td>' + fmtNum(area) + '</td><td>' + Math.round(pct) + '%</td><td>' + fmtCost(cost) + '</td></tr>';
                }

                statsEl.innerHTML =
                    '<div class="hex-detail">' +
                    '<h3>Hex ' + hexId + '<button class="close-btn" id="close-hex" type="button" aria-label="Close hex details">&times;</button></h3>' +
                    '<table><tr><th>Type</th><th>Area</th><th>Coverage</th><th>Est. Cost</th></tr>' +
                    rows +
                    '<tr class="total-row"><td>Total</td><td>' + fmtNum(totalArea) + '</td><td>' + Math.min(Math.round(totalPct), 100) + '%</td><td>' + fmtCost(totalCost) + '</td></tr>' +
                    '</table></div>';

                document.getElementById('close-hex').addEventListener('click', () => {
                    statsEl.innerHTML = savedPanelHTML;
                });
            });
        }
        registerHexHandlers();

        // Coverage heatmap colors are read from the --legend-* CSS vars (which
        // differ per theme) at the same breakpoints as the #legend bar, so the
        // map and its legend always agree. Rebuilt on theme toggle.
        function hexFillColor() {
            const cs = getComputedStyle(document.documentElement);
            const lg = n => cs.getPropertyValue('--legend-' + n).trim();
            return ['interpolate', ['linear'], ['get', 'pct_covered'],
                0, lg(1), 10, lg(2), 25, lg(3), 50, lg(4), 75, lg(5), 100, lg(6)];
        }
        document.addEventListener('pvmt:themechange', () => {
            if (map.getLayer('hex-fill')) {
                map.setPaintProperty('hex-fill', 'fill-color', hexFillColor());
            }
        });

        // --- Drag-to-select region -> aggregate totals ---------------------
        // Lets the user drag a rectangle on the map and see aggregate totals
        // (hex count, area, area-weighted % covered, total est. cost) for the
        // hexes whose centroid falls inside the rectangle. Operates entirely on
        // the currently-visible city's blended hex FC (buildBlendedHexFC, which
        // already respects activeTypes + currentScope); no backend calls.
        (function setupRegionSelect() {
            const btn = buttonById('region-select-btn');
            const band = document.getElementById('region-rubberband');
            if (!btn || !band) return;
            const mapEl = document.getElementById('map');

            let startPt = null; // { x, y } in map-container pixels at mousedown

            // Centroid of a GeoJSON polygon as the average of its outer-ring
            // vertices (plain JS, no turf). Documented approximation: hexes whose
            // centroid straddles the selection edge may be in/excluded.
            function polyCentroid(geom) {
                let ring = null;
                if (geom.type === 'Polygon') ring = geom.coordinates[0];
                else if (geom.type === 'MultiPolygon') ring = geom.coordinates[0] && geom.coordinates[0][0];
                if (!ring || ring.length === 0) return null;
                let sx = 0, sy = 0, n = 0;
                for (const v of ring) { sx += v[0]; sy += v[1]; n++; }
                return n ? [sx / n, sy / n] : null;
            }

            // Convert a mouse event to pixel coords relative to the map element.
            function relPoint(e) {
                const r = mapEl.getBoundingClientRect();
                return { x: e.clientX - r.left, y: e.clientY - r.top };
            }

            function setBand(a, b) {
                const left = Math.min(a.x, b.x), top = Math.min(a.y, b.y);
                band.style.left = left + 'px';
                band.style.top = top + 'px';
                band.style.width = Math.abs(a.x - b.x) + 'px';
                band.style.height = Math.abs(a.y - b.y) + 'px';
                band.hidden = false;
            }

            // Tear down selection mode and restore normal map interaction. Safe
            // to call from any exit path (mouseup, Esc, error). ALWAYS re-enables
            // dragPan so panning works again afterwards.
            function stopSelecting() {
                selecting = false;
                startPt = null;
                band.hidden = true;
                btn.setAttribute('aria-pressed', 'false');
                map.dragPan.enable();
                map.getCanvas().style.cursor = '';
            }

            function startSelecting() {
                selecting = true;
                btn.setAttribute('aria-pressed', 'true');
                map.dragPan.disable();
                map.getCanvas().style.cursor = 'crosshair';
            }

            btn.addEventListener('click', () => {
                if (btn.disabled) return;
                if (selecting) { stopSelecting(); return; }
                startSelecting();
            });

            // Use the MAP's own pointer events so we don't fight dragPan or block
            // its handlers with a capturing DOM overlay.
            map.on('mousedown', (e) => {
                if (!selecting) return;
                startPt = relPoint(e.originalEvent);
                setBand(startPt, startPt);
            });
            map.on('mousemove', (e) => {
                if (!selecting || !startPt) return;
                setBand(startPt, relPoint(e.originalEvent));
            });
            map.on('mouseup', (e) => {
                if (!selecting || !startPt) return;
                const endPt = relPoint(e.originalEvent);
                const s = startPt;
                try {
                    // Screen pixels -> geographic via map.unproject. map.unproject
                    // expects coords relative to the map canvas (same origin as our
                    // relPoint, which measures from the map element).
                    const p1 = map.unproject([s.x, s.y]);
                    const p2 = map.unproject([endPt.x, endPt.y]);
                    const minLng = Math.min(p1.lng, p2.lng), maxLng = Math.max(p1.lng, p2.lng);
                    const minLat = Math.min(p1.lat, p2.lat), maxLat = Math.max(p1.lat, p2.lat);
                    aggregateSelection([minLng, minLat, maxLng, maxLat]);
                } catch (err) {
                    // unproject (or anything else) failed — still restore the map.
                    console.error('region select failed', err);
                } finally {
                    stopSelecting();
                }
            });

            // Esc cancels mid-drag (or while armed) without aggregating.
            document.addEventListener('keydown', (e) => {
                if (e.key === 'Escape' && selecting) stopSelecting();
            });

            // Aggregate the blended hex FC over the selection bbox and render the
            // result card into #stats. Empty selection shows zeros, not an error.
            function aggregateSelection(bbox) {
                const [minLng, minLat, maxLng, maxLat] = bbox;
                const fc = buildBlendedHexFC();
                let count = 0, sumArea = 0, sumCost = 0, sumPctArea = 0;
                for (const f of (fc.features || [])) {
                    const c = polyCentroid(f.geometry);
                    if (!c) continue;
                    if (c[0] < minLng || c[0] > maxLng || c[1] < minLat || c[1] > maxLat) continue;
                    const area = f.properties.area || 0;
                    count++;
                    sumArea += area;
                    sumCost += f.properties.est_cost || 0;
                    sumPctArea += (f.properties.pct_covered || 0) * area;
                }
                const wPct = sumArea > 0 ? (sumPctArea / sumArea) : 0;

                const statsEl = document.getElementById('stats');
                // Single-slot savedPanelHTML is shared with the hex-click panel.
                // Only capture when neither detail panel is already showing, so
                // closing restores the real stats, not another detail view.
                if (!statsEl.querySelector('.hex-detail') && !statsEl.querySelector('.region-detail')) {
                    savedPanelHTML = statsEl.innerHTML;
                }
                statsEl.innerHTML =
                    '<div class="region-detail">' +
                    '<div class="stat-card summary-card">' +
                    '<h3>Selected Region<button class="close-btn" id="close-region" type="button" aria-label="Close region selection">&times;</button></h3>' +
                    '<div class="stat-row"><span class="stat-label">Hexes selected</span><span class="stat-value">' + fmtNum(count) + '</span></div>' +
                    '<div class="stat-row"><span class="stat-label">Total Area (' + areaLargeUnit() + ')</span><span class="stat-value">' + toAreaLarge(sumArea).toFixed(1) + '</span></div>' +
                    '<div class="stat-row"><span class="stat-label">% Covered (area-weighted)</span><span class="stat-value">' + wPct.toFixed(1) + '%</span></div>' +
                    '<div class="stat-row"><span class="stat-label">Est. Annual Cost</span><span class="stat-value">' + fmtCost(sumCost) + '</span></div>' +
                    '</div></div>';
                const closeBtn = document.getElementById('close-region');
                if (closeBtn) closeBtn.addEventListener('click', () => { statsEl.innerHTML = savedPanelHTML; });
            }
        })();

        map.on('load', () => loadCityData());

        // City switcher
        function selectCity(slug, opts) {
            const sel = selectById('city-select');
            if (!sel) return;
            if (sel.value !== slug) sel.value = slug;
            const opt = sel.selectedOptions[0];
            if (!opt) return;
            DATA_PREFIX = 'cities/' + slug + '/';
            const bboxParts = opt.dataset.bbox.split(',').map(Number);
            map.fitBounds([[bboxParts[1], bboxParts[0]], [bboxParts[3], bboxParts[2]]], { padding: 40 });
            document.querySelector('#panel h2').textContent = opt.textContent.trim();
            // Snapshot ids are per-city. Reset the picker on city change so a
            // stale id can't be sent against the new city's data endpoints.
            const snapEl = selectById('snapshot-picker');
            if (snapEl) snapEl.value = '';
            loadSnapshots(slug);
            loadCityData();
            loadFinancials();
            reloadMaterialsIfActive();
            if (!opts || opts.push !== false) Router.push();
        }
        const citySelect = selectById('city-select');
        if (citySelect) {
            citySelect.addEventListener('change', () => selectCity(citySelect.value));
        }

        const tagScopeSelect = selectById('tag-scope');
        if (tagScopeSelect) {
            tagScopeSelect.addEventListener('change', () => onTagScopeChange(tagScopeSelect.value));
        }

        // Snapshot picker (live server only). loadSnapshots fetches the list
        // for the current city and populates options; selectSnapshot is the
        // counterpart of selectCity / selectTab / selectScope.
        async function loadSnapshots(slug) {
            const sel = selectById('snapshot-picker');
            if (!sel) return; // static export
            const url = slug ? 'api/cities/' + slug + '/snapshots' : 'api/snapshots';
            const list = await loadJSON(url);
            if (!list || list.length === 0) {
                sel.innerHTML = '<option value="">Latest (no snapshots)</option>';
                return;
            }
            const fmt = (ts) => {
                try { return new Date(ts).toISOString().slice(0, 16).replace('T', ' '); }
                catch (e) { return ts; }
            };
            sel.innerHTML = '<option value="">Latest</option>' +
                list.map(s => '<option value="' + s.id + '" title="' + (s.config_hash || '').slice(0, 12) + '">' + fmt(s.computed_at) + '</option>').join('');
            // Restore from URL (covers initial load and bfcache restores).
            const want = Router.read().snapshot;
            if (want && list.some(s => String(s.id) === String(want))) {
                const prev = sel.value;
                sel.value = String(want);
                // Boot's initial loadCityData/loadFinancials ran with the picker
                // empty (Latest). If the URL deep-links a real snapshot, reload
                // once so the board/financials reflect snapshot N. Guard against a
                // redundant load when the picker already held this value; the
                // Bug-1 generation guard makes a stray concurrent reload safe, but
                // avoid an obviously wasteful double-load.
                if (sel.value !== prev) {
                    loadCityData();
                    loadFinancials();
                    reloadMaterialsIfActive();
                }
            }
        }

        function selectSnapshot(id, opts) {
            const sel = selectById('snapshot-picker');
            if (!sel) return;
            const target = id == null ? '' : String(id);
            if (sel.value !== target) sel.value = target;
            loadCityData();
            loadFinancials();
            reloadMaterialsIfActive();
            if (!opts || opts.push !== false) Router.push();
        }
        const snapshotPicker = selectById('snapshot-picker');
        if (snapshotPicker) {
            snapshotPicker.addEventListener('change', () => selectSnapshot(snapshotPicker.value));
            // Initial fetch — uses the city already reflected in DATA_PREFIX,
            // which the applyInitialState IIFE has already set from URL.
            const initSlug = (typeof CITIES !== 'undefined' && CITIES.length > 0)
                ? (selectById('city-select')?.value || CITIES[0].slug)
                : '';
            loadSnapshots(initSlug);
        }

        // Financials
        const COLORS = ['#ef4444', '#f59e0b', '#22c55e', '#3b82f6', '#8b5cf6', '#ec4899'];

        // Shared forecast helpers, used by the Financials, Materials, and
        // interactive-forecast views.
        //
        // Cost/material tier → chart color; tierColor() applies the neutral
        // fallback for an unrecognized tier label.
        const TIER_COLORS = { preventive: '#22c55e', rehab: '#f59e0b', reconstruction: '#ef4444' };
        function tierColor(label) { return TIER_COLORS[label] || '#9ca3af'; }
        // Chart.js legend items for a tier-colored bar chart (Financials +
        // Materials share this exact legend).
        function tierLegendLabels() {
            return [
                { text: 'Preventive', fillStyle: TIER_COLORS.preventive, strokeStyle: TIER_COLORS.preventive },
                { text: 'Rehab', fillStyle: TIER_COLORS.rehab, strokeStyle: TIER_COLORS.rehab },
                { text: 'Reconstruction', fillStyle: TIER_COLORS.reconstruction, strokeStyle: TIER_COLORS.reconstruction }
            ];
        }
        // The funding-level scenarios the charts plot (order matters for COLORS).
        const FUNDING_SCENARIO_NAMES = ['baseline', 'fund-25pct', 'fund-50pct', 'fund-100pct'];
        // scenarios.json / city.scenarios is scope-keyed ({city, bbox, summary}).
        // effectiveScope resolves a preferred scope to one actually present;
        // scenariosForScope selects the list for a scope with the city→bbox
        // fallback every caller uses.
        function effectiveScope(byScope, preferred) {
            if (preferred === 'city' && !byScope.city && byScope.bbox) return 'bbox';
            if (preferred === 'bbox' && !byScope.bbox && byScope.city) return 'city';
            return preferred;
        }
        function scenariosForScope(byScope, scope) {
            return byScope[scope] || byScope.city || byScope.bbox;
        }

        // Vertical crosshair plugin: hover line + click-to-pin
        const verticalCrosshairPlugin = {
            id: 'verticalCrosshair',
            afterInit(chart) {
                chart.$crosshair = { x: null, pinnedX: null, pinnedIndex: null };
            },
            afterEvent(chart, args) {
                if (chart.config.options.plugins?.verticalCrosshair === false) return;
                const evt = args.event;
                const area = chart.chartArea;
                if (!area) return;
                const inArea = evt.x >= area.left && evt.x <= area.right &&
                               evt.y >= area.top && evt.y <= area.bottom;
                if (evt.type === 'mousemove') {
                    chart.$crosshair.x = inArea ? evt.x : null;
                    chart.canvas.style.cursor = inArea ? 'crosshair' : 'default';
                    args.changed = true;
                }
                if (evt.type === 'mouseout') {
                    chart.$crosshair.x = null;
                    chart.canvas.style.cursor = 'default';
                    args.changed = true;
                }
                if (evt.type === 'click' && inArea) {
                    const xScale = chart.scales.x;
                    const idx = xScale.getValueForPixel(evt.x);
                    const snapped = xScale.getPixelForValue(idx);
                    if (chart.$crosshair.pinnedIndex === idx) {
                        chart.$crosshair.pinnedX = null;
                        chart.$crosshair.pinnedIndex = null;
                    } else {
                        chart.$crosshair.pinnedX = snapped;
                        chart.$crosshair.pinnedIndex = idx;
                    }
                    args.changed = true;
                }
            },
            afterDraw(chart) {
                const cs = chart.$crosshair;
                if (!cs) return;
                if (chart.config.options.plugins?.verticalCrosshair === false) return;
                const area = chart.chartArea;
                const ctx = chart.ctx;
                if (cs.pinnedX !== null) {
                    ctx.save();
                    ctx.beginPath();
                    ctx.moveTo(cs.pinnedX, area.top);
                    ctx.lineTo(cs.pinnedX, area.bottom);
                    ctx.lineWidth = 1.5;
                    ctx.strokeStyle = 'rgba(100, 100, 100, 0.8)';
                    ctx.setLineDash([]);
                    ctx.stroke();
                    ctx.restore();
                }
                if (cs.x !== null && (cs.pinnedX === null || Math.abs(cs.x - cs.pinnedX) > 3)) {
                    ctx.save();
                    ctx.beginPath();
                    ctx.moveTo(cs.x, area.top);
                    ctx.lineTo(cs.x, area.bottom);
                    ctx.lineWidth = 1;
                    ctx.strokeStyle = 'rgba(100, 100, 100, 0.4)';
                    ctx.setLineDash([4, 4]);
                    ctx.stroke();
                    ctx.restore();
                }
            }
        };
        Chart.register(verticalCrosshairPlugin);

        function applyChartTheme() {
            var s = getComputedStyle(document.documentElement);
            Chart.defaults.color = s.getPropertyValue('--text-muted').trim();
            Chart.defaults.borderColor = s.getPropertyValue('--border').trim();
        }
        applyChartTheme();

        document.addEventListener('pvmt:themechange', function() {
            applyChartTheme();
            (typeof currentCharts !== 'undefined' ? currentCharts : [])
                .concat(typeof compareCharts !== 'undefined' ? compareCharts : [])
                .concat(typeof aggregateCharts !== 'undefined' ? aggregateCharts : [])
                .concat(typeof materialsCharts !== 'undefined' ? materialsCharts : [])
                .forEach(function(c) { c.update('none'); });
        });

        function fmt$(n) {
            if (n >= 1e9) return '$' + (n/1e9).toFixed(1) + 'B';
            if (n >= 1e6) return '$' + (n/1e6).toFixed(1) + 'M';
            if (n >= 1e3) return '$' + (n/1e3).toFixed(0) + 'K';
            return '$' + n.toFixed(0);
        }

        let scenarioData = null;
        let currentCharts = [];

        function destroyCharts() {
            currentCharts.forEach(c => c.destroy());
            currentCharts = [];
        }

        // Declared as a reassignable `let` (not a function declaration) so the
        // chart-ref-capturing wrapper installed further down — see
        // _origRenderFinancials — is legal to both ESLint (no-func-assign) and
        // tsc --checkJs (TS2630). Callers run only after this point, so there is
        // no temporal-dead-zone hazard (same reason no-use-before-define is off).
        let renderFinancials = function(scenarios, scope) {
            if (!scenarios || scenarios.length === 0) {
                document.getElementById('charts').innerHTML = '';
                return;
            }

            destroyCharts();

            // Find baseline (do-nothing)
            const baseline = scenarios.find(s => s.scenario.strategy === 0 || s.scenario.name === 'do-nothing' || s.scenario.name === 'baseline');
            if (!baseline) return;

            // Charts
            const chartsDiv = document.getElementById('charts');
            chartsDiv.innerHTML = '';

            // Get funding-level scenarios
            const fundingScenarios = scenarios.filter(s =>
                FUNDING_SCENARIO_NAMES.includes(s.scenario.name)
            );

            // 1. PCI over time
            if (fundingScenarios.length > 0) {
                const card = makeChartCard('PCI Over Time by Funding Level');
                chartsDiv.appendChild(card);
                const ctx = card.querySelector('canvas').getContext('2d');
                currentCharts.push(new Chart(ctx, {
                    type: 'line',
                    data: {
                        labels: fundingScenarios[0].years.map(y => 'Year ' + y.year),
                        datasets: fundingScenarios.map((s, i) => ({
                            label: s.scenario.label,
                            data: s.years.map(y => y.pci),
                            borderColor: COLORS[i % COLORS.length],
                            backgroundColor: 'transparent',
                            tension: 0.3,
                            pointRadius: 0
                        }))
                    },
                    options: { responsive: true, interaction: { mode: 'index', intersect: false }, scales: { y: { min: 0, max: 100, title: { display: true, text: 'PCI' } } }, plugins: { legend: { position: 'bottom' } } }
                }));
            }

            // 2. Deferred backlog growth
            if (fundingScenarios.length > 0) {
                const card = makeChartCard('Deferred Maintenance Backlog');
                chartsDiv.appendChild(card);
                const ctx = card.querySelector('canvas').getContext('2d');
                currentCharts.push(new Chart(ctx, {
                    type: 'line',
                    data: {
                        labels: fundingScenarios[0].years.map(y => 'Year ' + y.year),
                        datasets: fundingScenarios.map((s, i) => ({
                            label: s.scenario.label,
                            data: s.years.map(y => y.deferred_backlog),
                            borderColor: COLORS[i % COLORS.length],
                            backgroundColor: COLORS[i % COLORS.length] + '20',
                            fill: s.scenario.name === 'baseline',
                            tension: 0.3,
                            pointRadius: 0
                        }))
                    },
                    options: { responsive: true, interaction: { mode: 'index', intersect: false }, scales: { y: { title: { display: true, text: 'Dollars ($)' }, ticks: { callback: v => fmt$(v) } } }, plugins: { legend: { position: 'bottom' }, tooltip: { callbacks: { label: ctx => ctx.dataset.label + ': ' + fmt$(ctx.parsed.y) } } } }
                }));
            }

            // 3. Cumulative cost (exclude baseline)
            const fundedScenarios = fundingScenarios.filter(s => s.scenario.name !== 'baseline');
            if (fundedScenarios.length > 0) {
                const card = makeChartCard('Cumulative Spending');
                chartsDiv.appendChild(card);
                const ctx = card.querySelector('canvas').getContext('2d');
                currentCharts.push(new Chart(ctx, {
                    type: 'line',
                    data: {
                        labels: fundedScenarios[0].years.map(y => 'Year ' + y.year),
                        datasets: fundedScenarios.map((s, i) => {
                            let cum = 0;
                            return {
                                label: s.scenario.label,
                                data: s.years.map(y => { cum += y.annual_spend; return cum; }),
                                borderColor: COLORS[(i + 1) % COLORS.length],
                                backgroundColor: 'transparent',
                                tension: 0.3,
                                pointRadius: 0
                            };
                        })
                    },
                    options: { responsive: true, interaction: { mode: 'index', intersect: false }, scales: { y: { title: { display: true, text: 'Dollars ($)' }, ticks: { callback: v => fmt$(v) } } }, plugins: { legend: { position: 'bottom' }, tooltip: { callbacks: { label: ctx => ctx.dataset.label + ': ' + fmt$(ctx.parsed.y) } } } }
                }));
            }

            // 4. Annual cost by tier (baseline)
            if (baseline) {
                const card = makeChartCard('Annual Treatment Need by Condition Tier');
                chartsDiv.appendChild(card);
                const ctx = card.querySelector('canvas').getContext('2d');
                const bgColors = baseline.years.map(y => tierColor(y.cost_tier));
                currentCharts.push(new Chart(ctx, {
                    type: 'bar',
                    data: {
                        labels: baseline.years.map(y => 'Year ' + y.year),
                        datasets: [{
                            label: 'Annual Need',
                            data: baseline.years.map(y => y.annual_need),
                            backgroundColor: bgColors
                        }]
                    },
                    options: {
                        responsive: true,
                        scales: { y: { title: { display: true, text: 'Dollars ($)' }, ticks: { callback: v => fmt$(v) } } },
                        plugins: {
                            verticalCrosshair: false,
                            legend: {
                                display: true,
                                position: 'bottom',
                                labels: { generateLabels: tierLegendLabels }
                            }
                        }
                    }
                }));
            }
        }

        var forecastData = null;
        var lastCohorts = null;
        var cohortSortCol = 1; // default: area descending
        var cohortSortAsc = false;

        // Column definitions for sorting: [key, type]
        var cohortColumns = [
            ['classification', 'string'],
            ['area', 'number'],
            ['area_pct', 'number'],
            ['decay_rate', 'number'],
            ['end_pci', 'number'],
            ['total_spend', 'number'],
            ['total_deficit', 'number']
        ];

        function renderCohortBreakdown(cohorts, keepSort) {
            lastCohorts = cohorts;
            const totalArea = cohorts.reduce((s, c) => s + c.area, 0);

            // Enrich with computed fields for sorting
            const enriched = cohorts.map(c => ({
                ...c,
                area_pct: totalArea > 0 ? c.area / totalArea * 100 : 0,
                total_spend: c.total_spend || 0,
                total_deficit: c.total_deficit || 0
            }));

            // Sort
            if (!keepSort) { cohortSortCol = 1; cohortSortAsc = false; }
            const key = cohortColumns[cohortSortCol][0];
            const isStr = cohortColumns[cohortSortCol][1] === 'string';
            const sorted = [...enriched].sort((a, b) => {
                const av = a[key], bv = b[key];
                const cmp = isStr ? String(av).localeCompare(String(bv)) : av - bv;
                return cohortSortAsc ? cmp : -cmp;
            });

            function fmtCurrency(v) {
                return '$' + v.toLocaleString('en-US', { minimumFractionDigits: 0, maximumFractionDigits: 0 });
            }

            // Update header sort arrows
            queryAll('#cohort-table thead th').forEach((th, i) => {
                var arrow = th.querySelector('.sort-arrow');
                if (!arrow) { arrow = document.createElement('span'); arrow.className = 'sort-arrow'; th.appendChild(arrow); }
                arrow.textContent = i === cohortSortCol ? (cohortSortAsc ? '\u25B2' : '\u25BC') : '';
            });

            const tbody = document.querySelector('#cohort-table tbody');
            tbody.innerHTML = '';
            let totalSpend = 0;
            let totalDeficit = 0;

            sorted.forEach(c => {
                const areaLargeVal = toAreaLarge(c.area);
                const pci = c.end_pci;
                let cls = 'pci-good';
                if (pci < 40) cls = 'pci-poor';
                else if (pci < 60) cls = 'pci-fair';

                totalSpend += c.total_spend;
                totalDeficit += c.total_deficit;

                const tr = document.createElement('tr');
                tr.innerHTML =
                    '<td style="text-transform:capitalize">' + c.classification + '</td>' +
                    '<td class="number">' + areaLargeVal.toFixed(1) + '</td>' +
                    '<td class="number">' + c.area_pct.toFixed(1) + '%</td>' +
                    '<td class="number">' + c.decay_rate.toFixed(4) + '</td>' +
                    '<td class="number"><span class="pci-badge ' + cls + '">' + pci.toFixed(0) + '</span></td>' +
                    '<td class="number">' + fmtCurrency(c.total_spend) + '</td>' +
                    '<td class="number">' + fmtCurrency(c.total_deficit) + '</td>';
                tbody.appendChild(tr);
            });

            // Totals row
            const totalAreaLarge = toAreaLarge(totalArea);
            const totTr = document.createElement('tr');
            totTr.className = 'total-row';
            totTr.innerHTML =
                '<td>Total</td>' +
                '<td class="number">' + totalAreaLarge.toFixed(1) + '</td>' +
                '<td class="number">100%</td>' +
                '<td></td><td></td>' +
                '<td class="number">' + fmtCurrency(totalSpend) + '</td>' +
                '<td class="number">' + fmtCurrency(totalDeficit) + '</td>';
            tbody.appendChild(totTr);

            document.getElementById('cohort-section').style.display = 'block';
        }

        // Bind sort click handlers
        queryAll('#cohort-table thead th').forEach((th, i) => {
            th.addEventListener('click', () => {
                if (!lastCohorts) return;
                if (cohortSortCol === i) {
                    cohortSortAsc = !cohortSortAsc;
                } else {
                    cohortSortCol = i;
                    cohortSortAsc = cohortColumns[i][1] === 'string'; // strings default asc, numbers desc
                }
                renderCohortBreakdown(lastCohorts, true);
            });
        });

        async function loadFinancials() {
            const gen = ++financialsLoadGen; // bail stale loads after each await
            const prefix = DATA_PREFIX; // snapshot to avoid mid-await mutation
            // Clear any prior city's solvency headline up front. forecastData is
            // only reassigned on a successful forecast.json load below, so
            // without this an early return (no scenarios) or a failed
            // forecast.json fetch would leave the previous city's metrics on
            // screen — a wrong dollar claim under the new city's name.
            forecastData = null;
            renderSolvencyHeadline();
            // Also hide the previous city's headline tiles, cohort table, and
            // forecast controls up front, mirroring the solvency-headline reset
            // above: an early return below (no scenarios / failed fetch) must not
            // leave stale dollar tiles or a cohort table under the new city name.
            document.getElementById('headlines').style.display = 'none';
            document.getElementById('cohort-section').style.display = 'none';
            document.querySelector('#cohort-table tbody').innerHTML = '';
            document.getElementById('forecast-controls').classList.remove('visible');
            // Horizon-derived labels (never hardcode 20). FORECAST_SEED.years is
            // the configured forecast horizon; the headline/cohort values are
            // summed over it, so the captions must follow it too.
            {
                const yrs = (FORECAST_SEED && FORECAST_SEED.years) || 20;
                document.getElementById('hl-spend-label').textContent = yrs + '-Year Spend';
                document.getElementById('hl-deficit-label').textContent = yrs + '-Year Deficit';
                document.getElementById('cohort-spend-th').textContent = yrs + 'yr Spend';
                document.getElementById('cohort-deficit-th').textContent = yrs + 'yr Deficit';
            }
            const loadedScenarios = await loadJSON(dataURL(prefix, 'scenarios.json'));
            if (gen !== financialsLoadGen) return; // a newer load superseded us
            scenarioData = loadedScenarios;
            if (!scenarioData) {
                document.getElementById('charts').innerHTML = '<div class="no-data">No financial data available. Run <code>pvmt forecast</code> and <code>pvmt export</code> first.</div>';
                return;
            }

            // Fall back if currentScope is unsupported by the loaded
            // scenarios (e.g. URL said scope=city but this city only has
            // bbox data). Mirrors the same repair in loadCityData.
            currentScope = effectiveScope(scenarioData, currentScope);
            queryAll('#scope-toggle button').forEach(b =>
                b.classList.toggle('active', b.dataset.scope === currentScope));
            maybeRevealScopeToggle();
            const scenarios = scenariosForScope(scenarioData, currentScope);

            if (!scenarios || scenarios.length === 0) {
                document.getElementById('charts').innerHTML = '<div class="no-data">No scenario data found.</div>';
                return;
            }

            renderFinancials(scenarios, currentScope);

            // Update headline tiles from baseline scenario (works without WASM)
            const baseline = scenarios.find(s =>
                s.scenario.strategy === 0 || s.scenario.name === 'do-nothing' || s.scenario.name === 'baseline'
            );
            if (baseline) {
                updateSummaryCards(baseline);
            }

            // Fetch per-city forecast seed for WASM interactive controls
            {
                const seed = await loadJSON(dataURL(prefix, 'forecast_seed.json'));
                if (gen !== financialsLoadGen) return; // a newer load superseded us
                if (seed) {
                    FORECAST_SEED = seed;
                    // Re-apply horizon-derived labels now that this city's seed
                    // (and its configured years) is loaded.
                    if (seed.years) {
                        document.getElementById('hl-spend-label').textContent = seed.years + '-Year Spend';
                        document.getElementById('hl-deficit-label').textContent = seed.years + '-Year Deficit';
                        document.getElementById('cohort-spend-th').textContent = seed.years + 'yr Spend';
                        document.getElementById('cohort-deficit-th').textContent = seed.years + 'yr Deficit';
                    }
                    syncPCISliderFromSeed();
                    rebuildTierInputs();
                    if (wasmReady) runCustomScenario();
                }
            }

            // Fetch forecast.json (used by scope toggle for static fallback)
            {
                const f = await loadJSON(dataURL(prefix, 'forecast.json'));
                if (gen !== financialsLoadGen) return; // a newer load superseded us
                if (f) forecastData = f;
                renderSolvencyHeadline();
            }
        }

        // Materials
        //
        // Material demand is a linear function of already-computed forecast data:
        // each simulated year retreats one treatment-cycle slice of the network
        // (treatedArea = year.area / treatment_cycle_years, the same 1/N slice the
        // cost model bills as eligibleFrac), and the per-tier material intensity
        // shipped in the seed (material_tiers) turns that treated area into asphalt
        // mix, binder, and oil-equivalent barrels. The per-year tier is the
        // network-blended cost_tier — coarser than the per-cohort interpolated
        // dollar model, so these figures track the scenario's condition trajectory
        // rather than reconciling with annual_need. The headline uses the
        // best-funded scenario (highest final PCI) as the "network kept in repair"
        // figure; the do-nothing baseline's rising reconstruction share shows the
        // deferred-maintenance material blowout.
        var BARRELS_PER_TON_BINDER = 6.23;

        function destroyMaterialsCharts() {
            materialsCharts.forEach(function(c) { c.destroy(); });
            materialsCharts = [];
        }

        // Reactive reload for city/scope/snapshot changes. Unlike Financials —
        // whose data feeds other views and so always reloads — Materials is
        // self-contained to its own tab, so only refresh when it's the active
        // tab. Opening the tab (selectTab / deep link) reloads unconditionally,
        // so a change made while elsewhere is still reflected on next open.
        function reloadMaterialsIfActive() {
            var el = document.getElementById('materials-tab');
            if (el && el.classList.contains('active')) loadMaterials();
        }

        // Metric tonnes by default; US short tons under imperial display (barrels
        // are unit-neutral). Mirrors the UNIT_SYSTEM branch in the area helpers.
        function fmtMass(kg) {
            if (UNIT_SYSTEM === 'metric') return fmtNum(kg / 1000) + ' t';
            return fmtNum(kg / 907.18474) + ' tons';
        }
        function fmtBarrels(bbl) {
            return fmtNum(bbl) + ' bbl';
        }

        // materialSeries computes per-year material quantities (kg + barrels) for
        // one scenario given the treatment cycle N, a label->tier lookup, and a
        // fallback tier used when a year's cost_tier has no matching material tier
        // (e.g. a custom tier set). fallbackTier is the highest-intensity tier —
        // mirroring the Go MaterialTierFor fallback — so an unmatched label
        // over-counts rather than silently reporting zero material.
        function materialSeries(scenario, cycleYears, tierByLabel, fallbackTier) {
            var zero = { mix_kg_per_sqm: 0, binder_pct: 0 };
            return scenario.years.map(function(y) {
                var treatedArea = cycleYears > 0 ? y.area / cycleYears : 0;
                var tier = tierByLabel[y.cost_tier] || fallbackTier || zero;
                var mixKg = treatedArea * tier.mix_kg_per_sqm;
                var binderKg = mixKg * tier.binder_pct;
                return {
                    year: y.year,
                    tier: y.cost_tier,
                    mixKg: mixKg,
                    binderKg: binderKg,
                    aggregateKg: mixKg - binderKg,
                    oilBarrels: (binderKg / 1000) * BARRELS_PER_TON_BINDER
                };
            });
        }

        function renderMaterialTierTable(tiers) {
            var section = document.getElementById('materials-tier-section');
            var tbody = document.querySelector('#materials-tier-table tbody');
            if (!tiers || tiers.length === 0) { section.style.display = 'none'; return; }
            tbody.innerHTML = tiers.map(function(t) {
                var binderKgSqM = t.mix_kg_per_sqm * t.binder_pct;
                // Over 1000 m^2 the binder mass in tonnes equals binderKgSqM, so
                // barrels/1000 m^2 = binderKgSqM * BARRELS_PER_TON_BINDER.
                var oilPer1000 = binderKgSqM * BARRELS_PER_TON_BINDER;
                var dot = '<span style="display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:6px;background:' +
                    tierColor(t.label) + '"></span>';
                return '<tr>' +
                    '<td>' + dot + esc(t.label) + '</td>' +
                    '<td class="number">' + t.mix_kg_per_sqm.toFixed(0) + '</td>' +
                    '<td class="number">' + (t.binder_pct * 100).toFixed(1) + '%</td>' +
                    '<td class="number">' + binderKgSqM.toFixed(1) + '</td>' +
                    '<td class="number">' + oilPer1000.toFixed(1) + '</td>' +
                    '</tr>';
            }).join('');
            section.style.display = '';
        }

        function renderMaterials(scenarios, seed, scope) {
            var chartsDiv = document.getElementById('materials-charts');
            destroyMaterialsCharts();
            chartsDiv.innerHTML = '';
            if (!scenarios || scenarios.length === 0) {
                document.getElementById('materials-headlines').style.display = 'none';
                document.getElementById('materials-tier-section').style.display = 'none';
                document.getElementById('materials-note').style.display = 'none';
                chartsDiv.innerHTML = '<div class="no-data">No scenario data found.</div>';
                return;
            }
            var tiers = (seed && seed.material_tiers) || [];
            var tierByLabel = {};
            tiers.forEach(function(t) { tierByLabel[t.label] = t; });
            // Highest-intensity tier, used as the unmatched-label fallback.
            var worstTier = tiers.reduce(function(a, b) {
                return b.mix_kg_per_sqm > a.mix_kg_per_sqm ? b : a;
            }, tiers[0]);
            var cycleYears = (seed && seed.treatment_cycle_years) || 12;

            var fundingScenarios = scenarios.filter(function(s) {
                return FUNDING_SCENARIO_NAMES.includes(s.scenario.name);
            });
            if (fundingScenarios.length === 0) fundingScenarios = scenarios.slice(0, 1);

            var seriesByName = {};
            fundingScenarios.forEach(function(s) {
                seriesByName[s.scenario.name] = materialSeries(s, cycleYears, tierByLabel, worstTier);
            });

            // Final-year PCI, tolerant of an empty years array.
            var finalPci = function(s) {
                var ys = s.years;
                return ys && ys.length ? ys[ys.length - 1].pci : 0;
            };
            // "Kept in repair" figure = best-funded scenario (highest final PCI).
            var best = fundingScenarios.reduce(function(a, b) {
                return finalPci(b) > finalPci(a) ? b : a;
            });
            var bestSeries = seriesByName[best.scenario.name];
            var n = bestSeries.length || 1;
            var avgMix = bestSeries.reduce(function(acc, r) { return acc + r.mixKg; }, 0) / n;
            var avgBinder = bestSeries.reduce(function(acc, r) { return acc + r.binderKg; }, 0) / n;
            var avgOil = bestSeries.reduce(function(acc, r) { return acc + r.oilBarrels; }, 0) / n;

            document.getElementById('hl-mix').textContent = fmtMass(avgMix) + '/yr';
            document.getElementById('hl-binder').textContent = fmtMass(avgBinder) + '/yr';
            document.getElementById('hl-oil').textContent = fmtBarrels(avgOil) + '/yr';
            document.getElementById('hl-mix-sub').textContent = 'Avg/yr under "' + esc(best.scenario.label) + '"';
            document.getElementById('hl-oil-sub').textContent = '~' + BARRELS_PER_TON_BINDER + ' bbl per tonne binder';
            document.getElementById('materials-headlines').style.display = '';

            var note = document.getElementById('materials-note');
            note.textContent = 'Each year ~1/' + Math.round(cycleYears) + ' of the network (area ÷ ' +
                'treatment cycle) is retreated; material intensity follows the blended condition tier, ' +
                'so the do-nothing line climbs as pavement decays into reconstruction. Planning-grade estimates.';
            note.style.display = '';

            renderMaterialTierTable(tiers);

            var labels = bestSeries.map(function(r) { return 'Year ' + r.year; });
            var massAxis = function() { return { title: { display: true, text: UNIT_SYSTEM === 'metric' ? 'Tonnes' : 'Tons' }, ticks: { callback: function(v) { return fmtMass(v); } } }; };

            // 1. Annual asphalt mix by funding level.
            (function() {
                var card = makeChartCard('Annual Asphalt Mix by Funding Level');
                chartsDiv.appendChild(card);
                var ctx = card.querySelector('canvas').getContext('2d');
                materialsCharts.push(new Chart(ctx, {
                    type: 'line',
                    data: {
                        labels: labels,
                        datasets: fundingScenarios.map(function(s, i) {
                            return {
                                label: s.scenario.label,
                                data: seriesByName[s.scenario.name].map(function(r) { return r.mixKg; }),
                                borderColor: COLORS[i % COLORS.length],
                                backgroundColor: 'transparent',
                                tension: 0.3,
                                pointRadius: 0
                            };
                        })
                    },
                    options: { responsive: true, interaction: { mode: 'index', intersect: false }, scales: { y: massAxis() }, plugins: { legend: { position: 'bottom' }, tooltip: { callbacks: { label: function(ctx) { return ctx.dataset.label + ': ' + fmtMass(ctx.parsed.y); } } } } }
                }));
            })();

            // 2. Annual binder / oil demand by funding level.
            (function() {
                var card = makeChartCard('Annual Binder & Oil-Equivalent Demand');
                chartsDiv.appendChild(card);
                var ctx = card.querySelector('canvas').getContext('2d');
                materialsCharts.push(new Chart(ctx, {
                    type: 'line',
                    data: {
                        labels: labels,
                        datasets: fundingScenarios.map(function(s, i) {
                            return {
                                label: s.scenario.label,
                                data: seriesByName[s.scenario.name].map(function(r) { return r.binderKg; }),
                                borderColor: COLORS[i % COLORS.length],
                                backgroundColor: 'transparent',
                                tension: 0.3,
                                pointRadius: 0
                            };
                        })
                    },
                    options: { responsive: true, interaction: { mode: 'index', intersect: false }, scales: { y: massAxis() }, plugins: { legend: { position: 'bottom' }, tooltip: { callbacks: { label: function(ctx) { return ctx.dataset.label + ': ' + fmtMass(ctx.parsed.y) + ' · ' + fmtBarrels(ctx.parsed.y / 1000 * BARRELS_PER_TON_BINDER); } } } } }
                }));
            })();

            // 3. Annual mix by condition tier for the do-nothing baseline — shows
            // the deferred-maintenance blowout as the network decays.
            (function() {
                var baseline = scenarios.find(function(s) {
                    return s.scenario.strategy === 0 || s.scenario.name === 'do-nothing' || s.scenario.name === 'baseline';
                });
                if (!baseline) return;
                var series = materialSeries(baseline, cycleYears, tierByLabel, worstTier);
                var card = makeChartCard('Annual Mix by Condition Tier (Do-Nothing)');
                chartsDiv.appendChild(card);
                var ctx = card.querySelector('canvas').getContext('2d');
                materialsCharts.push(new Chart(ctx, {
                    type: 'bar',
                    data: {
                        labels: series.map(function(r) { return 'Year ' + r.year; }),
                        datasets: [{
                            label: 'Asphalt Mix',
                            data: series.map(function(r) { return r.mixKg; }),
                            backgroundColor: series.map(function(r) { return tierColor(r.tier); })
                        }]
                    },
                    options: {
                        responsive: true,
                        scales: { y: massAxis() },
                        plugins: {
                            verticalCrosshair: false,
                            legend: { display: true, position: 'bottom', labels: { generateLabels: tierLegendLabels } },
                            tooltip: { callbacks: { label: function(ctx) { return fmtMass(ctx.parsed.y); } } }
                        }
                    }
                }));
            })();
        }

        async function loadMaterials() {
            var gen = ++materialsLoadGen; // bail stale loads after each await
            var prefix = DATA_PREFIX;
            var scenarios = await loadJSON(dataURL(prefix, 'scenarios.json'));
            if (gen !== materialsLoadGen) return;
            var seed = await loadJSON(dataURL(prefix, 'forecast_seed.json'));
            if (gen !== materialsLoadGen) return;
            var chartsDiv = document.getElementById('materials-charts');
            if (!scenarios) {
                document.getElementById('materials-headlines').style.display = 'none';
                document.getElementById('materials-tier-section').style.display = 'none';
                document.getElementById('materials-note').style.display = 'none';
                chartsDiv.innerHTML = '<div class="no-data">No material data available. Run <code>pvmt forecast</code> and <code>pvmt export</code> first.</div>';
                return;
            }
            // Scope fallback mirrors loadFinancials: honor currentScope but fall
            // back when the loaded scenarios lack it.
            var scope = effectiveScope(scenarios, currentScope);
            var list = scenariosForScope(scenarios, scope);
            renderMaterials(list, seed, scope);
        }

        // Set by renderAggregate once the region data loads: true when any
        // city in the config carries both city- and bbox-scoped scenarios.
        var regionHasBothScopes = false;

        // Reveal the scope toggle only when BOTH city and bbox data are
        // detectable in at least one data source (hex grid or scenarios).
        // Idempotent — both loaders call this after their fetches settle.
        // On the Aggregate tab the toggle is region-wide, so honor whether
        // ANY city has both scopes rather than just the selected dropdown city.
        function maybeRevealScopeToggle() {
            const haveHexCity = hexDataByScope.city && Object.keys(hexDataByScope.city).length > 0;
            const haveHexBbox = hexDataByScope.bbox && Object.keys(hexDataByScope.bbox).length > 0;
            const haveScenCity = !!(scenarioData && scenarioData.city);
            const haveScenBbox = !!(scenarioData && scenarioData.bbox);
            const haveCity = haveHexCity || haveScenCity;
            const haveBbox = haveHexBbox || haveScenBbox;
            const aggActive = (function() {
                const el = document.getElementById('aggregate-tab');
                return el && el.classList.contains('active');
            })();
            const reveal = (haveCity && haveBbox) || (aggActive && regionHasBothScopes);
            document.getElementById('gear-scope-row').hidden = !reveal;
        }

        // Resolve the display name of the city currently in view from
        // DATA_PREFIX (cities/<slug>/). Falls back gracefully for single-city
        // exports or an unmatched slug.
        function currentCityName() {
            var slug = DATA_PREFIX.replace(/^cities\//, '').replace(/\/$/, '');
            var c = (typeof CITIES !== 'undefined') ? CITIES.find(function(c) { return c.slug === slug; }) : null;
            if (c) return c.name;
            if (typeof CITIES !== 'undefined' && CITIES.length > 0) return CITIES[0].name;
            return 'This city';
        }

        // Render the roads-only solvency headline above the Financials charts
        // from the per-city forecast.json (the selected city's forecastData).
        // Break-even is always shown; the insolvency year and funding gap are
        // shown only when a current_budget is configured (cited). Metrics are
        // roads-only and labeled as such, so they don't read as whole-network.
        function renderSolvencyHeadline() {
            var el = document.getElementById('solvency-headline');
            if (!el) return;
            var roads = forecastData ? forecastData.find(function(f) { return f.resource_type === 'roads'; }) : null;
            if (!roads || !(roads.break_even_budget > 0)) {
                el.style.display = 'none';
                return;
            }
            var name = esc(currentCityName());
            var breakEven = roads.break_even_budget;
            var lead, detail;
            var hasBudget = roads.current_budget > 0;
            if (hasBudget) {
                var gapDollars = breakEven - roads.current_budget;
                if (roads.insolvency_year) {
                    // Forecast year 1 == the current calendar year.
                    var crossYear = new Date().getFullYear() + roads.insolvency_year - 1;
                    lead = 'At current funding, ' + name + '’s street-repair backlog grows to an entire treatment cycle of deferred work by <span class="num">' + crossYear + '</span> (year ' + roads.insolvency_year + ').';
                } else {
                    lead = 'At current funding, ' + name + '’s streets stay solvent through the forecast horizon.';
                }
                if (gapDollars > 0) {
                    detail = 'Holding the network steady would take <strong>' + fmtCost(breakEven) + '/yr</strong> — <strong>' + fmtCost(gapDollars) + ' more</strong> than today’s ' + fmtCost(roads.current_budget) + ' (streets/roads only).';
                } else {
                    detail = 'Today’s ' + fmtCost(roads.current_budget) + '/yr is at or above the ' + fmtCost(breakEven) + '/yr needed to hold the network steady (streets/roads only).';
                }
            } else {
                lead = 'Holding ' + name + '’s streets steady would take <span class="num">' + fmtCost(breakEven) + '/yr</span> (streets/roads only).';
                detail = 'No current budget configured for this city, so the funding gap and insolvency year aren’t shown. Set <code>forecast.current_budget</code> (cited) to see them.';
            }
            el.innerHTML = '<div class="lead">' + lead + '</div><div class="detail">' + detail + '</div>';
            el.style.display = 'block';
        }

        // Render the cohort breakdown from the scope-appropriate forecast
        // baseline. Extracted so selectScope can call it without duplicating
        // the bbox/city baseline-picking logic.
        function renderCohortFromForecast(scope) {
            if (!forecastData) return;
            const roads = forecastData.find(f => f.resource_type === 'roads');
            const src = (scope === 'bbox' && roads && roads.bbox_baseline) ? roads.bbox_baseline : (roads && roads.baseline);
            if (src && src.final_cohorts && src.final_cohorts.length > 1) {
                renderCohortBreakdown(src.final_cohorts);
            } else {
                document.getElementById('cohort-section').style.display = 'none';
            }
        }

        // Single fan-out for a scope change. Updates currentScope, the
        // toggle DOM state, the map heatmap, the financials view, and the
        // URL — every other code path (button click, popstate, internal
        // fallback) goes through here so the four views never drift apart.
        function selectScope(scope, opts) {
            const btn = document.querySelector('#scope-toggle button[data-scope="' + scope + '"]');
            if (!btn) return;
            currentScope = scope;
            queryAll('#scope-toggle button').forEach(b =>
                b.classList.toggle('active', b.dataset.scope === scope));

            updateHexGrid();

            if (scenarioData) {
                const scenarios = scenariosForScope(scenarioData, scope);
                if (scenarios) renderFinancials(scenarios, scope);
                if (wasmReady) {
                    runCustomScenario();
                } else {
                    renderCohortFromForecast(scope);
                }
            }

            // The Aggregate tab freezes nothing — it reflects the live scope.
            // Compare deliberately does NOT react (it's a one-shot frozen at
            // first load), so only the aggregate is re-rendered here. Guarded
            // so it only fires once its data has loaded and the tab is active.
            if (aggregateLoaded && allCityDataPromise) {
                const aggEl = document.getElementById('aggregate-tab');
                if (aggEl && aggEl.classList.contains('active')) loadAggregateData();
            }

            // Materials is per-city like Financials, so it must follow the scope
            // toggle too — but only when it's the active tab (it re-fetches, unlike
            // the in-memory renderFinancials re-render above).
            reloadMaterialsIfActive();

            if (!opts || opts.push !== false) Router.push();
        }
        queryAll('#scope-toggle button').forEach(btn => {
            btn.addEventListener('click', () => selectScope(btn.dataset.scope));
        });

        // Gear popover: groups snapshot picker, scope toggle, and theme
        // toggle behind a single settings affordance. Closes on outside
        // click or Escape.
        (function initGearMenu() {
            const btn = document.getElementById('gear-btn');
            const menu = document.getElementById('gear-menu');
            if (!btn || !menu) return;
            function setOpen(open) {
                menu.hidden = !open;
                btn.setAttribute('aria-expanded', open ? 'true' : 'false');
            }
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                setOpen(menu.hidden);
            });
            document.addEventListener('click', (e) => {
                if (menu.hidden) return;
                const t = e.target;
                if (t instanceof Node && (menu.contains(t) || btn.contains(t))) return;
                setOpen(false);
            });
            document.addEventListener('keydown', (e) => {
                if (e.key === 'Escape' && !menu.hidden) {
                    setOpen(false);
                    btn.focus();
                }
            });
        })();

        function makeChartCard(title) {
            const div = document.createElement('div');
            div.className = 'chart-card';
            const safeTitle = String(title).replace(/"/g, '&quot;');
            div.innerHTML = '<h3>' + title + '</h3><canvas role="img" aria-label="' + safeTitle + ' chart"></canvas>';
            return div;
        }

        // WASM forecast seed & interactive controls. Declared BEFORE the
        // top-level loadFinancials()/syncPCISliderFromSeed() calls below: both
        // read FORECAST_SEED synchronously (loadFinancials at its horizon-label
        // block, before its first await), so a `let` further down would throw a
        // temporal-dead-zone ReferenceError and silently reject the promise.
        let FORECAST_SEED = PVMT_CONFIG.forecastSeed;

        loadFinancials();

        let wasmReady = false;
        let chartRefs = { pci: null, backlog: null, spending: null };
        let customActive = false;
        let currentStrategy = 'worst-first';

        function syncPCISliderFromSeed() {
            if (!FORECAST_SEED || typeof FORECAST_SEED.initial_pci !== 'number') return;
            const slider = inputById('pci-slider');
            if (!slider) return;
            const min = parseInt(slider.min);
            const max = parseInt(slider.max);
            const v = Math.max(min, Math.min(max, Math.round(FORECAST_SEED.initial_pci)));
            slider.value = String(v);
            document.getElementById('pci-value').textContent = String(v);
        }
        syncPCISliderFromSeed();

        function rebuildTierInputs() {
            const tierDiv = document.getElementById('tier-inputs');
            tierDiv.innerHTML = '';
            (FORECAST_SEED.cost_tiers || []).forEach((t, i) => {
                const row = document.createElement('div');
                row.className = 'tier-row';
                row.innerHTML =
                    '<span>' + esc(t.label || 'Tier ' + (i+1)) + '</span>' +
                    '<input type="number" data-tier="' + i + '" data-field="min_pci" value="' + t.min_pci + '" step="1">' +
                    '<input type="number" data-tier="' + i + '" data-field="max_pci" value="' + t.max_pci + '" step="1">' +
                    '<input type="number" data-tier="' + i + '" data-field="cost_per_sqm" value="' + t.cost_per_sqm + '" step="0.01">';
                tierDiv.appendChild(row);
            });
            tierDiv.querySelectorAll('input').forEach(inp => {
                inp.addEventListener('input', runCustomScenario);
            });
        }

        function onWasmReady() {
            wasmReady = true;
            document.getElementById('forecast-controls').classList.add('visible');
            // Run initial simulation to populate cohort table
            runCustomScenario();
            // Populate tier inputs from seed
            rebuildTierInputs();
        }

        function getControlValues() {
            const budgetPct = parseInt(inputById('budget-slider').value) / 100;
            const initialPCI = parseInt(inputById('pci-slider').value);

            // Read tier values from inputs (advanced), fallback to seed
            const tiers = (FORECAST_SEED.cost_tiers || []).map((t, i) => {
                const minInput = queryInput('[data-tier="' + i + '"][data-field="min_pci"]');
                const maxInput = queryInput('[data-tier="' + i + '"][data-field="max_pci"]');
                const costInput = queryInput('[data-tier="' + i + '"][data-field="cost_per_sqm"]');
                // A cleared input yields parseFloat('') === NaN, which serializes
                // to JSON null and zeroes the tier in the WASM sim. Fall back to
                // the seed value whenever the parse isn't a finite number.
                const minV = minInput ? parseFloat(minInput.value) : NaN;
                const maxV = maxInput ? parseFloat(maxInput.value) : NaN;
                const costV = costInput ? parseFloat(costInput.value) : NaN;
                return {
                    min_pci: Number.isFinite(minV) ? minV : t.min_pci,
                    max_pci: Number.isFinite(maxV) ? maxV : t.max_pci,
                    cost_per_sqm: Number.isFinite(costV) ? costV : t.cost_per_sqm,
                    label: t.label
                };
            });

            // Area/cohorts for the active scope: city-scoped data by default,
            // full bounding-box data when scope === 'bbox'.
            const activeScope = queryEl('#scope-toggle button.active');
            const scope = activeScope ? activeScope.dataset.scope : 'city';
            const area = (scope === 'bbox' || !FORECAST_SEED.city_paved)
                ? FORECAST_SEED.total_area
                : FORECAST_SEED.city_paved;
            const seedCohorts = (scope === 'bbox' || !FORECAST_SEED.city_cohorts || FORECAST_SEED.city_cohorts.length === 0)
                ? (FORECAST_SEED.cohorts || [])
                : FORECAST_SEED.city_cohorts;

            // Compute baseline year-1 need to derive budget
            const baselineInput = {
                area: area,
                initial_pci: initialPCI,
                decay_rate: FORECAST_SEED.decay_rate,
                growth_rate: FORECAST_SEED.growth_rate,
                years: FORECAST_SEED.years,
                treatment_cycle_years: FORECAST_SEED.treatment_cycle_years,
                cost_tiers: tiers,
                annual_budget: 0,
                strategy: 'do-nothing',
                cohorts: seedCohorts
            };
            const baseline = safeSimulate(baselineInput);
            if (!baseline) return null;

            const year1Need = baseline.years && baseline.years.length > 0 ? baseline.years[0].annual_need : 0;
            const annualBudget = budgetPct > 0 ? year1Need * budgetPct : 0;

            return {
                area: area,
                initial_pci: initialPCI,
                decay_rate: FORECAST_SEED.decay_rate,
                growth_rate: FORECAST_SEED.growth_rate,
                years: FORECAST_SEED.years,
                treatment_cycle_years: FORECAST_SEED.treatment_cycle_years,
                cost_tiers: tiers,
                annual_budget: annualBudget,
                strategy: currentStrategy,
                cohorts: seedCohorts
            };
        }

        function runCustomScenario() {
            if (!wasmReady) return;
            const input = getControlValues();
            if (!input) return;

            const result = safeSimulate(input);
            if (!result) return;

            customActive = true;
            updateCustomScenario(result);
            updateSummaryCards(result);

            // Update cohort breakdown from WASM result
            if (result.final_cohorts && result.final_cohorts.length > 1) {
                renderCohortBreakdown(result.final_cohorts);
            } else {
                document.getElementById('cohort-section').style.display = 'none';
            }
        }

        function updateSummaryCards(result) {
            const years = result.years || [];
            const lastPCI = years.length > 0 ? years[years.length - 1].pci : 0;
            const totalSpend = years.reduce((s, y) => s + y.annual_spend, 0);
            const totalDeficit = years.length > 0 ? years[years.length - 1].deferred_backlog : 0;

            document.getElementById('hl-end-pci').textContent = lastPCI.toFixed(0);
            document.getElementById('hl-total-spend').textContent = fmt$(totalSpend);
            document.getElementById('hl-total-deficit').textContent = fmt$(totalDeficit);
            document.getElementById('headlines').style.display = '';
        }

        function updateCustomScenario(result) {
            const customColor = '#10b981';
            const years = result.years || [];

            // Update PCI chart
            if (chartRefs.pci) {
                updateChartDataset(chartRefs.pci, 'Custom Scenario', years.map(y => y.pci), customColor);
            }
            // Update backlog chart
            if (chartRefs.backlog) {
                updateChartDataset(chartRefs.backlog, 'Custom Scenario', years.map(y => y.deferred_backlog), customColor);
            }
            // Update spending chart
            if (chartRefs.spending) {
                let cum = 0;
                const cumData = years.map(y => { cum += y.annual_spend; return cum; });
                updateChartDataset(chartRefs.spending, 'Custom Scenario', cumData, customColor);
            }
            // Update tier chart
            if (chartRefs.tier) {
                const idx = chartRefs.tier.data.datasets.findIndex(ds => ds.label === 'Custom Need');
                const ds = {
                    label: 'Custom Need',
                    data: years.map(y => y.annual_need),
                    backgroundColor: years.map(y => tierColor(y.cost_tier) + '80'),
                    borderColor: customColor,
                    borderWidth: 1
                };
                if (idx >= 0) {
                    chartRefs.tier.data.datasets[idx] = ds;
                } else {
                    chartRefs.tier.data.datasets.push(ds);
                }
                chartRefs.tier.update('none');
            }
        }

        function updateChartDataset(chart, label, data, color) {
            const idx = chart.data.datasets.findIndex(ds => ds.label === label);
            const ds = {
                label: label,
                data: data,
                borderColor: color,
                backgroundColor: 'transparent',
                borderDash: [6, 3],
                tension: 0.3,
                pointRadius: 0
            };
            if (idx >= 0) {
                chart.data.datasets[idx] = ds;
            } else {
                chart.data.datasets.push(ds);
            }
            chart.update('none');
        }

        // Override renderFinancials to capture chart refs. renderFinancials is a
        // reassignable `let` (see its declaration) so this wrap needs no
        // suppression on either the ESLint or the tsc side.
        const _origRenderFinancials = renderFinancials;
        renderFinancials = function(scenarios, scope) {
            chartRefs = { pci: null, backlog: null, spending: null, tier: null };
            _origRenderFinancials(scenarios, scope);

            // Capture chart refs from currentCharts (order: pci, backlog, spending, tier)
            if (currentCharts.length >= 1) chartRefs.pci = currentCharts[0];
            if (currentCharts.length >= 2) chartRefs.backlog = currentCharts[1];
            if (currentCharts.length >= 3) chartRefs.spending = currentCharts[2];
            if (currentCharts.length >= 4) chartRefs.tier = currentCharts[3];

            // Re-apply custom scenario if active
            if (customActive && wasmReady) {
                runCustomScenario();
            }
        };

        // Control event bindings. Each listener is bound to its own slider, so it
        // closes over the typed element rather than narrowing e.target.
        const budgetSlider = inputById('budget-slider');
        budgetSlider.addEventListener('input', () => {
            document.getElementById('budget-value').textContent = budgetSlider.value + '%';
            runCustomScenario();
        });
        const pciSlider = inputById('pci-slider');
        pciSlider.addEventListener('input', () => {
            document.getElementById('pci-value').textContent = pciSlider.value;
            runCustomScenario();
        });
        queryAll('#strategy-buttons button').forEach(btn => {
            btn.addEventListener('click', () => {
                queryAll('#strategy-buttons button').forEach(b => b.classList.remove('active'));
                btn.classList.add('active');
                currentStrategy = btn.dataset.strategy;
                runCustomScenario();
            });
        });
        document.getElementById('advanced-toggle').addEventListener('click', () => {
            const adv = document.getElementById('advanced-controls');
            adv.classList.toggle('visible');
            document.getElementById('advanced-toggle').textContent =
                adv.classList.contains('visible') ? 'Hide advanced options' : 'Advanced options';
        });

        // ── Compare tab ──
        // State vars (compareLoaded/compareCharts/aggregateLoaded/aggregateCharts/
        // allCityDataPromise) are declared+initialized above the applyInitialState
        // IIFE — see the note there — so a deep link does not get its guards wiped.

        // Shared per-city fetch for the Compare and Aggregate tabs. Cache the
        // in-flight Promise (not the resolved data) so two near-simultaneous
        // openings — e.g. a deep-link plus a click — dedupe to one fan-out
        // instead of both observing an undefined data var and re-fetching.
        function loadAllCityData() {
            if (!allCityDataPromise) {
                allCityDataPromise = Promise.all(CITIES.map(async function(city) {
                    var prefix = 'cities/' + city.slug + '/';
                    const [meta, scenarios, seed, forecast] = await Promise.all([
                        loadJSON(prefix + 'data/meta.json'),
                        loadJSON(prefix + 'data/scenarios.json'),
                        loadJSON(prefix + 'data/forecast_seed.json'),
                        loadJSON(prefix + 'data/forecast.json')
                    ]);
                    return { slug: city.slug, name: city.name, meta, scenarios, seed, forecast };
                }));
            }
            return allCityDataPromise;
        }

        // cityTagsForSlug maps a slug to its tags. The fetched per-city data
        // objects carry slug/name/meta but not tags; CITIES (the flat config
        // array) carries them. Returns [] for an untagged or unknown city.
        function cityTagsForSlug(slug) {
            var c = (typeof CITIES !== 'undefined') ? CITIES.find(function(x) { return x.slug === slug; }) : null;
            return (c && c.tags) || [];
        }

        // scopeCityData filters fetched per-city data to the selected tag. An
        // empty selectedTag means "all cities". Filtering happens here at the
        // render step — NOT inside loadAllCityData, whose single cached promise
        // must always hold every city so switching tags never serves a stale
        // subset.
        function scopeCityData(cityData) {
            if (!selectedTag) return cityData;
            return cityData.filter(function(city) {
                return cityTagsForSlug(city.slug).indexOf(selectedTag) !== -1;
            });
        }

        async function loadCompareData() {
            if (compareLoaded || CITIES.length === 0) return;
            compareLoaded = true;
            renderCompare(scopeCityData(await loadAllCityData()));
        }

        // Aggregate re-renders on every scope toggle, so it is NOT one-shot:
        // the guard only blocks the initial fetch, while renderAggregate is
        // free to re-run over the cached data (extractMetrics reads
        // currentScope live).
        async function loadAggregateData() {
            if (CITIES.length === 0) return;
            aggregateLoaded = true;
            renderAggregate(scopeCityData(await loadAllCityData()));
        }

        // applyTagScope re-scopes the Compare/Aggregate tabs to a tag label
        // ('' = all cities). Compare is one-shot (compareLoaded gates its only
        // render), so reset that guard to force a re-render; Aggregate always
        // re-renders. Only the active tab is re-run — the other picks up
        // selectedTag when next opened. renderAggregate recomputes
        // regionHasBothScopes from the filtered set and re-reveals the
        // city/all-jurisdiction toggle accordingly. Shared with popstate, which
        // needs the same guard reset when a history entry changes the tag.
        function applyTagScope(value) {
            selectedTag = value;
            compareLoaded = false;
            var btn = queryEl('.tab-btn.active');
            var id = btn ? btn.dataset.tab : '';
            if (id === 'compare-tab') loadCompareData();
            else if (id === 'aggregate-tab') loadAggregateData();
        }

        // onTagScopeChange handles the #tag-scope selector: re-scope, then
        // record the tag in the URL so a filtered Compare/Aggregate view is
        // shareable and survives back/forward like every other selector.
        function onTagScopeChange(value) {
            applyTagScope(value);
            Router.push();
        }

        function extractMetrics(city) {
            var m = { name: city.name, slug: city.slug };

            // Per-type area from meta
            var statByType = {};
            if (city.meta && city.meta.stats) {
                city.meta.stats.forEach(function(st) { statByType[st.type] = st; });
                m.totalAreaLarge = city.meta.stats.reduce(function(s, st) { return s + toAreaLarge(st.total_area || 0); }, 0);
                m.totalSqM = city.meta.stats.reduce(function(s, st) { return s + (st.total_area || 0); }, 0);
                m.featureCount = city.meta.stats.reduce(function(s, st) { return s + (st.feature_count || 0); }, 0);
            } else {
                m.totalAreaLarge = 0; m.totalSqM = 0; m.featureCount = 0;
            }
            m.roadsAreaLarge = toAreaLarge(statByType.roads ? statByType.roads.total_area || 0 : 0);
            m.parkingAreaLarge = toAreaLarge(statByType.parking ? statByType.parking.total_area || 0 : 0);
            m.sidewalksAreaLarge = toAreaLarge(statByType.sidewalks ? statByType.sidewalks.total_area || 0 : 0);
            m.roadsSqM = statByType.roads ? statByType.roads.total_area : 0;
            m.parkingSqM = statByType.parking ? statByType.parking.total_area : 0;
            m.sidewalksSqM = statByType.sidewalks ? statByType.sidewalks.total_area : 0;
            m.roadsFeatures = statByType.roads ? statByType.roads.feature_count : 0;
            m.parkingFeatures = statByType.parking ? statByType.parking.feature_count : 0;
            m.sidewalksFeatures = statByType.sidewalks ? statByType.sidewalks.feature_count : 0;

            // City area and pct paved from meta
            m.cityAreaLarge = toAreaLarge(city.meta && city.meta.city_area ? city.meta.city_area : 0);
            m.pctPaved = city.meta && city.meta.pct_paved ? city.meta.pct_paved : 0;
            // Union-based total paved (sq m). meta.total_paved is the cross-resource
            // union the Go side maintains precisely so it does NOT double-count
            // road/sidewalk/parking buffer overlap; the Map and Compare % Paved use
            // it. Fall back to the per-resource stats sum only when absent.
            m.totalPavedSqM = (city.meta && city.meta.total_paved) ? city.meta.total_paved : m.totalSqM;

            // Ratios
            m.sidewalkToRoadPct = m.roadsSqM > 0 ? (m.sidewalksSqM / m.roadsSqM * 100) : 0;
            m.parkingToRoadPct = m.roadsSqM > 0 ? (m.parkingSqM / m.roadsSqM * 100) : 0;
            m.cityPct = 0;

            // Road cohorts from seed
            m.cohorts = {};
            m.cityRoadsSqM = 0;
            if (city.seed && city.seed.cohorts) {
                city.seed.cohorts.forEach(function(c) {
                    if (c.classification !== 'parking' && c.classification !== 'sidewalks') {
                        m.cohorts[c.classification] = c.area;
                    }
                });
            }
            if (city.seed && city.seed.city_paved && city.seed.total_area) {
                m.cityPct = city.seed.city_paved / city.seed.total_area * 100;
            }

            // Blended decay from cohorts (area-weighted)
            m.blendedDecay = 0;
            if (city.seed && city.seed.cohorts) {
                var totalArea = 0, weightedDecay = 0;
                city.seed.cohorts.forEach(function(c) {
                    totalArea += c.area;
                    weightedDecay += c.area * c.decay_rate;
                });
                m.blendedDecay = totalArea > 0 ? weightedDecay / totalArea : 0;
            }

            // Scenarios baseline for the active scope. scenarios.json is
            // scope-keyed ({city, bbox, summary}); mirror loadFinancials'
            // selection and honor currentScope (driven by the ?scope= param).
            m.baselineYears = [];
            var scenarios = city.scenarios
                ? (scenariosForScope(city.scenarios, currentScope) || null)
                : null;
            if (scenarios && scenarios.length > 0) {
                var baseline = scenarios[0];
                var years = baseline.years || [];
                m.baselineYears = years;
                m.year1Need = years.length > 0 ? years[0].annual_need : 0;
                m.year20Need = years.length > 0 ? years[years.length - 1].annual_need : 0;
                m.endBacklog = years.length > 0 ? years[years.length - 1].deferred_backlog : 0;
                m.hasForecasts = true;
            } else {
                m.year1Need = 0; m.year20Need = 0; m.endBacklog = 0;
                m.hasForecasts = false;
            }
            // Divide the (scope-selected) year-1 need by an area in the SAME
            // scope. m.totalAreaLarge sums meta.stats, which BuildMeta populates
            // from bare full-bbox/all-jurisdiction compute rows, so using it with
            // a city-scope numerator mixes scopes and understates the ratio for
            // cities with lots of state/county-owned network. Use seed.city_paved
            // when the city scope is active, else seed.total_area (both sq m, so
            // convert to display large-area units to match m.totalAreaLarge).
            var scopeAreaSqM = 0;
            if (city.seed) {
                scopeAreaSqM = (currentScope === 'city' && city.seed.city_paved)
                    ? city.seed.city_paved
                    : (city.seed.total_area || 0);
            }
            var scopeAreaLarge = scopeAreaSqM > 0 ? toAreaLarge(scopeAreaSqM) : m.totalAreaLarge;
            m.year1PerAreaUnit = scopeAreaLarge > 0 ? m.year1Need / scopeAreaLarge : 0;

            // Roads-only solvency metrics from forecast.json (an array of
            // per-resource ForecastExport — find the roads element). These are
            // nullable: insolvencyYear/fundingGap are present only for cities
            // with a configured (cited) current_budget; breakEvenBudget is
            // present for any city with roads. Leaderboard categories filter on
            // these so a missing metric never corrupts a sort/bar.
            m.breakEvenBudget = null; m.insolvencyYear = null; m.fundingGap = null; m.currentBudget = 0;
            if (city.forecast && city.forecast.find) {
                var roadsFe = city.forecast.find(function(f) { return f.resource_type === 'roads'; });
                if (roadsFe) {
                    if (roadsFe.break_even_budget > 0) m.breakEvenBudget = roadsFe.break_even_budget;
                    m.currentBudget = roadsFe.current_budget || 0;
                    if (roadsFe.current_budget > 0) {
                        // insolvency_year is null when solvent through horizon.
                        if (roadsFe.insolvency_year) m.insolvencyYear = roadsFe.insolvency_year;
                        if (roadsFe.funding_gap !== null && roadsFe.funding_gap !== undefined) {
                            m.fundingGap = roadsFe.funding_gap * 100; // percent
                        }
                    }
                }
            }

            return m;
        }

        function renderCompare(cityData) {
            var allMetrics = cityData.map(extractMetrics);
            var medals = ['\u{1F947}', '\u{1F948}', '\u{1F949}'];
            var rankClasses = ['rank-1', 'rank-2', 'rank-3'];

            // Horizon-derived labels (never hardcode 20). The Compare tab plots
            // each city's baselineYears, so the longest horizon is the trajectory
            // length; the "final year" bars/backlog also use that last plotted
            // point. Cities may configure different horizons — the leaderboard
            // then compares each city's own final year, which is a known
            // cross-city mixed-horizon limitation (the divergence note on the
            // Aggregate tab flags this). Fall back to 20 only when no city has a
            // forecast, matching the Financials tab's fallback.
            var compareMaxYears = 0;
            allMetrics.forEach(function(m) {
                if (m.baselineYears.length > compareMaxYears) compareMaxYears = m.baselineYears.length;
            });
            var compareHorizon = compareMaxYears > 0 ? compareMaxYears : 20;
            var costTitleEl = document.getElementById('compare-cost-title');
            if (costTitleEl) costTitleEl.textContent = compareHorizon + '-Year Cost Trajectory';
            var needTitleEl = document.getElementById('compare-need-title');
            if (needTitleEl) needTitleEl.textContent = 'Year 1 vs Year ' + compareHorizon + ' Maintenance Need';

            // Two scores: the primary Solvency Score (fiscal — the project's headline
            // question) and a secondary Infrastructure Index (a descriptive asset
            // composite). See methodology.md.
            function normalize(arr, key, higherBetter) {
                var vals = arr.map(function(m) { return m[key]; });
                var mn = Math.min.apply(null, vals), mx = Math.max.apply(null, vals);
                if (mx === mn) return arr.map(function() { return 1; });
                return arr.map(function(m) {
                    var n = (m[key] - mn) / (mx - mn);
                    return higherBetter ? n : (1 - n);
                });
            }

            // Solvency Score: budget coverage ratio = current / hold-steady budget
            // (roads only). 100 = break-even, 50 = needs ~2x, 25 = needs ~4x; surplus
            // capped at 100 for display. Coverage (uncapped) is the ranking key so
            // over-funded cities get distinct ranks (one gold). null when the city has
            // no cited current_budget -> unrankable, shown N/A.
            allMetrics.forEach(function(m) {
                m.solvCoverage = (m.currentBudget > 0 && m.breakEvenBudget > 0)
                    ? m.currentBudget / m.breakEvenBudget
                    : null;
                m.solvScore = m.solvCoverage === null ? null : Math.round(Math.min(100, m.solvCoverage * 100));
            });

            // Infrastructure Index: descriptive asset/scale/ownership/condition composite,
            // normalized across the compared cities. NOT a solvency judgment.
            var walkN = normalize(allMetrics, 'sidewalkToRoadPct', true);
            var sizeN = normalize(allMetrics, 'totalAreaLarge', true);
            var cityN = normalize(allMetrics, 'cityPct', true);
            var costN = normalize(allMetrics, 'year1PerAreaUnit', false);
            var decayN = normalize(allMetrics, 'blendedDecay', false);
            allMetrics.forEach(function(m, i) {
                m.infraScore = Math.round((walkN[i] * 0.2 + sizeN[i] * 0.15 + cityN[i] * 0.2 + costN[i] * 0.2 + decayN[i] * 0.25) * 100);
            });

            // Strict-position ranks over an already-sorted list: one #1, no shared medals.
            function assignRanks(list) { list.forEach(function(m, i) { m.rank = i + 1; }); return list; }

            function scoreCard(m, opts) {
                var medal = (opts.ranked && m.rank <= 3) ? medals[m.rank - 1] : '';
                var cls = (opts.ranked && m.rank <= 3) ? (' ' + rankClasses[m.rank - 1]) : '';
                return '<div class="score-card' + cls + (opts.muted ? ' score-card-muted' : '') + '">' +
                    '<div class="rank-medal">' + medal + '</div>' +
                    '<div class="city-name">' + esc(m.name) + '</div>' +
                    '<div class="score-value">' + opts.value + '</div>' +
                    '<div class="score-label">' + opts.label + '</div>' +
                    '<div class="score-breakdown">' + opts.breakdown + '</div>' +
                '</div>';
            }

            function solvencyBreakdown(m) {
                var gap = m.fundingGap !== null ? ((m.fundingGap >= 0 ? '+' : '') + m.fundingGap.toFixed(0) + '%') : '—';
                var insolv = m.currentBudget > 0
                    ? (m.insolvencyYear ? 'Year ' + m.insolvencyYear : 'Solvent through horizon')
                    : '—';
                return (m.currentBudget > 0 ? '<div class="sb-row"><span>Current Budget</span><span>' + fmtCost(m.currentBudget) + '/yr</span></div>' : '') +
                    (m.breakEvenBudget > 0 ? '<div class="sb-row"><span>Hold-Steady Budget</span><span>' + fmtCost(m.breakEvenBudget) + '/yr</span></div>' : '') +
                    '<div class="sb-row"><span>Funding Gap</span><span>' + gap + '</span></div>' +
                    '<div class="sb-row"><span>Insolvency</span><span>' + insolv + '</span></div>' +
                    '<div class="sb-row"><span>Total Network</span><span>' + m.totalAreaLarge.toFixed(0) + ' ' + areaLargeUnit() + '</span></div>';
            }

            function infraBreakdown(m) {
                return (m.cityAreaLarge > 0 ? '<div class="sb-row"><span>City Area</span><span>' + m.cityAreaLarge.toFixed(0) + ' ' + areaLargeUnit() + '</span></div>' : '') +
                    (m.pctPaved > 0 ? '<div class="sb-row"><span title="% Paved divides paved area by the Nominatim boundary polygon. Boundary scope varies across cities, so cross-city comparison is approximate.">% Paved <span class="sb-note">ⓘ</span></span><span>' + m.pctPaved.toFixed(1) + '%</span></div>' : '') +
                    '<div class="sb-row"><span>Total Network</span><span>' + m.totalAreaLarge.toFixed(0) + ' ' + areaLargeUnit() + '</span></div>' +
                    '<div class="sb-row"><span>Roads</span><span>' + m.roadsAreaLarge.toFixed(0) + ' ' + areaLargeUnit() + '</span></div>' +
                    '<div class="sb-row"><span>Parking</span><span>' + m.parkingAreaLarge.toFixed(0) + ' ' + areaLargeUnit() + '</span></div>' +
                    '<div class="sb-row"><span>Sidewalks</span><span>' + m.sidewalksAreaLarge.toFixed(1) + ' ' + areaLargeUnit() + '</span></div>' +
                    '<div class="sb-row"><span>Sidewalk/Road</span><span>' + m.sidewalkToRoadPct.toFixed(1) + '%</span></div>' +
                    '<div class="sb-row"><span>City-Owned</span><span>' + m.cityPct.toFixed(0) + '%</span></div>' +
                    '<div class="sb-row"><span>Blended Decay</span><span>' + (m.blendedDecay * 100).toFixed(2) + '%/yr</span></div>';
            }

            // Solvency cards: scored cities ranked (desc coverage, tie-break later/no
            // insolvency), then budget-less cities appended as muted N/A.
            var solvScored = assignRanks(allMetrics.filter(function(m) { return m.solvScore !== null; })
                .sort(function(a, b) {
                    if (b.solvCoverage !== a.solvCoverage) return b.solvCoverage - a.solvCoverage;
                    return (b.insolvencyYear || Infinity) - (a.insolvencyYear || Infinity);
                }));
            var solvUnscored = allMetrics.filter(function(m) { return m.solvScore === null; });
            var solvCards = solvScored.map(function(m) {
                return scoreCard(m, { ranked: true, value: m.solvScore, label: 'Solvency Score', breakdown: solvencyBreakdown(m) });
            }).concat(solvUnscored.map(function(m) {
                return scoreCard(m, { ranked: false, muted: true, value: 'N/A', label: 'No cited budget — unranked', breakdown: solvencyBreakdown(m) });
            }));
            document.getElementById('solvency-cards').innerHTML = solvCards.join('');

            // Infrastructure Index cards: every city places (composite is always computable).
            var infraRanked = assignRanks(allMetrics.slice().sort(function(a, b) { return b.infraScore - a.infraScore; }));
            document.getElementById('infra-cards').innerHTML = infraRanked.map(function(m) {
                return scoreCard(m, { ranked: true, value: m.infraScore, label: 'Infrastructure Index', breakdown: infraBreakdown(m) });
            }).join('');

            // Leaderboards — data-driven categories
            var categories = [
                { key: 'cityAreaLarge', label: 'City Area', award: 'Largest City', higher: true, fmt: function(v) { return v.toFixed(0) + ' ' + areaLargeUnit(); }, color: '#059669', note: 'City Area is the Nominatim admin polygon. Polygon scope varies (e.g. Boston includes harbor water; Nashville is consolidated Davidson County), so cross-city comparison is approximate.' },
                { key: 'pctPaved', label: '% Paved', award: 'Most Paved', higher: true, fmt: function(v) { return v.toFixed(1) + '%'; }, color: '#dc2626', note: '% Paved divides paved area by the Nominatim boundary polygon for each city. Polygon scope varies across cities (Boston includes harbor water; Nashville is consolidated Davidson County), so cross-city comparison is approximate.' },
                { key: 'totalAreaLarge', label: 'Total Managed Area', award: 'Biggest Network', higher: true, fmt: function(v) { return v.toFixed(0) + ' ' + areaLargeUnit(); }, color: '#6366f1' },
                { key: 'sidewalkToRoadPct', label: 'Walkability (Sidewalk-to-Road Ratio)', award: 'Most Walkable', higher: true, fmt: function(v) { return v.toFixed(1) + '%'; }, color: '#10b981' },
                { key: 'parkingAreaLarge', label: 'Parking Infrastructure', award: 'Most Parking', higher: true, fmt: function(v) { return v.toFixed(0) + ' ' + areaLargeUnit(); }, color: '#3b82f6' },
                { key: 'cityPct', label: 'City Ownership Share', award: 'Most City-Owned', higher: true, fmt: function(v) { return v.toFixed(0) + '%'; }, color: '#8b5cf6' },
                { key: 'featureCount', label: 'Total Features Tracked', award: 'Most Detailed', higher: true, fmt: function(v) { return v.toLocaleString(); }, color: '#f59e0b' },
                { key: 'blendedDecay', label: 'Blended Decay Rate (lower is better)', award: 'Slowest Decay', higher: false, fmt: function(v) { return (v * 100).toFixed(2) + '%/yr'; }, color: '#ef4444' },
                { key: 'year1PerAreaUnit', label: 'Year 1 Maintenance Cost per ' + areaLargeUnit().charAt(0).toUpperCase() + areaLargeUnit().slice(1), award: 'Most Efficient', higher: false, fmt: function(v) { return '$' + (v >= 1e3 ? (v/1e3).toFixed(1) + 'K' : v.toFixed(0)); }, color: '#ec4899' },
                // endBacklog is each city's own final-year backlog; with mixed
                // horizons this compares different end-years (known limitation).
                { key: 'endBacklog', label: compareHorizon + '-Year Deferred Backlog', award: 'Least Deferred', higher: false, fmt: function(v) { return '$' + (v >= 1e9 ? (v/1e9).toFixed(2) + 'B' : (v/1e6).toFixed(1) + 'M'); }, color: '#d946ef' },
                // Roads-only solvency metrics. filter excludes cities that lack
                // the metric (no cited budget / no roads) so a null value never
                // produces a NaN sort key or a corrupt bar width.
                { key: 'fundingGap', label: 'Street Funding Gap (roads, lower is better)', award: 'Best Funded', higher: false, filter: function(m) { return m.fundingGap !== null; }, fmt: function(v) { return (v >= 0 ? '+' : '') + v.toFixed(0) + '%'; }, color: '#0ea5e9', note: 'Funding gap = (hold-steady budget − current budget) / current budget, computed on roads/streets only. Shown only for cities with a cited current_budget. Negative means the budget already exceeds the hold-steady level.' },
                { key: 'insolvencyYear', label: 'Years to Insolvency (roads, higher is better)', award: 'Most Solvent', higher: true, filter: function(m) { return m.insolvencyYear !== null; }, fmt: function(v) { return 'Year ' + v; }, color: '#14b8a6', note: 'Insolvency year = the first forecast year a city’s cumulative roads backlog grows to an entire treatment cycle of deferred work — a whole network’s worth of treatment, unrecoverable. Computed on roads/streets only at the current budget; shown only for cities with a cited current_budget that go insolvent within the horizon. Cities that stay solvent (most, with realistic budgets) are omitted — funding gap is the primary metric there.' }
            ];

            var lbHtml = categories.map(function(cat) {
                // Restrict to cities that actually have this metric. Without
                // this, a null/undefined value yields NaN sort keys and bar
                // widths that corrupt the whole category.
                var pool = cat.filter ? allMetrics.filter(cat.filter) : allMetrics;
                if (pool.length === 0) return '';
                var sorted = pool.slice().sort(function(a, b) {
                    return cat.higher ? (b[cat.key] - a[cat.key]) : (a[cat.key] - b[cat.key]);
                });
                var vals = sorted.map(function(m) { return m[cat.key]; });
                var maxVal = Math.max.apply(null, vals);
                if (maxVal === 0) maxVal = 1;

                var rows = sorted.map(function(m, i) {
                    // Clamp to [0,100]: the funding-gap metric is signed
                    // (negative when over-funded) and maxVal can be negative if
                    // every city is over-funded, either of which would produce
                    // an invalid/overflowing bar width.
                    var pct = Math.max(0, Math.min(100, m[cat.key] / maxVal * 100)).toFixed(1);
                    var medalIcon = i < 3 && i < medals.length ? medals[i] : (i + 1) + '.';
                    return '<div class="lb-row">' +
                        '<span class="lb-medal">' + medalIcon + '</span>' +
                        '<span class="lb-name">' + esc(m.name) + '</span>' +
                        '<div class="lb-bar-wrap"><div class="lb-bar" style="width:' + pct + '%;background:' + cat.color + ';"></div></div>' +
                        '<span class="lb-val">' + cat.fmt(m[cat.key]) + '</span>' +
                    '</div>';
                }).join('');

                var noteIcon = cat.note ? ' <span class="lb-note" title="' + cat.note.replace(/"/g, '&quot;') + '">ⓘ</span>' : '';
                return '<div class="lb-category">' +
                    '<div class="lb-title"><h4>' + cat.label + noteIcon + '</h4><span class="lb-award">' + medals[0] + ' ' + cat.award + ': ' + esc(sorted[0].name) + '</span></div>' +
                    rows +
                '</div>';
            }).join('');
            document.getElementById('leaderboard-list').innerHTML = lbHtml;

            // Charts
            compareCharts.forEach(function(c) { c.destroy(); });
            compareCharts = [];
            var cityColors = ['#3b82f6', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6', '#ec4899', '#06b6d4', '#84cc16'];

            // 1. Stacked area breakdown
            compareCharts.push(new Chart(canvasById('compare-area-chart').getContext('2d'), {
                type: 'bar',
                data: {
                    labels: allMetrics.map(function(m) { return m.name; }),
                    datasets: [
                        { label: 'Roads', data: allMetrics.map(function(m) { return m.roadsAreaLarge; }), backgroundColor: '#6b7280' },
                        { label: 'Parking', data: allMetrics.map(function(m) { return m.parkingAreaLarge; }), backgroundColor: '#3b82f6' },
                        { label: 'Sidewalks', data: allMetrics.map(function(m) { return m.sidewalksAreaLarge; }), backgroundColor: '#f59e0b' }
                    ]
                },
                options: {
                    responsive: true,
                    scales: { x: { stacked: true }, y: { stacked: true, title: { display: true, text: areaLargeUnit().charAt(0).toUpperCase() + areaLargeUnit().slice(1) } } },
                    plugins: { verticalCrosshair: false, legend: { position: 'top' } }
                }
            }));

            // 2. Road classification mix (100% stacked)
            var classNames = {};
            allMetrics.forEach(function(m) { Object.keys(m.cohorts).forEach(function(k) { classNames[k] = true; }); });
            var classes = Object.keys(classNames).sort();
            var classColors = { residential: '#60a5fa', service: '#a78bfa', tertiary: '#34d399', secondary: '#fbbf24', primary: '#f87171', motorway: '#1e3a5f', trunk: '#fb923c' };

            compareCharts.push(new Chart(canvasById('compare-mix-chart').getContext('2d'), {
                type: 'bar',
                data: {
                    labels: allMetrics.map(function(m) { return m.name; }),
                    datasets: classes.map(function(cls) {
                        return {
                            label: cls,
                            data: allMetrics.map(function(m) {
                                var total = Object.values(m.cohorts).reduce(function(s, v) { return s + v; }, 0);
                                return total > 0 ? ((m.cohorts[cls] || 0) / total * 100) : 0;
                            }),
                            backgroundColor: classColors[cls] || '#94a3b8'
                        };
                    })
                },
                options: {
                    responsive: true,
                    scales: {
                        x: { stacked: true },
                        y: { stacked: true, max: 100, title: { display: true, text: '% of Road Network' },
                             ticks: { callback: function(v) { return v + '%'; } } }
                    },
                    plugins: {
                        verticalCrosshair: false,
                        legend: { position: 'top' },
                        tooltip: { callbacks: { label: function(ctx) { return ctx.dataset.label + ': ' + ctx.raw.toFixed(1) + '%'; } } }
                    }
                }
            }));

            // 3. 20-year cost trajectory (line chart)
            var maxYears = Math.max.apply(null, allMetrics.map(function(m) { return m.baselineYears.length; }));
            var yearLabels = [];
            for (var i = 1; i <= maxYears; i++) yearLabels.push('Year ' + i);

            compareCharts.push(new Chart(canvasById('compare-cost-chart').getContext('2d'), {
                type: 'line',
                data: {
                    labels: yearLabels,
                    datasets: allMetrics.map(function(m, idx) {
                        return {
                            label: m.name,
                            data: m.baselineYears.map(function(y) { return y.deferred_backlog; }),
                            borderColor: cityColors[idx % cityColors.length],
                            backgroundColor: cityColors[idx % cityColors.length] + '22',
                            fill: false,
                            tension: 0.3,
                            borderWidth: 2.5
                        };
                    })
                },
                options: {
                    responsive: true,
                    scales: {
                        y: {
                            beginAtZero: true,
                            title: { display: true, text: 'Deferred Backlog ($)' },
                            ticks: { callback: function(v) { return v >= 1e9 ? '$' + (v/1e9).toFixed(1) + 'B' : '$' + (v/1e6).toFixed(0) + 'M'; } }
                        }
                    },
                    plugins: { legend: { position: 'top' } },
                    interaction: { mode: 'index', intersect: false }
                }
            }));

            // 4. Year 1 vs Year 20 need
            compareCharts.push(new Chart(canvasById('compare-need-chart').getContext('2d'), {
                type: 'bar',
                data: {
                    labels: allMetrics.map(function(m) { return m.name; }),
                    datasets: [
                        {
                            label: 'Year 1 Need',
                            data: allMetrics.map(function(m) { return m.year1Need; }),
                            backgroundColor: allMetrics.map(function(_, i) { return cityColors[i % cityColors.length] + 'cc'; }),
                            borderColor: allMetrics.map(function(_, i) { return cityColors[i % cityColors.length]; }),
                            borderWidth: 1
                        },
                        {
                            label: 'Year ' + compareHorizon + ' Need',
                            data: allMetrics.map(function(m) { return m.year20Need; }),
                            backgroundColor: allMetrics.map(function(_, i) { return cityColors[i % cityColors.length] + '55'; }),
                            borderColor: allMetrics.map(function(_, i) { return cityColors[i % cityColors.length]; }),
                            borderWidth: 1
                        }
                    ]
                },
                options: {
                    responsive: true,
                    scales: {
                        y: {
                            beginAtZero: true,
                            title: { display: true, text: 'Annual Need ($)' },
                            ticks: { callback: function(v) { return '$' + (v/1e6).toFixed(0) + 'M'; } }
                        }
                    },
                    plugins: {
                        verticalCrosshair: false,
                        legend: { position: 'top' },
                        tooltip: { callbacks: { label: function(ctx) { return ctx.dataset.label + ': $' + (ctx.raw/1e6).toFixed(1) + 'M'; } } }
                    }
                }
            }));

            document.getElementById('compare-loading').style.display = 'none';
            document.getElementById('compare-content').style.display = 'block';
        }

        // Whole-config region rollup: treat every city as one fiscal entity
        // and sum element-wise over what Compare already fetched. Honors the
        // active scope (extractMetrics reads currentScope live) and re-runs on
        // every scope toggle, so it must be free of one-shot side-effects.
        function renderAggregate(cityData) {
            var allMetrics = cityData.map(extractMetrics);
            var fc = allMetrics.filter(function(m) { return m.hasForecasts && m.baselineYears.length > 0; });

            // Does the region as a whole support both scopes? Drives the
            // region-wide scope-toggle reveal (maybeRevealScopeToggle).
            regionHasBothScopes = cityData.some(function(city) {
                return city.scenarios && city.scenarios.city && city.scenarios.bbox;
            });
            maybeRevealScopeToggle();

            // Truncate to the shortest horizon so summed years align and the
            // headline backlog matches the chart's final plotted point.
            var minLen = fc.length > 0 ? Math.min.apply(null, fc.map(function(m) { return m.baselineYears.length; })) : 0;

            // Region area sums (every city, forecast or not).
            var totalRoads = 0, totalParking = 0, totalSidewalks = 0, totalNetwork = 0, totalCityArea = 0, totalPaved = 0;
            allMetrics.forEach(function(m) {
                totalRoads += m.roadsAreaLarge; totalParking += m.parkingAreaLarge; totalSidewalks += m.sidewalksAreaLarge;
                totalNetwork += m.totalAreaLarge; totalCityArea += m.cityAreaLarge;
                // Union-based paved area (large units) for an apples-to-apples
                // Paved Share that matches the Map/Compare % Paved, instead of the
                // overlap-double-counting per-resource sum (totalNetwork).
                totalPaved += toAreaLarge(m.totalPavedSqM || 0);
            });

            // Dollar + PCI sums over forecast cities only, so the weighted PCI
            // denominator and the trajectories stay honest.
            var fcArea = 0, pciWeighted = 0, regionYear1 = 0, regionBacklog = 0;
            fc.forEach(function(m) {
                fcArea += m.totalAreaLarge;
                pciWeighted += (m.baselineYears[0].pci || 0) * m.totalAreaLarge;
                regionYear1 += m.baselineYears[0].annual_need || 0;
                regionBacklog += m.baselineYears[minLen - 1] ? (m.baselineYears[minLen - 1].deferred_backlog || 0) : 0;
            });
            var weightedPCI = fcArea > 0 ? pciWeighted / fcArea : null;
            var perArea = fcArea > 0 ? regionYear1 / fcArea : null;
            var unit = areaLargeUnit();
            // A config with cities but no computed forecasts (e.g. exported
            // before `forecast`) has no horizon; drop the "N-Year" prefix and
            // dash out the forecast-dependent figures rather than show "0-Year".
            var hasFc = fc.length > 0;
            var horizon = hasFc ? minLen + '-Year ' : '';

            // Headline cards.
            function aggCard(label, value, sub) {
                return '<div class="headline-card"><div class="label">' + label + '</div>' +
                       '<div class="value">' + value + '</div><div class="sub">' + sub + '</div></div>';
            }
            document.getElementById('aggregate-headlines').innerHTML = [
                aggCard('Total Network Area', fmtNum(totalNetwork) + ' ' + unit, 'Roads + parking + sidewalks across ' + allMetrics.length + ' cities'),
                aggCard('Paved Share', totalCityArea > 0 ? (totalPaved / totalCityArea * 100).toFixed(1) + '%' : '—', 'Paved (union) area vs. municipal area'),
                aggCard('Area-Weighted PCI', weightedPCI === null ? '—' : weightedPCI.toFixed(0), 'Baseline condition (simplified single-cohort)'),
                aggCard('Year-1 Maintenance Need', hasFc ? fmt$(regionYear1) : '—', 'To hold the whole network steady'),
                aggCard(horizon + 'Deferred Backlog', hasFc ? fmt$(regionBacklog) : '—', hasFc ? 'Unfunded need accumulated by year ' + minLen : 'No forecast data in this region'),
                aggCard('Year-1 Need / ' + unit, perArea === null ? '—' : '$' + fmtNum(perArea), 'Region maintenance intensity')
            ].join('');

            // Forecast divergence note: per-city [forecast] can override
            // horizon, initial PCI, decay, and cost tiers — summed dollars
            // would then mix cost bases. forecast_seed.json carries all four.
            var sigs = {};
            cityData.forEach(function(city, idx) {
                if (!allMetrics[idx].hasForecasts || !city.seed) return;
                var s = city.seed;
                sigs[JSON.stringify([s.initial_pci, s.decay_rate, s.years, s.cost_tiers])] = true;
            });
            var noteEl = document.getElementById('aggregate-divergence-note');
            if (Object.keys(sigs).length > 1) {
                noteEl.hidden = false;
                noteEl.textContent = 'Heads up: cities in this region use different forecast settings (horizon, initial PCI, decay, or cost tiers). ' +
                    'Summed dollars may mix cost bases, and trajectories are truncated to the shortest horizon (' + minLen + ' years).';
            } else {
                noteEl.hidden = true;
            }

            // Dynamic, horizon-derived chart + column titles (never hardcode 20).
            document.getElementById('agg-cost-title').textContent = 'Region ' + horizon + 'Cost Trajectory';
            document.getElementById('agg-backlog-title').textContent = 'Region ' + horizon + 'Deferred Backlog';
            document.getElementById('agg-contrib-backlog-th').textContent = hasFc ? minLen + '-Yr Backlog' : 'Backlog';

            // Element-wise summed trajectories over the aligned horizon.
            var yearLabels = [], costSeries = [], backlogSeries = [];
            for (var y = 0; y < minLen; y++) {
                var needSum = 0, backSum = 0;
                fc.forEach(function(m) {
                    needSum += m.baselineYears[y].annual_need || 0;
                    backSum += m.baselineYears[y].deferred_backlog || 0;
                });
                yearLabels.push('Year ' + (y + 1));
                costSeries.push(needSum);
                backlogSeries.push(backSum);
            }
            var dollarTicks = function(v) { return v >= 1e9 ? '$' + (v / 1e9).toFixed(1) + 'B' : '$' + (v / 1e6).toFixed(0) + 'M'; };

            aggregateCharts.forEach(function(c) { c.destroy(); });
            aggregateCharts = [];

            aggregateCharts.push(new Chart(canvasById('aggregate-cost-chart').getContext('2d'), {
                type: 'line',
                data: { labels: yearLabels, datasets: [{ label: 'Region Annual Need', data: costSeries, borderColor: '#3b82f6', backgroundColor: '#3b82f622', fill: true, tension: 0.3, borderWidth: 2.5 }] },
                options: {
                    responsive: true,
                    scales: { y: { beginAtZero: true, title: { display: true, text: 'Annual Need ($)' }, ticks: { callback: dollarTicks } } },
                    plugins: { verticalCrosshair: false, legend: { display: false }, tooltip: { callbacks: { label: function(c) { return fmt$(c.raw); } } } },
                    interaction: { mode: 'index', intersect: false }
                }
            }));

            aggregateCharts.push(new Chart(canvasById('aggregate-backlog-chart').getContext('2d'), {
                type: 'line',
                data: { labels: yearLabels, datasets: [{ label: 'Region Deferred Backlog', data: backlogSeries, borderColor: '#d946ef', backgroundColor: '#d946ef22', fill: true, tension: 0.3, borderWidth: 2.5 }] },
                options: {
                    responsive: true,
                    scales: { y: { beginAtZero: true, title: { display: true, text: 'Deferred Backlog ($)' }, ticks: { callback: dollarTicks } } },
                    plugins: { verticalCrosshair: false, legend: { display: false }, tooltip: { callbacks: { label: function(c) { return fmt$(c.raw); } } } },
                    interaction: { mode: 'index', intersect: false }
                }
            }));

            aggregateCharts.push(new Chart(canvasById('aggregate-mix-chart').getContext('2d'), {
                type: 'bar',
                data: {
                    labels: ['Roads', 'Parking', 'Sidewalks'],
                    datasets: [{ label: 'Network area (' + unit + ')', data: [totalRoads, totalParking, totalSidewalks], backgroundColor: ['#6b7280', '#3b82f6', '#f59e0b'] }]
                },
                options: {
                    indexAxis: 'y',
                    responsive: true,
                    scales: { x: { beginAtZero: true, title: { display: true, text: 'Network area (' + unit + ')' }, ticks: { callback: function(v) { return fmtNum(v); } } } },
                    plugins: { verticalCrosshair: false, legend: { display: false }, tooltip: { callbacks: { label: function(c) { return fmtNum(c.raw) + ' ' + unit; } } } }
                }
            }));

            // Per-city contribution table (inverse lens to Compare's ranking),
            // sorted by Year-1 need.
            var sorted = allMetrics.slice().sort(function(a, b) { return (b.year1Need || 0) - (a.year1Need || 0); });
            document.querySelector('#aggregate-contrib-table tbody').innerHTML = sorted.map(function(m) {
                var y1 = m.hasForecasts ? m.year1Need : 0;
                var bk = (m.hasForecasts && m.baselineYears[minLen - 1]) ? (m.baselineYears[minLen - 1].deferred_backlog || 0) : 0;
                var y1Share = regionYear1 > 0 ? (y1 / regionYear1 * 100).toFixed(1) + '%' : '—';
                var bkShare = regionBacklog > 0 ? (bk / regionBacklog * 100).toFixed(1) + '%' : '—';
                var name = m.hasForecasts ? esc(m.name) : esc(m.name) + ' <span class="lb-note" title="No forecast data">(no forecast)</span>';
                return '<tr><td>' + name + '</td>' +
                    '<td class="number">' + fmtNum(m.totalAreaLarge) + ' ' + unit + '</td>' +
                    '<td class="number">' + (m.hasForecasts ? fmt$(y1) : '—') + '</td>' +
                    '<td class="number">' + (m.hasForecasts ? y1Share : '—') + '</td>' +
                    '<td class="number">' + (m.hasForecasts ? fmt$(bk) : '—') + '</td>' +
                    '<td class="number">' + (m.hasForecasts ? bkShare : '—') + '</td></tr>';
            }).join('');

            var excluded = allMetrics.length - fc.length;
            document.getElementById('aggregate-footnote').textContent =
                'Area-weighted PCI uses each city’s simplified single-cohort baseline condition (scenarios.json), not the cohort-blended decay. ' +
                'Trajectories sum the baseline (no-intervention) scenario across cities with forecast data' +
                (excluded > 0 ? ' (' + excluded + ' without forecasts excluded)' : '') +
                '. Dollars are nominal in the config’s cost basis.';

            document.getElementById('aggregate-loading').style.display = 'none';
            document.getElementById('aggregate-content').style.display = 'block';
        }

        // Load WASM
        if (typeof WebAssembly !== 'undefined') {
            const script = document.createElement('script');
            script.src = PVMT_CONFIG.wasmPrefix + 'wasm_exec.js';
            script.onload = function() {
                const go = new Go();
                WebAssembly.instantiateStreaming(fetch(PVMT_CONFIG.wasmPrefix + 'pvmt.wasm'), go.importObject)
                    .then(result => { go.run(result.instance); onWasmReady(); })
                    .catch(err => {
                        console.warn('WASM load failed:', err);
                        showLoadError('forecast WASM', err.message || String(err));
                    });
            };
            document.head.appendChild(script);
        }
