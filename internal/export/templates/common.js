// Shared page plumbing for the index (app.js) and play (game.js) pages: money
// formatting, the error banner, the JSON fetch wrapper, and booting and calling
// the forecast WASM. One implementation, loaded by both pages as a plain
// classic script BEFORE the page script, exposing a single window.PvmtCommon
// namespace (no module wrapper: like the page scripts it is go:embed'd
// verbatim and loaded by a bare <script src>). The banner it drives is the
// error_banner partial in common.html.tmpl.
window.PvmtCommon = (function () {
    'use strict';

    // Every figure is formatted in one locale, whatever the browser's.
    const LOCALE = 'en-US';

    // Compact money units, largest first: the divisor, its suffix and the
    // decimals shown at that magnitude.
    const MONEY_UNITS = [
        { div: 1e9, suffix: 'B', decimals: 2 },
        { div: 1e6, suffix: 'M', decimals: 1 },
        { div: 1e3, suffix: 'K', decimals: 1 },
    ];

    /** @param {number} abs a non-negative whole-dollar amount */
    function wholeDollars(abs) {
        return Math.round(abs).toLocaleString(LOCALE, { maximumFractionDigits: 0 });
    }

    /**
     * compactDollars renders a non-negative amount at its magnitude:
     * billions to two decimals, millions and thousands to one, and whole
     * dollars below $1,000. An amount that would round up to 1000 of a unit
     * is shown in the next unit ($999,960 is "$1.0M", not "$1000.0K").
     * @param {number} abs
     */
    function compactDollars(abs) {
        for (let i = 0; i < MONEY_UNITS.length; i++) {
            const u = MONEY_UNITS[i];
            const lower = MONEY_UNITS[i + 1];
            const lowerRoundsUp = lower
                ? Number((abs / lower.div).toFixed(lower.decimals)) >= 1000
                : Math.round(abs) >= 1000;
            if (abs >= u.div || lowerRoundsUp) return (abs / u.div).toFixed(u.decimals) + u.suffix;
        }
        return wholeDollars(abs);
    }

    /**
     * fmtMoney is the one dollar formatter. Compact by default ("$1.25B",
     * "$12.3M", "$45.6K", "$789"); { full: true } gives whole dollars with
     * thousands separators at any size ("$12,345,678"), for table cells that
     * list exact amounts. A negative amount carries a leading minus
     * ("-$1.2M"); a missing or non-finite one reads "$0".
     * @param {number | null | undefined} n
     * @param {{ full?: boolean }} [opts]
     * @returns {string}
     */
    function fmtMoney(n, opts) {
        if (n == null || !Number.isFinite(n)) return '$0';
        const abs = Math.abs(n);
        const body = opts && opts.full ? wholeDollars(abs) : compactDollars(abs);
        // A small negative that rounds to zero is not "-$0".
        const negative = n < 0 && body !== '0';
        return (negative ? '-' : '') + '$' + body;
    }

    /**
     * showError reports a failure where the reader can see it: one line in
     * the page's error banner (the error_banner partial), which it reveals,
     * and console.error for the developer. label names what failed (a URL, a
     * WASM export); detail says how.
     * @param {string} label
     * @param {string} [detail]
     */
    function showError(label, detail) {
        console.error('[pvmt]', label, detail);
        const banner = document.getElementById('error-banner');
        const list = document.getElementById('error-banner-list');
        if (!banner || !list) return;
        const li = document.createElement('li');
        li.textContent = label + (detail ? ' (' + detail + ')' : '');
        list.appendChild(li);
        banner.hidden = false;
    }

    // The banner's Dismiss button hides it; the next error reveals it again
    // with the earlier lines still listed.
    {
        const banner = document.getElementById('error-banner');
        const dismiss = document.getElementById('error-banner-dismiss');
        if (banner && dismiss) dismiss.addEventListener('click', function () { banner.hidden = true; });
    }

    /** @param {unknown} err */
    function errorText(err) {
        return (err && /** @type {any} */ (err).message) || String(err);
    }

    /**
     * loadJSON fetches and parses a JSON file. A network or HTTP failure is
     * reported through showError and resolves to null, so callers fall back
     * instead of leaving an empty chart that looks like "still loading".
     * opts.silent404 resolves a 404 to null without reporting it, for files
     * a site legitimately may not have.
     * @param {string} url
     * @param {{ silent404?: boolean }} [opts]
     * @returns {Promise<any>}
     */
    async function loadJSON(url, opts) {
        try {
            const resp = await fetch(url);
            if (!resp.ok) {
                if (!(opts && opts.silent404 && resp.status === 404)) {
                    showError(url, 'HTTP ' + resp.status);
                }
                return null;
            }
            return await resp.json();
        } catch (err) {
            showError(url, errorText(err));
            return null;
        }
    }

    /**
     * callWasm calls one of the forecast WASM's exports (window[fnName]:
     * JSON-string out, may throw) and returns its parsed result. A missing
     * export, a throw, an unparsable response or an {"error": ...} envelope
     * is reported through showError under label (fnName when omitted) and
     * returns null; callers check for null and bail.
     * @param {string} fnName
     * @param {any[]} args
     * @param {string} [label]
     * @returns {any}
     */
    function callWasm(fnName, args, label) {
        const what = label || fnName;
        // window is indexed by a runtime string, so one `any` hop here; WasmFn
        // (globals.d.ts) carries the call signature through the rest.
        /** @type {WasmFn | undefined} */
        const fn = /** @type {any} */ (window)[fnName];
        if (typeof fn !== 'function') {
            showError(what, 'WASM function not registered');
            return null;
        }
        let raw;
        try {
            raw = fn(...args);
        } catch (err) {
            showError(what, errorText(err));
            return null;
        }
        let parsed;
        try {
            parsed = JSON.parse(raw);
        } catch (err) {
            showError(what, 'invalid response: ' + errorText(err));
            return null;
        }
        if (parsed && parsed.error) {
            showError(what, parsed.error);
            return null;
        }
        return parsed;
    }

    /**
     * goRuntime resolves once the Go WASM runtime constructor (wasm_exec.js)
     * is defined, loading that script when the page has not already.
     * @returns {Promise<void>}
     */
    function goRuntime() {
        if (typeof Go !== 'undefined') return Promise.resolve();
        return new Promise(function (resolve, reject) {
            const script = document.createElement('script');
            script.src = 'wasm_exec.js';
            script.onload = function () { resolve(); };
            script.onerror = function () { reject(new Error('could not load wasm_exec.js')); };
            document.head.appendChild(script);
        });
    }

    /** @type {Promise<void> | null} */
    let wasmBoot = null;

    /**
     * loadWasm boots the forecast WASM (pvmt.wasm) once per page and resolves
     * when its exports are registered on window; it rejects when the browser
     * has no WebAssembly or either file fails to load. The caller decides how
     * to report a rejection: the dashboard works without the simulator, the
     * game does not.
     * @returns {Promise<void>}
     */
    function loadWasm() {
        if (!wasmBoot) {
            wasmBoot = (async function () {
                if (typeof WebAssembly === 'undefined') throw new Error('WebAssembly unavailable');
                await goRuntime();
                const go = new Go();
                const result = await WebAssembly.instantiateStreaming(fetch('pvmt.wasm'), go.importObject);
                go.run(result.instance);
            })();
        }
        return wasmBoot;
    }

    return {
        fmtMoney: fmtMoney,
        showError: showError,
        errorText: errorText,
        loadJSON: loadJSON,
        callWasm: callWasm,
        loadWasm: loadWasm,
    };
})();
