        // Config injected by the template as window.PVMT_CONFIG (see
        // TemplateData.IndexConfigJSON in internal/export/meta.go).
        const PVMT_CONFIG = window.PVMT_CONFIG;
        // Money formatting, the error banner and the JSON fetch wrapper are the
        // shared ones from common.js, which the page loads before this script.
        const { fmtMoney, shownPCI, markPressed, showError, loadJSON } = PvmtCommon;

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

        // Tag dimensions for the filters (PvmtSelectors, selectors.js); absent
        // on a single-city page, which has no selector to filter.
        const TAG_DIMS = PVMT_CONFIG.tag_dimensions;

        // The Play link in the tab bar carries the current city and tag
        // filters so the game opens on the same selection. Captured before any
        // query string is appended so re-syncs don't stack parameters.
        const playLink = queryEl('a.nav-link');
        const playLinkBase = playLink ? playLink.getAttribute('href') : '';
        /** @param {{city: string | null, tags: Record<string, string>}} s */
        function syncPlayLink(s) {
            if (!playLink) return;
            const p = new URLSearchParams();
            if (s.city) p.set('city', s.city);
            PvmtSelectors.urlTags(s.tags || {}, TAG_DIMS).forEach(t => p.append('tag', t));
            const qs = p.toString();
            playLink.setAttribute('href', playLinkBase + (qs ? '?' + qs : ''));
        }

        // URL state router: city, tab, tags, scope, snapshot. DOM is the source
        // of truth; the router serializes DOM → URL and applies URL → DOM via
        // the same selectX helpers the event handlers use. The snapshot slot
        // is reserved for the snapshot-picker. `tags` is
        // a {dimension: configTag} map in state and repeated ?tag=dim:value
        // params in the URL (PvmtSelectors.parseTags/urlTags); it serializes on
        // every tab because the filters narrow the city selector everywhere.
        const Router = {
            read() {
                const p = new URLSearchParams(location.search);
                return {
                    city:     p.get('city'),
                    tab:      p.get('tab'),
                    tags:     PvmtSelectors.parseTags(p, TAG_DIMS),
                    scope:    p.get('scope'),
                    snapshot: p.get('snapshot'),
                    metric:   p.get('metric'),
                    budget:   p.get('budget'),
                    maintain: p.get('maintain'),
                    strategy: p.get('strategy'),
                    pci:      p.get('pci'),
                    cost:     p.get('cost'),
                    tiers:    p.get('tiers'),
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
                // The "Color by" metric lives on the Map tab; serializing it
                // elsewhere would pin ?metric= to a tab where it does nothing
                // (the same rule scope follows). Coverage is the default and is
                // left out of the URL.
                const onMap = tabBtn && tabBtn.dataset.tab === 'map-tab';
                // The Financials budget, likewise, only once the reader has
                // set one: the default (today's budget) needs no URL.
                const onFinancials = tabBtn && tabBtn.dataset.tab === 'financials-tab';
                // With no city matching the filters the select holds a disabled
                // placeholder (value '') and no city is in view (the no-match
                // panel replaces the city tabs), so the URL names none. A
                // reload of ?tag=<unsatisfiable> then lands on the same empty
                // state, as /play does, instead of a ?city= that wins and
                // silently drops the filters.
                return {
                    city:     sel ? (sel.value || null) : null,
                    tab:      tabBtn ? tabBtn.dataset.tab : null,
                    tags:     PvmtSelectors.readFilters(),
                    // City-only is the default and is left out of the URL,
                    // like the metric and budget defaults: only ?scope=all
                    // is ever written.
                    scope:    (scopeVis && scopeBtn && scopeBtn.dataset.scope !== 'city') ? scopeBtn.dataset.scope : null,
                    snapshot: snapEl ? snapEl.value : null,
                    metric:   (onMap && hexMetric !== DEFAULT_METRIC) ? hexMetric : null,
                    budget:   (onFinancials && budgetUserSet) ? String(Math.round(budgetDollars)) : null,
                    // Likewise what the forecast maintains, once it differs
                    // from the default (streets only).
                    maintain: (onFinancials && !sameMaintain(maintained, DEFAULT_MAINTAIN)) ? maintained.join(',') : null,
                    // And the model settings (strategy, Initial PCI, cost
                    // multiplier, tier costs) that differ from the city's seed.
                    ...modelURLState(onFinancials),
                };
            },
            // url serializes a state object to this page's path + query string.
            url(s) {
                const p = new URLSearchParams();
                for (const k of ['city', 'tab', 'scope', 'snapshot', 'metric', 'budget', 'maintain', 'strategy', 'pci', 'cost', 'tiers']) {
                    if (s[k]) p.set(k, s[k]);
                }
                PvmtSelectors.urlTags(s.tags, TAG_DIMS).forEach(t => p.append('tag', t));
                const qs = p.toString();
                return location.pathname + (qs ? '?' + qs : '');
            },
            push() {
                this.flushSoon();
                const s = this.current();
                history.pushState(s, '', this.url(s));
                syncPlayLink(s);
            },
            // replace records the current DOM state without a history entry —
            // used at boot so the address bar reflects what was actually
            // applied (e.g. a ?tag= dropped because the deep-linked city was
            // outside it), not the raw link that was followed.
            replace() {
                this.cancelSoon();
                const s = this.current();
                history.replaceState(s, '', this.url(s));
                syncPlayLink(s);
            },
            // replaceSoon is replace for controls that fire on every input
            // event (the Financials sliders and tier inputs): it records the
            // state now but writes it once the input pauses, since Safari
            // throws SecurityError after ~100 replaceState calls in a short
            // window. push writes a pending state to
            // the entry it leaves first; replace and popstate drop it. The
            // sliders also flush on release and the page on pagehide, so the
            // only edit a Back can drop is one still being dragged.
            /** @type {any} */
            pending: null,
            /** @type {ReturnType<typeof setTimeout> | undefined} */
            timer: undefined,
            replaceSoon() {
                const s = this.current();
                this.pending = s;
                syncPlayLink(s);
                clearTimeout(this.timer);
                this.timer = setTimeout(() => this.flushSoon(), REPLACE_DEBOUNCE_MS);
            },
            // flushSoon writes a pending replaceSoon state now.
            flushSoon() {
                const s = this.pending;
                this.cancelSoon();
                if (s) history.replaceState(s, '', this.url(s));
            },
            cancelSoon() {
                clearTimeout(this.timer);
                this.pending = null;
            },
        };
        const REPLACE_DEBOUNCE_MS = 250;
        // Leaving the page inside the debounce window must not lose the last
        // edit: write it before the document goes (reload, link, tab close).
        window.addEventListener('pagehide', () => Router.flushSoon());
        // flushOnRelease writes a slider's pending state as soon as the drag
        // or key press ends, so a Back right after an edit cannot drop it:
        // popstate fires after the entry has changed, too late to flush.
        /** @param {HTMLElement} el */
        function flushOnRelease(el) {
            el.addEventListener('pointerup', () => Router.flushSoon());
            el.addEventListener('keyup', () => Router.flushSoon());
        }

        // Scope state. Declared above applyInitialState so the URL-state IIFE
        // can read/write it before the map state block runs.
        let currentScope = 'city';

        // Financials budget state, declared here for the
        // same reason: Router.current serializes it. budgetDollars is the
        // interactive forecast's annual budget in dollars. Until the reader
        // moves the slider (budgetUserSet) it tracks the default — today's cited
        // budget, else the hold-steady budget — so it lands right whichever data
        // arrives first; after that it stays put while Initial PCI, the cost
        // multiplier and the tiers move. budgetCity names the city the state
        // belongs to: resetBudget settles it on boot, a city switch and
        // back/forward.
        let budgetDollars = 0;
        let budgetUserSet = false;
        let budgetCity = null;

        // What the interactive forecast maintains: a
        // subset of the resource types, in MAINTAIN_TYPES order. The default is
        // streets only — the network the solvency headline, the cited "Today"
        // budget (a street budget) and Play all describe. ?maintain= carries any
        // other choice. Independent of the map's layer toggles.
        const MAINTAIN_TYPES = ['roads', 'parking', 'sidewalks'];
        const DEFAULT_MAINTAIN = ['roads'];
        let maintained = DEFAULT_MAINTAIN.slice();

        // BUDGET_MAX_DOLLARS bounds a ?budget= before the city's anchors are
        // known; BUDGET_MAX_ANCHOR_MULT bounds it after, as a multiple of the
        // larger of today's budget and hold steady. The slider has a notch per
        // 25% of hold steady, so an unbounded ?budget=1e15 would build
        // millions of DOM nodes and crash the tab. Ten times hold steady is far past
        // where extra money still buys condition.
        const BUDGET_MAX_DOLLARS = 1e12;
        const BUDGET_MAX_ANCHOR_MULT = 10;

        // resetBudget settles the budget state for the city DATA_PREFIX names:
        // the dollars in `param` (a ?budget= value) when it parses as a
        // non-negative number, else the default, which applyBudget fills in.
        /** @param {string | null | undefined} param */
        function resetBudget(param) {
            const v = param != null && param !== '' ? parseFloat(param) : NaN;
            budgetCity = DATA_PREFIX;
            budgetUserSet = Number.isFinite(v) && v >= 0;
            if (budgetUserSet) budgetDollars = Math.min(v, BUDGET_MAX_DOLLARS);
        }

        // parseMaintain reads a ?maintain= value into a MAINTAIN_TYPES subset,
        // or null when it names none of them.
        /** @param {string | null | undefined} param @returns {string[] | null} */
        function parseMaintain(param) {
            const want = String(param || '').split(',');
            const got = MAINTAIN_TYPES.filter(t => want.includes(t));
            return got.length ? got : null;
        }

        /** @param {string[]} a @param {string[]} b */
        function sameMaintain(a, b) {
            return a.length === b.length && a.every((t, i) => t === b[i]);
        }

        // The Financials model settings the URL carries:
        // ?strategy=, ?pci= (Initial PCI), ?cost= (the cost multiplier) and
        // ?tiers= (the cost tiers as min-max:$/sq m, comma-separated, e.g.
        // 0-40:150,40-70:50,70-101:5; metric whatever the display units). Each
        // is written only on the Financials tab and only when it differs from
        // the city's seed, so a shared link reproduces the headline numbers.
        //
        // The controls are seeded from forecast_seed.json, which arrives after
        // the URL is read, so the linked settings wait in pendingModel until
        // loadFinancials has seeded the controls and applies them over the
        // seed (applyModel). A null field means "the seed's". While a model is
        // pending, the URL serializes it rather than the controls, which still
        // describe the seed or the previous city. A city switch pends the
        // empty model: the new city starts from its own seed.
        // The strategies' names are forecast.Strategy's (snake_case).
        const STRATEGIES = ['worst_first', 'preventive_first'];
        const DEFAULT_STRATEGY = 'worst_first';
        let currentStrategy = DEFAULT_STRATEGY;
        /** @typedef {{min_pci: number, max_pci: number, cost_per_sqm: number}} TierSetting */
        /** @typedef {{strategy: string | null, pci: number | null, cost: number | null, tiers: TierSetting[] | null}} ModelSettings */
        /** @type {ModelSettings | null} */
        let pendingModel = null;
        // The cost multiplier's range, as config validation bounds it.
        const COST_MAX = 5;

        /** @param {string | null | undefined} v @returns {number} */
        function paramNum(v) {
            return v == null || String(v).trim() === '' ? NaN : Number(v);
        }

        // parseTiers reads a ?tiers= value, or null when any tier is malformed,
        // out of range or overlaps another. The tier count is checked against
        // the seed when it is applied.
        /** @param {string | null | undefined} param @returns {TierSetting[] | null} */
        function parseTiers(param) {
            if (!param) return null;
            const tiers = [];
            for (const part of String(param).split(',')) {
                const m = /^([^-:]+)-([^-:]+):(.+)$/.exec(part.trim());
                if (!m) return null;
                const lo = paramNum(m[1]), hi = paramNum(m[2]), cost = paramNum(m[3]);
                if (!(lo >= 0 && hi <= 101 && lo < hi && cost > 0 && Number.isFinite(cost))) return null;
                tiers.push({ min_pci: lo, max_pci: hi, cost_per_sqm: cost });
            }
            const sorted = tiers.slice().sort((a, b) => a.min_pci - b.min_pci);
            for (let k = 1; k < sorted.length; k++) {
                if (sorted[k].min_pci < sorted[k - 1].max_pci) return null;
            }
            return tiers;
        }

        /** @param {TierSetting[]} tiers */
        function formatTiers(tiers) {
            return tiers.map(t => t.min_pci + '-' + t.max_pci + ':' + t.cost_per_sqm).join(',');
        }

        // parseModel reads the model settings from a URL state, dropping any
        // malformed value (it falls back to the seed). A kebab-case strategy
        // (?strategy=preventive-first) reads as its snake_case name, so older
        // links still apply.
        /** @param {{strategy?: string | null, pci?: string | null, cost?: string | null, tiers?: string | null}} s @returns {ModelSettings} */
        function parseModel(s) {
            const pci = paramNum(s.pci), cost = paramNum(s.cost);
            const strategy = s.strategy ? String(s.strategy).replace(/-/g, '_') : null;
            return {
                strategy: strategy && STRATEGIES.includes(strategy) ? strategy : null,
                pci: pci >= 0 && pci <= 100 ? Math.round(pci) : null,
                cost: cost > 0 && cost <= COST_MAX ? cost : null,
                tiers: parseTiers(s.tiers),
            };
        }

        // modelURLState is the model settings' share of Router.current.
        /** @param {boolean} onFinancials */
        function modelURLState(onFinancials) {
            const m = onFinancials ? (pendingModel || currentModel()) : null;
            return {
                strategy: m && m.strategy && m.strategy !== DEFAULT_STRATEGY ? m.strategy : null,
                pci:      m && m.pci != null ? String(m.pci) : null,
                cost:     m && m.cost != null ? String(m.cost) : null,
                tiers:    m && m.tiers ? formatTiers(m.tiers) : null,
            };
        }
        // The roads solvency headline recomputed at the live settings
        // (computeLiveSolvency); null until the forecast simulator has run for
        // this city. Declared up here because renderSolvencyHeadline reads it
        // from the first financials load.
        /** @type {{break_even_budget: number, current_budget: number, insolvency_year: number | null} | null} */
        let liveSolvency = null;

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
        // "Color by" state. Every hex coloring is a registry metric
        // (internal/hexmetric): DERIVED ones (coverage, area, spaces, est.
        // cost) arrive once in PVMT_CONFIG.hex_metrics and are computed here
        // from hexgrid.geojson over the toggled resources; STORED ones arrive
        // per city in hex-metrics.json (hexMetrics). hexMetric is the selected
        // key. Declared here (like the tab guards) so a ?metric= deep link
        // survives the init IIFE and the per-city load can honour it once the
        // city's files are in.
        const DEFAULT_METRIC = 'paved_share';
        /** @type {Record<string, PvmtHexMetric>} */
        const derivedMetrics = {};
        for (const spec of (PVMT_CONFIG.hex_metrics || [])) derivedMetrics[spec.key] = spec;
        var hexMetric = DEFAULT_METRIC;
        /** @type {Record<string, PvmtHexMetric>} */
        var hexMetrics = {};
        // hexGeomById holds every grid hex's geometry (hexgrid emits hexes
        // without pavement rows too) so a stored metric can paint them.
        /** @type {Record<string, any>} */
        var hexGeomById = {};
        // Active tag filters: {dimension: configTag}, empty means all cities.
        // They narrow the city selector on every tab and scope Compare /
        // Aggregate. Declared here (alongside the other tab guards) so a deep
        // link does not re-run the initializer and reset it.
        /** @type {Record<string, string>} */
        var selectedTags = {};

        // rebuildCitySelector regenerates #city-select for the active filters
        // and returns the slug now selected ('' when nothing matches). `prefer`
        // names the slug to keep selected when it survives the filter.
        function rebuildCitySelector(prefer) {
            const sel = selectById('city-select');
            if (!sel || CITIES.length === 0) return '';
            const picked = PvmtSelectors.rebuildCitySelect(sel, CITIES, TAG_DIMS, selectedTags, prefer);
            syncNoMatchState();
            return picked;
        }

        // syncNoMatchState shows the one shared empty state when the tag
        // filters match no city: the city-scoped tabs (Map, Financials,
        // Materials, Compare, Aggregate) are replaced by "No cities match
        // <filters> — Clear filters" instead of a stale city or empty charts.
        // Run after every selector rebuild and tab
        // change.
        function syncNoMatchState() {
            const sel = selectById('city-select');
            const empty = !!sel && CITIES.length > 0 && !sel.value;
            document.body.classList.toggle('no-city-match', empty);
            const panel = document.getElementById('no-match');
            if (!panel) return;
            const active = queryEl('.tab-content.active');
            panel.hidden = !(empty && active && active.hasAttribute('data-city-scoped'));
            if (!empty) return;
            const labels = queryAll('select.tag-filter')
                .map(el => /** @type {HTMLSelectElement} */ (el))
                .filter(f => f.value && f.selectedOptions[0])
                .map(f => f.selectedOptions[0].textContent.trim());
            document.getElementById('no-match-text').textContent =
                'No cities match ' + (labels.length ? labels.join(' + ') : 'these filters') + '.';
        }

        // A deep-linked ?snapshot=, read before the boot's Router.replace drops
        // it: the picker holds no snapshot names until loadSnapshots fills it.
        const bootSnapshot = Router.read().snapshot;

        // Apply URL state synchronously, before first render, to avoid flicker.
        // currentScope is set here (default 'city') and is the sole source of
        // truth thereafter — selectScope is the only function that changes it.
        (function applyInitialState() {
            const init = Router.read();
            const sel = selectById('city-select');
            // Tag filters FIRST, before anything reads the selector: the
            // selector is rebuilt for the filters, and the tab block below calls
            // loadCompareData()/loadAggregateData(), which read selectedTags via
            // scopeCityData. A deep-linked ?city= always wins — filters that
            // would hide it are dropped rather than the link landing on another
            // city. Unknown ?tag= values are ignored rather than filtering
            // everything away, and writeFilters resets any value a browser
            // restored into the selects across reload/bfcache (Firefox) so the
            // controls never disagree with the data.
            if (sel && CITIES.length > 0) {
                let filters = init.tags;
                const linked = init.city ? CITIES.find(c => c.slug === init.city) : null;
                if (linked && !PvmtSelectors.matches(linked.tags, filters)) filters = {};
                selectedTags = filters;
                PvmtSelectors.writeFilters(selectedTags);
                rebuildCitySelector(init.city || sel.value);
            }
            if (init.city && CITIES.some(c => c.slug === init.city)) {
                DATA_PREFIX = 'cities/' + init.city + '/';
                if (sel) sel.value = init.city;
            } else {
                // No ?city= deep link: the browser default-selects the first DOM <option>,
                // which follows the tag groups (tag groups first) and can differ from
                // CITIES[0] (flat alphabetical) that seeded DATA_PREFIX above. Align the
                // data prefix with the shown selection so we never load one city's data
                // while the dropdown names another (and replaceState records the truth).
                if (sel && sel.value) DATA_PREFIX = 'cities/' + sel.value + '/';
            }
            // Only a tab's own id counts: ?tab=map names an element, not a tab.
            const initTabBtn = tabButton(init.tab);
            const initTab = document.getElementById(init.tab);
            if (initTabBtn && initTab && initTab.classList.contains('tab-content')) {
                queryAll('.tab-btn').forEach(b => b.classList.toggle('active', b === initTabBtn));
                queryAll('.tab-content').forEach(c => c.classList.toggle('active', c === initTab));
                syncTabAria();
                revealTabButton(initTabBtn);
                // Toggling classes does NOT trigger the per-tab data load that
                // a selectTab() click would, so a deep-linked ?tab= for a
                // lazy-loaded tab would otherwise hang on its loading spinner.
                if (init.tab === 'aggregate-tab') loadAggregateData();
                else if (init.tab === 'compare-tab') loadCompareData();
                else if (init.tab === 'materials-tab') loadMaterials();
            }
            if (init.scope === 'city' || init.scope === 'all') {
                currentScope = init.scope;
                syncScopeToggle();
            }
            // The metric list arrives with the city's data; keep the requested
            // key and let loadCityData fall back to coverage if it is absent.
            if (init.metric) hexMetric = init.metric;
            // Settle the budget, the maintained network and the model settings
            // before the replaceState below, or it would serialize the defaults
            // and drop a shared ?budget= / ?maintain= / ?pci= ... from the
            // address bar. loadFinancials applies the model once the seed lands.
            // Router.current writes them only on Financials, so they are read
            // only there too: elsewhere they would apply settings the address
            // bar no longer shows.
            const fin = queryEl('.tab-btn.active')?.dataset.tab === 'financials-tab' ? init : {};
            resetBudget(fin.budget);
            maintained = parseMaintain(fin.maintain) || DEFAULT_MAINTAIN.slice();
            pendingModel = parseModel(fin);
            Router.replace();
        })();

        window.addEventListener('popstate', (e) => {
            // A state replaceSoon has not written yet belongs to the entry
            // just left; writing it now would land on this one.
            Router.cancelSoon();
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
            // No ?scope= is the default, city-only.
            const scopeBefore = currentScope;
            currentScope = s.scope === 'all' ? 'all' : 'city';
            // A city with data in one scope only never writes ?scope= (its
            // scope row is hidden), so on a same-city entry repair the scope
            // the way the loaders do, or the map and Financials go blank. A
            // city change repairs it in loadCityData/loadFinancials.
            if (!cityChanged) {
                const cityEmpty = Object.keys(hexDataByScope.city).length === 0;
                const allEmpty = Object.keys(hexDataByScope.all).length === 0;
                if (currentScope === 'city' && cityEmpty && !allEmpty) currentScope = 'all';
                else if (currentScope === 'all' && allEmpty && !cityEmpty) currentScope = 'city';
                if (scenarioData) currentScope = effectiveScope(scenarioData, currentScope);
            }
            syncScopeToggle();
            // Compare renders once per scope; an entry in another scope needs
            // it re-rendered, as selectScope does.
            if (currentScope !== scopeBefore) compareLoaded = false;
            // Only fire selectCity when the city actually changed. selectCity
            // resets the snapshot picker (snapshots are per-city), so calling it
            // on a same-city snapshot-only navigation would clobber targetSnap
            // (set above) and reload against Latest. The snapshot-only branch
            // below handles that case explicitly.
            // Restore the tag filters before the tab switch below: selectTab
            // kicks off loadCompareData/loadAggregateData, which read
            // selectedTags via scopeCityData. Clearing compareLoaded here (not
            // after) is what lets that one-shot loader re-render; otherwise the
            // leaderboard keeps the previous filter's rows while the URL claims
            // otherwise. The selector is rebuilt for the restored filters with
            // the history entry's city kept selected.
            const targetTags = s.tags || {};
            const tagChanged = !PvmtSelectors.sameFilters(selectedTags, targetTags);
            if (tagChanged) {
                selectedTags = targetTags;
                PvmtSelectors.writeFilters(selectedTags);
                rebuildCitySelector(s.city || (sel ? sel.value : ''));
                compareLoaded = false;
            }
            if (cityChanged) selectCity(s.city, { push: false });
            if (s.tab)  selectTab(s.tab,   { push: false });
            // A hand-edited URL can carry ?tag= with no ?tab=, so selectTab
            // never fired and nothing re-rendered. Router.push always writes
            // both, so this only guards that case.
            if (tagChanged && !s.tab) rerenderTagScopedTabs();
            if (tagChanged) syncPlayLink(Router.current());
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
            // Restore the metric. When the city or snapshot changed,
            // loadCityData has just reset hexMetrics and will honour (or fall
            // back from) hexMetric once the reloaded files arrive — so set the
            // key directly, as the deep-link path does; selectHexMetric would
            // see an empty map and collapse to coverage.
            // Only an entry with the same data in view repaints in place.
            const targetMetric = s.metric || DEFAULT_METRIC;
            if (cityChanged || snapshotChanged) hexMetric = targetMetric;
            else if (targetMetric !== hexMetric) selectHexMetric(targetMetric, { push: false });
            // Only a Financials entry carries the budget, maintained network
            // and model settings (Router.current), so another tab's entry for
            // the same city says nothing about them: keep the ones in force,
            // or Financials -> Map -> About -> Back -> Financials would reset
            // them to the seed. A city change has reset
            // them in selectCity already.
            const tabNow = queryEl('.tab-btn.active');
            if (!cityChanged && !(tabNow && tabNow.dataset.tab === 'financials-tab')) return;
            // Restore the Financials budget and maintained network. selectCity
            // has already reset the budget for a new city; this puts the
            // entry's own back. A city or snapshot change reloads Financials,
            // which picks both up; otherwise repaint in place.
            const budgetBefore = budgetUserSet ? budgetDollars : null;
            const maintainBefore = maintained;
            resetBudget(s.budget);
            maintained = parseMaintain(s.maintain) || DEFAULT_MAINTAIN.slice();
            renderMaintainToggles();
            const budgetAfter = budgetUserSet ? budgetDollars : null;
            // The model settings likewise. A reload (city, snapshot, or a load
            // still in flight) applies them when its seed lands; otherwise they
            // go onto the seeded controls here.
            const inPlace = !cityChanged && !snapshotChanged && pendingModel === null && forecastSeedReady;
            const modelBefore = inPlace ? JSON.stringify(currentModel()) : '';
            pendingModel = parseModel(s);
            let modelChanged = false;
            if (inPlace) {
                modelChanged = JSON.stringify(pendingModel) !== modelBefore;
                if (modelChanged) applyModel(pendingModel);
                pendingModel = null;
            }
            if (!cityChanged && !snapshotChanged &&
                (budgetAfter !== budgetBefore || !sameMaintain(maintained, maintainBefore) || modelChanged)) {
                renderFinancialsFromCache();
            }
        });

        // dataURL builds a /data/{file} request URL, appending ?snapshot=<name>
        // if the snapshot-picker has a non-empty value. The picker only exists
        // in live-server mode; in static export this returns the bare URL.
        function dataURL(prefix, file) {
            const sel = selectById('snapshot-picker');
            const snap = sel && sel.value ? sel.value : '';
            return prefix + 'data/' + file + (snap ? '?snapshot=' + encodeURIComponent(snap) : '');
        }

        // safeSimulate runs the forecast simulator (the WASM's simulateForecast
        // export) on input. A WASM-side panic or reported error surfaces in the
        // error banner instead of silently freezing the chart, and returns
        // null; callers must check for null and bail.
        function safeSimulate(input) {
            return PvmtCommon.callWasm('simulateForecast', [JSON.stringify(input)], 'forecast simulator');
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
        // revealTabButton scrolls the tab row (which scrolls sideways on a
        // phone) so the selected tab is in view, e.g. a ?tab=about link.
        function revealTabButton(btn) {
            const list = btn.closest('.tab-list');
            if (!list || list.scrollWidth <= list.clientWidth) return;
            const left = btn.offsetLeft - list.offsetLeft;
            if (left < list.scrollLeft || left + btn.offsetWidth > list.scrollLeft + list.clientWidth) {
                // Clear of the phone strip's 28 px left fade.
                list.scrollLeft = Math.max(0, left - 32);
            }
        }
        /** The tab button for tabId, matched by value so a hostile ?tab= cannot break a selector. @param {string|null} tabId */
        function tabButton(tabId) {
            return tabId ? queryAll('.tab-btn').find(b => b.dataset.tab === tabId) || null : null;
        }
        // WASM_TABS are the tabs that run the forecast simulator (or show
        // what it computes); opening one boots it (bootWasm).
        const WASM_TABS = ['financials-tab', 'materials-tab', 'compare-tab', 'aggregate-tab'];
        let wasmBooting = false;
        // bootWasm starts the forecast simulator's one boot (PvmtCommon.loadWasm)
        // and runs onWasmReady once it is up; later calls do nothing.
        function bootWasm() {
            if (wasmBooting || typeof WebAssembly === 'undefined') return;
            wasmBooting = true;
            PvmtCommon.loadWasm().then(onWasmReady).catch(err => {
                console.warn('WASM load failed:', err);
                showError('forecast WASM', PvmtCommon.errorText(err));
            });
        }

        function selectTab(tabId, opts) {
            const btn = tabButton(tabId);
            const content = document.getElementById(tabId);
            if (!btn || !content) return;
            queryAll('.tab-btn').forEach(b => b.classList.remove('active'));
            queryAll('.tab-content').forEach(c => c.classList.remove('active'));
            btn.classList.add('active');
            content.classList.add('active');
            syncTabAria();
            syncNoMatchState();
            revealTabButton(btn);
            if (opts && opts.focus) btn.focus();
            if (tabId === 'map-tab' && window._map) window._map.resize();
            if (WASM_TABS.includes(tabId)) bootWasm();
            if (tabId === 'compare-tab') loadCompareData();
            if (tabId === 'aggregate-tab') loadAggregateData();
            if (tabId === 'materials-tab') loadMaterials();
            // The aggregate honors the region-wide scope, so re-evaluate the
            // scope-toggle reveal whenever the active tab changes. A push
            // follows, which writes the revealed scope itself.
            const push = !opts || opts.push !== false;
            maybeRevealScopeToggle(push);
            if (push) Router.push();
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

        // Phone tab strip fades: the .tab-list scrolls sideways on a phone,
        // and each edge fades only while more tabs lie beyond it, so the
        // last tab (Play) is not left faded at the end of the scroll. The
        // masks themselves are CSS, keyed on these classes, and only apply
        // in the phone layout. 1px of slack absorbs fractional scrollLeft.
        (function bindTabStripFades() {
            const strip = queryEl('.tab-list');
            if (!strip) return;
            const sync = () => {
                const max = strip.scrollWidth - strip.clientWidth;
                strip.classList.toggle('fade-left', strip.scrollLeft > 1);
                strip.classList.toggle('fade-right', strip.scrollLeft < max - 1);
            };
            strip.addEventListener('scroll', sync, { passive: true });
            window.addEventListener('resize', sync);
            sync();
        })();

        // Map
        const CENTER = PVMT_CONFIG.center;
        const layerColors = PVMT_CONFIG.layer_colors;
        // Display units are a client-side preference. All emitted data is raw
        // metric and the area/mass helpers below convert on the fly, so the unit
        // system can change at runtime (see the gear-menu Units toggle) without a
        // reload. localStorage persists the choice across visits; the config's
        // [display].units (PVMT_CONFIG.unit_system) seeds the first visit — so a
        // standalone example keeps its configured default, and in the combined
        // site every included city follows the one live preference.
        let UNIT_SYSTEM = (function () {
            try {
                const u = localStorage.getItem('pvmtUnits');
                if (u === 'metric' || u === 'imperial') return u;
            } catch (e) { /* storage disabled (private mode): fall through */ }
            return PVMT_CONFIG.unit_system;
        })();

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
        // Singular large-area unit, for rates ("per acre", "per hectare").
        function areaLargeUnitSingular() {
            return UNIT_SYSTEM === 'metric' ? 'hectare' : 'acre';
        }
        function areaVeryLargeUnit() {
            return UNIT_SYSTEM === 'metric' ? 'sq km' : 'sq mi';
        }
        // Base-area unit label (the units fmtArea and the raw $/area figures use).
        function areaUnit() {
            return UNIT_SYSTEM === 'metric' ? 'sq m' : 'sq ft';
        }
        // toAreaBase converts sq m to the base display unit (areaUnit).
        function toAreaBase(sqm) {
            return UNIT_SYSTEM === 'metric' ? sqm : sqm * SQM_TO_SQFT;
        }
        function fmtArea(sqm) {
            return fmtNum(toAreaBase(sqm)) + ' ' + areaUnit();
        }
        // A cost rate is stored and simulated in $/sq m and shown in $/areaUnit():
        // costPerAreaToDisplay converts a $/sq m rate for display, and
        // costPerAreaFromDisplay converts a displayed rate back.
        /** @param {number} perSqm */
        function costPerAreaToDisplay(perSqm) {
            return UNIT_SYSTEM === 'metric' ? perSqm : perSqm / SQM_TO_SQFT;
        }
        /** @param {number} perUnit */
        function costPerAreaFromDisplay(perUnit) {
            return UNIT_SYSTEM === 'metric' ? perUnit : perUnit * SQM_TO_SQFT;
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
        // repaint between city-only and all-jurisdiction without re-fetching;
        // currentScope (declared above applyInitialState) is the single
        // source of truth for which bucket buildBlendedHexFC / the click
        // handler read from.
        let hexDataByScope = { city: {}, all: {} };       // { scope: { type: [features] } }
        let costSummaryByScope = { city: {}, all: {} };   // { scope: { type: {year1_need,area} } }
        let activeTypes = new Set(Object.keys(layerColors));
        // #stats shows either the city summary or one detail panel (a clicked
        // hex, a dragged region) over it. savedPanelHTML is the summary the
        // detail panel's close button restores; renderOpenDetail re-renders
        // the open detail panel from live state, and is null while the
        // summary is showing. A resource toggle, scope switch or units switch
        // refreshes both (refreshCityStats), so the panel stays open and
        // current and closing it lands on an up-to-date summary.
        let savedPanelHTML = '';
        /** @type {(() => void) | null} */
        let renderOpenDetail = null;
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

        // mapFitPadding is the padding a fit to the city uses. The stats
        // panel floats over the map, down the right edge on a wide screen and
        // docked along the bottom on a phone, so the side it covers is padded
        // by how far the panel reaches in from it; a uniform padding put the
        // city's east side under the panel. The panel
        // hugs whichever side costs the smaller strip of map. A panel with no
        // box (the Map tab hidden) or one that would leave too little map
        // keeps the plain padding, since MapLibre refuses a fit that cannot
        // be made.
        const FIT_PAD = 40;
        const FIT_MIN_VIEW = 120;
        function mapFitPadding() {
            const pad = { top: FIT_PAD, right: FIT_PAD, bottom: FIT_PAD, left: FIT_PAD };
            const mapEl = document.getElementById('map');
            const panelEl = document.getElementById('panel');
            if (!mapEl || !panelEl) return pad;
            const m = mapEl.getBoundingClientRect();
            const p = panelEl.getBoundingClientRect();
            if (!m.width || !m.height || !p.width || !p.height) return pad;
            const fromRight = Math.max(0, m.right - p.left);
            const fromBottom = Math.max(0, m.bottom - p.top);
            const out = { ...pad };
            if (fromRight * m.height <= fromBottom * m.width) out.right = Math.round(fromRight) + FIT_PAD;
            else out.bottom = Math.round(fromBottom) + FIT_PAD;
            if (m.width - out.left - out.right < FIT_MIN_VIEW || m.height - out.top - out.bottom < FIT_MIN_VIEW) return pad;
            return out;
        }

        // framingBounds is the [[west, south], [east, north]] box the map
        // fits a city's boundary to: the boundary's parts, largest first, up
        // to FRAME_AREA_SHARE of its area. San Francisco's boundary carries
        // the Farallon Islands 40 km out to sea, and framing its whole bbox
        // would draw the city as a sliver at the map's edge. The parts left out stay in the boundary,
        // the hexes and every total; this is only the view. Areas are
        // compared, not measured, so the outer rings' shoelace area in
        // degrees, scaled by cos(latitude), is enough. Null when the
        // boundary has no polygon.
        const FRAME_AREA_SHARE = 0.99;
        /** @param {any} fc @returns {[[number, number], [number, number]] | null} */
        function framingBounds(fc) {
            /** @type {{ ring: number[][], area: number }[]} */
            const parts = [];
            for (const f of (fc && fc.features) || []) {
                const g = f && f.geometry;
                if (!g) continue;
                const polys = g.type === 'Polygon' ? [g.coordinates] : g.type === 'MultiPolygon' ? g.coordinates : [];
                for (const poly of polys) {
                    const ring = poly && poly[0];
                    if (!ring || ring.length === 0) continue;
                    let twice = 0;
                    for (let i = 0; i < ring.length - 1; i++) {
                        twice += ring[i][0] * ring[i + 1][1] - ring[i + 1][0] * ring[i][1];
                    }
                    parts.push({ ring, area: Math.abs(twice / 2) * Math.cos(ring[0][1] * Math.PI / 180) });
                }
            }
            if (parts.length === 0) return null;
            parts.sort((a, b) => b.area - a.area);
            const total = parts.reduce((s, p) => s + p.area, 0);
            let w = Infinity, s = Infinity, e = -Infinity, n = -Infinity;
            let kept = 0;
            for (const part of parts) {
                for (const c of part.ring) {
                    if (c[0] < w) w = c[0];
                    if (c[0] > e) e = c[0];
                    if (c[1] < s) s = c[1];
                    if (c[1] > n) n = c[1];
                }
                kept += part.area;
                if (total > 0 && kept >= FRAME_AREA_SHARE * total) break;
            }
            return [[w, s], [e, n]];
        }
        // True while the user is in drag-to-select-region mode. Read by the
        // hex-click handler (suppressed mid-selection) and the region-select
        // pointer handlers below.
        let selecting = false;

        // showNoData replaces a tab's chart area with its empty-state message
        // (msg is trusted HTML written in this file).
        /** @param {string} containerId @param {string} msg */
        function showNoData(containerId, msg) {
            document.getElementById(containerId).innerHTML = '<div class="no-data">' + msg + '</div>';
        }

        function fmtNum(n) {
            return Math.round(n).toLocaleString('en-US');
        }
        // fmtNum1 is fmtNum to one decimal place, thousands separated like it.
        /** @param {number} n */
        function fmtNum1(n) {
            return n.toLocaleString(undefined, { minimumFractionDigits: 1, maximumFractionDigits: 1 });
        }

        // Escape config-sourced strings (city names, cost-tier labels) before
        // concatenating them into innerHTML. html/template's JS-string escaping at
        // page-generation time protects only the script literal, not the runtime
        // DOM insertion, so a name/label containing markup would otherwise execute.
        // missingLayers is meta.json's missing_resources: the resources the
        // city's compute had no features for (an ingest that failed or never
        // ran). They are not zeros, so the page says the data is unavailable
        // and leaves the city out of rankings that need them.
        /** @param {any} meta @returns {string[]} */
        function missingLayers(meta) {
            return (meta && Array.isArray(meta.missing_resources)) ? meta.missing_resources : [];
        }

        // ingestFailures is meta.json's failing_sources: resource -> the
        // configured sources whose last ingest failed and that hold none of its
        // rows. partialLayers are those resources that still have data from
        // another source (Albany's roads: ArcGIS answered, OpenStreetMap did
        // not). They are shown with a note and ranked like a missing layer.
        /** @param {any} meta @returns {Object<string, string[]>} */
        function ingestFailures(meta) {
            return (meta && meta.failing_sources && typeof meta.failing_sources === 'object') ? meta.failing_sources : {};
        }
        // emptyLayers is meta.json's empty_sources: missing resource -> the
        // configured sources, every one of which last answered with no rows
        // (OpenStreetMap maps no sidewalks in a small town). Still missing,
        // and still unranked: no data is not a measured zero.
        /** @param {any} meta @returns {Object<string, string[]>} */
        function emptyLayers(meta) {
            return (meta && meta.empty_sources && typeof meta.empty_sources === 'object') ? meta.empty_sources : {};
        }
        /** @param {any} meta @returns {string[]} */
        function partialLayers(meta) {
            const missing = missingLayers(meta);
            return Object.keys(ingestFailures(meta)).filter(rt => !missing.includes(rt)).sort();
        }
        /** @param {string[]} sources @returns {string} */
        function sourceNames(sources) {
            return sources.map(s => s === 'overpass' ? 'OpenStreetMap' : s === 'arcgis' ? 'ArcGIS' : s).join(', ');
        }
        // unrankedLabel describes the layers that keep m out of a ranking.
        /** @param {any} m @returns {string} */
        function unrankedLabel(m) {
            const parts = [];
            if (m.missing.length > 0) parts.push('no ' + m.missing.join(', ') + ' data');
            if (m.partial.length > 0) parts.push('partial ' + m.partial.join(', ') + ' data');
            const s = parts.join('; ');
            return s.charAt(0).toUpperCase() + s.slice(1);
        }

        function esc(s) {
            return String(s == null ? '' : s)
                .replace(/&/g, '&amp;')
                .replace(/</g, '&lt;')
                .replace(/>/g, '&gt;')
                .replace(/"/g, '&quot;')
                .replace(/'/g, '&#39;');
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
                        byHex[hid] = { geometry: f.geometry, area: 0, share: 0, year1_need: 0, spaces: 0 };
                    }
                    byHex[hid].area += f.properties.area || 0;
                    byHex[hid].share += f.properties.share || 0;
                    byHex[hid].spaces += f.properties.spaces || 0;
                }
            }

            for (const type of activeTypes) {
                const cs = costSummary[type];
                if (!cs || !cs.area) continue;
                const features = hexDataByType[type] || [];
                for (const f of features) {
                    const hid = f.properties.hex_id;
                    if (!byHex[hid]) continue;
                    byHex[hid].year1_need += ((f.properties.area || 0) / cs.area) * cs.year1_need;
                }
            }

            // The active metric's value (and, for a metric referencing points
            // of service, the referenced point's name) rides on each feature as
            // metric_v / metric_ref, so one paint expression and one tooltip
            // line serve every metric. A metric that also covers hexes without
            // pavement rows (stored, or a ratio over a stored denominator)
            // brings those grid hexes in too.
            const metric = activeMetric();
            if (metric) {
                for (const hid of metricExtraHexes(metric)) {
                    if (!byHex[hid] && hexGeomById[hid]) {
                        byHex[hid] = { geometry: hexGeomById[hid], area: 0, share: 0, year1_need: 0, spaces: 0 };
                    }
                }
            }
            const features = [];
            for (const [hid, data] of Object.entries(byHex)) {
                /** @type {Record<string, any>} */
                const extra = {};
                const v = metric ? metricValue(metric, hid, data) : null;
                if (v != null && Number.isFinite(v)) {
                    extra.metric_v = v;
                    const refIdx = metric && metric.refs ? metric.refs[hid] : undefined;
                    if (metric && refIdx != null && metric.points && metric.points[refIdx]) extra.metric_ref = metric.points[refIdx].name || metric.points[refIdx].id;
                }
                features.push({
                    type: 'Feature',
                    geometry: data.geometry,
                    properties: {
                        hex_id: hid,
                        spaces: data.spaces,
                        ...extra,
                        area: data.area,
                        share: Math.min(data.share, 1),
                        year1_need: data.year1_need
                    }
                });
            }
            return { type: 'FeatureCollection', features };
        }

        // updateHexGrid re-blends after a scope swap or a resource toggle.
        // Either can change which derived metrics have inputs (untoggling
        // parking leaves nothing for "Parking spaces"), so the selector is
        // rebuilt and an orphaned selection falls back to the default.
        function updateHexGrid() {
            const src = map.getSource('hexgrid');
            if (!src) return;
            if (!metricAvailable(hexMetric)) {
                selectHexMetric(DEFAULT_METRIC, { push: false });
                Router.replace();
            } else {
                populateMetricSelect();
                src.setData(buildBlendedHexFC());
            }
            // After the blend either way: the per-resident totals and an open
            // detail panel follow the toggles and scope, not the metric.
            refreshCityStats();
        }

        // renderCitySettings fills a multi-city page's Config tab with the
        // settings the city in view is computed under (meta.json
        // settings_toml). A single-city page renders its whole config
        // server-side and has no #config-city-toml.
        function renderCitySettings(meta) {
            const code = document.getElementById('config-city-toml');
            if (!code) return;
            const name = document.getElementById('config-city-name');
            if (name) name.textContent = meta.name || currentCityName();
            code.textContent = meta.settings_toml || 'No settings were published for this city.';
        }

        // lastCityMeta holds the most recently loaded city's meta.json so a units
        // toggle can re-render the (always-visible) stats panel from it without
        // rebuilding the map layers loadCityData otherwise touches.
        let lastCityMeta = null;

        // renderCityStats shows the side-panel stat cards for one city's meta,
        // closing any open detail panel (a new city's summary replaces it).
        function renderCityStats(meta) {
            const statsEl = document.getElementById('stats');
            if (!statsEl || !meta) return;
            savedPanelHTML = cityStatsHTML(meta);
            renderOpenDetail = null;
            statsEl.innerHTML = savedPanelHTML;
        }

        // cityStatsHTML renders the stat cards for one city's meta in the
        // current units, with the per-resident card over the toggled resources
        // in the current scope.
        function cityStatsHTML(meta) {
            let statsHTML = '';
            const missing = missingLayers(meta);
            const failures = ingestFailures(meta);
            const empty = emptyLayers(meta);
            const partial = partialLayers(meta);
            if (meta.city_area && meta.city_area > 0) {
                statsHTML +=
                    '<div class="stat-card summary-card"><h3>City Summary</h3>' +
                    '<div class="stat-row"><span class="stat-label">City Area (' + areaLargeUnit() + ')</span><span class="stat-value">' + fmtNum1(toAreaLarge(meta.city_area)) + '</span></div>' +
                    '<div class="stat-row"><span class="stat-label">City Area (' + areaVeryLargeUnit() + ')</span><span class="stat-value">' + toAreaVeryLarge(meta.city_area).toFixed(2) + '</span></div>' +
                    '<div class="stat-row"><span class="stat-label" title="' + PAVED_FOOTPRINT_TIP + '">Paved Footprint (' + areaLargeUnit() + ')</span><span class="stat-value">' + fmtNum1(toAreaLarge(meta.total_paved || 0)) + '</span></div>' +
                    '<div class="stat-row"><span class="stat-label">% Paved</span><span class="stat-value">' + ((meta.paved_share || 0) * 100).toFixed(1) + '%</span></div>' +
                    (missing.length > 0 ? '<div class="stat-row stat-note">Excludes ' + esc(missing.join(', ')) + ' (no data)</div>' : '') +
                    (partial.length > 0 ? '<div class="stat-row stat-note">Partial: ' + esc(partial.join(', ')) + ' (a source failed)</div>' : '') +
                    '</div>';
            }
            missing.forEach(rt => {
                // An empty latest compute (a settings change before the
                // recompute) has every layer missing: it is not computed,
                // not un-ingested.
                // A layer every source answered empty for is mapped by none
                // of them, not un-ingested.
                const uncomputed = meta.latest_compute && (meta.stats || []).length === 0;
                const why = failures[rt] ? 'the last ingest from ' + sourceNames(failures[rt]) + ' failed'
                    : empty[rt] ? 'none mapped in ' + sourceNames(empty[rt])
                    : uncomputed ? 'not computed under the current settings' : 'not ingested for this city';
                statsHTML +=
                    '<div class="stat-card missing-card"><h3>' + esc(rt) + '</h3>' +
                    '<div class="stat-row"><span class="stat-label">Data unavailable: ' + esc(why) + '</span></div>' +
                    '</div>';
            });
            (meta.stats || []).filter(s => !missing.includes(s.resource_type)).forEach(s => {
                const areaLargeVal = toAreaLarge(s.area);
                statsHTML +=
                    '<div class="stat-card"><h3>' + s.resource_type + '</h3>' +
                    '<div class="stat-row"><span class="stat-label">Area (' + areaLargeUnit() + ')</span><span class="stat-value">' + fmtNum1(areaLargeVal) + '</span></div>' +
                    '<div class="stat-row"><span class="stat-label">Area (' + areaVeryLargeUnit() + ')</span><span class="stat-value">' + toAreaVeryLarge(s.area).toFixed(2) + '</span></div>' +
                    // Parking only: a unitless count, so no units conversion.
                    (s.estimated_spaces > 0 ? '<div class="stat-row"><span class="stat-label">Est. Spaces</span><span class="stat-value">' + s.estimated_spaces.toLocaleString('en-US') + '</span></div>' : '') +
                    (partial.includes(s.resource_type) ? '<div class="stat-row stat-note">Partial: no ' + esc(sourceNames(failures[s.resource_type])) + ' data (its last ingest failed)</div>' : '') +
                    '</div>';
            });
            // One card per per-hex metric (meta.metrics, summarised over the
            // painted hexes so it describes what the map shows).
            Object.keys(meta.metrics || {}).sort().forEach(key => {
                const s = meta.metrics[key];
                statsHTML +=
                    '<div class="stat-card metric-card"><h3>' + esc(s.label) + '</h3>' +
                    // Additive counts lead with the city total; a time,
                    // rate or density has no meaningful sum.
                    (s.total != null ? '<div class="stat-row"><span class="stat-label">Total</span><span class="stat-value">' + fmtMetric(s.total, s.format) + '</span></div>' : '') +
                    (s.threshold_s > 0 ? '<div class="stat-row"><span class="stat-label">Within ' + fmtMetric(s.threshold_s, s.format) + '</span><span class="stat-value">' + ((s.within_threshold_share || 0) * 100).toFixed(1) + '%</span></div>' : '') +
                    // Hexes on the road network no point of service reaches:
                    // left blank on the map (they have no time), but counted
                    // against the share above.
                    (s.unreachable_count > 0 ? '<div class="stat-row"><span class="stat-label">Unreachable hexes</span><span class="stat-value">' + s.unreachable_count.toLocaleString('en-US') + '</span></div>' : '') +
                    // The median and range are over the painted hexes, not
                    // the city as a whole: say so.
                    '<div class="stat-row"><span class="stat-label">Median per hex</span><span class="stat-value">' + fmtMetric(s.median, s.format) + '</span></div>' +
                    // Compact, unit once: "0 – 24K / sq mi", not the unit twice.
                    '<div class="stat-row"><span class="stat-label">Range per hex</span><span class="stat-value">' + fmtLegend(s.min, s.format, false) + ' – ' + fmtLegend(s.max, s.format, true) + '</span></div>' +
                    pointsRows(s) +
                    '</div>';
            });
            statsHTML += ratioTotalsCard();
            return statsHTML;
        }

        // pointsRows is a metric card's points-of-service rows: how many
        // points (fire: stations) are some hex's nearest, how many of those
        // are outside the city, and the exporter's caveat when the list looks
        // incomplete (meta.metrics[].points_caveat).
        /** @param {any} s @returns {string} */
        function pointsRows(s) {
            let html = '';
            if (s.point_count > 0) {
                const outside = s.outside_point_count > 0 ? ' (' + s.outside_point_count + ' outside the city)' : '';
                html += '<div class="stat-row"><span class="stat-label" title="Nearest to at least one hex, inside the city boundary or across its line">' +
                    esc(s.point_label || 'Points of service') + '</span><span class="stat-value">' + s.point_count + outside + '</span></div>';
            }
            if (s.points_caveat) html += '<div class="stat-row stat-note points-caveat">' + esc(s.points_caveat) + '</div>';
            return html;
        }

        // refreshCityStats re-renders the stats panel once hex data arrives or
        // the resource toggles, scope or units change (the per-resident totals
        // depend on the first three, every number on the last). A detail
        // panel the user opened stays open and is re-rendered too; the
        // refreshed summary is what its close button restores.
        function refreshCityStats() {
            const statsEl = document.getElementById('stats');
            if (!statsEl || !lastCityMeta) return;
            if (!renderOpenDetail) {
                renderCityStats(lastCityMeta);
                return;
            }
            savedPanelHTML = cityStatsHTML(lastCityMeta);
            renderOpenDetail();
        }

        // openDetail shows a detail panel in #stats, keeping the summary its
        // close button restores. render redraws the panel from live state and
        // is kept so refreshCityStats can redraw it after a toggle.
        function openDetail(render) {
            const statsEl = document.getElementById('stats');
            if (!statsEl) return;
            if (!renderOpenDetail) savedPanelHTML = statsEl.innerHTML;
            renderOpenDetail = render;
            render();
        }

        // One delegated listener for every detail panel's close button: the
        // panels are rebuilt as innerHTML on each refresh, so a listener bound
        // to the button itself would die with the first rebuild.
        {
            const statsEl = document.getElementById('stats');
            if (statsEl) statsEl.addEventListener('click', e => {
                if (!(e.target instanceof Element) || !e.target.closest('.close-btn')) return;
                renderOpenDetail = null;
                statsEl.innerHTML = savedPanelHTML;
            });
        }

        // blendedTotal sums a derivation property over the city's toggled
        // resources in the current scope: the city-wide numerator of a
        // per-resident ratio. year1_need totals each resource's year-1 cost
        // (the per-hex estimate splits exactly that by area share).
        function blendedTotal(prop) {
            const byType = hexDataByScope[currentScope] || {};
            const cs = costSummaryByScope[currentScope] || {};
            let total = 0;
            for (const t of activeTypes) {
                if (prop === 'year1_need') {
                    if (cs[t] && cs[t].area > 0) total += cs[t].year1_need || 0;
                    continue;
                }
                const field = DERIVE_PROPS[prop];
                for (const f of byType[t] || []) total += f.properties[field] || 0;
            }
            return total;
        }

        // ratioTotalsCard is the "Per resident" stats card: each available
        // ratio metric as a city-wide Σ numerator / Σ denominator — not the
        // median of per-hex ratios, which over-weights sparse hexes.
        function ratioTotalsCard() {
            let rows = '';
            for (const key of availableMetricKeys()) {
                const m = lookupMetric(key);
                if (!m || !m.derive || m.derive.op !== 'ratio') continue;
                const den = hexMetrics[m.derive.den || ''];
                let d = 0;
                for (const v of Object.values((den && den.values) || {})) d += v;
                if (d <= 0) continue;
                rows += '<div class="stat-row"><span class="stat-label">' + esc(m.label) + '</span><span class="stat-value">' +
                    fmtMetric(blendedTotal(m.derive.prop) / d, m.format) + '</span></div>';
            }
            return rows ? '<div class="stat-card metric-card ratio-card"><h3>Per resident (city)</h3>' + rows + '</div>' : '';
        }

        async function loadCityData() {
            const gen = ++cityLoadGen; // bail stale loads after each await
            const prefix = DATA_PREFIX; // snapshot to avoid mid-await mutation
            // Remove existing layers and sources
            ['hex-outline', 'hex-fill', 'boundary-line', 'metric-points'].concat(Object.keys(layerColors).map(t => t + '-fill')).forEach(id => {
                if (map.getLayer(id)) map.removeLayer(id);
            });
            ['hexgrid', 'boundary', 'metric-points'].concat(Object.keys(layerColors)).forEach(id => {
                if (map.getSource(id)) map.removeSource(id);
            });
            hexDataByScope = { city: {}, all: {} };
            costSummaryByScope = { city: {}, all: {} };
            activeTypes = new Set(Object.keys(layerColors));
            hexMetrics = {};
            hexGeomById = {};

            // Reload meta.json for stats panel
            {
                const meta = await loadJSON(dataURL(prefix, 'meta.json'));
                if (gen !== cityLoadGen) return; // a newer load superseded us
                // Without its meta.json, nothing of the previous city's date,
                // badge, stats or settings may stay on screen.
                const subtitleEl = document.querySelector('#panel .subtitle');
                if (subtitleEl) {
                    subtitleEl.textContent = 'Pavement & Parking Analysis' +
                        (meta && meta.snapshot_date ? ' | Last computed ' + meta.snapshot_date : '');
                }
                // pvmt serve shows a city with no snapshot, or
                // ?snapshot=latest, from its latest compute: say so.
                const badgeEl = selectById('latest-compute-badge');
                if (badgeEl) badgeEl.hidden = !(meta && meta.latest_compute);
                if (meta) {
                    setCityHeading(meta.name);
                    lastCityMeta = meta;
                    renderCityStats(meta);
                    renderCitySettings(meta);
                } else {
                    lastCityMeta = null;
                    const statsEl = document.getElementById('stats');
                    if (statsEl) statsEl.innerHTML = '';
                    savedPanelHTML = '';
                    renderOpenDetail = null;
                    renderCitySettings({ settings_toml: 'This city\'s settings could not be loaded.' });
                }
                // The headline dates its insolvency year from snapshot_date,
                // and the Financials load can finish first.
                renderSolvencyHeadline();
            }

            // Stored per-hex metrics (optional file: absent until a producer such
            // as `pvmt services compute` has run for the city). An unknown
            // version is refused with the banner, as hexgrid's is, but the map
            // still draws: only the stored Color-by options are lost. The
            // fallback for a requested metric the city lacks runs once the hex
            // grid is in (see metricAvailable).
            {
                const hm = await loadJSON(dataURL(prefix, 'hex-metrics.json'), { silent404: true });
                if (gen !== cityLoadGen) return;
                if (hm && hm.v !== PVMT_CONFIG.hex_metrics_version) {
                    showError(dataURL(prefix, 'hex-metrics.json'),
                        'unsupported hex-metrics format (expected v' + PVMT_CONFIG.hex_metrics_version +
                        ') — re-export this site with a current pvmt');
                } else if (hm && hm.metrics) hexMetrics = hm.metrics;
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
                    const frame = framingBounds(data);
                    if (frame) map.fitBounds(frame, { padding: mapFitPadding() });
                }
            }

            let toggles = '';

            // Resource toggles — filter the blended hex grid by type. There is
            // no per-resource fill layer; empty data-layers skips the
            // visibility-setter while data-type still drives activeTypes and
            // updateHexGrid().
            for (const type of Object.keys(layerColors)) {
                toggles += '<label><input type="checkbox" checked data-layers="" data-type="' + type + '"><span class="legend-dot" style="background:' + layerColors[type] + '"></span>' + type + '</label>';
            }

            // Load hex cost summary: map[scope]map[resource]{...}, scope in
            // {city, all}. "city" is omitted when the city has no :city rows.
            // A city exported without one draws its map without the cost
            // colorings, so its absence is not reported.
            {
                const cs = await loadJSON(dataURL(prefix, 'hex-cost-summary.json'), { silent404: true });
                if (gen !== cityLoadGen) return; // a newer load superseded us
                if (cs) {
                    costSummaryByScope.city = cs.city || {};
                    costSummaryByScope.all = cs.all || {};
                }
            }

            // Load the hex grid — a single multi-scope file, one feature per
            // hex with nested {all, city?} coverage keyed by resource. Expand
            // it back into the per-scope/per-resource feature arrays the render
            // path consumes.
            //
            // A feature carries city-scope data in one of two shapes:
            //   - a "city" object, when it differs from "all";
            //   - "city_same": 1, when the two were byte-identical and the
            //     writer emitted the flag instead of a duplicate copy.
            // Only a feature with NEITHER has no city-scope data. That is the
            // condition that leaves hexDataByScope.city empty and keeps the
            // scope toggle hidden.
            //
            // Reading city_same is not optional once the writer emits it: about
            // 70% of features site-wide have identical scopes, so treating the
            // flag as "no city data" would silently drop them from the city
            // heatmap and every cost rollup built off it. The map would still
            // render — just under-reporting.
            const hexData = await loadJSON(dataURL(prefix, 'hexgrid.geojson'), { silent404: true });
            if (gen !== cityLoadGen) return; // a newer load superseded us
            // Refuse a hexgrid whose property shape we don't know. `hexData` is
            // legitimately null for a city with no hex stats (the export skips
            // the file and loadJSON returns null on a silent 404), so the null
            // check comes first — this must not fire on that.
            //
            // The failure this prevents is silent, not loud: misreading a
            // changed property shape (e.g. not expanding city_same) drops ~70%
            // of city-scope features with no error anywhere. Better to show the
            // banner and render nothing than a plausible wrong map.
            // The version is the exporter's (export.HexGridFileVersion),
            // injected so the two cannot drift.
            if (hexData && hexData.v !== PVMT_CONFIG.hexgrid_version) {
                showError(dataURL(prefix, 'hexgrid.geojson'),
                    'unsupported hexgrid format (expected v' + PVMT_CONFIG.hexgrid_version +
                    ') — re-export this site with a current pvmt');
                return;
            }
            if (hexData && hexData.features) {
                for (const f of hexData.features) {
                    const hid = f.properties.id;
                    hexGeomById[hid] = f.geometry;
                    for (const scope of ['all', 'city']) {
                        const byRes = (scope === 'city' && f.properties.city_same)
                            ? f.properties.all
                            : f.properties[scope];
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
                                    share: m.share,
                                    // Estimated parking spaces apportioned to
                                    // the hex; absent (0) for other resources.
                                    spaces: m.spaces || 0,
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
            const allEmpty = Object.keys(hexDataByScope.all).length === 0;
            if (currentScope === 'city' && cityEmpty && !allEmpty) {
                currentScope = 'all';
                syncScopeToggle();
            } else if (currentScope === 'all' && allEmpty && !cityEmpty) {
                currentScope = 'city';
                syncScopeToggle();
            }
            maybeRevealScopeToggle();

            // Every metric's inputs are in now (stored metrics, hex data, cost
            // summary). If the requested metric is unavailable for this city,
            // fall back to the default and rewrite the address bar so it no
            // longer claims it.
            if (!metricAvailable(hexMetric)) {
                hexMetric = DEFAULT_METRIC;
                Router.replace();
            }
            populateMetricSelect();
            renderLegend();
            refreshCityStats();

            {
                const haveAnyHex = Object.keys(hexDataByScope.city).length > 0 ||
                                    Object.keys(hexDataByScope.all).length > 0;
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
                        updateMetricPoints();
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
        // module state (hexDataByScope/costSummaryByScope/currentScope/activeTypes),
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
                        '<strong>Coverage:</strong> ' + (p.share != null ? Math.round(p.share * 100) : 0) + '%<br>' +
                        '<strong>Area:</strong> ' + fmtArea(p.area || 0) + '<br>' +
                        '<strong>Est. year-1 cost:</strong> ' + fmtMoney(p.year1_need || 0) +
                        (p.spaces > 0 ? '<br><strong>Est. Parking Spaces:</strong> ' + Math.round(p.spaces).toLocaleString('en-US') : '') +
                        metricTooltipLine(p);
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
                const hexId = e.features[0].properties.hex_id;
                openDetail(() => renderHexDetail(hexId));
            });
        }

        // renderHexDetail shows one hex's per-resource breakdown in #stats,
        // over the toggled resources in the active scope, so the panel matches
        // what the heatmap paints for that hex.
        function renderHexDetail(hexId) {
            const statsEl = document.getElementById('stats');
            if (!statsEl) return;
            const hexDataByType = hexDataByScope[currentScope] || {};
            const costSummary = costSummaryByScope[currentScope] || {};
            let rows = '';
            let totalArea = 0, totalPct = 0, totalCost = 0, totalSpaces = 0;
            for (const type of Object.keys(layerColors)) {
                if (!activeTypes.has(type)) continue;
                const features = hexDataByType[type] || [];
                const match = features.find(f => f.properties.hex_id === hexId);
                if (!match) continue;
                const area = match.properties.area || 0;
                const pct = (match.properties.share || 0) * 100;
                totalSpaces += match.properties.spaces || 0;
                let cost = 0;
                const cs = costSummary[type];
                if (cs && cs.area) {
                    cost = (area / cs.area) * cs.year1_need;
                }
                totalArea += area;
                totalPct += pct;
                totalCost += cost;
                rows += '<tr><td style="text-transform:capitalize">' + type + '</td><td>' + fmtArea(area) + '</td><td>' + Math.round(pct) + '%</td><td>' + fmtMoney(cost) + '</td></tr>';
            }

            statsEl.innerHTML =
                '<div class="hex-detail">' +
                '<h3>Hex ' + hexId + '<button class="close-btn" id="close-hex" type="button" aria-label="Close hex details">&times;</button></h3>' +
                '<table><tr><th>Type</th><th>Area</th><th>Coverage</th><th>Est. Cost</th></tr>' +
                rows +
                '<tr class="total-row"><td>Total</td><td>' + fmtArea(totalArea) + '</td><td>' + Math.min(Math.round(totalPct), 100) + '%</td><td>' + fmtMoney(totalCost) + '</td></tr>' +
                '</table>' +
                (totalSpaces > 0 ? '<div class="stat-row"><span class="stat-label">Est. Parking Spaces</span><span class="stat-value">' + Math.round(totalSpaces).toLocaleString('en-US') + '</span></div>' : '') +
                '</div>';
        }
        registerHexHandlers();

        // Heatmap colors are read from CSS vars (which differ per theme), and
        // the legend bar is built from the same colours and edges, so the map
        // and its legend always agree. Rebuilt on theme toggle. Every metric
        // paints its value (metric_v) through its registry scale: Kind picks
        // the colour family (coverage → --legend-*, sequential →
        // --metric-seq-*, diverging → --metric-div-*); a "ramp" interpolates
        // between its edges, "bands" (the default) paints one discrete colour
        // per band. A hex with no value gets --metric-none (a bare step or
        // interpolate on a missing property would be an expression error).
        function cssVar(name) {
            return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
        }
        /** @param {{kind?: string}} scale */
        function scaleFamily(scale) {
            const kind = scale && scale.kind;
            if (kind === 'coverage') return '--legend-';
            if (kind === 'diverging') return '--metric-div-';
            return '--metric-seq-';
        }
        // scaleColors spreads n colours over the family's 6 stops.
        /** @param {{kind?: string}} scale */
        function scaleColors(scale, n) {
            const family = scaleFamily(scale);
            const colors = [];
            for (let i = 0; i < n; i++) {
                const stop = n === 1 ? 6 : 1 + Math.round(i * 5 / (n - 1));
                colors.push(cssVar(family + stop));
            }
            return colors;
        }
        /** @param {{mode?: string}} scale */
        function isRamp(scale) {
            return !!scale && scale.mode === 'ramp';
        }
        /** @param {{kind?: string, bands?: number[] | null, mode?: string}} scale */
        function metricBandColors(scale) {
            const bands = (scale && scale.bands) || [];
            // A ramp has one colour per stop; bands one per band (edges + 1).
            return scaleColors(scale, isRamp(scale) ? bands.length : bands.length + 1);
        }
        // lookupMetric resolves a key against the derived specs and this
        // city's stored metrics. Own-property lookup, so a shared link like
        // ?metric=constructor cannot resolve to a prototype member.
        /** @returns {PvmtHexMetric | null} */
        function lookupMetric(key) {
            if (Object.hasOwn(derivedMetrics, key)) return derivedMetrics[key];
            if (Object.hasOwn(hexMetrics, key)) return hexMetrics[key];
            return null;
        }
        // DERIVE_PROPS maps a derivation's per-resource property onto the
        // blended hex record buildBlendedHexFC accumulates.
        const DERIVE_PROPS = { share: 'share', area: 'area', spaces: 'spaces', year1_need: 'year1_need' };
        // derivedInputPresent reports whether this city has data for a derived
        // metric's per-resource property: a coloring with nothing to show (no
        // parking spaces, no forecast cost) is not offered rather than
        // painting every hex at zero.
        function derivedInputPresent(prop) {
            const byType = hexDataByScope[currentScope] || {};
            const types = [...activeTypes].filter(t => (byType[t] || []).length > 0);
            if (prop === 'year1_need') {
                const cs = costSummaryByScope[currentScope] || {};
                return types.some(t => cs[t] && cs[t].area > 0);
            }
            if (prop === 'spaces') return types.some(t => byType[t].some(f => f.properties.spaces > 0));
            return types.length > 0;
        }
        // metricAvailable: a stored metric is available when this city has
        // it; a derived sum when its input is present; a ratio when its
        // numerator input and its stored denominator both are.
        function metricAvailable(key) {
            const m = lookupMetric(key);
            if (!m) return false;
            if (!m.derive) return true;
            if (!derivedInputPresent(m.derive.prop)) return false;
            return m.derive.op !== 'ratio' || Object.hasOwn(hexMetrics, m.derive.den || '');
        }
        // activeMetric returns the selected metric's description, or null
        // when it is unavailable (the city lacks it or its inputs).
        /** @returns {PvmtHexMetric | null} */
        function activeMetric() {
            return metricAvailable(hexMetric) ? lookupMetric(hexMetric) : null;
        }
        // shownMetric is the metric the paint and legend describe: the active
        // one, or the default for a city with nothing to colour yet (no hex
        // data), so the legend still explains the map's default meaning.
        /** @returns {PvmtHexMetric | null} */
        function shownMetric() {
            return activeMetric() || lookupMetric(DEFAULT_METRIC);
        }
        // metricExtraHexes lists the hexes a metric covers beyond those with
        // toggled pavement rows: a stored metric's own hexes, or a ratio's
        // denominator hexes. A derived sum covers only pavement hexes.
        /** @param {PvmtHexMetric} m */
        function metricExtraHexes(m) {
            if (!m.derive) return Object.keys(m.values || {});
            if (m.derive.op === 'ratio') {
                const den = hexMetrics[m.derive.den || ''];
                return den ? Object.keys(den.values || {}) : [];
            }
            return [];
        }
        // metricValue resolves the metric's value for one hex from the blended
        // record (derived) or the stored values; null means "no value".
        /** @param {PvmtHexMetric} m */
        function metricValue(m, hid, data) {
            if (!m.derive) {
                const v = m.values ? m.values[hid] : undefined;
                return v == null ? null : v;
            }
            const num = data[DERIVE_PROPS[m.derive.prop]] || 0;
            if (m.derive.op === 'sum') {
                return m.derive.max ? Math.min(num, m.derive.max) : num;
            }
            const den = hexMetrics[m.derive.den || ''];
            const d = den && den.values ? den.values[hid] : undefined;
            // Below min_den (or a non-positive denominator) the ratio is noise.
            if (d == null || d <= 0 || d < (m.derive.min_den || 0)) return null;
            return num / d;
        }
        function hexFillColor() {
            const metric = shownMetric();
            if (!metric) return cssVar('--metric-none');
            const bands = (metric.scale && metric.scale.bands) || [];
            const colors = metricBandColors(metric.scale);
            /** @type {any[] | string} */
            let expr;
            if (isRamp(metric.scale) && bands.length >= 2) {
                /** @type {any[]} */
                const ramp = ['interpolate', ['linear'], ['get', 'metric_v']];
                bands.forEach((edge, i) => { ramp.push(edge, colors[i]); });
                expr = ramp;
            } else if (bands.length === 0) {
                // The style spec's `step` needs at least one stop; a bandless
                // scale (rejected by the registry, but the file is data) is
                // one flat colour.
                expr = colors[0];
            } else {
                /** @type {any[]} */
                const step = ['step', ['get', 'metric_v'], colors[0]];
                bands.forEach((edge, i) => { step.push(edge, colors[i + 1]); });
                expr = step;
            }
            return ['case', ['has', 'metric_v'], expr, cssVar('--metric-none')];
        }
        document.addEventListener('pvmt:themechange', () => {
            if (map.getLayer('hex-fill')) {
                map.setPaintProperty('hex-fill', 'fill-color', hexFillColor());
            }
            if (map.getLayer('metric-points')) {
                map.setPaintProperty('metric-points', 'circle-color', metricPointColor());
            }
            renderLegend();
        });

        // fmtMetric renders a per-hex metric value per its registry format
        // (hexmetric.Format*): minutes shows m:ss from seconds, area follows
        // the viewer's unit system.
        function fmtMetric(v, format) {
            if (v == null || !Number.isFinite(v)) return '—';
            switch (format) {
                case 'minutes': {
                    // Round to whole seconds FIRST, or 359.7 renders as "5:60".
                    const t = Math.round(v), m = Math.floor(t / 60), s = t % 60;
                    return m + ':' + (s < 10 ? '0' : '') + s;
                }
                case 'float1': return v.toFixed(1);
                case 'usd': return fmtMoney(v);
                case 'percent': return Math.round(v * 100) + '%';
                case 'area': return fmtArea(v);
                case 'density': return Math.round(densityInUnit(v)).toLocaleString('en-US') + ' / ' + areaVeryLargeUnit();
                default: return Math.round(v).toLocaleString('en-US');
            }
        }

        // densityInUnit converts a per-m² density to per the viewer's large
        // area unit (km², or sq mi in imperial).
        function densityInUnit(perSqM) {
            return perSqM / toAreaVeryLarge(1);
        }

        // compactNum renders a legend edge in at most ~3 characters of digits:
        // 500, 5.4K, 22K, 1.2M. Band edges are fixed metric values, so after
        // a unit conversion they are rarely round; full precision
        // ("5,382 sq ft") made legends wider than the map.
        function compactNum(v) {
            const a = Math.abs(v);
            const two = x => (Math.abs(x) < 10 ? String(Math.round(x * 10) / 10) : String(Math.round(x)));
            if (a >= 1e6) return two(v / 1e6) + 'M';
            if (a >= 1e3) return two(v / 1e3) + 'K';
            return two(v);
        }

        // fmtLegend renders a legend edge: compact counts and areas (area in
        // the viewer's unit, the unit itself only where withUnit asks so a row
        // of ranges names it once), everything else as fmtMetric does.
        function fmtLegend(v, format, withUnit) {
            if (format === 'area') {
                return compactNum(toAreaBase(v)) + (withUnit ? ' ' + areaUnit() : '');
            }
            if (format === 'density') {
                return compactNum(densityInUnit(v)) + (withUnit ? ' / ' + areaVeryLargeUnit() : '');
            }
            // A dollar edge is compacted like the count and area edges, so a
            // row of ranges stays narrow ("$2.5K–$5K").
            if (format === 'usd') return '$' + compactNum(v);
            // A count reads in whole units: an apportioned 6.3 housing
            // units is "6", not "6.3"; large counts still compact ("1.2K").
            if (format === 'int') return Math.abs(v) < 1000 ? Math.round(v).toLocaleString('en-US') : compactNum(v);
            return fmtMetric(v, format);
        }

        // metricTooltipLine is the tooltip's extra line for the active metric.
        // A derived sum is already one of the tooltip's pavement lines
        // (coverage, area, cost, spaces), so it adds nothing.
        function metricTooltipLine(p) {
            const metric = activeMetric();
            if (!metric || p.metric_v == null) return '';
            if (metric.derive && metric.derive.op === 'sum') return '';
            return '<br><strong>' + esc(metric.label) + ':</strong> ' + fmtMetric(p.metric_v, metric.format) +
                (p.metric_ref ? ' · ' + esc(String(p.metric_ref)) : '');
        }

        // availableMetricKeys lists the selectable metrics: the default first,
        // then the other derived metrics in registry order, then the city's
        // stored metrics by key.
        function availableMetricKeys() {
            const keys = [DEFAULT_METRIC];
            for (const k of Object.keys(derivedMetrics)) if (k !== DEFAULT_METRIC) keys.push(k);
            for (const k of Object.keys(hexMetrics).sort()) if (!Object.hasOwn(derivedMetrics, k)) keys.push(k);
            return keys.filter(metricAvailable);
        }

        // populateMetricSelect lists every available metric. With a single
        // choice the selector hides and the legend title names that metric.
        function populateMetricSelect() {
            const sel = selectById('hex-metric');
            if (!sel) return;
            const keys = availableMetricKeys();
            sel.innerHTML = '';
            keys.forEach(key => {
                const o = document.createElement('option');
                o.value = key;
                const m = lookupMetric(key);
                o.textContent = (m && m.label) || key;
                sel.appendChild(o);
            });
            sel.value = hexMetric;
            sel.hidden = keys.length <= 1;
            const row = sel.closest('.legend-row');
            // One choice (or none yet: a city without hex data) titles the
            // legend with that metric, the default when there is none.
            const only = keys.length <= 1 ? lookupMetric(keys[0] || DEFAULT_METRIC) : null;
            if (row) row.querySelector('label').textContent = only ? only.label : 'Color by';
        }

        // renderLegend redraws the legend bar + labels for the active metric:
        // a ramp is a continuous gradient with its colour stops at their
        // values' positions and its ticks as evenly spaced labels; bands are
        // one hard-stop block per band, labelled with its range ("≤ 5:20",
        // "5:20–6:40", …, "> 11:20").
        function renderLegend() {
            const bar = queryEl('#legend .legend-bar');
            const labels = queryEl('#legend .legend-labels');
            if (!bar || !labels) return;
            const metric = shownMetric();
            if (!metric) {
                bar.classList.remove('discrete');
                bar.style.background = cssVar('--metric-none');
                labels.classList.remove('discrete', 'ticks');
                labels.style.gridTemplateColumns = '';
                labels.innerHTML = '';
                return;
            }
            const bands = (metric.scale && metric.scale.bands) || [];
            const colors = metricBandColors(metric.scale);
            const f = (v, withUnit) => fmtLegend(v, metric.format, withUnit);
            if (isRamp(metric.scale) && bands.length >= 2) {
                const lo = bands[0], span = bands[bands.length - 1] - lo;
                const stops = bands.map((b, i) => colors[i] + ' ' + ((b - lo) * 100 / span) + '%');
                bar.classList.remove('discrete');
                bar.style.background = 'linear-gradient(90deg, ' + stops.join(', ') + ')';
                const ticks = (metric.scale.ticks && metric.scale.ticks.length) ? metric.scale.ticks : bands;
                labels.classList.remove('discrete');
                labels.classList.add('ticks');
                labels.style.gridTemplateColumns = '';
                // Each tick sits at its value's position on the bar, with a
                // tick mark, so the top of the scale (100% coverage) is
                // labelled where the bar ends rather than past it.
                labels.innerHTML = ticks.map(t => {
                    const pos = Math.min(100, Math.max(0, (t - lo) * 100 / span));
                    return '<span style="left:' + pos + '%">' + esc(f(t, true)) + '</span>';
                }).join('');
                return;
            }
            const n = colors.length;
            const stops = colors.map((c, i) => c + ' ' + (i * 100 / n) + '% ' + ((i + 1) * 100 / n) + '%');
            bar.classList.add('discrete');
            bar.style.background = 'linear-gradient(90deg, ' + stops.join(', ') + ')';
            // One label per band, each centred under its colour block (a grid
            // with as many columns as bands), naming the band's range: the
            // first is "≤ edge", the last "> edge" (which also names the unit,
            // once for the row), the rest "lo–hi".
            const parts = colors.map((_, i) => {
                if (bands.length === 0) return 'all';
                if (i === 0) return '≤ ' + f(bands[0], false);
                if (i === bands.length) return '> ' + f(bands[bands.length - 1], true);
                return f(bands[i - 1], false) + '–' + f(bands[i], false);
            });
            labels.classList.remove('ticks');
            labels.classList.add('discrete');
            labels.style.gridTemplateColumns = 'repeat(' + n + ', 1fr)';
            labels.innerHTML = parts.map(t => '<span>' + esc(t) + '</span>').join('');
        }

        // updateMetricPoints shows the active metric's points of service
        // (stations) as circles, or removes the layer for a metric without.
        function metricPointColor() {
            return cssVar('--metric-seq-6') || '#b2182b';
        }
        function updateMetricPoints() {
            const metric = activeMetric();
            const points = metric && metric.points ? metric.points : [];
            if (map.getLayer('metric-points')) map.removeLayer('metric-points');
            if (map.getSource('metric-points')) map.removeSource('metric-points');
            if (points.length === 0) return;
            map.addSource('metric-points', { type: 'geojson', data: {
                type: 'FeatureCollection',
                features: points.map(pt => ({ type: 'Feature', geometry: { type: 'Point', coordinates: pt.position }, properties: { name: pt.name || pt.id, source: pt.source } })),
            } });
            map.addLayer({
                id: 'metric-points', type: 'circle', source: 'metric-points',
                paint: { 'circle-radius': 6, 'circle-color': metricPointColor(), 'circle-stroke-color': '#ffffff', 'circle-stroke-width': 2 },
            });
        }

        // selectHexMetric switches "Color by": repaints the hex layer from the
        // same blended source, redraws the legend and the points layer, and
        // records the choice in the URL (counterpart of selectCity/selectScope).
        function selectHexMetric(key, opts) {
            hexMetric = (key && metricAvailable(key)) ? key : DEFAULT_METRIC;
            populateMetricSelect();
            const src = map.getSource('hexgrid');
            if (src) src.setData(buildBlendedHexFC());
            if (map.getLayer('hex-fill')) map.setPaintProperty('hex-fill', 'fill-color', hexFillColor());
            renderLegend();
            updateMetricPoints();
            if (!opts || opts.push !== false) Router.push();
        }
        {
            const metricSel = selectById('hex-metric');
            if (metricSel) metricSel.addEventListener('change', () => selectHexMetric(metricSel.value));
        }

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

            // aggregateSelection opens the region panel for the selection bbox.
            // The bbox is kept, so a toggle re-aggregates the same region.
            function aggregateSelection(bbox) {
                openDetail(() => renderRegionDetail(bbox));
            }

            // renderRegionDetail aggregates the blended hex FC over bbox and
            // renders the result card into #stats. Empty selection shows
            // zeros, not an error.
            function renderRegionDetail(bbox) {
                const [minLng, minLat, maxLng, maxLat] = bbox;
                const fc = buildBlendedHexFC();
                let count = 0, sumArea = 0, sumCost = 0, sumShareArea = 0;
                for (const f of (fc.features || [])) {
                    const c = polyCentroid(f.geometry);
                    if (!c) continue;
                    if (c[0] < minLng || c[0] > maxLng || c[1] < minLat || c[1] > maxLat) continue;
                    const area = f.properties.area || 0;
                    // A stored metric adds pavement-less hexes to the map
                    // (metricExtraHexes); they hold nothing to total, so the
                    // count must not change with Color by.
                    if (area <= 0) continue;
                    count++;
                    sumArea += area;
                    sumCost += f.properties.year1_need || 0;
                    sumShareArea += (f.properties.share || 0) * area;
                }
                const wPct = sumArea > 0 ? (sumShareArea / sumArea) * 100 : 0;

                const statsEl = document.getElementById('stats');
                if (!statsEl) return;
                statsEl.innerHTML =
                    '<div class="region-detail">' +
                    '<div class="stat-card summary-card">' +
                    '<h3>Selected Region<button class="close-btn" id="close-region" type="button" aria-label="Close region selection">&times;</button></h3>' +
                    '<div class="stat-row"><span class="stat-label">Hexes selected</span><span class="stat-value">' + fmtNum(count) + '</span></div>' +
                    '<div class="stat-row"><span class="stat-label">Total Area (' + areaLargeUnit() + ')</span><span class="stat-value">' + fmtNum1(toAreaLarge(sumArea)) + '</span></div>' +
                    '<div class="stat-row"><span class="stat-label">% Covered (area-weighted)</span><span class="stat-value">' + wPct.toFixed(1) + '%</span></div>' +
                    '<div class="stat-row"><span class="stat-label">Est. year-1 cost</span><span class="stat-value">' + fmtMoney(sumCost) + '</span></div>' +
                    '</div></div>';
            }
        })();

        // Start the city's data once the style is in: adding sources and
        // layers needs nothing more. Not on 'load', which also waits for every
        // basemap tile and fires only from inside a render. A tile that fails
        // with anything but a 404 (a blocked or rate-limited tile server, no
        // network) is marked errored without asking for another frame, so
        // when the last tiles fail the map is loaded() with no render left to
        // say so and 'load' never fires, which would leave the dashboard
        // blank. 'load' stays as a fallback; the flag keeps
        // the two from loading the city twice.
        let mapDataStarted = false;
        function startMapData() {
            if (mapDataStarted) return;
            mapDataStarted = true;
            loadCityData();
        }
        map.once('style.load', startMapData);
        map.once('load', startMapData);

        // setCityHeading names the shown city in the panel heading and the
        // browser tab, so tabs, bookmarks, history and link previews follow
        // the selected city rather than the first one in the config.
        function setCityHeading(name) {
            if (!name) return;
            document.querySelector('#panel h2').textContent = name;
            document.title = 'Solvent Streets \u2014 ' + name;
        }

        // City switcher
        function selectCity(slug, opts) {
            const sel = selectById('city-select');
            if (!sel) return;
            if (sel.value !== slug) sel.value = slug;
            const opt = sel.selectedOptions[0];
            if (!opt) return;
            DATA_PREFIX = 'cities/' + slug + '/';
            // A budget belongs to one city: drop it before Router.push below
            // writes the URL, so the new city's entry doesn't carry it.
            resetBudget(null);
            // So do the model settings: the new city starts from its own seed.
            pendingModel = parseModel({});
            setStrategy(DEFAULT_STRATEGY);
            const bboxParts = opt.dataset.bbox.split(',').map(Number);
            // data-bbox is [west, south, east, north], fitBounds' own order.
            map.fitBounds([[bboxParts[0], bboxParts[1]], [bboxParts[2], bboxParts[3]]], { padding: mapFitPadding() });
            setCityHeading(opt.textContent.trim());
            // Each city has its own snapshot list. Reset the picker on city
            // change so a snapshot the new city lacks is never requested.
            const snapEl = selectById('snapshot-picker');
            if (snapEl) snapEl.value = '';
            loadSnapshots(slug);
            loadCityData();
            loadFinancials();
            markCurrentCity();
            reloadMaterialsIfActive();
            if (!opts || opts.push !== false) Router.push();
        }
        const citySelect = selectById('city-select');
        if (citySelect) {
            citySelect.addEventListener('change', () => selectCity(citySelect.value));
        }

        // In-page links in the About prose ([...](#how-spending-moves-condition))
        // scroll the tab to their heading here rather than as a fragment
        // navigation. That would add a history entry and fire popstate, and the
        // state popstate reads back from an About-tab URL carries none of the
        // Financials settings (only the Financials tab writes them), so
        // following a link would reset the reader's budget and model.
        const aboutTab = document.getElementById('about-tab');
        if (aboutTab) {
            aboutTab.addEventListener('click', (e) => {
                const a = e.target instanceof Element ? e.target.closest('a[href^="#"]') : null;
                if (!a) return;
                const id = decodeURIComponent((a.getAttribute('href') || '').slice(1));
                const target = id ? document.getElementById(id) : null;
                if (!target || !aboutTab.contains(target)) return;
                e.preventDefault();
                target.scrollIntoView({ block: 'start' });
            });
        }

        queryAll('select.tag-filter').forEach(el => {
            el.addEventListener('change', () => onTagFilterChange());
        });

        // Snapshot picker (live server only). loadSnapshots fetches the list
        // for the current city and populates options; selectSnapshot is the
        // counterpart of selectCity / selectTab / selectScope.
        //
        // The default option ("") is what the server pinned: the newest
        // snapshot, or for a city with none its latest compute. "latest"
        // (?snapshot=latest) is the latest compute, snapshotted or not.
        const LATEST_COMPUTE_LABEL = 'Latest compute (not snapshotted)';
        // Bumped per call, so a list arriving for a city already left is
        // dropped instead of replacing the new city's.
        let snapshotListGen = 0;
        /** @param {string} slug @param {string|null} [want] snapshot to select once the list lands */
        async function loadSnapshots(slug, want) {
            const sel = selectById('snapshot-picker');
            if (!sel) return; // static export
            const gen = ++snapshotListGen;
            const url = slug ? 'api/cities/' + slug + '/snapshots' : 'api/snapshots';
            const list = await loadJSON(url);
            if (gen !== snapshotListGen) return;
            // Without an explicit want, the URL names the snapshot: a
            // back/forward entry's, since popstate's selectCity passes none.
            if (want == null) want = Router.read().snapshot;
            if (!list || list.length === 0) {
                sel.innerHTML = '<option value="">' + LATEST_COMPUTE_LABEL + '</option>';
                return;
            }
            const fmt = (ts) => {
                try { return new Date(ts).toISOString().slice(0, 16).replace('T', ' '); }
                catch (e) { return ts; }
            };
            // Snapshots are named (`pvmt snapshots create <name>`); names are
            // [A-Za-z0-9._-] only, so they need no escaping in markup or URLs.
            // The default option is the snapshot the server pinned (`served`):
            // the newest, unless `pvmt serve --snapshot` named another.
            const served = list.find(s => s.served) || list[0];
            const label = served === list[0] ? 'Newest' : 'Served';
            sel.innerHTML = '<option value="">' + label + ' (' + served.name + ', ' + fmt(served.created_at).slice(0, 10) + ')</option>' +
                '<option value="latest">' + LATEST_COMPUTE_LABEL + '</option>' +
                list.map(s => '<option value="' + s.name + '" title="' + (s.config_hash || '').slice(0, 12) + '">' + s.name + ' ' + fmt(s.created_at) + '</option>').join('');
            if (want && (String(want) === 'latest' || list.some(s => s.name === String(want)))) {
                const prev = sel.value;
                sel.value = String(want);
                // Boot's initial loadCityData/loadFinancials ran with the picker
                // empty, so reload once for the deep-linked snapshot and put it
                // back in the URL the boot's replace wrote without it.
                if (sel.value !== prev) {
                    loadCityData();
                    loadFinancials();
                    reloadMaterialsIfActive();
                    Router.replace();
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
            loadSnapshots(initSlug, bootSnapshot);
        }

        // Financials
        const COLORS = ['#ef4444', '#f59e0b', '#22c55e', '#3b82f6', '#8b5cf6', '#ec4899'];

        // Shared forecast helpers, used by the Financials, Materials, and
        // interactive-forecast views.
        //
        // Cost tiers. The city's schedule is FORECAST_SEED.cost_tiers
        // ({min_pci, max_pci, cost_per_sqm, label}; the defaults are
        // preventive / rehab / reconstruction, and a config can set its own
        // bands and labels). Everything the page derives from a tier goes
        // through the helpers below: which tier a PCI falls in, a tier's
        // colour, the chart legend, and the cohort table's PCI badge.
        //
        // rankedCostTiers is the schedule from the best condition (highest
        // PCI, cheapest work) to the worst.
        function rankedCostTiers() {
            const tiers = (FORECAST_SEED && FORECAST_SEED.cost_tiers) || [];
            return tiers.slice().sort((a, b) => b.min_pci - a.min_pci);
        }
        // tierForPCI is the tier whose band [min_pci, max_pci) holds pci. A
        // schedule may leave gaps, so a PCI in none takes the nearest band (as
        // Go's TieredCostProjector.TierForPCI does). null with no schedule.
        function tierForPCI(pci) {
            let best = null, bestDist = Infinity;
            for (const t of rankedCostTiers()) {
                if (pci >= t.min_pci && pci < t.max_pci) return t;
                const d = pci < t.min_pci ? t.min_pci - pci : pci - t.max_pci;
                if (d < bestDist) { bestDist = d; best = t; }
            }
            return best;
        }
        // Tier colours run green (best) to red (worst) by rank, through amber:
        // three tiers get exactly the three stops.
        const TIER_RAMP = [[0x22, 0xc5, 0x5e], [0xf5, 0x9e, 0x0b], [0xef, 0x44, 0x44]];
        const TIER_COLOR_UNKNOWN = '#9ca3af';
        /** @param {number} rank @param {number} count */
        function tierRampColor(rank, count) {
            const pos = count > 1 ? rank / (count - 1) * (TIER_RAMP.length - 1) : 0;
            const i = Math.min(Math.floor(pos), TIER_RAMP.length - 2), f = pos - i;
            return '#' + TIER_RAMP[i].map((c, k) =>
                Math.round(c + (TIER_RAMP[i + 1][k] - c) * f).toString(16).padStart(2, '0')).join('');
        }
        // tierColor is the colour of the tier with this label. Material tiers
        // carry the default labels whatever the city's cost schedule, so a
        // label the schedule lacks takes its default colour; any other is grey.
        const DEFAULT_TIER_LABELS = ['preventive', 'rehab', 'reconstruction'];
        function tierColor(label) {
            const tiers = rankedCostTiers();
            const rank = tiers.findIndex(t => t.label === label);
            if (rank >= 0) return tierRampColor(rank, tiers.length);
            const dflt = DEFAULT_TIER_LABELS.indexOf(label);
            return dflt >= 0 ? tierRampColor(dflt, DEFAULT_TIER_LABELS.length) : TIER_COLOR_UNKNOWN;
        }
        // Chart.js legend items for the Financials tier-coloured bar charts:
        // the city's cost tiers, best first.
        function tierLegendLabels() {
            return rankedCostTiers().map(t => {
                const name = String(t.label || '');
                const color = tierColor(t.label);
                return { text: name.charAt(0).toUpperCase() + name.slice(1), fillStyle: color, strokeStyle: color };
            });
        }
        // Material tiers are banded on their own, not by the city's cost
        // tiers (forecast.MaterialTier), so the Materials tab colours and
        // lists them by their own rank, best condition first.
        // rankedMaterialTiers is seed.material_tiers in that order.
        /** @param {any} seed @returns {{label: string, min_pci: number}[]} */
        function rankedMaterialTiers(seed) {
            const tiers = (seed && seed.material_tiers) || [];
            return tiers.slice().sort((/** @type {any} */ a, /** @type {any} */ b) => b.min_pci - a.min_pci);
        }
        /** @param {string} label @param {{label: string}[]} ranked */
        function materialTierColor(label, ranked) {
            const rank = ranked.findIndex(t => t.label === label);
            return rank >= 0 ? tierRampColor(rank, ranked.length) : TIER_COLOR_UNKNOWN;
        }
        /** @param {{label: string}[]} ranked */
        function materialTierLegendLabels(ranked) {
            return ranked.map(t => {
                const name = String(t.label || '');
                const color = materialTierColor(t.label, ranked);
                return { text: name.charAt(0).toUpperCase() + name.slice(1), fillStyle: color, strokeStyle: color };
            });
        }
        // pciTooltipLabel is a PCI chart's tooltip line, at the shown PCI.
        /** @param {any} ctx */
        function pciTooltipLabel(ctx) {
            return ctx.dataset.label + ': ' + shownPCI(ctx.parsed.y);
        }
        // pciBadgeClass is the cohort table's condition badge for a PCI: good
        // in the best tier, poor in the worst, fair in between.
        function pciBadgeClass(pci) {
            const tiers = rankedCostTiers();
            const rank = tiers.indexOf(tierForPCI(pci));
            if (rank <= 0) return 'pci-good';
            return rank === tiers.length - 1 ? 'pci-poor' : 'pci-fair';
        }
        // The forecast horizon. A config has one (forecast.horizon_years), exported as
        // horizon_years on every city's seed. Every "N-Year" label and every
        // figure "at the horizon" on Financials, Compare and Aggregate goes
        // through the helpers below, so a label and the figure under it name
        // the same year.
        //
        // FORECAST_DEFAULTS are the Go-owned forecast settings (the page seed's,
        // else the config defaults), for a value a city's seed omits.
        // DEFAULT_HORIZON_YEARS labels a page that has no forecast to read a
        // horizon from.
        const FORECAST_DEFAULTS = PVMT_CONFIG.forecast_defaults;
        const DEFAULT_HORIZON_YEARS = FORECAST_DEFAULTS.horizon_years;
        // forecastHorizon is one city's horizon in years: the seed's
        // horizon_years, else the length of its series, else 0.
        function forecastHorizon(seed, years) {
            const h = seed ? Number(seed.horizon_years) : 0;
            if (h > 0) return h;
            return years ? years.length : 0;
        }
        // yearAtHorizon is the entry of a per-year series for year `horizon`
        // (years[0] is year 1). A series that stops short of the horizon gives
        // its last year; an empty one gives null.
        function yearAtHorizon(years, horizon) {
            if (!years || years.length === 0 || horizon <= 0) return null;
            return years[Math.min(horizon, years.length) - 1];
        }
        // applyRegionHorizon settles the horizon for a set of cities shown
        // together (Compare, Aggregate) and stores each city's figures at it.
        // The horizon is the config's, which every city's seed carries; if
        // the cities disagree (a seed without horizon_years falls back to its
        // series length) the longest wins. A city whose series stops short
        // shows its last year, and m.shortSeriesYears is that year so the page
        // can flag it (shortSeriesNote). Returns 0 when no city has a forecast.
        function applyRegionHorizon(allMetrics) {
            let horizon = 0;
            allMetrics.forEach(function(m) {
                if (m.hasForecasts && m.horizon > horizon) horizon = m.horizon;
            });
            allMetrics.forEach(function(m) {
                const at = m.hasForecasts ? yearAtHorizon(m.baselineYears, horizon) : null;
                m.horizonNeed = at ? (at.annual_need || 0) : 0;
                m.horizonBacklog = at ? (at.deferred_backlog || 0) : 0;
                // The backlog is over the area of the year it belongs to (the
                // network grown to that year), else the scope's maintained area.
                m.horizonAreaLarge = at && at.area > 0 ? toAreaLarge(at.area) : m.scopeAreaLarge;
                m.horizonBacklogPerAreaUnit = m.horizonAreaLarge > 0 ? m.horizonBacklog / m.horizonAreaLarge : 0;
                m.shortSeriesYears = (at && m.baselineYears.length < horizon) ? m.baselineYears.length : 0;
            });
            return horizon;
        }
        // shortSeriesNote is the inline flag for a city whose figure at the
        // horizon is its last forecast year instead (HTML; '' for most cities).
        // The two area figures the site shows, named the same everywhere:
        // the paved footprint counts the ground once
        // where a sidewalk or lot meets a road; the maintained area sums the
        // resources the forecast prices, so a sidewalk beside a road counts in
        // both.
        const PAVED_FOOTPRINT_TIP = 'Paved footprint: the ground covered by pavement, every owner, with overlaps between roads, sidewalks and lots counted once.';
        const MAINTAINED_AREA_TIP = 'Maintained area: roads + parking + sidewalks summed as the forecast prices them, so a sidewalk beside a road counts in both. Larger than the paved footprint.';
        const PER_AREA_TIP = 'Divides by the maintained area the forecast prices, in the current scope (city-maintained or all owners), shown beside each city.';

        // scopeName names the current scope for an area label.
        function scopeName() {
            return currentScope === 'city' ? 'city-maintained' : 'all owners';
        }

        // perAreaNote shows a per-area row's denominator: area, else the
        // scope's maintained area.
        /** @param {any} m @param {number} [area] @returns {string} */
        function perAreaNote(m, area) {
            var a = area === undefined ? m.scopeAreaLarge : area;
            if (!(a > 0)) return '';
            return ' <span class="lb-note" title="' + PER_AREA_TIP + '">(÷ ' + fmtNum(a) + ' ' + areaLargeUnit() + ')</span>';
        }

        function shortSeriesNote(m) {
            if (!m.shortSeriesYears) return '';
            return ' <span class="lb-note" title="This city’s forecast ends at year ' + m.shortSeriesYears +
                '; its last year is shown.">(through year ' + m.shortSeriesYears + ')</span>';
        }

        // findBaseline is the no-intervention scenario of a scope's set: the
        // entry whose strategy is 'do_nothing' or whose name is 'baseline'.
        // compare.json keeps only that scenario per scope,
        // stripped to its years, so a set whose entries carry no scenario
        // metadata is its first entry. null when the set has no baseline.
        function findBaseline(scenarios) {
            if (!scenarios || scenarios.length === 0) return null;
            const marked = scenarios.find(s => s.scenario &&
                (s.scenario.strategy === 'do_nothing' || s.scenario.name === 'baseline'));
            if (marked) return marked;
            return scenarios.some(s => s.scenario) ? null : scenarios[0];
        }

        // describedScenarios is the entries of a scope's set the charts plot one
        // line each for: those carrying their scenario's name and label, in the
        // set's own order (which the COLORS follow). Go defines the set
        // (forecast.ReferenceScenarios); compare.json's stripped entries carry
        // no scenario and are never charted.
        function describedScenarios(scenarios) {
            return scenarios.filter(s => s.scenario);
        }
        // scenarios.json / city.scenarios is scope-keyed ({city, all, summary}).
        // effectiveScope resolves a preferred scope to one actually present;
        // scenariosForScope selects the list for a scope with the city→all
        // fallback every caller uses.
        function effectiveScope(byScope, preferred) {
            if (preferred === 'city' && !byScope.city && byScope.all) return 'all';
            if (preferred === 'all' && !byScope.all && byScope.city) return 'city';
            return preferred;
        }
        function scenariosForScope(byScope, scope) {
            return byScope[scope] || byScope.city || byScope.all;
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

        let scenarioData = null;
        // True once THIS city's forecast_seed.json has landed and been applied to
        // FORECAST_SEED and the interactive controls. Reset by loadFinancials'
        // teardown, so it always describes the city currently on screen.
        let forecastSeedReady = false;
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
        // The Financials charts are drawn without the entry animation. The
        // interactive "Your budget" series is added right after each chart is
        // built and pushed with update('none'), which would leave the other
        // series frozen partway through their first animation (Cumulative
        // Spending lines starting below $0 and ending ~20% low, with the data
        // correct). With nothing animating, every element sits at
        // its final position from the first frame.
        const FINANCIAL_CHART_ANIMATION = false;

        let renderFinancials = function(scenarios, scope) {
            if (!scenarios || scenarios.length === 0) {
                document.getElementById('charts').innerHTML = '';
                return;
            }

            destroyCharts();

            const baseline = findBaseline(scenarios);
            if (!baseline) return;

            // Charts
            const chartsDiv = document.getElementById('charts');
            chartsDiv.innerHTML = '';

            // The baseline and the funding levels, one line each.
            const fundingScenarios = describedScenarios(scenarios);

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
                    options: { responsive: true, animation: FINANCIAL_CHART_ANIMATION, interaction: { mode: 'index', intersect: false }, scales: { y: { min: 0, max: 100, title: { display: true, text: 'PCI' } } }, plugins: { legend: { position: 'bottom' }, tooltip: { callbacks: { label: pciTooltipLabel } } } }
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
                            fill: s === baseline,
                            tension: 0.3,
                            pointRadius: 0
                        }))
                    },
                    options: { responsive: true, animation: FINANCIAL_CHART_ANIMATION, interaction: { mode: 'index', intersect: false }, scales: { y: { title: { display: true, text: 'Dollars ($)' }, ticks: { callback: v => fmtMoney(v) } } }, plugins: { legend: { position: 'bottom' }, tooltip: { callbacks: { label: ctx => ctx.dataset.label + ': ' + fmtMoney(ctx.parsed.y) } } } }
                }));
            }

            // 3. Cumulative cost (exclude baseline)
            const fundedScenarios = fundingScenarios.filter(s => s !== baseline);
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
                    options: { responsive: true, animation: FINANCIAL_CHART_ANIMATION, interaction: { mode: 'index', intersect: false }, scales: { y: { title: { display: true, text: 'Dollars ($)' }, ticks: { callback: v => fmtMoney(v) } } }, plugins: { legend: { position: 'bottom' }, tooltip: { callbacks: { label: ctx => ctx.dataset.label + ': ' + fmtMoney(ctx.parsed.y) } } } }
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
                        animation: FINANCIAL_CHART_ANIMATION,
                        scales: { y: { title: { display: true, text: 'Dollars ($)' }, ticks: { callback: v => fmtMoney(v) } } },
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
            ['deferred_backlog', 'number']
        ];

        function renderCohortBreakdown(cohorts, keepSort) {
            lastCohorts = cohorts;
            const totalArea = cohorts.reduce((s, c) => s + c.area, 0);

            // Enrich with computed fields for sorting
            const enriched = cohorts.map(c => ({
                ...c,
                area_pct: totalArea > 0 ? c.area / totalArea * 100 : 0,
                total_spend: c.total_spend || 0,
                deferred_backlog: c.deferred_backlog || 0
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
                // The badge follows the number it shows.
                const pci = shownPCI(c.end_pci);
                const cls = pciBadgeClass(pci);

                totalSpend += c.total_spend;
                totalDeficit += c.deferred_backlog;

                const tr = document.createElement('tr');
                tr.innerHTML =
                    '<td style="text-transform:capitalize">' + c.classification + '</td>' +
                    '<td class="number">' + fmtNum1(areaLargeVal) + '</td>' +
                    '<td class="number">' + c.area_pct.toFixed(1) + '%</td>' +
                    '<td class="number">' + c.decay_rate.toFixed(4) + '</td>' +
                    '<td class="number"><span class="pci-badge ' + cls + '">' + pci + '</span></td>' +
                    '<td class="number">' + fmtMoney(c.total_spend, { full: true }) + '</td>' +
                    '<td class="number">' + fmtMoney(c.deferred_backlog, { full: true }) + '</td>';
                tbody.appendChild(tr);
            });

            // Totals row
            const totalAreaLarge = toAreaLarge(totalArea);
            const totTr = document.createElement('tr');
            totTr.className = 'total-row';
            totTr.innerHTML =
                '<td>Total</td>' +
                '<td class="number">' + fmtNum1(totalAreaLarge) + '</td>' +
                '<td class="number">100%</td>' +
                '<td></td><td></td>' +
                '<td class="number">' + fmtMoney(totalSpend, { full: true }) + '</td>' +
                '<td class="number">' + fmtMoney(totalDeficit, { full: true }) + '</td>';
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

        // financialsHorizon is the horizon the Financials tab labels and reads
        // its headline tiles at: this city's (forecastHorizon over the seed
        // and the exported series of the active scope), or the default while
        // neither is loaded.
        function financialsHorizon() {
            const scenarios = scenarioData ? scenariosForScope(scenarioData, currentScope) : null;
            const baseline = findBaseline(scenarios);
            const years = baseline ? baseline.years : null;
            return forecastHorizon(FORECAST_SEED, years) || DEFAULT_HORIZON_YEARS;
        }

        // Horizon-derived labels (never hardcode the number). The headline
        // and cohort values are read at financialsHorizon(), so the captions
        // follow it too. The cohort Area
        // cells convert to the live large-area unit (ha/acres), so that header
        // carries the unit too — which is why the units toggle has to re-run
        // this (via renderFinancialsFromCache) and not just the table body.
        // Called twice per load: once before the fetches off the previous
        // seed, once from the render pass after this city's seed has landed.
        //
        // ORDERING: these textContent writes blow away the .sort-arrow spans
        // renderCohortBreakdown appends to the same <th>s, so this must run
        // BEFORE the cohort render, never after. Both call paths
        // (renderFinancialsFromCache, and the pre-fetch call in loadFinancials)
        // satisfy that today.
        function applyHorizonLabels() {
            const yrs = financialsHorizon();
            document.getElementById('hl-spend-label').textContent = yrs + '-Year Spend';
            document.getElementById('hl-deficit-label').textContent = yrs + '-Year Deficit';
            document.getElementById('cohort-spend-th').textContent = yrs + 'yr Spend';
            document.getElementById('cohort-deficit-th').textContent = yrs + 'yr Deficit';
            // "End Area", not "Area": CohortSummary.Area reports the FINAL-year
            // grown area, so at growth_rate > 0 it runs above the year-0 "Total
            // paved" / network-area figures elsewhere on this page. Named to
            // match its sibling "End PCI" column. area_pct is self-normalising,
            // so it still sums to 100 against this basis.
            const areaTh = document.getElementById('cohort-area-th');
            areaTh.textContent = 'End Area (' + areaLargeUnit() + ')';
            areaTh.title = 'Maintained area in year ' + yrs + ', after growth: '
                + 'larger than today\'s at a growth rate above 0.';
        }

        // Render the Financials tab from data already in memory: scenarioData,
        // FORECAST_SEED, and forecastData. Split out of loadFinancials so a
        // display-unit change can re-render in place. loadFinancials is a
        // teardown-and-refetch path — it nulls forecastData, hides the solvency
        // headline / tiles / cohort table, and rebuilds the PCI slider and
        // cost-tier inputs from the seed — so driving a units toggle through it
        // refetched three JSON files, blanked the headline mid-flight, and
        // silently discarded whatever the user had typed into the controls.
        //
        // Safe to call before any city has loaded: scenarioData is null then and
        // this is a no-op.
        function renderFinancialsFromCache() {
            if (!scenarioData) return;
            renderMaintainToggles();
            const scenarios = financialScenarios(currentScope);
            if (!scenarios || scenarios.length === 0) return;
            applyHorizonLabels();
            // Either branch below settles the cohort table itself, so the
            // wrapper must not simulate on the way through. When the WASM branch
            // is not taken the sim would be wrong anyway — that is what
            // forecastSeedReady guards.
            deferCustomScenario = true;
            try {
                renderFinancials(scenarios, currentScope);
            } finally {
                deferCustomScenario = false;
            }

            // Headline tiles come from the baseline scenario, so they work without WASM.
            const baseline = findBaseline(scenarios);
            if (baseline) {
                updateSummaryCards(baseline);
            }
            renderSolvencyHeadline();

            // Cohort table: WASM drives it when the simulator is up, and the
            // static forecast.json baseline stands in when it is not. Without
            // the fallback the table never appeared at all in a no-WASM browser
            // until the user happened to toggle scope. Mirrors selectScope.
            //
            // forecastSeedReady gates the WASM branch: if this city's
            // forecast_seed.json failed to load, FORECAST_SEED, the PCI slider and
            // the tier inputs still hold the PREVIOUS city's parameters, and
            // runCustomScenario would simulate those — then overwrite the correct
            // baseline tiles above (it calls updateSummaryCards) and draw a custom
            // line from the wrong city. Fall back to the static baseline instead,
            // which is this city's (forecast.json is fetched separately).
            if (wasmReady && forecastSeedReady) {
                // Re-reveal the interactive controls. loadFinancials strips
                // .visible on every teardown, but onWasmReady — the only other
                // place that adds it — fires once per page load, so without this
                // the PCI slider and tier inputs vanished for the rest of the
                // session the first time the user switched city after WASM came
                // up. Gated on the same condition as the sim below: with no seed
                // the controls still describe the PREVIOUS city, so showing them
                // would invite edits against the wrong parameters.
                document.getElementById('forecast-controls').classList.add('visible');
                runCustomScenario();
            } else {
                renderCohortFromForecast(currentScope);
            }
        }

        async function loadFinancials() {
            const gen = ++financialsLoadGen; // bail stale loads after each await
            let tidyURL = false;
            const prefix = DATA_PREFIX; // snapshot to avoid mid-await mutation
            // Clear any prior city's solvency headline up front. forecastData is
            // only reassigned on a successful forecast.json load below, so
            // without this an early return (no scenarios) or a failed
            // forecast.json fetch would leave the previous city's metrics on
            // screen — a wrong dollar claim under the new city's name.
            forecastData = null;
            liveSolvency = null;
            // A tier error dimmed the previous city's figures; they are being
            // replaced, and a city without a seed never re-validates.
            markForecastStale(false);
            renderSolvencyHeadline();
            // Drop the previous city's scenarios and seed-loaded flag for the same
            // reason, and so renderFinancialsFromCache — which any units toggle can
            // fire at any moment — has no stale city to render. Without this, a
            // toggle landing inside this load's fetch window would repaint the
            // previous city's charts and (via updateSummaryCards) un-hide its dollar
            // tiles under the new city's name; if this load then took either early
            // return below, those tiles would stay on screen for good.
            scenarioData = null;
            // A reload of the same city (a snapshot switch) keeps the reader's
            // model settings, unless the URL or a city switch has pended others.
            if (pendingModel === null && forecastSeedReady) pendingModel = currentModel();
            forecastSeedReady = false;
            // Also hide the previous city's headline tiles, cohort table, and
            // forecast controls up front, mirroring the solvency-headline reset
            // above: an early return below (no scenarios / failed fetch) must not
            // leave stale dollar tiles or a cohort table under the new city name.
            document.getElementById('headlines').style.display = 'none';
            document.getElementById('cohort-section').style.display = 'none';
            document.querySelector('#cohort-table tbody').innerHTML = '';
            document.getElementById('forecast-controls').classList.remove('visible');
            // Label off the outgoing seed so the tiles aren't unlabeled while the
            // fetches are in flight; the render pass below relabels off this
            // city's seed once it lands.
            applyHorizonLabels();
            // The three files are independent, so fetch them at once: the tab
            // waits for the slowest, not the sum. They are applied in order
            // below, and nothing renders until all three have landed.
            const [loadedScenarios, seed, loadedForecast] = await Promise.all([
                loadJSON(dataURL(prefix, 'scenarios.json')),
                loadJSON(dataURL(prefix, 'forecast_seed.json')),
                loadJSON(dataURL(prefix, 'forecast.json')),
            ]);
            if (gen !== financialsLoadGen) return; // a newer load superseded us
            scenarioData = loadedScenarios;
            if (!scenarioData) {
                destroyCharts();
                showNoData('charts', 'No financial data available. Run <code>pvmt forecast</code> and <code>pvmt export</code> first.');
                return;
            }

            // Fall back if currentScope is unsupported by the loaded
            // scenarios (e.g. URL said scope=city but this city only has
            // all-scope data). Mirrors the same repair in loadCityData.
            currentScope = effectiveScope(scenarioData, currentScope);
            syncScopeToggle();
            maybeRevealScopeToggle();
            const scenarios = scenariosForScope(scenarioData, currentScope);

            if (!scenarios || scenarios.length === 0) {
                destroyCharts();
                showNoData('charts', 'No scenario data found.');
                return;
            }

            // The per-city forecast seed, for the WASM interactive controls.
            {
                if (seed) {
                    FORECAST_SEED = seed;
                    // Reset the interactive controls to the new city's seed, then
                    // put the linked model settings over it. Only this fetch path
                    // does so — a units toggle must not, or it throws away the
                    // values the user typed.
                    const linked = pendingModel;
                    applyModel(linked || parseModel({}));
                    pendingModel = null;
                    // Tidy the address bar once the seed can tell which linked
                    // settings are the city's own (and so need no URL).
                    tidyURL = !!linked && Object.values(linked).some(v => v != null);
                    // Gate the WASM sim: until this lands, FORECAST_SEED and the
                    // controls still describe the *previous* city (see
                    // renderFinancialsFromCache).
                    forecastSeedReady = true;
                }
            }

            // forecast.json: the static, WASM-free source for the cohort table
            // and the solvency headline; also used by the scope toggle.
            if (loadedForecast) forecastData = loadedForecast;

            // Single render pass once everything above has landed: charts,
            // headline tiles, solvency headline, and the cohort table. Rendering
            // here rather than before the awaits also means the WASM custom
            // scenario runs against this city's seed and controls, not the
            // previous city's.
            renderFinancialsFromCache();
            if (tidyURL) Router.replace();
        }

        // Materials
        //
        // Go computes the material demand (forecast.WithMaterials) and ships it
        // on every scenario year in scenarios.json: each simulated year retreats
        // one treatment-cycle slice of the asphalt area, and the material tier
        // whose PCI band holds the year's network-blended condition picks the
        // intensity (material_tier; not the cost tier's label, which a city
        // names its own way). That tier is
        // coarser than the per-cohort interpolated dollar model, so these
        // figures track the scenario's condition trajectory rather than
        // reconciling with annual_need. The headline uses the best-funded
        // scenario (highest final PCI) as the "network kept in repair" figure;
        // the do-nothing baseline's rising reconstruction share shows the
        // deferred-maintenance material blowout.

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
        function fmtMass(tonnes) {
            return fmtDisplayMass(massToDisplay(tonnes));
        }
        // massToDisplay converts tonnes to the display mass unit, so a chart
        // plots display units and its axis ticks land on round numbers.
        /** @param {number} tonnes */
        function massToDisplay(tonnes) {
            return UNIT_SYSTEM === 'metric' ? tonnes : tonnes / TONNES_PER_SHORT_TON;
        }
        // fmtDisplayMass formats a mass already in display units.
        /** @param {number} v */
        function fmtDisplayMass(v) {
            return fmtNum(v) + (UNIT_SYSTEM === 'metric' ? ' t' : ' tons');
        }
        function fmtBarrels(bbl) {
            return fmtNum(bbl) + ' bbl';
        }
        // A tonne is 1000 kg; a US short ton is 907.18474 kg.
        const TONNES_PER_SHORT_TON = 0.90718474;
        const LB_PER_KG = 2.20462262185;
        // massUnitSingular names the unit fmtMass prints, for rates ("per tonne").
        function massUnitSingular() {
            return UNIT_SYSTEM === 'metric' ? 'tonne' : 'ton';
        }
        // perTonneToDisplay converts a per-tonne rate to per-massUnitSingular().
        /** @param {number} perTonne */
        function perTonneToDisplay(perTonne) {
            return UNIT_SYSTEM === 'metric' ? perTonne : perTonne * TONNES_PER_SHORT_TON;
        }
        // The tier table's intensities: kg/m² and barrels per 1000 m² in
        // metric, lb/sq ft and barrels per 10,000 sq ft (~929 m²) in imperial.
        /** @param {number} kgPerSqm */
        function intensityToDisplay(kgPerSqm) {
            return UNIT_SYSTEM === 'metric' ? kgPerSqm : kgPerSqm * LB_PER_KG / SQM_TO_SQFT;
        }
        function intensityUnit() {
            return UNIT_SYSTEM === 'metric' ? 'kg/m²' : 'lb/sq ft';
        }
        /** @param {number} per1000Sqm */
        function oilIntensityToDisplay(per1000Sqm) {
            return UNIT_SYSTEM === 'metric' ? per1000Sqm : per1000Sqm * 10 / SQM_TO_SQFT;
        }
        function oilIntensityUnit() {
            return UNIT_SYSTEM === 'metric' ? 'bbl/1000 m²' : 'bbl/10,000 sq ft';
        }

        // materialSeries is one scenario's per-year material demand, in tonnes
        // and barrels, as scenarios.json carries it.
        function materialSeries(scenario) {
            return scenario.years.map(function(y) {
                return {
                    year: y.year,
                    // The material tier the tonnes were priced at, which a
                    // city's own cost tiers need not match (ern4.25).
                    tier: y.material_tier || y.cost_tier,
                    mixTonnes: y.materials.mix_tonnes,
                    binderTonnes: y.materials.binder_tonnes,
                    oilBarrels: y.materials.oil_barrels
                };
            });
        }

        // hasMaterials reports whether every scenario year carries its material
        // demand; an export from before scenarios.json shipped it does not.
        function hasMaterials(scenarios) {
            return scenarios.every(function(s) {
                return s.years.every(function(y) { return !!y.materials; });
            });
        }

        function renderMaterialTierTable(tiers) {
            var section = document.getElementById('materials-tier-section');
            var tbody = document.querySelector('#materials-tier-table tbody');
            if (!tiers || tiers.length === 0) { section.style.display = 'none'; return; }
            var ranked = rankedMaterialTiers({ material_tiers: tiers });
            // Headers and cells follow the unit system.
            var metric = UNIT_SYSTEM === 'metric';
            document.getElementById('materials-mix-th').textContent = 'Mix (' + intensityUnit() + ')';
            document.getElementById('materials-binder-th').textContent = 'Binder (' + intensityUnit() + ')';
            document.getElementById('materials-oil-th').textContent = 'Oil (' + oilIntensityUnit() + ')';
            tbody.innerHTML = tiers.map(function(t) {
                // Tonnes of binder over 1000 m^2 is kilograms per m^2.
                var binderKgSqM = t.per_1000_sqm.binder_tonnes;
                var oilPer1000 = t.per_1000_sqm.oil_barrels;
                var dot = '<span style="display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:6px;background:' +
                    materialTierColor(t.label, ranked) + '"></span>';
                // Material tiers are chosen by condition,
                // so the row names its PCI band; the top band's exclusive 101 reads 100.
                var band = (typeof t.min_pci === 'number' && typeof t.max_pci === 'number')
                    ? ' <span style="color:var(--text-muted)">PCI ' + t.min_pci + '–' + Math.min(100, t.max_pci) + '</span>' : '';
                return '<tr>' +
                    '<td>' + dot + esc(t.label) + band + '</td>' +
                    '<td class="number">' + intensityToDisplay(t.mix_kg_per_sqm).toFixed(metric ? 0 : 1) + '</td>' +
                    '<td class="number">' + (t.binder_share * 100).toFixed(1) + '%</td>' +
                    '<td class="number">' + intensityToDisplay(binderKgSqM).toFixed(metric ? 1 : 2) + '</td>' +
                    '<td class="number">' + oilIntensityToDisplay(oilPer1000).toFixed(1) + '</td>' +
                    '</tr>';
            }).join('');
            section.style.display = '';
        }

        // showMaterialsEmpty is the Materials tab with nothing to chart: the
        // headline tiles, tier table and note hidden, and msg in their place.
        /** @param {string} msg */
        function showMaterialsEmpty(msg) {
            destroyMaterialsCharts();
            document.getElementById('materials-headlines').style.display = 'none';
            document.getElementById('materials-tier-section').style.display = 'none';
            document.getElementById('materials-note').style.display = 'none';
            showNoData('materials-charts', msg);
        }

        function renderMaterials(scenarios, seed, scope) {
            var chartsDiv = document.getElementById('materials-charts');
            destroyMaterialsCharts();
            chartsDiv.innerHTML = '';
            if (!scenarios || scenarios.length === 0) {
                showMaterialsEmpty('No scenario data found.');
                return;
            }
            var tiers = (seed && seed.material_tiers) || [];
            // An older export, or a seed that failed to load: say so rather than
            // draw nothing.
            if (tiers.length === 0 || !tiers[0].per_1000_sqm || !hasMaterials(scenarios)) {
                showMaterialsEmpty('No material quantities in the forecast data. Re-run <code>pvmt export</code> to refresh it.');
                return;
            }
            var barrelsPerTonBinder = (seed && seed.barrels_per_ton_binder) || FORECAST_DEFAULTS.barrels_per_ton_binder;
            var cycleYears = (seed && seed.treatment_cycle_years) || FORECAST_DEFAULTS.treatment_cycle_years;
            // The share of the network area the figures cover, for the note
            // below. Scope-selected the same way the forecast controls pick
            // their area and cohorts: the all-scope share unless a city scope
            // is actually loaded.
            var asphaltShare = (scope === 'all' || !(seed && seed.city_maintained_area))
                ? (seed && seed.asphalt_area_share)
                : (seed && seed.city_asphalt_area_share);

            var fundingScenarios = describedScenarios(scenarios);
            if (fundingScenarios.length === 0) fundingScenarios = scenarios.slice(0, 1);

            var seriesByName = {};
            fundingScenarios.forEach(function(s) {
                seriesByName[s.scenario.name] = materialSeries(s);
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
            var avgMix = bestSeries.reduce(function(acc, r) { return acc + r.mixTonnes; }, 0) / n;
            var avgBinder = bestSeries.reduce(function(acc, r) { return acc + r.binderTonnes; }, 0) / n;
            var avgOil = bestSeries.reduce(function(acc, r) { return acc + r.oilBarrels; }, 0) / n;

            document.getElementById('hl-mix').textContent = fmtMass(avgMix) + '/yr';
            document.getElementById('hl-binder').textContent = fmtMass(avgBinder) + '/yr';
            document.getElementById('hl-oil').textContent = fmtBarrels(avgOil) + '/yr';
            document.getElementById('hl-mix-sub').textContent = 'Avg/yr under "' + best.scenario.label + '"';
            document.getElementById('hl-oil-sub').textContent = '~' + Number(perTonneToDisplay(barrelsPerTonBinder).toFixed(1)) +
                ' bbl per ' + massUnitSingular() + ' binder';
            document.getElementById('materials-headlines').style.display = '';

            var note = document.getElementById('materials-note');
            var noteText = 'Each year ~1/' + Math.round(cycleYears) + ' of the network (area ÷ ' +
                'treatment cycle) is retreated; material intensity follows the blended condition, ' +
                'so the do-nothing line climbs as pavement decays into reconstruction. Planning-grade estimates.';
            // Say so when the area basis here is narrower than the network area
            // shown on the other tabs, rather than leaving the reader to wonder
            // why the figures do not reconcile.
            if (asphaltShare <= 0) {
                // A concrete-only network: asphalt_area_share is legitimately 0
                // (see the asphalt_area_share comment in seeds.go for why that
                // is reachable). Every headline above is a zero, so the
                // percentage sentence has no percentage to report — printing
                // "0%" reads as a rounding artifact and clamping it up to "1%"
                // would be an outright false claim about the network.
                noteText += ' None of this network area is asphalt-surfaced — it is concrete ' +
                    'sidewalks, which consume no asphalt binder — so these figures are all zero.';
            } else if (asphaltShare < 1) {
                // Round, then clamp the TOP only. toFixed(0) printed "100%" for
                // any share in [0.995, 1) — reachable wherever OSM sidewalk
                // coverage is sparse — so the note claimed sidewalks were
                // excluded AND that the figures covered the whole network in one
                // breath. 99 is the honest ceiling while a nonzero area is
                // excluded.
                //
                // Math.round, not Math.floor: flooring biases every share down
                // by up to a point (0.899 -> "89%"), so the note would stop
                // agreeing with the share derivable from the Overview tab. The
                // bottom needs no clamp — this branch only runs for a strictly
                // positive share, and a share under 0.005 rounding to "0%" is
                // the correct reading of a network that is essentially all
                // concrete.
                var shownPct = Math.min(99, Math.round(asphaltShare * 100));
                noteText += ' Concrete sidewalks are excluded — they consume no asphalt binder — so these ' +
                    'figures cover the ' + shownPct + '% of network area that is ' +
                    'asphalt-surfaced (roads and parking).';
            }
            note.textContent = noteText;
            note.style.display = '';

            renderMaterialTierTable(tiers);

            var labels = bestSeries.map(function(r) { return 'Year ' + r.year; });
            // The series are plotted in display mass units (massToDisplay), so
            // the axis ticks are round in tons as well as tonnes.
            var massAxis = function() { return { title: { display: true, text: UNIT_SYSTEM === 'metric' ? 'Tonnes' : 'Tons' }, ticks: { callback: function(v) { return fmtDisplayMass(v); } } }; };
            // The do-nothing line plots the material the decaying network would
            // need, not material anyone spends, so its label says so.
            var baselineScenario = findBaseline(scenarios);
            var materialsLabel = function(s) {
                return s === baselineScenario ? s.scenario.label + ' (material need)' : s.scenario.label;
            };

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
                                label: materialsLabel(s),
                                data: seriesByName[s.scenario.name].map(function(r) { return massToDisplay(r.mixTonnes); }),
                                borderColor: COLORS[i % COLORS.length],
                                backgroundColor: 'transparent',
                                tension: 0.3,
                                pointRadius: 0
                            };
                        })
                    },
                    options: { responsive: true, interaction: { mode: 'index', intersect: false }, scales: { y: massAxis() }, plugins: { legend: { position: 'bottom' }, tooltip: { callbacks: { label: function(ctx) { return ctx.dataset.label + ': ' + fmtDisplayMass(ctx.parsed.y); } } } } }
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
                                label: materialsLabel(s),
                                data: seriesByName[s.scenario.name].map(function(r) { return massToDisplay(r.binderTonnes); }),
                                borderColor: COLORS[i % COLORS.length],
                                backgroundColor: 'transparent',
                                tension: 0.3,
                                pointRadius: 0
                            };
                        })
                    },
                    options: { responsive: true, interaction: { mode: 'index', intersect: false }, scales: { y: massAxis() }, plugins: { legend: { position: 'bottom' }, tooltip: { callbacks: { label: function(ctx) { return ctx.dataset.label + ': ' + fmtDisplayMass(ctx.parsed.y) + ' · ' + fmtBarrels(seriesByName[fundingScenarios[ctx.datasetIndex].scenario.name][ctx.dataIndex].oilBarrels); } } } } }
                }));
            })();

            // 3. Annual mix by condition tier for the do-nothing baseline — shows
            // the deferred-maintenance blowout as the network decays.
            (function() {
                var baseline = findBaseline(scenarios);
                if (!baseline) return;
                var series = materialSeries(baseline);
                var ranked = rankedMaterialTiers(seed);
                var card = makeChartCard('Annual Mix by Condition Tier (Do-Nothing)');
                chartsDiv.appendChild(card);
                var ctx = card.querySelector('canvas').getContext('2d');
                materialsCharts.push(new Chart(ctx, {
                    type: 'bar',
                    data: {
                        labels: series.map(function(r) { return 'Year ' + r.year; }),
                        datasets: [{
                            label: 'Asphalt Mix',
                            data: series.map(function(r) { return massToDisplay(r.mixTonnes); }),
                            backgroundColor: series.map(function(r) { return materialTierColor(r.tier, ranked); })
                        }]
                    },
                    options: {
                        responsive: true,
                        scales: { y: massAxis() },
                        plugins: {
                            verticalCrosshair: false,
                            legend: { display: true, position: 'bottom', labels: { generateLabels: function() { return materialTierLegendLabels(ranked); } } },
                            tooltip: { callbacks: { label: function(ctx) { return fmtDisplayMass(ctx.parsed.y); } } }
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
            if (!scenarios) {
                showMaterialsEmpty('No material data available. Run <code>pvmt forecast</code> and <code>pvmt export</code> first.');
                return;
            }
            // Scope fallback mirrors loadFinancials: honor currentScope but fall
            // back when the loaded scenarios lack it.
            var scope = effectiveScope(scenarios, currentScope);
            var list = scenariosForScope(scenarios, scope);
            renderMaterials(list, seed, scope);
        }

        // Set by renderAggregate once the region data loads: true when any
        // city in the config carries both city- and all-scope scenarios.
        var regionHasBothScopes = false;

        // Reveal the scope toggle only when BOTH city and all-scope data are
        // detectable in at least one data source (hex grid or scenarios).
        // Idempotent — both loaders call this after their fetches settle.
        // On the Aggregate tab the toggle is region-wide, so honor whether
        // ANY city has both scopes rather than just the selected dropdown city.
        // urlFollows is set by a caller that writes the URL right after, so
        // the reveal leaves the history entry to it.
        /** @param {boolean} [urlFollows] */
        function maybeRevealScopeToggle(urlFollows) {
            const haveHexCity = hexDataByScope.city && Object.keys(hexDataByScope.city).length > 0;
            const haveHexAll = hexDataByScope.all && Object.keys(hexDataByScope.all).length > 0;
            const haveScenCity = !!(scenarioData && scenarioData.city);
            const haveScenAll = !!(scenarioData && scenarioData.all);
            const haveCity = haveHexCity || haveScenCity;
            const haveAll = haveHexAll || haveScenAll;
            const aggActive = (function() {
                const el = document.getElementById('aggregate-tab');
                return el && el.classList.contains('active');
            })();
            const reveal = (haveCity && haveAll) || (aggActive && regionHasBothScopes);
            const row = document.getElementById('gear-scope-row');
            const revealed = reveal && row.hidden;
            row.hidden = !reveal;
            // Router.current writes ?scope= only while the row shows, and the
            // boot replace runs before any data reveals it, so a deep-linked
            // ?scope=all is rewritten into the address bar once it does.
            if (revealed && currentScope !== 'city' && !urlFollows) Router.replace();
        }

        // Resolve the display name of the city currently in view from
        // DATA_PREFIX (cities/<slug>/). Falls back gracefully for single-city
        // exports or an unmatched slug.
        function currentCityName() {
            var slug = DATA_PREFIX.replace(/^cities\//, '').replace(/\/$/, '');
            var c = (typeof CITIES !== 'undefined') ? CITIES.find(function(c) { return c.slug === slug; }) : null;
            if (c) return c.name;
            if (typeof CITIES !== 'undefined' && CITIES.length > 0) return CITIES[0].name;
            // Single-city pages have no CITIES; the loaded meta.json names the
            // city. The last resort is read mid-sentence
            // ("At current funding, this city's ..."), so it is lower case.
            if (lastCityMeta && lastCityMeta.name) return lastCityMeta.name;
            return 'this city';
        }

        // forecastYearOne is the calendar year of forecast year 1: the year of
        // the loaded city's snapshot_date, so the page names the same years
        // whenever it is viewed. Without a meta.json it is the viewer's year.
        function forecastYearOne() {
            var d = lastCityMeta && lastCityMeta.snapshot_date;
            var y = typeof d === 'string' && /^\d{4}-/.test(d) ? parseInt(d.slice(0, 4), 10) : NaN;
            return y > 0 ? y : new Date().getFullYear();
        }

        // Render the roads-only solvency headline above the Financials charts
        // from the per-city forecast.json (the selected city's forecastData).
        // Break-even is always shown; the insolvency year and funding gap are
        // shown only when a current_budget is configured (cited). Metrics are
        // roads-only and labeled as such, so they don't read as whole-network.
        function renderSolvencyHeadline() {
            var el = document.getElementById('solvency-headline');
            if (!el) return;
            // Live (WASM) figures at the current settings once they exist, else
            // forecast.json's, which is what the live ones reproduce at the
            // seeded settings.
            var roads = liveSolvency || (forecastData ? forecastData.find(function(f) { return f.resource_type === 'roads'; }) : null);
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
                    var crossYear = forecastYearOne() + roads.insolvency_year - 1;
                    lead = 'At current funding, ' + name + '’s street-repair backlog grows to an entire treatment cycle of deferred work by <span class="num">' + crossYear + '</span> (year ' + roads.insolvency_year + ').';
                } else {
                    lead = 'At current funding, ' + name + '’s streets stay solvent through the forecast horizon.';
                }
                if (gapDollars > 0) {
                    detail = 'Holding the network steady would take <strong>' + fmtMoney(breakEven) + '/yr</strong> — <strong>' + fmtMoney(gapDollars) + ' more</strong> than today’s ' + fmtMoney(roads.current_budget) + ' (streets/roads only).';
                } else {
                    detail = 'Today’s ' + fmtMoney(roads.current_budget) + '/yr is at or above the ' + fmtMoney(breakEven) + '/yr needed to hold the network steady (streets/roads only).';
                }
            } else {
                lead = 'Holding ' + name + '’s streets steady would take <span class="num">' + fmtMoney(breakEven) + '/yr</span> (streets/roads only).';
                detail = 'No cited street budget for ' + name + ' is on file, so its funding gap and insolvency year aren’t shown.';
            }
            var fe = forecastData ? forecastData.find(function(f) { return f.resource_type === 'roads'; }) : null;
            var seededPCI = FORECAST_SEED ? Number(FORECAST_SEED.initial_pci) : NaN;
            var pciNow = parseFloat(inputById('pci-slider').value);
            var defaultPCI = !!fe && fe.initial_pci_source === 'default' && (!liveSolvency || pciNow === seededPCI);
            el.innerHTML = '<div class="lead">' + lead + '</div><div class="detail">' + detail
                + '</div>' + (defaultPCI ? '<div class="detail default-pci-detail">† ' + DEFAULT_PCI_NOTE
                    + ' (' + seededPCI + '). Set Initial PCI under Advanced options to test another.</div>' : '')
                + '<div class="detail cost-regime" id="cost-regime-note">' + costRegimeText() + '</div>';
            el.style.display = 'block';
        }

        // Name the cost basis. At the default 1x this is not a hedge: the model's
        // system output is already loaded-equivalent — bare tiers x the condition
        // spread x the 12-year treatment cycle reproduce Berkeley's cited real
        // hold-steady spend, which is itself a loaded municipal figure. Saying
        // "comparable to a published pavement budget" is the accurate claim, and
        // it is the claim a reader needs in order to use the number.
        //
        // Names the multiplier the headline was priced at: the slider's once the
        // headline is live (liveSolvency), else the seed's, which is what
        // forecast.json was priced at.
        function costRegimeText() {
            const seeded = FORECAST_SEED && Number(FORECAST_SEED.cost_multiplier);
            const mult = liveSolvency ? currentMultiplier() : seeded;
            if (!Number.isFinite(mult) || mult <= 1) {
                return 'Figures are calibrated against real municipal hold-steady spending, so they are '
                    + 'comparable to a published pavement budget. See Methodology for the cost basis.';
            }
            const where = (Number.isFinite(seeded) && Math.abs(mult - seeded) < 1e-9)
                ? 'set for this city in <code>forecast.cost_multiplier</code>'
                : 'set on the Cost multiplier slider';
            return 'Figures carry a <strong>' + mult.toFixed(2).replace(/0$/, '')
                + '\u00d7 cost multiplier</strong> over the calibrated default \u2014 ' + where + '.';
        }

        // The slider moves the interactive line's basis and the live headline, so
        // keep its label honest while the user drags.
        function renderMultiplierRegimeLabel() {
            const el = document.getElementById('multiplier-value');
            if (!el) return;
            el.title = currentMultiplier() <= 1
                ? 'Calibrated default cost basis'
                : currentMultiplier().toFixed(2).replace(/0$/, '') + '\u00d7 the calibrated default cost basis';
        }

        // Render the cohort breakdown from the scope-appropriate forecast
        // baseline. Extracted so selectScope can call it without duplicating
        // the all/city baseline-picking logic.
        function renderCohortFromForecast(scope) {
            if (!forecastData) return;
            const roads = forecastData.find(f => f.resource_type === 'roads');
            const src = roads && ((scope !== 'all' && roads.city_baseline) || roads.baseline);
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
        // syncScopeToggle marks the scope toggle's button for currentScope.
        function syncScopeToggle() {
            markPressed(queryAll('#scope-toggle button'), b => b.dataset.scope === currentScope);
        }
        function selectScope(scope, opts) {
            const btn = document.querySelector('#scope-toggle button[data-scope="' + scope + '"]');
            if (!btn) return;
            currentScope = scope;
            syncScopeToggle();

            updateHexGrid();

            if (scenarioData) {
                renderMaintainToggles();
                const scenarios = financialScenarios(scope);
                if (scenarios) {
                    deferCustomScenario = true;
                    try {
                        renderFinancials(scenarios, scope);
                    } finally {
                        deferCustomScenario = false;
                    }
                }
                // forecastSeedReady mirrors renderFinancialsFromCache: without
                // this city's seed, FORECAST_SEED and the controls still hold the
                // PREVIOUS city's parameters, so the sim would draw a custom line
                // from the wrong city. Fall back to this city's static baseline.
                if (wasmReady && forecastSeedReady) {
                    runCustomScenario();
                } else {
                    renderCohortFromForecast(scope);
                }
            }

            // Compare and Aggregate read currentScope through extractMetrics,
            // so they follow the toggle as they follow units and tag filters.
            rerenderTagScopedTabs();

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
        // toggle behind a single settings affordance. A disclosure (button
        // with aria-expanded), not an ARIA menu: it holds form controls, not
        // menu items. Closes on outside click or Escape.
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
        let FORECAST_SEED = PVMT_CONFIG.forecast_seed;

        loadFinancials();

        let wasmReady = false;
        let chartRefs = { pci: null, backlog: null, spending: null };
        let customActive = false;
        // Set by a caller that will run runCustomScenario() itself immediately
        // after renderFinancials returns, so the renderFinancials wrapper below
        // does not run a second, identical simulation. Each runCustomScenario is
        // TWO WASM sims (the year-1 baseline inside getControlValues plus the
        // result), so the duplicate was four sims per render, not two.
        //
        // The wrapper cannot simply own the re-run instead: it fires INSIDE
        // renderFinancials, i.e. before renderFinancialsFromCache's
        // updateSummaryCards(baseline) write, so dropping the caller's call
        // would leave baseline numbers in the headline tiles under a
        // custom-scenario chart line.
        let deferCustomScenario = false;

        // The shipped track (min/max/step) of each seeded range input, captured
        // the first time that input is seeded. Every sync restores it before
        // deciding whether this city's seed needs the track widened. Without the
        // restore the widening ACCUMULATES across city switches: a city seeded
        // at 4x leaves max=4 for every city after it, and a city seeded at 0.83
        // leaves a step ladder of 0.83/0.88/.../0.98/1.03 on which the default
        // 1.0 is not reachable at all.
        const shippedTracks = new WeakMap();

        // Whether v lands on the input's step ladder, which starts at its min
        // (HTML: the step base is the min content attribute). Computed with a
        // tolerance because the steps are not binary-exact — (1.33 - 1) / 0.05
        // is 6.6000000000000005, not 6.6. A non-numeric step is step="any",
        // which accepts everything.
        /** @param {number} v @param {number} base @param {number} step @returns {boolean} */
        function isStepAligned(v, base, step) {
            if (!Number.isFinite(step) || step <= 0) return true;
            const n = (v - base) / step;
            return Math.abs(n - Math.round(n)) < 1e-6;
        }

        // The markup carries aria-valuenow alongside the native value, and AT
        // honours the ARIA value over the native one. Nothing kept it in step,
        // so a screen reader read the shipped default (85 / 1x) for the life of
        // the page no matter where the slider actually sat. Call this wherever
        // a slider's value changes: at seed time and on every drag.
        /** @param {HTMLInputElement} slider */
        function syncAriaValueNow(slider) {
            slider.setAttribute('aria-valuenow', slider.value);
        }

        // seedRangeInput sets a range input to a seeded value EXACTLY, and
        // returns the value the control ended up holding.
        //
        // There are two ways a range input silently refuses a value, and both
        // put the interactive line on a different basis from the static export
        // lines drawn on the same chart — the divergence seeding exists to
        // prevent:
        //
        //   - Out of [min,max]: the UA clamps. Config validation is wider than
        //     the markup's track on both sliders (initial_pci is legal down to
        //     0 against a 50 floor; cost_multiplier up to 5 against a 2.5
        //     ceiling), so widen the track to admit the seed rather than
        //     clamping the seed into the track.
        //   - Step-mismatched: the UA rounds to the nearest step-aligned value
        //     (HTML, the Range state's value sanitization algorithm). A legal
        //     cost_multiplier of 1.33 on step=0.05 becomes 1.35 in a browser while
        //     costRegimeText keeps printing "1.33x". jsdom implements the clamp
        //     but NOT the snap, so no amount of reading back .value in a test
        //     can see it — drop to step="any" whenever the seed is off-ladder.
        //
        // step="any" costs this: on a city whose configured multiplier is off the
        // ladder, dragging then yields arbitrary-precision values, and the label
        // (toFixed(2)) can round away up to 0.005x of what the sim prices at.
        // That is the better trade — the alternative silently misprices the
        // SEEDED value for every visitor at page load, and the obvious way to
        // keep a 0.05 ladder while admitting 1.33 is to move min up to 1.03,
        // which makes the default 1.0 unreachable all over again.
        /** @param {HTMLInputElement} slider @param {number} v @returns {number} */
        function seedRangeInput(slider, v) {
            let track = shippedTracks.get(slider);
            if (!track) {
                track = { min: slider.min, max: slider.max, step: slider.step };
                shippedTracks.set(slider, track);
            }
            slider.min = track.min;
            slider.max = track.max;
            slider.step = track.step;

            if (v > parseFloat(slider.max)) slider.max = String(v);
            if (v < parseFloat(slider.min)) slider.min = String(v);
            if (!isStepAligned(v, parseFloat(slider.min), parseFloat(track.step))) slider.step = 'any';

            slider.setAttribute('aria-valuemin', slider.min);
            slider.setAttribute('aria-valuemax', slider.max);
            slider.value = String(v);
            syncAriaValueNow(slider);
            // Read back rather than returning v: the caller labels the control
            // with this, and the label has to say what the control actually
            // holds. If a UA still refuses the value, a visibly wrong label is
            // the bug surfacing, not a second bug on top of it.
            return parseFloat(slider.value);
        }

        // Config validation accepts initial_pci anywhere in 0-100 and
        // NormalizeForecast only replaces <=0 or >100, so a city configured at
        // 45 reaches the seed intact — while the markup's track floors at 50.
        // Clamping it started the interactive line, and the End PCI / N-Year
        // Spend / N-Year Deficit tiles it overwrites, from a condition the city
        // never configured. Widen, don't clamp — the same call the multiplier
        // slider makes. Like the multiplier slider it always goes through
        // seedRangeInput, which restores the shipped track: a seed with no
        // usable initial_pci falls back to the markup default (seededPCI)
        // rather than keeping the previous city's track and value.
        function syncPCISliderFromSeed() {
            const slider = inputById('pci-slider');
            if (!slider) return;
            const v = seedRangeInput(slider, seededPCI());
            document.getElementById('pci-value').textContent = String(v);
        }
        syncPCISliderFromSeed();

        // Cost multiplier: scales every cost tier. The default is 1.0 — the
        // shipped tiers plus the treatment cycle are already calibrated against
        // real municipal hold-steady spending, so no load is applied on top (see
        // Methodology). Seeded per city from forecast_seed.json so
        // the interactive line prices work the same way the static export lines
        // did — the same reason the treatment cycle is seeded rather than
        // hardcoded. A missing or non-finite value falls back to 1.0 (bare)
        // rather than to the config default, matching the WASM bridge's clamp:
        // if the seed is stale, showing construction-only dollars is recoverable,
        // and showing $0 is not.
        // The markup's 1-2.5 range covers every plausible load stack, but config
        // validation accepts cost_multiplier up to 5, and the seed is the source
        // of truth: see seedRangeInput for why the track is widened (and
        // un-stepped) to admit it rather than the other way round.
        function syncMultiplierSliderFromSeed() {
            const slider = inputById('multiplier-slider');
            if (!slider) return;
            seedRangeInput(slider, seededMultiplier());
            renderMultiplierValue();
        }

        // seededMultiplier is the multiplier the seed prices at (see above).
        function seededMultiplier() {
            const seeded = FORECAST_SEED && Number(FORECAST_SEED.cost_multiplier);
            return Number.isFinite(seeded) && seeded > 0 ? seeded : 1;
        }

        function renderMultiplierValue() {
            const slider = inputById('multiplier-slider');
            if (!slider) return;
            document.getElementById('multiplier-value').textContent =
                parseFloat(slider.value).toFixed(2).replace(/0$/, '') + '\u00d7';
        }

        // currentMultiplier is what getControlValues ships to the WASM bridge.
        function currentMultiplier() {
            const slider = inputById('multiplier-slider');
            const v = slider ? parseFloat(slider.value) : NaN;
            return Number.isFinite(v) && v > 0 ? v : 1;
        }
        syncMultiplierSliderFromSeed();

        /** @param {number} v */
        function roundCents(v) { return Math.round(v * 100) / 100; }

        // validateTierInputs checks the Advanced-options tier table before it
        // reaches the simulator, against the bounds the simulator enforces
        // (forecast.Inputs.Validate): finite numbers, a cost above zero, PCI
        // bounds in 0-100 with min below max, and no two tiers covering the
        // same PCI. Invalid fields are marked and the reasons shown next to
        // the table, in the units on screen; it returns false so the sim is
        // not run on them, and a bad value never surfaces as the page-level
        // error banner.
        function validateTierInputs() {
            const errs = [];
            const ranges = [];
            queryAll('#tier-inputs input').forEach(el => el.removeAttribute('aria-invalid'));
            (FORECAST_SEED.cost_tiers || []).forEach((t, i) => {
                const name = t.label || 'Tier ' + (i + 1);
                const get = (field) => queryInput('[data-tier="' + i + '"][data-field="' + field + '"]');
                const minEl = get('min_pci'), maxEl = get('max_pci'), costEl = get('cost_per_sqm');
                if (!minEl || !maxEl || !costEl) return;
                const bad = (el, msg) => { el.setAttribute('aria-invalid', 'true'); errs.push(name + ': ' + msg); };
                const min = parseFloat(minEl.value), max = parseFloat(maxEl.value), cost = parseFloat(costEl.value);
                if (costEl.value !== '' && !(Number.isFinite(cost) && cost > 0)) bad(costEl, 'cost must be more than $0/' + areaUnit());
                const minOK = minEl.value === '' || (Number.isFinite(min) && min >= 0 && min <= 100);
                const maxOK = maxEl.value === '' || (Number.isFinite(max) && max >= 0 && max <= 100);
                if (!minOK) bad(minEl, 'minimum PCI must be between 0 and 100');
                if (!maxOK) bad(maxEl, 'maximum PCI must be between 0 and 100');
                if (!minOK || !maxOK) return;
                const lo = minEl.value === '' ? t.min_pci : min;
                const hi = maxEl.value === '' ? t.max_pci : (max >= 100 ? Math.max(100, t.max_pci) : max);
                if (lo >= hi) { bad(minEl, 'minimum PCI must be below its maximum'); return; }
                ranges.push({ name, lo, hi, el: minEl });
            });
            ranges.sort((a, b) => a.lo - b.lo);
            for (let k = 1; k < ranges.length; k++) {
                if (ranges[k].lo < ranges[k - 1].hi) {
                    ranges[k].el.setAttribute('aria-invalid', 'true');
                    errs.push(ranges[k - 1].name + ' and ' + ranges[k].name + ' overlap (PCI ' + ranges[k].lo + '\u2013' + Math.min(100, ranges[k - 1].hi) + ')');
                }
            }
            const box = document.getElementById('tier-errors');
            if (box) {
                box.hidden = errs.length === 0;
                box.textContent = errs.length ? 'Not run: ' + errs.join('; ') + '.' : '';
            }
            markForecastStale(errs.length > 0);
            return errs.length === 0;
        }

        // markForecastStale dims the Financials figures the interactive
        // forecast draws. While the tier inputs are invalid it is not run, so
        // they still show the last valid run: dim them, and say why. The list
        // is inline, not a const: loadFinancials calls this during boot,
        // before a const declared here would be initialised.
        function markForecastStale(stale) {
            ['solvency-headline', 'headlines', 'cohort-section', 'charts'].forEach(id => {
                const el = document.getElementById(id);
                if (!el) return;
                el.classList.toggle('forecast-stale', stale);
                if (stale) el.setAttribute('title', 'Not updated: fix the cost tiers under Advanced options');
                else el.removeAttribute('title');
            });
        }

        function rebuildTierInputs() {
            const tierDiv = document.getElementById('tier-inputs');
            tierDiv.innerHTML = '';
            (FORECAST_SEED.cost_tiers || []).forEach((t, i) => {
                const row = document.createElement('div');
                row.className = 'tier-row';
                // cost_per_sqm is stored/simulated in $/sq m (metric internal); show
                // it in the live unit so the value and its label agree. runCustomScenario
                // converts the entered value back to $/sq m before it reaches the sim.
                // data-sqm carries the canonical $/sq m alongside the displayed value so
                // a units toggle can re-derive the display from it exactly (see
                // syncTierCostInputsToUnits) instead of rounding the visible number.
                //
                // The shown cost is rounded to cents; the sim reads data-sqm, so
                // rounding never moves the numbers. The open top bound (max_pci
                // 101, an internal sentinel so PCI 100 falls inside) shows as 100
                // and reads back as the seed's own top.
                const costDisplay = costPerAreaToDisplay(t.cost_per_sqm);
                const name = esc(t.label || 'Tier ' + (i+1));
                row.innerHTML =
                    '<span>' + name + '</span>' +
                    '<input type="number" data-tier="' + i + '" data-field="min_pci" value="' + t.min_pci + '" step="1" min="0" max="100" aria-label="' + name + ' minimum PCI">' +
                    '<input type="number" data-tier="' + i + '" data-field="max_pci" value="' + Math.min(100, t.max_pci) + '" step="1" min="0" max="100" aria-label="' + name + ' maximum PCI">' +
                    '<input type="number" data-tier="' + i + '" data-field="cost_per_sqm" data-sqm="' + t.cost_per_sqm + '" value="' + roundCents(costDisplay) + '" step="0.01" min="0" aria-label="' + name + ' cost per ' + areaUnit() + '">';
                tierDiv.appendChild(row);
            });
            // Label the cost column header with the live unit (the input values are
            // shown in $/<live area unit>, converted back to $/sq m on read).
            document.getElementById('tier-cost-th').textContent = '$/' + areaUnit();
            queryAll('#tier-inputs input').forEach(el => {
                const inp = /** @type {HTMLInputElement} */ (el);
                if (inp.dataset.field === 'cost_per_sqm') {
                    // Keep data-sqm in step with what the user types, in the same
                    // $/sq m basis getControlValues converts to.
                    inp.addEventListener('input', () => {
                        const v = parseFloat(inp.value);
                        if (Number.isFinite(v)) {
                            inp.dataset.sqm = String(costPerAreaFromDisplay(v));
                        }
                    });
                }
                inp.addEventListener('input', () => {
                    runCustomScenario();
                    Router.replaceSoon();
                });
                // A typed value is committed on blur or Enter.
                inp.addEventListener('change', () => Router.flushSoon());
            });
        }

        function onWasmReady() {
            wasmReady = true;
            document.getElementById('forecast-controls').classList.add('visible');
            // Run initial simulation to populate cohort table. When the reader
            // maintains part of the network, the reference lines drawn from
            // scenarios.json until now cover all of it: redraw them too.
            renderMaintainToggles();
            if (scenarioData && forecastSeedReady && effectiveMaintained().length !== presentTypes().length) {
                renderFinancialsFromCache();
            } else {
                runCustomScenario();
            }
            // Populate tier inputs from the fallback seed when this city's has
            // not landed: once it has, loadFinancials built them, with any
            // linked tier costs, and a rebuild would drop those.
            if (!forecastSeedReady) rebuildTierInputs();
        }

        // Display names for the maintained-network control and its summary.
        /** @type {Record<string, string>} */
        const MAINTAIN_LABELS = { roads: 'Streets', parking: 'Parking lots', sidewalks: 'Sidewalks' };
        /** @type {Record<string, string>} */
        const MAINTAIN_SHORT = { roads: 'streets', parking: 'lots', sidewalks: 'sidewalks' };

        // cohortType is a seed cohort's resource type, which the export emits on
        // every CohortSeed so no page has to guess it from
        // the class name.
        /** @param {{resource_type: string}} c */
        function cohortType(c) {
            return c.resource_type;
        }

        // seedCohortsFor is the seed's cohorts of one resource type for a
        // scope. 'all' is every jurisdiction's. 'city' is what the city
        // maintains of that resource, which is none of it when the city has
        // city-scope cohorts but this resource does not. A city with no
        // city-scope cohorts at all has only the all-jurisdiction view. This
        // is the rule forecast.json and scenarios.json are built with (Go:
        // buildResourceForecast, collectCohortSeeds).
        /** @param {string} type @param {string} scope */
        function seedCohortsFor(type, scope) {
            const of = (/** @type {any[]} */ list) => (list || []).filter(c => cohortType(c) === type);
            const city = FORECAST_SEED.city_cohorts || [];
            return of(scope === 'all' || city.length === 0 ? FORECAST_SEED.cohorts : city);
        }

        // seedTypes is the resource types the seed has cohorts for, in seed order.
        function seedTypes() {
            return Array.from(new Set((FORECAST_SEED.cohorts || []).concat(FORECAST_SEED.city_cohorts || []).map(cohortType)));
        }

        function activeScopeName() {
            const activeScope = queryEl('#scope-toggle button.active');
            return activeScope ? activeScope.dataset.scope : 'city';
        }

        // scopeSeedCohorts is the seed cohorts of the active scope, resource
        // by resource (seedCohortsFor): the network the budget slider simulates.
        function scopeSeedCohorts() {
            const scope = activeScopeName();
            return seedTypes().flatMap(t => seedCohortsFor(t, scope));
        }

        // headlineRoadCohorts is the roads the solvency headline prices: the
        // city scope's, whatever the scope toggle says, because the cited
        // budget it is measured against pays for city-maintained streets.
        function headlineRoadCohorts() {
            return seedCohortsFor('roads', 'city');
        }

        // sliderRoadsAreWider reports that the slider's streets are every
        // jurisdiction's while the headline's are the city's own, so the two
        // hold-steady budgets on the page are different figures and the
        // slider's must say so (renderBudgetSlider).
        function sliderRoadsAreWider() {
            return activeScopeName() === 'all' &&
                (FORECAST_SEED.city_cohorts || []).some(c => cohortType(c) === 'roads');
        }

        // presentTypes is the resource types this city's seed has cohorts for.
        function presentTypes() {
            const have = new Set(scopeSeedCohorts().map(cohortType));
            return MAINTAIN_TYPES.filter(t => have.has(t));
        }

        // effectiveMaintained is the maintained set this city can honour: the
        // choice narrowed to the types it has, else the default, else all of
        // them. A ?maintain= naming only absent types can't empty the forecast.
        function effectiveMaintained() {
            const have = presentTypes();
            for (const want of [maintained, DEFAULT_MAINTAIN, have]) {
                const got = want.filter(t => have.includes(t));
                if (got.length) return got;
            }
            return DEFAULT_MAINTAIN.slice();
        }

        // renderMaintainToggles draws one checkbox per resource type the city
        // has, in the map's layer-toggle style. The last checked box is
        // disabled: the forecast always maintains something.
        function renderMaintainToggles() {
            const wrap = document.getElementById('maintain-toggles');
            if (!wrap) return;
            const have = presentTypes();
            const on = effectiveMaintained();
            document.getElementById('maintain-row').hidden = have.length < 2;
            wrap.innerHTML = have.map(t =>
                '<label><input type="checkbox" data-type="' + t + '"' + (on.includes(t) ? ' checked' : '') +
                (on.length === 1 && on.includes(t) ? ' disabled' : '') + '>' +
                '<span class="legend-dot" style="background:' + layerColors[t] + '"></span>' + MAINTAIN_LABELS[t] + '</label>'
            ).join('');
        }

        // referenceCache memoizes the re-simulated reference lines (see
        // financialScenarios) by their inputs.
        const referenceCache = { key: '', value: null };

        // financialScenarios is the reference set (baseline and funding levels)
        // the Financials charts draw for `scope`. scenarios.json covers every
        // resource, so when the reader maintains only part of the network the
        // set is re-simulated over those cohorts, at the seeded settings the
        // export used — the reference lines stay fixed while the sliders move,
        // as before; only "Your budget" follows them. Without the simulator or
        // this city's seed, the exported set stands in.
        /** @param {string} scope */
        function financialScenarios(scope) {
            const exported = scenariosForScope(scenarioData, scope);
            if (!exported || !wasmReady || !forecastSeedReady) return exported;
            const types = effectiveMaintained();
            if (types.length === presentTypes().length) return exported;
            const cohorts = scopeSeedCohorts().filter(c => types.includes(cohortType(c)));
            if (cohorts.length === 0) return exported;
            const input = {
                area: cohorts.reduce((sum, c) => sum + (c.area || 0), 0),
                initial_pci: FORECAST_SEED.initial_pci,
                decay_rate: FORECAST_SEED.decay_rate,
                growth_rate: FORECAST_SEED.growth_rate,
                horizon_years: FORECAST_SEED.horizon_years,
                treatment_cycle_years: FORECAST_SEED.treatment_cycle_years,
                cost_tiers: FORECAST_SEED.cost_tiers || [],
                cost_multiplier: Number(FORECAST_SEED.cost_multiplier) > 0 ? Number(FORECAST_SEED.cost_multiplier) : 1,
                annual_budget: 0,
                strategy: 'worst_first',
                cohorts: cohorts,
                scenarios: true,
            };
            const key = JSON.stringify(input);
            if (key !== referenceCache.key) {
                const r = safeSimulate(input);
                referenceCache.key = key;
                referenceCache.value = r && r.scenarios && r.scenarios.length ? r.scenarios : null;
            }
            return referenceCache.value || exported;
        }

        // readTierInputs reads the cost tiers from the Advanced-options inputs,
        // falling back to the seed field by field.
        function readTierInputs() {
            return (FORECAST_SEED.cost_tiers || []).map((t, i) => {
                const minInput = queryInput('[data-tier="' + i + '"][data-field="min_pci"]');
                const maxInput = queryInput('[data-tier="' + i + '"][data-field="max_pci"]');
                const costInput = queryInput('[data-tier="' + i + '"][data-field="cost_per_sqm"]');
                // A cleared input yields parseFloat('') === NaN, which serializes
                // to JSON null and zeroes the tier in the WASM sim. Fall back to
                // the seed value whenever the parse isn't a finite number.
                const minV = minInput ? parseFloat(minInput.value) : NaN;
                const maxRaw = maxInput ? parseFloat(maxInput.value) : NaN;
                // 100 is shown for the open top bound; reading it back keeps the
                // seed's own top (the 101 sentinel, where it uses one).
                const maxV = maxRaw >= 100 ? Math.max(100, t.max_pci) : maxRaw;
                // The cost is read from data-sqm, the exact $/sq m the input
                // stands for (the sim's and the static lines' unit), kept in
                // step with typing; the shown value is rounded to cents, so
                // re-parsing it would move the custom line off the seed. A
                // cleared box falls back to the seed below.
                const costV = (costInput && costInput.value !== '') ? parseFloat(costInput.dataset.sqm || '') : NaN;
                return {
                    min_pci: Number.isFinite(minV) ? minV : t.min_pci,
                    max_pci: Number.isFinite(maxV) ? maxV : t.max_pci,
                    cost_per_sqm: Number.isFinite(costV) ? costV : t.cost_per_sqm,
                    label: t.label
                };
            });
        }

        // seededPCI is the Initial PCI the slider is seeded at: the seed's,
        // else (no seed yet, or one without a usable initial_pci) the markup's.
        function seededPCI() {
            return FORECAST_SEED && typeof FORECAST_SEED.initial_pci === 'number' && Number.isFinite(FORECAST_SEED.initial_pci)
                ? Math.round(FORECAST_SEED.initial_pci)
                : parseInt(inputById('pci-slider').defaultValue);
        }

        // currentModel is the model settings on the controls, each null where
        // it is the seed's (see pendingModel).
        /** @returns {ModelSettings} */
        function currentModel() {
            const pci = parseInt(inputById('pci-slider').value);
            const cost = currentMultiplier();
            const seedTiers = FORECAST_SEED.cost_tiers || [];
            const tiers = readTierInputs().map(t => ({ min_pci: t.min_pci, max_pci: t.max_pci, cost_per_sqm: t.cost_per_sqm }));
            const tiersSeeded = tiers.every((t, i) => t.min_pci === seedTiers[i].min_pci &&
                t.max_pci === seedTiers[i].max_pci && t.cost_per_sqm === seedTiers[i].cost_per_sqm);
            return {
                strategy: currentStrategy !== DEFAULT_STRATEGY ? currentStrategy : null,
                pci: pci !== seededPCI() ? pci : null,
                cost: Math.abs(cost - seededMultiplier()) > 1e-9 ? cost : null,
                tiers: tiersSeeded ? null : tiers,
            };
        }

        // applyModel resets the controls to the seed and puts m's settings over
        // it. Linked tiers are applied only when there are as many as the seed
        // has, since each input row is one of the seed's tiers.
        /** @param {ModelSettings} m */
        function applyModel(m) {
            syncPCISliderFromSeed();
            syncMultiplierSliderFromSeed();
            rebuildTierInputs();
            setStrategy(m.strategy || DEFAULT_STRATEGY);
            if (m.pci != null) {
                document.getElementById('pci-value').textContent = String(seedRangeInput(inputById('pci-slider'), m.pci));
            }
            if (m.cost != null) {
                seedRangeInput(inputById('multiplier-slider'), m.cost);
                renderMultiplierValue();
            }
            renderMultiplierRegimeLabel();
            const seedTiers = FORECAST_SEED.cost_tiers || [];
            if (m.tiers && m.tiers.length === seedTiers.length) {
                m.tiers.forEach((t, i) => {
                    const get = (field) => queryInput('[data-tier="' + i + '"][data-field="' + field + '"]');
                    const minEl = get('min_pci'), maxEl = get('max_pci'), costEl = get('cost_per_sqm');
                    if (!minEl || !maxEl || !costEl) return;
                    minEl.value = String(t.min_pci);
                    maxEl.value = String(Math.min(100, t.max_pci));
                    costEl.dataset.sqm = String(t.cost_per_sqm);
                    costEl.value = String(roundCents(costPerAreaToDisplay(t.cost_per_sqm)));
                });
            }
        }

        // setStrategy selects a repair strategy and its button.
        /** @param {string} strategy */
        function setStrategy(strategy) {
            currentStrategy = strategy;
            markPressed(queryAll('#strategy-buttons button'), b => b.dataset.strategy === strategy);
        }

        function getControlValues() {
            const initialPCI = parseInt(inputById('pci-slider').value);

            // Read tier values from inputs (advanced), fallback to seed
            const tiers = readTierInputs();

            // Area/cohorts for the active scope: city-scoped data by default,
            // all-jurisdiction data when scope === 'all'.
            const scope = activeScopeName();
            const area = (scope === 'all' || !FORECAST_SEED.city_maintained_area)
                ? FORECAST_SEED.maintained_area
                : FORECAST_SEED.city_maintained_area;
            // Simulate only the maintained part of the network (the cohort
            // filter computeLiveSolvency applies for the headline). The bridge
            // reads `area` only when there are no cohorts (a fresh DB).
            const types = effectiveMaintained();
            const seedCohorts = scopeSeedCohorts().filter(c => types.includes(cohortType(c)));

            return {
                area: area,
                initial_pci: initialPCI,
                decay_rate: FORECAST_SEED.decay_rate,
                growth_rate: FORECAST_SEED.growth_rate,
                horizon_years: FORECAST_SEED.horizon_years,
                treatment_cycle_years: FORECAST_SEED.treatment_cycle_years,
                cost_tiers: tiers,
                cost_multiplier: currentMultiplier(),
                annual_budget: budgetDollars,
                strategy: currentStrategy,
                cohorts: seedCohorts
            };
        }

        // memoSolvency runs a solvency probe of input through the bridge (the
        // worst-first hold-steady budget, plus the insolvency year at
        // input.annual_budget) and caches the last answer, so a budget drag that
        // leaves the probe unchanged costs no bisection.
        /** @param {{key: string, value: any}} cache @param {any} probe */
        function memoSolvency(cache, probe) {
            const key = JSON.stringify(probe);
            if (key !== cache.key) {
                cache.key = key;
                cache.value = safeSimulate(Object.assign({}, probe, { solvency: true }));
            }
            return cache.value;
        }
        const holdCache = { key: '', value: null };
        const headlineCache = { key: '', value: null };

        // holdSteadyBudget is the budget that holds the network the slider
        // simulates (the seeded cohorts of the active scope) steady at the live
        // settings: the slider's "Hold steady" anchor.
        function holdSteadyBudget(input) {
            const r = memoSolvency(holdCache, Object.assign({}, input, { annual_budget: 0, strategy: 'worst_first' }));
            return r && r.break_even_budget > 0 ? r.break_even_budget : 0;
        }

        // todayBudget is the city's cited annual street budget, 0 when none.
        function todayBudget() {
            const roads = forecastData ? forecastData.find(f => f.resource_type === 'roads') : null;
            return roads && roads.current_budget > 0 ? roads.current_budget : 0;
        }

        // liveSolvency recomputes the roads-only solvency headline at the live
        // settings: the same cohorts, horizon and budget
        // forecast.json's roads entry used, so it reproduces the exported figures
        // at the seeded settings and follows Initial PCI, the cost multiplier and
        // the tiers when they move. null when there is no roads forecast.
        function computeLiveSolvency(input) {
            const roads = forecastData ? forecastData.find(f => f.resource_type === 'roads') : null;
            const cohorts = headlineRoadCohorts();
            if (!roads || cohorts.length === 0) return null;
            const current = roads.current_budget > 0 ? roads.current_budget : 0;
            const r = memoSolvency(headlineCache, Object.assign({}, input, {
                cohorts: cohorts,
                area: cohorts.reduce((sum, c) => sum + (c.area || 0), 0),
                annual_budget: current,
                strategy: 'worst_first',
            }));
            if (!r || !(r.break_even_budget > 0)) return null;
            return {
                break_even_budget: r.break_even_budget,
                current_budget: current,
                insolvency_year: current > 0 && r.insolvency_year ? r.insolvency_year : null,
            };
        }

        // applyBudget settles the interactive budget for this run: follows the
        // default until the reader sets one (slider, anchor or ?budget=), then
        // re-ranges the slider and its anchors around it. Returns the dollars to
        // simulate. resetBudget (boot, city switch, back/forward) owns the
        // per-city reset; the guard here only covers a path that skipped it.
        function applyBudget(input) {
            const today = todayBudget();
            const hold = holdSteadyBudget(input);
            if (budgetCity !== DATA_PREFIX) resetBudget(null);
            if (!budgetUserSet) budgetDollars = today > 0 ? today : hold;
            const cap = BUDGET_MAX_ANCHOR_MULT * Math.max(today, hold);
            if (budgetUserSet && cap > 0 && budgetDollars > cap) {
                // Clamp a linked budget, and say so in the address bar.
                budgetDollars = cap;
                Router.replace();
            }
            renderBudgetSlider(today, hold);
            return budgetDollars;
        }

        // The budget slider's track is a percentage of the hold-steady budget,
        // in tenths of a percent: 0 to 150%, with Hold steady always at the same
        // place (two-thirds along) and a notch every 25%. Every cited city spends
        // well under hold steady (31-80% in docs/validation.md), so 150% leaves
        // room to explore catching up without wasting the track. The budget
        // itself stays in dollars (budgetDollars, ?budget=); the range only
        // grows, in 25% steps, to fit a cited or linked budget beyond 150%.
        // budgetScale converts between the two for the input handler.
        const BUDGET_TOP_PCT = 1.5;
        const BUDGET_NOTCH_PCT = 0.25;
        const BUDGET_MAX_NOTCHES = 40;
        /** @type {{base: number, top: number}} */
        let budgetScale = { base: 1, top: BUDGET_TOP_PCT };
        // budgetSnaps is today's budget and hold steady in dollars: a drag that
        // ends within BUDGET_SNAP of one lands on it exactly. (A <datalist>
        // would snap natively, but Chromium draws its own ticks off our
        // notches and Firefox/Safari don't snap at all.)
        /** @type {number[]} */
        let budgetSnaps = [];
        const BUDGET_SNAP = 0.015;

        // budgetFromSlider is the dollars at a slider position, rounded to a
        // tidy step four significant figures below the hold-steady budget.
        /** @param {number} permille */
        function budgetFromSlider(permille) {
            const dollars = budgetScale.base * permille / 1000;
            for (const v of budgetSnaps) {
                if (Math.abs(dollars - v) <= BUDGET_SNAP * budgetScale.base) return v;
            }
            const step = Math.pow(10, Math.floor(Math.log10(budgetScale.base)) - 3);
            return step > 0 && Number.isFinite(step) ? Math.round(dollars / step) * step : dollars;
        }

        /** @param {number} today @param {number} hold */
        function renderBudgetSlider(today, hold) {
            const slider = inputById('budget-slider');
            const base = hold > 0 ? hold : (today > 0 ? today : Math.max(budgetDollars, 1));
            // Grow past 150% only for a budget beyond it, to the next notch. The
            // 1% slack absorbs the tidy rounding at the top end, so a drag to the
            // end can't widen the range under the pointer.
            const need = Math.max(BUDGET_TOP_PCT, today / base, budgetDollars / base);
            const top = Math.ceil(need / BUDGET_NOTCH_PCT - 0.01) * BUDGET_NOTCH_PCT;
            budgetScale = { base: base, top: top };
            slider.max = String(Math.round(top * 1000));
            slider.step = '1';
            slider.value = String(Math.round(1000 * budgetDollars / base));
            slider.setAttribute('aria-valuemax', slider.max);
            syncAriaValueNow(slider);
            // The thumb against the % notches shows the share of hold steady;
            // a screen reader hears it with the dollars.
            slider.setAttribute('aria-valuetext', fmtMoney(budgetDollars) + ' a year' +
                (hold > 0 ? ', ' + Math.round(100 * budgetDollars / hold) + '% of hold-steady' : ''));
            document.getElementById('budget-value').textContent = fmtMoney(budgetDollars) + '/yr';
            // Name the network when it isn't the headline's (city-maintained
            // streets), so this hold-steady budget can't be read as the one
            // the headline quotes: other resource types, or every
            // jurisdiction's streets under the all-jurisdiction scope.
            const types = effectiveMaintained();
            const wider = types.includes('roads') && sliderRoadsAreWider();
            document.getElementById('budget-share').textContent = (sameMaintain(types, DEFAULT_MAINTAIN) && !wider)
                ? '' : 'for ' + types.map(t => MAINTAIN_SHORT[t]).join(' + ') + (wider ? ', all jurisdictions' : '');

            budgetSnaps = [today, hold].filter(v => v > 0);

            // Notches, in % of hold steady; each names its dollars on hover.
            // Without a hold-steady budget the percentages mean nothing: hide.
            const scale = document.getElementById('budget-scale');
            scale.hidden = !(hold > 0);
            queryAll('.budget-notch', scale).forEach(n => n.remove());
            const at = (/** @type {number} */ f) => 'calc(var(--thumb) / 2 + (100% - var(--thumb)) * ' + (f / top) + ')';
            // At most BUDGET_MAX_NOTCHES ticks: a wider range ticks every whole
            // multiple of 25% that keeps it under the cap.
            const notchPct = BUDGET_NOTCH_PCT * Math.max(1, Math.ceil(top / BUDGET_NOTCH_PCT / BUDGET_MAX_NOTCHES));
            const count = Math.round(top / notchPct);
            // A range grown past 150% keeps every tick but labels fewer of them
            // (about seven), always the ends and 100%, so phones don't crowd.
            const every = Math.ceil(count / 6);
            const holdIdx = Math.round(1 / notchPct);
            for (let i = 0; i <= count; i++) {
                const f = i * notchPct;
                const n = document.createElement('span');
                // Labelled: the ends, 100%, and every `every`-th notch that
                // isn't crowding the end label or 100%'s.
                const hold100 = i === holdIdx && Math.abs(f - 1) < 1e-9;
                const quiet = !(i === 0 || i === count || hold100) &&
                    (i % every !== 0 || count - i < every || Math.abs(i - holdIdx) < every);
                n.className = 'budget-notch' + (i === 0 ? ' first' : '') + (i === count ? ' last' : '') + (hold100 ? ' hold' : '') + (quiet ? ' quiet' : '');
                n.style.left = i === 0 ? '0' : (i === count ? '100%' : at(f));
                // 100% is labelled by the hold-steady button (its dollars).
                n.textContent = hold100 ? '' : Math.round(f * 100) + '%';
                n.title = fmtMoney(f * base) + '/yr';
                n.setAttribute('aria-hidden', 'true');
                scale.appendChild(n);
            }
            const holdBtn = buttonById('budget-hold');
            holdBtn.hidden = !(hold > 0);
            holdBtn.dataset.budget = String(hold);
            holdBtn.style.left = at(1);
            queryEl('.num', holdBtn).textContent = fmtMoney(hold);
            holdBtn.setAttribute('aria-label', 'Hold steady, ' + fmtMoney(hold) + ' a year');
            const todayBtn = buttonById('budget-today');
            todayBtn.hidden = !(today > 0);
            todayBtn.dataset.budget = String(today);
            queryEl('.num', todayBtn).textContent = fmtMoney(today);
        }

        function runCustomScenario() {
            if (!wasmReady) return;
            if (!validateTierInputs()) return;
            const input = getControlValues();
            if (!input) return;
            input.annual_budget = applyBudget(input);
            liveSolvency = computeLiveSolvency(input);
            renderSolvencyHeadline();

            const result = safeSimulate(input);
            if (!result) return;

            customActive = true;
            updateCustomScenario(result);
            updateSummaryCards(result, budgetDollars);

            // Update cohort breakdown from WASM result
            if (result.final_cohorts && result.final_cohorts.length > 1) {
                renderCohortBreakdown(result.final_cohorts);
            } else {
                document.getElementById('cohort-section').style.display = 'none';
            }
        }

        // updateSummaryCards fills the headline tiles. budget is the annual
        // budget the result ran at (0 for the static baseline). The spend tile
        // counts only spend that buys condition, which is below budget x years
        // once a class has more money than it can use,
        // so the sub-line names the budget too.
        function updateSummaryCards(result, budget) {
            // Read at the horizon the tiles are labelled with (applyHorizonLabels).
            const horizon = financialsHorizon();
            const years = (result.years || []).slice(0, horizon);
            const at = yearAtHorizon(years, horizon);
            const lastPCI = at ? at.pci : 0;
            const totalSpend = years.reduce((s, y) => s + y.annual_spend, 0);
            const totalDeficit = at ? at.deferred_backlog : 0;

            document.getElementById('hl-end-pci').textContent = String(shownPCI(lastPCI));
            document.getElementById('hl-total-spend').textContent = fmtMoney(totalSpend);
            const budgeted = (budget || 0) * years.length;
            document.getElementById('hl-spend-sub').textContent =
                budgeted > 0 && totalSpend < budgeted * 0.995
                    ? 'Used of ' + fmtMoney(budgeted) + ' budgeted'
                    : 'Spend that buys condition';
            document.getElementById('hl-total-deficit').textContent = fmtMoney(totalDeficit);
            document.getElementById('headlines').style.display = '';
        }

        function updateCustomScenario(result) {
            const customColor = '#10b981';
            const years = result.years || [];

            // Update PCI chart
            if (chartRefs.pci) {
                updateChartDataset(chartRefs.pci, 'Your budget', years.map(y => y.pci), customColor);
            }
            // Update backlog chart
            if (chartRefs.backlog) {
                updateChartDataset(chartRefs.backlog, 'Your budget', years.map(y => y.deferred_backlog), customColor);
            }
            // Update spending chart
            if (chartRefs.spending) {
                let cum = 0;
                const cumData = years.map(y => { cum += y.annual_spend; return cum; });
                updateChartDataset(chartRefs.spending, 'Your budget', cumData, customColor);
            }
            // Update tier chart
            if (chartRefs.tier) {
                const idx = chartRefs.tier.data.datasets.findIndex(ds => ds.label === 'Need at your budget');
                const ds = {
                    label: 'Need at your budget',
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

            // Re-apply custom scenario if active, unless the caller is about to
            // do it itself (see deferCustomScenario).
            if (customActive && wasmReady && !deferCustomScenario) {
                runCustomScenario();
            }
        };

        // Control event bindings. Each listener is bound to its own slider, so it
        // closes over the typed element rather than narrowing e.target.
        const budgetSlider = inputById('budget-slider');
        // The sliders fire on every input event (keyboard repeat included),
        // so they record the URL with Router.replaceSoon; clicks use
        // Router.replace.
        /** @param {number} dollars @param {boolean} [soon] */
        function setBudget(dollars, soon) {
            budgetDollars = dollars;
            budgetUserSet = true;
            runCustomScenario();
            if (soon) Router.replaceSoon();
            else Router.replace();
        }
        budgetSlider.addEventListener('input', () => setBudget(budgetFromSlider(parseFloat(budgetSlider.value) || 0), true));
        flushOnRelease(budgetSlider);
        for (const id of ['budget-today', 'budget-hold']) {
            const btn = buttonById(id);
            btn.addEventListener('click', () => setBudget(parseFloat(btn.dataset.budget || '0') || 0));
        }
        const pciSlider = inputById('pci-slider');
        pciSlider.addEventListener('input', () => {
            document.getElementById('pci-value').textContent = pciSlider.value;
            syncAriaValueNow(pciSlider);
            runCustomScenario();
            Router.replaceSoon();
        });
        flushOnRelease(pciSlider);
        const multiplierSlider = inputById('multiplier-slider');
        multiplierSlider.addEventListener('input', () => {
            renderMultiplierValue();
            renderMultiplierRegimeLabel();
            syncAriaValueNow(multiplierSlider);
            runCustomScenario();
            Router.replaceSoon();
        });
        flushOnRelease(multiplierSlider);
        queryAll('#strategy-buttons button').forEach(btn => {
            btn.addEventListener('click', () => {
                setStrategy(btn.dataset.strategy || DEFAULT_STRATEGY);
                runCustomScenario();
                Router.replace();
            });
        });
        document.getElementById('maintain-toggles').addEventListener('change', () => {
            const checked = queryAll('#maintain-toggles input:checked').map(cb => cb.dataset.type || '');
            maintained = MAINTAIN_TYPES.filter(t => checked.includes(t));
            if (maintained.length === 0) maintained = DEFAULT_MAINTAIN.slice();
            renderFinancialsFromCache();
            Router.replace();
        });
        document.getElementById('advanced-toggle').addEventListener('click', () => {
            const adv = document.getElementById('advanced-controls');
            adv.classList.toggle('visible');
            document.getElementById('advanced-toggle').textContent =
                adv.classList.contains('visible') ? 'Hide advanced options' : 'Advanced options';
        });

        // ── Compare tab ──
        // State vars (compareLoaded/compareCharts/aggregateCharts/
        // allCityDataPromise) are declared+initialized above the applyInitialState
        // IIFE — see the note there — so a deep link does not get its guards wiped.

        // Shared per-city fetch for the Compare and Aggregate tabs. Cache the
        // in-flight Promise (not the resolved data) so two near-simultaneous
        // openings — e.g. a deep-link plus a click — dedupe to one fan-out
        // instead of both observing an undefined data var and re-fetching.
        // loadAllCityData resolves every city's Compare/Aggregate inputs
        // ({slug, name, meta, scenarios, seed, forecast}). A static export ships
        // them in one rollup, compare.json, holding just the slice of each data
        // file those tabs read; a city missing from it
        // reads as missing files, with no banner. `pvmt serve` has no rollup, so
        // a 404 (or a version this page does not know) falls back to fetching
        // the four files per city.
        function loadAllCityData() {
            if (!allCityDataPromise) {
                allCityDataPromise = (async function() {
                    const rollup = await loadJSON('compare.json', { silent404: true });
                    if (rollup && rollup.v === PVMT_CONFIG.compare_version && Array.isArray(rollup.cities)) {
                        /** @type {Map<string, any>} */
                        const bySlug = new Map(rollup.cities.map(c => [c.slug, c]));
                        return CITIES.map(function(city) {
                            const c = bySlug.get(city.slug) || {};
                            return {
                                slug: city.slug, name: city.name,
                                meta: c.meta || null, scenarios: c.scenarios || null,
                                seed: c.seed || null, forecast: c.forecast || null,
                            };
                        });
                    }
                    return Promise.all(CITIES.map(async function(city) {
                        var prefix = 'cities/' + city.slug + '/';
                        const [meta, scenarios, seed, forecast] = await Promise.all([
                            loadJSON(prefix + 'data/meta.json'),
                            loadJSON(prefix + 'data/scenarios.json'),
                            loadJSON(prefix + 'data/forecast_seed.json'),
                            loadJSON(prefix + 'data/forecast.json')
                        ]);
                        return { slug: city.slug, name: city.name, meta, scenarios, seed, forecast };
                    }));
                })();
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

        // scopeCityData filters fetched per-city data to the selected tag
        // filters (AND across dimensions). An empty selection means "all
        // cities". Filtering happens here at the render step — NOT inside
        // loadAllCityData, whose single cached promise must always hold every
        // city so switching tags never serves a stale subset.
        function scopeCityData(cityData) {
            if (Object.keys(selectedTags).length === 0) return cityData;
            return cityData.filter(function(city) {
                return PvmtSelectors.matches(cityTagsForSlug(city.slug), selectedTags);
            });
        }

        async function loadCompareData() {
            if (compareLoaded || CITIES.length === 0) return;
            compareLoaded = true;
            renderCompare(scopeCityData(await loadAllCityData()));
        }

        // Aggregate is NOT one-shot: every call re-renders over the cached
        // all-city data (extractMetrics reads currentScope live).
        async function loadAggregateData() {
            if (CITIES.length === 0) return;
            renderAggregate(scopeCityData(await loadAllCityData()));
        }

        // rerenderTagScopedTabs re-runs whichever of Compare/Aggregate is
        // active after the tag filters or the scope changed. Compare is one-shot
        // (compareLoaded gates its only render), so reset that guard to force a
        // re-render; Aggregate always re-renders. Only the active tab is re-run
        // — the other picks up selectedTags when next opened. renderAggregate
        // recomputes regionHasBothScopes from the filtered set and re-reveals
        // the city/all-jurisdiction toggle accordingly. Shared with popstate,
        // which needs the same guard reset when a history entry changes the
        // filters.
        function rerenderTagScopedTabs() {
            compareLoaded = false;
            var btn = queryEl('.tab-btn.active');
            var id = btn ? btn.dataset.tab : '';
            if (id === 'compare-tab') loadCompareData();
            else if (id === 'aggregate-tab') loadAggregateData();
        }

        // applyTagFilters applies a {dimension: configTag} selection: the city
        // selector is rebuilt to the matching cities and, when the city in view
        // is filtered out, the first visible city is loaded in its place (an
        // empty match shows the no-match panel; the last city's data stays
        // loaded behind it, so clearing the filters returns to it). Then the
        // Compare/Aggregate scope follows.
        /** @param {Record<string, string>} selected */
        function applyTagFilters(selected) {
            selectedTags = selected;
            const sel = selectById('city-select');
            const current = sel ? sel.value : '';
            const picked = rebuildCitySelector(current);
            if (picked && picked !== current) selectCity(picked, { push: false });
            rerenderTagScopedTabs();
        }

        // onTagFilterChange handles any .tag-filter select: apply the filters
        // read from the DOM, then record them in the URL so a filtered view is
        // shareable and survives back/forward like every other selector.
        function onTagFilterChange() {
            applyTagFilters(PvmtSelectors.readFilters());
            Router.push();
        }
        {
            const clear = document.getElementById('no-match-clear');
            if (clear) clear.addEventListener('click', () => {
                PvmtSelectors.writeFilters({});
                onTagFilterChange();
            });
        }

        // --- Units toggle (gear menu) --------------------------------------
        // Display units are a client-side preference; the data is raw metric, so
        // a switch only re-runs the unit-dependent render paths. It touches the
        // map layers as little as possible: the stats panel re-renders from the
        // cached meta, hex tooltips read UNIT_SYSTEM live on hover, and each
        // data-tab re-renders through its own path.
        function syncUnitsToggle() {
            markPressed(queryAll('#units-toggle button'), b => b.dataset.units === UNIT_SYSTEM);
        }
        // Re-derive the cost-tier inputs for the live display unit. The inputs show
        // $/<live area unit> and getControlValues reads UNIT_SYSTEM live, so the
        // number on screen has to move with the unit or the same typed cost would
        // silently change meaning by ~10.76x. rebuildTierInputs() converts too, but
        // it rebuilds from the seed and so discards what the user typed — the bug
        // this path exists to avoid.
        //
        // Reads data-sqm (the canonical $/sq m, kept current by rebuildTierInputs
        // and the input listener) rather than rescaling the displayed number, so
        // this is exactly the value rebuildTierInputs would render — no rounding,
        // and toggling back and forth is an identity, not a slow drift away from
        // the configured cost.
        function syncTierCostInputsToUnits() {
            queryAll('#tier-inputs input[data-field="cost_per_sqm"]').forEach(function (el) {
                const inp = /** @type {HTMLInputElement} */ (el);
                // A cleared or unparsable box stays as the user left it —
                // getControlValues falls back to the seed for it.
                if (!Number.isFinite(parseFloat(inp.value))) return;
                const sqm = parseFloat(inp.dataset.sqm);
                if (!Number.isFinite(sqm)) return;
                inp.value = String(roundCents(costPerAreaToDisplay(sqm)));
                const label = inp.getAttribute('aria-label');
                if (label) inp.setAttribute('aria-label', label.replace(/cost per .*$/, 'cost per ' + areaUnit()));
            });
            // Header follows the values (rebuildTierInputs sets it the same way).
            document.getElementById('tier-cost-th').textContent = '$/' + areaUnit();
        }

        function selectUnits(sys) {
            if ((sys !== 'metric' && sys !== 'imperial') || sys === UNIT_SYSTEM) return;
            UNIT_SYSTEM = sys;
            try { localStorage.setItem('pvmtUnits', sys); } catch (e) { /* storage disabled */ }
            syncUnitsToggle();
            // Stats panel is always visible — re-render it in place (no map
            // rebuild), an open detail panel included.
            refreshCityStats();
            // The Color-by legend's edges are formatted in the display units.
            renderLegend();
            // Financials is loaded once per city and is NOT re-triggered by
            // selectTab, so re-render it now regardless of the active tab. Render
            // from the cached data rather than calling loadFinancials(): a display
            // preference must not refetch, and must not reset the PCI slider or
            // the cost-tier inputs the user has typed into. The tier inputs are
            // re-derived (not rebuilt) first, from their canonical $/sq m, so
            // runCustomScenario below simulates the same costs it did a moment ago.
            syncTierCostInputsToUnits();
            renderFinancialsFromCache();
            // Materials reloads unconditionally on tab open, so only the active
            // tab needs an immediate reload.
            reloadMaterialsIfActive();
            // Compare is a one-shot (compareLoaded gates its only render): reset the
            // guard so a later reopen re-renders in the new units, and re-render now
            // if Compare/Aggregate is the active tab. Both read the cached all-city
            // data — no refetch. Mirrors rerenderTagScopedTabs.
            compareLoaded = false;
            var active = queryEl('.tab-btn.active');
            var id = active ? active.dataset.tab : '';
            if (id === 'compare-tab') loadCompareData();
            else if (id === 'aggregate-tab') loadAggregateData();
        }
        queryAll('#units-toggle button').forEach(function (b) {
            b.addEventListener('click', function () { selectUnits(b.dataset.units); });
        });
        syncUnitsToggle();

        // scopeAreaLarge is the maintained area, in display large-area units,
        // that a city's scope-selected forecast dollars and PCI cover:
        // seed.city_maintained_area under the city scope, else
        // seed.maintained_area. Those sum the resources as the forecast prices
        // them (the cohort basis), so dollars and area share one basis. It is
        // the denominator of every per-area figure and the weight of the
        // area-weighted PCI on Compare and Aggregate. m.totalAreaLarge (the
        // meta.stats sum) is the same basis but always all owners, so under a
        // city-scope numerator it would understate the ratio for a city with a
        // lot of state- or county-owned network. It stands in only for a city
        // whose seed carries no area.
        function scopeAreaLarge(city, m) {
            var sqm = 0;
            if (city.seed) {
                sqm = (currentScope === 'city' && city.seed.city_maintained_area)
                    ? city.seed.city_maintained_area
                    : (city.seed.maintained_area || 0);
            }
            return sqm > 0 ? toAreaLarge(sqm) : m.totalAreaLarge;
        }

        // defaultPCIMark flags a city whose condition is the default PCI
        // because its config cites no survey (forecast.json's
        // initial_pci_source "default"): its
        // hold-steady budget and solvency score rest on an assumed starting
        // condition.
        const DEFAULT_PCI_NOTE = 'Condition assumed: no cited pavement survey, so the forecast starts at the default PCI';
        /** @param {{defaultPCI?: boolean}} m */
        function defaultPCIMark(m) {
            return m && m.defaultPCI
                ? ' <span class="default-pci" title="' + DEFAULT_PCI_NOTE + '" aria-label="' + DEFAULT_PCI_NOTE + '">†</span>'
                : '';
        }

        function extractMetrics(city) {
            var m = { name: city.name, slug: city.slug };
            m.missing = missingLayers(city.meta);
            m.partial = partialLayers(city.meta);
            // unranked: the layers a ranking that reads them cannot trust.
            m.unranked = m.missing.concat(m.partial);

            // Per-type area from meta
            var statByType = {};
            if (city.meta && city.meta.stats) {
                city.meta.stats.forEach(function(st) { statByType[st.resource_type] = st; });
                m.totalAreaLarge = city.meta.stats.reduce(function(s, st) { return s + toAreaLarge(st.area || 0); }, 0);
                m.totalSqM = city.meta.stats.reduce(function(s, st) { return s + (st.area || 0); }, 0);
            } else {
                m.totalAreaLarge = 0; m.totalSqM = 0;
            }
            m.roadsAreaLarge = toAreaLarge(statByType.roads ? statByType.roads.area || 0 : 0);
            m.parkingAreaLarge = toAreaLarge(statByType.parking ? statByType.parking.area || 0 : 0);
            m.sidewalksAreaLarge = toAreaLarge(statByType.sidewalks ? statByType.sidewalks.area || 0 : 0);
            m.roadsSqM = statByType.roads ? statByType.roads.area : 0;
            m.parkingSqM = statByType.parking ? statByType.parking.area : 0;
            m.sidewalksSqM = statByType.sidewalks ? statByType.sidewalks.area : 0;
            // Estimated parking spaces (lots + garages); 0 when the city was
            // computed before the estimate existed, which the leaderboard
            // category filters out rather than ranking at zero.
            m.parkingSpaces = statByType.parking ? (statByType.parking.estimated_spaces || 0) : 0;

            // City area and pct paved from meta
            m.cityAreaLarge = toAreaLarge(city.meta && city.meta.city_area ? city.meta.city_area : 0);
            m.pctPaved = city.meta && city.meta.paved_share ? city.meta.paved_share * 100 : 0;
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
                    if (cohortType(c) === 'roads') {
                        m.cohorts[c.classification] = c.area;
                    }
                });
            }
            if (city.seed && city.seed.city_maintained_area && city.seed.maintained_area) {
                m.cityPct = city.seed.city_maintained_area / city.seed.maintained_area * 100;
            }

            // Blended decay (area-weighted) over the active scope's cohorts:
            // city_cohorts under the city scope, the all-jurisdiction cohorts
            // otherwise or when the city has none (seedCohortsFor's rule).
            m.blendedDecay = 0;
            var decayCohorts = city.seed
                ? ((currentScope === 'city' && city.seed.city_cohorts && city.seed.city_cohorts.length > 0)
                    ? city.seed.city_cohorts
                    : city.seed.cohorts)
                : null;
            if (decayCohorts) {
                var totalArea = 0, weightedDecay = 0;
                decayCohorts.forEach(function(c) {
                    totalArea += c.area;
                    weightedDecay += c.area * c.decay_rate;
                });
                m.blendedDecay = totalArea > 0 ? weightedDecay / totalArea : 0;
            }

            // Scenarios baseline for the active scope. scenarios.json is
            // scope-keyed ({city, all, summary}); mirror loadFinancials'
            // selection and honor currentScope (driven by the ?scope= param).
            m.baselineYears = [];
            var scenarios = city.scenarios
                ? (scenariosForScope(city.scenarios, currentScope) || null)
                : null;
            var baseline = findBaseline(scenarios);
            if (baseline) {
                var years = baseline.years || [];
                m.baselineYears = years;
                m.year1Need = years.length > 0 ? years[0].annual_need : 0;
                m.hasForecasts = true;
            } else {
                m.year1Need = 0;
                m.hasForecasts = false;
            }
            // This city's own horizon. The figures at the horizon
            // (horizonNeed, horizonBacklog, horizonBacklogPerAreaUnit) are
            // filled in by applyRegionHorizon once the set of cities is known.
            m.horizon = m.hasForecasts ? forecastHorizon(city.seed, m.baselineYears) : 0;
            // Per-area figures divide the scope-selected dollars by the area in
            // the same scope.
            m.scopeAreaLarge = scopeAreaLarge(city, m);
            // The year-1 need prices the year-1 area (the scope's area after
            // one year of growth), so it is the per-area denominator when the
            // scenario carries it.
            var year1Area = m.baselineYears.length > 0 ? (m.baselineYears[0].area || 0) : 0;
            m.year1AreaLarge = year1Area > 0 ? toAreaLarge(year1Area) : m.scopeAreaLarge;
            m.year1PerAreaUnit = m.year1AreaLarge > 0 ? m.year1Need / m.year1AreaLarge : 0;

            // Roads-only solvency metrics from forecast.json (an array of
            // per-resource ForecastExport — find the roads element). These are
            // nullable: insolvencyYear/fundingGap are present only for cities
            // with a configured (cited) current_budget; breakEvenBudget is
            // present for any city with roads. Leaderboard categories filter on
            // these so a missing metric never corrupts a sort/bar.
            m.breakEvenBudget = null; m.insolvencyYear = null; m.fundingGap = null; m.currentBudget = 0;
            m.defaultPCI = false;
            m.solvencyRoadsAreaLarge = 0; // the city-maintained roads the solvency figures price
            if (city.forecast && city.forecast.find) {
                var roadsFe = city.forecast.find(function(f) { return f.resource_type === 'roads'; });
                if (roadsFe) {
                    if (roadsFe.break_even_budget > 0) m.breakEvenBudget = roadsFe.break_even_budget;
                    m.currentBudget = roadsFe.current_budget || 0;
                    m.defaultPCI = roadsFe.initial_pci_source === 'default';
                    if (roadsFe.roads_area > 0) m.solvencyRoadsAreaLarge = toAreaLarge(roadsFe.roads_area);
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

        // A Solvency Score card takes a medal only at or above this score:
        // 50 is a budget of half the hold-steady need (see Methodology).
        const SOLVENCY_MEDAL_MIN = 50;
        // The roads-solvency leaderboards (funding gap, years to insolvency)
        // hold their medals to the same floor, so a city far short of its
        // hold-steady budget is not crowned for being the least bad. The
        // score is null only for a city without a hold-steady figure; its
        // funding gap then stands in (a score of 50 is a gap of +100%).
        /** @param {{solvScore: number | null, fundingGap: number | null}} m */
        function solvencyMedalOK(m) {
            if (m.solvScore != null) return m.solvScore >= SOLVENCY_MEDAL_MIN;
            return m.fundingGap != null && m.fundingGap <= (100 / SOLVENCY_MEDAL_MIN - 1) * 100;
        }

        // markCurrentCity highlights the city in view on the Compare cards and
        // leaderboard rows: a class for the eye, aria-current and hidden text
        // for a screen reader. Run after each render and on a city switch.
        function markCurrentCity() {
            const slug = DATA_PREFIX.replace(/^cities\//, '').replace(/\/$/, '');
            queryAll('#compare-tab .current-city-cue').forEach(el => el.remove());
            queryAll('#compare-tab .score-card[data-slug], #compare-tab .lb-row[data-slug]').forEach(el => {
                const on = el.getAttribute('data-slug') === slug;
                el.classList.toggle('current-city', on);
                if (on) el.setAttribute('aria-current', 'true');
                else el.removeAttribute('aria-current');
                const name = el.querySelector('.city-name, .lb-name');
                if (on && name) {
                    const cue = document.createElement('span');
                    cue.className = 'current-city-cue visually-hidden';
                    cue.textContent = ' (selected city)';
                    // Beside the name, not inside it, so the name's own text
                    // stays the city's name.
                    name.after(cue);
                }
            });
        }

        function renderCompare(cityData) {
            var allMetrics = cityData.map(extractMetrics);
            var medals = ['\u{1F947}', '\u{1F948}', '\u{1F949}'];
            var rankClasses = ['rank-1', 'rank-2', 'rank-3'];

            // One horizon for the titles, the leaderboard label and every
            // city's figure at it (applyRegionHorizon; Aggregate uses the same
            // call). With no forecast in the set the titles take the default,
            // as the Financials tab does.
            var forecastYears = applyRegionHorizon(allMetrics);
            var compareHorizon = forecastYears || DEFAULT_HORIZON_YEARS;
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
            // A city missing a layer is not scored: every component reads
            // every layer, and its zeros would also skew the others' scale.
            var complete = allMetrics.filter(function(m) { return m.unranked.length === 0; });
            var walkN = normalize(complete, 'sidewalkToRoadPct', true);
            var sizeN = normalize(complete, 'totalAreaLarge', true);
            var cityN = normalize(complete, 'cityPct', true);
            var costN = normalize(complete, 'year1PerAreaUnit', false);
            var decayN = normalize(complete, 'blendedDecay', false);
            allMetrics.forEach(function(m) { m.infraScore = null; });
            complete.forEach(function(m, i) {
                m.infraScore = Math.round((walkN[i] * 0.2 + sizeN[i] * 0.15 + cityN[i] * 0.2 + costN[i] * 0.2 + decayN[i] * 0.25) * 100);
            });

            // Strict-position ranks over an already-sorted list: one #1, no shared medals.
            function assignRanks(list) { list.forEach(function(m, i) { m.rank = i + 1; }); return list; }

            // A medal needs a field: with fewer than two ranked cities the one
            // city would take gold by default, so
            // opts.ranked is passed false then and no medal is drawn. A medal
            // also needs a score of at least opts.medalMin, where one is set:
            // a city funded at a few percent of hold steady is not on a podium,
            // whatever its place. A ranked card
            // without a medal shows its rank.
            function scoreCard(m, opts) {
                var podium = opts.ranked && m.rank <= 3 && (opts.medalMin == null || opts.value >= opts.medalMin);
                var medal = podium ? medals[m.rank - 1] : (opts.ranked ? '#' + m.rank : '');
                var cls = podium ? (' ' + rankClasses[m.rank - 1]) : '';
                return '<div class="score-card' + cls + (opts.muted ? ' score-card-muted' : '') + '" data-slug="' + esc(m.slug || '') + '">' +
                    '<div class="rank-medal' + (podium ? '' : ' rank-num') + '">' + medal + '</div>' +
                    '<div class="city-name">' + esc(m.name) + defaultPCIMark(m) + '</div>' +
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
                return (m.currentBudget > 0 ? '<div class="sb-row"><span>Current Budget</span><span>' + fmtMoney(m.currentBudget) + '/yr</span></div>' : '') +
                    (m.breakEvenBudget > 0 ? '<div class="sb-row"><span>Hold-Steady Budget</span><span>' + fmtMoney(m.breakEvenBudget) + '/yr</span></div>' : '') +
                    '<div class="sb-row"><span>Funding Gap</span><span>' + gap + '</span></div>' +
                    '<div class="sb-row"><span>Insolvency</span><span>' + insolv + '</span></div>' +
                    // The budgets above price city-maintained roads only, so
                    // show that network, not the all-resource total.
                    (m.solvencyRoadsAreaLarge > 0 ? '<div class="sb-row"><span>City-Maintained Roads</span><span>' + fmtNum(m.solvencyRoadsAreaLarge) + ' ' + areaLargeUnit() + '</span></div>' : '');
            }

            function infraBreakdown(m) {
                return (m.cityAreaLarge > 0 ? '<div class="sb-row"><span>City Area</span><span>' + fmtNum(m.cityAreaLarge) + ' ' + areaLargeUnit() + '</span></div>' : '') +
                    (m.pctPaved > 0 ? '<div class="sb-row"><span title="% Paved divides paved area by the Nominatim boundary polygon. Boundary scope varies across cities, so cross-city comparison is approximate.">% Paved <span class="sb-note">ⓘ</span></span><span>' + m.pctPaved.toFixed(1) + '%</span></div>' : '') +
                    '<div class="sb-row"><span title="' + MAINTAINED_AREA_TIP + '">Maintained Area</span><span>' + fmtNum(m.totalAreaLarge) + ' ' + areaLargeUnit() + '</span></div>' +
                    '<div class="sb-row"><span>Roads</span><span>' + fmtNum(m.roadsAreaLarge) + ' ' + areaLargeUnit() + '</span></div>' +
                    '<div class="sb-row"><span>Parking</span><span>' + fmtNum(m.parkingAreaLarge) + ' ' + areaLargeUnit() + '</span></div>' +
                    '<div class="sb-row"><span>Sidewalks</span><span>' + m.sidewalksAreaLarge.toFixed(1) + ' ' + areaLargeUnit() + '</span></div>' +
                    '<div class="sb-row"><span>Sidewalk/Road</span><span>' + m.sidewalkToRoadPct.toFixed(1) + '%</span></div>' +
                    (m.parkingSpaces > 0 ? '<div class="sb-row"><span>Parking Spaces (est.)</span><span>' + m.parkingSpaces.toLocaleString('en-US') + '</span></div>' : '') +
                    '<div class="sb-row"><span>City-Owned</span><span>' + m.cityPct.toFixed(0) + '%</span></div>' +
                    '<div class="sb-row"><span>Blended Decay</span><span>' + (m.blendedDecay * 100).toFixed(2) + '%/yr</span></div>';
            }

            // Solvency cards: scored cities ranked (desc coverage, tie-break later/no
            // insolvency), then budget-less cities appended as muted N/A.
            var solvScored = assignRanks(allMetrics.filter(function(m) { return m.solvScore !== null; })
                .sort(function(a, b) {
                    if (b.solvCoverage !== a.solvCoverage) return b.solvCoverage - a.solvCoverage;
                    // Later (or no) insolvency first. Compared, not subtracted:
                    // two solvent cities are Infinity - Infinity, which is NaN.
                    var ya = a.insolvencyYear || Infinity, yb = b.insolvencyYear || Infinity;
                    return ya === yb ? 0 : (yb > ya ? 1 : -1);
                }));
            var solvUnscored = allMetrics.filter(function(m) { return m.solvScore === null; });
            var solvCards = solvScored.map(function(m) {
                return scoreCard(m, { ranked: solvScored.length >= 2, medalMin: SOLVENCY_MEDAL_MIN, value: m.solvScore, label: 'Solvency Score', breakdown: solvencyBreakdown(m) });
            }).concat(solvUnscored.map(function(m) {
                return scoreCard(m, { ranked: false, muted: true, value: 'N/A', label: 'No cited budget — unranked', breakdown: solvencyBreakdown(m) });
            }));
            document.getElementById('solvency-cards').innerHTML = solvCards.join('');
            var defaultNote = document.getElementById('default-pci-note');
            if (defaultNote) defaultNote.hidden = !allMetrics.some(function(m) { return m.defaultPCI; });

            // Infrastructure Index cards: every city with every layer places;
            // one missing a layer is shown unranked.
            var infraRanked = assignRanks(complete.slice().sort(function(a, b) { return b.infraScore - a.infraScore; }));
            document.getElementById('infra-cards').innerHTML = infraRanked.map(function(m) {
                return scoreCard(m, { ranked: infraRanked.length >= 2, value: m.infraScore, label: 'Infrastructure Index', breakdown: infraBreakdown(m) });
            }).concat(allMetrics.filter(function(m) { return m.unranked.length > 0; }).map(function(m) {
                return scoreCard(m, { ranked: false, muted: true, value: 'N/A', label: unrankedLabel(m) + ' — unranked', breakdown: infraBreakdown(m) });
            })).join('');

            // Leaderboards — data-driven categories
            var categories = [
                { key: 'cityAreaLarge', label: 'City Area', award: 'Largest City', higher: true, fmt: function(v) { return fmtNum(v) + ' ' + areaLargeUnit(); }, color: '#059669', note: 'City Area is the Nominatim admin polygon. Polygon scope varies (e.g. Boston includes harbor water; Nashville is consolidated Davidson County), so cross-city comparison is approximate.' },
                { key: 'pctPaved', needs: ['roads', 'parking', 'sidewalks'],  label: '% Paved', award: 'Most Paved', higher: true, fmt: function(v) { return v.toFixed(1) + '%'; }, color: '#dc2626', note: '% Paved divides paved area by the Nominatim boundary polygon for each city. Polygon scope varies across cities (Boston includes harbor water; Nashville is consolidated Davidson County), so cross-city comparison is approximate.' },
                { key: 'totalAreaLarge', needs: ['roads', 'parking', 'sidewalks'],  label: 'Maintained Area (all owners)', award: 'Biggest Network', higher: true, fmt: function(v) { return fmtNum(v) + ' ' + areaLargeUnit(); }, color: '#6366f1', note: MAINTAINED_AREA_TIP },
                { key: 'sidewalkToRoadPct', needs: ['roads', 'sidewalks'],  label: 'Walkability (Sidewalk-to-Road Ratio)', award: 'Most Walkable', higher: true, fmt: function(v) { return v.toFixed(1) + '%'; }, color: '#10b981' },
                { key: 'parkingAreaLarge', needs: ['parking'],  label: 'Parking Infrastructure', award: 'Most Parking', higher: true, fmt: function(v) { return fmtNum(v) + ' ' + areaLargeUnit(); }, color: '#3b82f6' },
                // No award of its own: "Most Parking" above already crowns parking, and two parking medals would be noise.
                { key: 'parkingSpaces', needs: ['parking'],  label: 'Parking Spaces (est.)', higher: true, filter: function(m) { return m.parkingSpaces > 0; }, fmt: function(v) { return v.toLocaleString('en-US'); }, color: '#60a5fa', note: 'Estimated vehicle capacity of mapped lots and garages: the OSM capacity tag where present, otherwise lot footprint ÷ 30 m² per space (× levels for structured parking). On-street parking is not counted. Cities computed before the estimate existed are omitted.' },
                { key: 'cityPct', needs: ['roads', 'parking', 'sidewalks'],  label: 'City Ownership Share', award: 'Most City-Owned', higher: true, fmt: function(v) { return v.toFixed(0) + '%'; }, color: '#8b5cf6' },
                { key: 'blendedDecay', needs: ['roads', 'parking', 'sidewalks'],  label: 'Blended Decay Rate (lower is better)', award: 'Slowest Decay', higher: false, fmt: function(v) { return (v * 100).toFixed(2) + '%/yr'; }, color: '#ef4444' },
                { key: 'year1PerAreaUnit', needs: ['roads', 'parking', 'sidewalks'],  rowNote: function(m) { return perAreaNote(m, m.year1AreaLarge); }, note: PER_AREA_TIP, label: 'Year 1 Maintenance Cost per ' + areaLargeUnitSingular(), award: 'Lowest Cost per ' + areaLargeUnitSingular(), higher: false, fmt: function(v) { return fmtMoney(v); }, color: '#ec4899' },
                // The backlog at the horizon per unit of network (same scope as
                // the need): per area, so size alone does not win (raw dollars
                // made the smallest city "Least Deferred"). A city whose
                // forecast stops short of the horizon is flagged on its row.
                { key: 'horizonBacklogPerAreaUnit', needs: ['roads', 'parking', 'sidewalks'],  rowNote: function(m) { return perAreaNote(m, m.horizonAreaLarge) + shortSeriesNote(m); }, note: PER_AREA_TIP, label: compareHorizon + '-Year Deferred Backlog per ' + areaLargeUnitSingular(), award: 'Least Deferred per ' + areaLargeUnitSingular(), higher: false, fmt: function(v) { return fmtMoney(v); }, color: '#d946ef' },
                // Roads-only solvency metrics. filter excludes cities that lack
                // the metric (no cited budget / no roads) so a null value never
                // produces a NaN sort key or a corrupt bar width.
                { key: 'fundingGap', needs: ['roads'],  label: 'Street Funding Gap (roads, lower is better)', award: 'Smallest Funding Gap', higher: false, medalIf: solvencyMedalOK, filter: function(m) { return m.fundingGap !== null; }, fmt: function(v) { return (v >= 0 ? '+' : '') + v.toFixed(0) + '%'; }, color: '#0ea5e9', note: 'Funding gap = (hold-steady budget − current budget) / current budget, computed on roads/streets only. Shown only for cities with a cited current_budget. Negative means the budget already exceeds the hold-steady level.' },
                { key: 'insolvencyYear', needs: ['roads'],  label: 'Years to Insolvency (roads, higher is better)', award: 'Latest Insolvency', higher: true, medalIf: solvencyMedalOK, filter: function(m) { return m.insolvencyYear !== null; }, fmt: function(v) { return 'Year ' + v; }, color: '#14b8a6', note: 'Insolvency year = the first forecast year a city’s cumulative roads backlog grows to an entire treatment cycle of deferred work — a whole network’s worth of treatment, unrecoverable. Computed on roads/streets only at the current budget; shown only for cities with a cited current_budget that go insolvent within the horizon. Cities that stay solvent (most, with realistic budgets) are omitted — funding gap is the primary metric there.' }
            ];

            var lbHtml = categories.map(function(cat) {
                // Restrict to cities that actually have this metric. Without
                // this, a null/undefined value yields NaN sort keys and bar
                // widths that corrupt the whole category.
                // A city missing a layer the metric reads is listed below the
                // ranking as n/a instead of ranked on a zero (56r1).
                var lacks = function(m) { return (cat.needs || []).filter(function(rt) { return m.unranked.includes(rt); }); };
                var unavailable = allMetrics.filter(function(m) { return lacks(m).length > 0; });
                var pool = allMetrics.filter(function(m) { return lacks(m).length === 0 && (!cat.filter || cat.filter(m)); });
                if (pool.length === 0) return '';
                var sorted = pool.slice().sort(function(a, b) {
                    return cat.higher ? (b[cat.key] - a[cat.key]) : (a[cat.key] - b[cat.key]);
                });
                var vals = sorted.map(function(m) { return m[cat.key]; });
                // Floor at 0 before the zero repair. The funding-gap metric is
                // signed (negative when over-funded), so a scope where EVERY
                // city is over-funded yields a negative maxVal -- and dividing
                // a negative value by a negative max gives a positive ratio, so
                // every bar clamped to full width and the ranking the bars
                // exist to show conveyed nothing. Flooring renders over-funded
                // cities as an empty bar instead, which is what the mixed-sign
                // case already does via the [0,100] clamp below.
                var maxVal = Math.max(0, Math.max.apply(null, vals));
                if (maxVal === 0) maxVal = 1;

                // Medals run down the board and stop at the first city under
                // the floor: a board sorted by something other than the
                // score (insolvency year) must not show a silver without a
                // gold.
                var medalRows = sorted.length;
                if (cat.medalIf) {
                    var firstFail = sorted.findIndex(function(m) { return !cat.medalIf(m); });
                    if (firstFail >= 0) medalRows = firstFail;
                }
                var rows = sorted.map(function(m, i) {
                    // Clamp to [0,100]: the funding-gap metric is signed
                    // (negative when over-funded), which would otherwise produce
                    // an invalid bar width. maxVal is floored at 0 above, so the
                    // over-funded case lands at 0% here rather than overflowing.
                    var pct = Math.max(0, Math.min(100, m[cat.key] / maxVal * 100)).toFixed(1);
                    var medalIcon = (sorted.length >= 2 && i < 3 && i < medals.length && i < medalRows) ? medals[i] : (i + 1) + '.';
                    return '<div class="lb-row" data-slug="' + esc(m.slug || '') + '">' +
                        '<span class="lb-medal">' + medalIcon + '</span>' +
                        '<span class="lb-name">' + esc(m.name) + defaultPCIMark(m) + (cat.rowNote ? cat.rowNote(m) : '') + '</span>' +
                        '<div class="lb-bar-wrap"><div class="lb-bar" style="width:' + pct + '%;background:' + cat.color + ';"></div></div>' +
                        '<span class="lb-val">' + cat.fmt(m[cat.key]) + '</span>' +
                    '</div>';
                }).join('') + unavailable.map(function(m) {
                    return '<div class="lb-row lb-na" data-slug="' + esc(m.slug || '') + '">' +
                        '<span class="lb-medal">–</span>' +
                        '<span class="lb-name">' + esc(m.name) + defaultPCIMark(m) + '</span>' +
                        '<div class="lb-bar-wrap"></div>' +
                        '<span class="lb-val" title="No complete ' + esc(lacks(m).join(', ')) + ' data for this city">n/a</span>' +
                    '</div>';
                }).join('');

                var noteIcon = cat.note ? ' <span class="lb-note" title="' + cat.note.replace(/"/g, '&quot;') + '">ⓘ</span>' : '';
                return '<div class="lb-category">' +
                    '<div class="lb-title"><h4>' + cat.label + noteIcon + '</h4>' + (cat.award && sorted.length >= 2 && medalRows > 0 ? '<span class="lb-award">' + medals[0] + ' ' + cat.award + ': ' + esc(sorted[0].name) + '</span>' : '') + '</div>' +
                    rows +
                '</div>';
            }).join('');
            document.getElementById('leaderboard-list').innerHTML = lbHtml;
            markCurrentCity();

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
                        { label: 'Roads', data: allMetrics.map(function(m) { return m.roadsAreaLarge; }), backgroundColor: layerColors.roads },
                        { label: 'Parking', data: allMetrics.map(function(m) { return m.parkingAreaLarge; }), backgroundColor: layerColors.parking },
                        { label: 'Sidewalks', data: allMetrics.map(function(m) { return m.sidewalksAreaLarge; }), backgroundColor: layerColors.sidewalks }
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

            // 3. Cost trajectory to the horizon (line chart). A city whose
            // forecast stops short ends its line there.
            var yearLabels = [];
            for (var i = 1; i <= forecastYears; i++) yearLabels.push('Year ' + i);

            compareCharts.push(new Chart(canvasById('compare-cost-chart').getContext('2d'), {
                type: 'line',
                data: {
                    labels: yearLabels,
                    datasets: allMetrics.map(function(m, idx) {
                        return {
                            label: m.name,
                            data: m.baselineYears.slice(0, forecastYears).map(function(y) { return y.deferred_backlog; }),
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
                            ticks: { callback: function(v) { return fmtMoney(v); } }
                        }
                    },
                    plugins: { legend: { position: 'top' } },
                    interaction: { mode: 'index', intersect: false }
                }
            }));

            // 4. Year 1 vs the horizon year's need
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
                            data: allMetrics.map(function(m) { return m.horizonNeed; }),
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
                            ticks: { callback: function(v) { return fmtMoney(v); } }
                        }
                    },
                    plugins: {
                        verticalCrosshair: false,
                        legend: { position: 'top' },
                        tooltip: { callbacks: { label: function(ctx) { return ctx.dataset.label + ': ' + fmtMoney(ctx.raw); } } }
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
        // incompleteNote names the cities whose totals leave out a layer that
        // was never ingested, so a summed figure does not pass for complete.
        /** @param {any[]} allMetrics */
        function incompleteNote(allMetrics) {
            var gaps = allMetrics.filter(function(m) { return m.unranked.length > 0; })
                .map(function(m) { return m.name + ' (' + unrankedLabel(m).toLowerCase() + ')'; });
            return gaps.length > 0 ? ' Totals omit or undercount layers with no or partial data: ' + gaps.join('; ') + '.' : '';
        }

        function renderAggregate(cityData) {
            var allMetrics = cityData.map(extractMetrics);
            var fc = allMetrics.filter(function(m) { return m.hasForecasts && m.baselineYears.length > 0; });

            // Does the region as a whole support both scopes? Drives the
            // region-wide scope-toggle reveal (maybeRevealScopeToggle).
            regionHasBothScopes = cityData.some(function(city) {
                return city.scenarios && city.scenarios.city && city.scenarios.all;
            });
            maybeRevealScopeToggle();

            // The horizon and each city's figures at it, by the same call
            // Compare makes. The summed trajectories below run to it too, so
            // the headline backlog is the backlog chart's final point.
            var horizonYears = applyRegionHorizon(allMetrics);

            // Region area sums (every city, forecast or not).
            var totalRoads = 0, totalParking = 0, totalSidewalks = 0, totalNetwork = 0, totalCityArea = 0, totalPaved = 0, totalSpaces = 0;
            allMetrics.forEach(function(m) {
                totalRoads += m.roadsAreaLarge; totalParking += m.parkingAreaLarge; totalSidewalks += m.sidewalksAreaLarge;
                totalSpaces += m.parkingSpaces || 0;
                totalNetwork += m.totalAreaLarge; totalCityArea += m.cityAreaLarge;
                // Union-based paved area (large units) for an apples-to-apples
                // Paved Share that matches the Map/Compare % Paved, instead of the
                // overlap-double-counting per-resource sum (totalNetwork).
                totalPaved += toAreaLarge(m.totalPavedSqM || 0);
            });

            // Dollar + PCI sums over forecast cities only, so the weighted PCI
            // denominator and the trajectories stay honest. The area is the one
            // the scope's dollars and PCI cover (scopeAreaLarge), not the
            // all-jurisdiction Maintained Area above.
            var fcArea = 0, fcYear1Area = 0, pciWeighted = 0, regionYear1 = 0, regionBacklog = 0;
            fc.forEach(function(m) {
                fcArea += m.scopeAreaLarge;
                fcYear1Area += m.year1AreaLarge;
                pciWeighted += (m.baselineYears[0].pci || 0) * m.scopeAreaLarge;
                regionYear1 += m.baselineYears[0].annual_need || 0;
                regionBacklog += m.horizonBacklog;
            });
            var weightedPCI = fcArea > 0 ? pciWeighted / fcArea : null;
            // The year-1 need over the year-1 area it prices, as Compare's
            // per-area need divides.
            var perArea = fcYear1Area > 0 ? regionYear1 / fcYear1Area : null;
            var unit = areaLargeUnit();
            // A config with cities but no computed forecasts (e.g. exported
            // before `forecast`) has no horizon; drop the "N-Year" prefix and
            // dash out the forecast-dependent figures rather than show "0-Year".
            var hasFc = fc.length > 0;
            var horizon = hasFc ? horizonYears + '-Year ' : '';

            // Headline cards.
            function aggCard(label, value, sub) {
                return '<div class="headline-card"><div class="label">' + label + '</div>' +
                       '<div class="value">' + value + '</div><div class="sub">' + sub + '</div></div>';
            }
            document.getElementById('aggregate-headlines').innerHTML = [
                aggCard('Maintained Area', fmtNum(totalNetwork) + ' ' + unit, 'Roads + parking + sidewalks, all owners, across ' + allMetrics.length + ' cities'),
                aggCard('Paved Share', totalCityArea > 0 ? (totalPaved / totalCityArea * 100).toFixed(1) + '%' : '—', 'Paved (union) area vs. municipal area'),
                // baselineYears[0] is year 1, after one year's decay from the
                // configured starting PCI, so say so.
                aggCard('Area-Weighted PCI', weightedPCI === null ? '—' : weightedPCI.toFixed(0), 'Year-1 condition, after one year of decay with no spend'),
                aggCard('Year-1 Maintenance Need', hasFc ? fmtMoney(regionYear1) : '—', 'Year 1\'s share of the treatment cycle, before any deferred backlog; the hold-steady budget is on Financials'),
                aggCard(horizon + 'Deferred Backlog', hasFc ? fmtMoney(regionBacklog) : '—', hasFc ? 'Unfunded need accumulated by year ' + horizonYears : 'No forecast data in this region'),
                aggCard('Year-1 Need per ' + areaLargeUnitSingular(), perArea === null ? '—' : fmtMoney(perArea, { full: true }), 'Year-1 need ÷ ' + fmtNum(fcYear1Area) + ' ' + unit + ' of maintained area (' + scopeName() + ')'),
                aggCard('Est. Parking Spaces', totalSpaces > 0 ? fmtNum(totalSpaces) : '—', 'Lots and garages: OSM capacity, else footprint ÷ 30 m²/space')
            ].join('');

            // Forecast divergence note: per-city [forecast] can override
            // every setting that moves a city's dollars (initial PCI, decay,
            // growth, treatment cycle, cost multiplier, cost tiers), and summed
            // dollars would then mix cost bases. forecast_seed.json carries
            // them all. The horizon is one per config, so it is not part of
            // the signature.
            var sigs = {};
            cityData.forEach(function(city, idx) {
                if (!allMetrics[idx].hasForecasts || !city.seed) return;
                var s = city.seed;
                sigs[JSON.stringify([s.initial_pci, s.decay_rate, s.growth_rate, s.treatment_cycle_years,
                    s.cost_multiplier, s.cost_tiers])] = true;
            });
            var noteEl = document.getElementById('aggregate-divergence-note');
            if (Object.keys(sigs).length > 1) {
                noteEl.hidden = false;
                noteEl.textContent = 'Heads up: cities in this region use different forecast settings ' +
                    '(initial PCI, decay, growth, treatment cycle, cost multiplier or cost tiers). ' +
                    'Summed dollars may mix cost bases.';
            } else {
                noteEl.hidden = true;
            }

            // Dynamic, horizon-derived chart + column titles (never hardcode the number).
            document.getElementById('agg-cost-title').textContent = 'Region ' + horizon + 'Cost Trajectory';
            document.getElementById('agg-backlog-title').textContent = 'Region ' + horizon + 'Deferred Backlog';
            document.getElementById('agg-contrib-backlog-th').textContent = hasFc ? horizonYears + '-Yr Backlog' : 'Backlog';

            // Element-wise summed trajectories to the horizon. A city whose
            // forecast stops short carries its last year forward, the value
            // its row in the contribution table shows.
            var yearLabels = [], costSeries = [], backlogSeries = [];
            for (var y = 0; y < horizonYears; y++) {
                var needSum = 0, backSum = 0;
                fc.forEach(function(m) {
                    var at = yearAtHorizon(m.baselineYears, y + 1);
                    needSum += at.annual_need || 0;
                    backSum += at.deferred_backlog || 0;
                });
                yearLabels.push('Year ' + (y + 1));
                costSeries.push(needSum);
                backlogSeries.push(backSum);
            }
            var dollarTicks = function(v) { return fmtMoney(v); };

            aggregateCharts.forEach(function(c) { c.destroy(); });
            aggregateCharts = [];

            aggregateCharts.push(new Chart(canvasById('aggregate-cost-chart').getContext('2d'), {
                type: 'line',
                data: { labels: yearLabels, datasets: [{ label: 'Region Annual Need', data: costSeries, borderColor: '#3b82f6', backgroundColor: '#3b82f622', fill: true, tension: 0.3, borderWidth: 2.5 }] },
                options: {
                    responsive: true,
                    scales: { y: { beginAtZero: true, title: { display: true, text: 'Annual Need ($)' }, ticks: { callback: dollarTicks } } },
                    plugins: { verticalCrosshair: false, legend: { display: false }, tooltip: { callbacks: { label: function(c) { return fmtMoney(c.raw); } } } },
                    interaction: { mode: 'index', intersect: false }
                }
            }));

            aggregateCharts.push(new Chart(canvasById('aggregate-backlog-chart').getContext('2d'), {
                type: 'line',
                data: { labels: yearLabels, datasets: [{ label: 'Region Deferred Backlog', data: backlogSeries, borderColor: '#d946ef', backgroundColor: '#d946ef22', fill: true, tension: 0.3, borderWidth: 2.5 }] },
                options: {
                    responsive: true,
                    scales: { y: { beginAtZero: true, title: { display: true, text: 'Deferred Backlog ($)' }, ticks: { callback: dollarTicks } } },
                    plugins: { verticalCrosshair: false, legend: { display: false }, tooltip: { callbacks: { label: function(c) { return fmtMoney(c.raw); } } } },
                    interaction: { mode: 'index', intersect: false }
                }
            }));

            aggregateCharts.push(new Chart(canvasById('aggregate-mix-chart').getContext('2d'), {
                type: 'bar',
                data: {
                    labels: ['Roads', 'Parking', 'Sidewalks'],
                    datasets: [{ label: 'Maintained area (' + unit + ')', data: [totalRoads, totalParking, totalSidewalks], backgroundColor: [layerColors.roads, layerColors.parking, layerColors.sidewalks] }]
                },
                options: {
                    indexAxis: 'y',
                    responsive: true,
                    scales: { x: { beginAtZero: true, title: { display: true, text: 'Maintained area (' + unit + ')' }, ticks: { callback: function(v) { return fmtNum(v); } } } },
                    plugins: { verticalCrosshair: false, legend: { display: false }, tooltip: { callbacks: { label: function(c) { return fmtNum(c.raw) + ' ' + unit; } } } }
                }
            }));

            // Per-city contribution table (inverse lens to Compare's ranking),
            // sorted by Year-1 need.
            var sorted = allMetrics.slice().sort(function(a, b) { return (b.year1Need || 0) - (a.year1Need || 0); });
            document.querySelector('#aggregate-contrib-table tbody').innerHTML = sorted.map(function(m) {
                var y1 = m.hasForecasts ? m.year1Need : 0;
                var bk = m.horizonBacklog;
                var y1Share = regionYear1 > 0 ? (y1 / regionYear1 * 100).toFixed(1) + '%' : '—';
                var bkShare = regionBacklog > 0 ? (bk / regionBacklog * 100).toFixed(1) + '%' : '—';
                var name = m.hasForecasts ? esc(m.name) + shortSeriesNote(m) : esc(m.name) + ' <span class="lb-note" title="No forecast data">(no forecast)</span>';
                return '<tr><td>' + name + '</td>' +
                    '<td class="number">' + fmtNum(m.totalAreaLarge) + ' ' + unit + '</td>' +
                    '<td class="number">' + (m.hasForecasts ? fmtMoney(y1) : '—') + '</td>' +
                    '<td class="number">' + (m.hasForecasts ? y1Share : '—') + '</td>' +
                    '<td class="number">' + (m.hasForecasts ? fmtMoney(bk) : '—') + '</td>' +
                    '<td class="number">' + (m.hasForecasts ? bkShare : '—') + '</td></tr>';
            }).join('');

            var excluded = allMetrics.length - fc.length;
            document.getElementById('aggregate-footnote').textContent =
                'Area-weighted PCI is each city’s year-1 condition in its no-intervention scenario (scenarios.json, run over its per-class cohorts), weighted by the maintained area in the selected scope. ' +
                'Trajectories sum the baseline (no-intervention) scenario across cities with forecast data' +
                (excluded > 0 ? ' (' + excluded + ' without forecasts excluded)' : '') +
                '. Dollars are nominal in the config’s cost basis.' +
                incompleteNote(allMetrics);

            document.getElementById('aggregate-loading').style.display = 'none';
            document.getElementById('aggregate-content').style.display = 'block';
        }

        // Boot the forecast simulator once the map has drawn, in the browser's
        // idle time, or as soon as a tab that runs it opens (bootWasm, from
        // selectTab), whichever comes first. The Map tab never runs it, so its
        // ~1 MB download does not compete with the hex grid on first paint.
        // The page works without it (the static exports stand in), so a
        // browser with no WebAssembly is not an error.
        // A page opened on such a tab (a ?tab= deep link, which sets the tab's
        // classes rather than calling selectTab) boots it at once.
        {
            const idle = window.requestIdleCallback || ((fn) => setTimeout(fn, 0));
            const active = document.querySelector('.tab-content.active');
            if (!window._map || (active && WASM_TABS.includes(active.id))) bootWasm();
            else window._map.once('idle', () => idle(bootWasm));
        }
