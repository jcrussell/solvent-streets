// Shared selector logic for the index (app.js) and play (game.js) pages: the
// dimension:value tag vocabulary, ?tag= parsing, AND-matching across
// dimensions, and the client-side rebuild of #city-select when a filter
// changes. One implementation, loaded by both pages as a plain classic script
// BEFORE the page script, exposing a single window.PvmtSelectors namespace (no
// module wrapper — like app.js/game.js it is go:embed'd verbatim and loaded by
// a bare <script src>; see TemplateData.IndexConfigJSON/GameConfigJSON for the
// tagDimensions payload it consumes).
//
// Tag forms. A city's tags are in CONFIG form — "Bay Area" for the default
// "group" dimension, "state:CA" for any other. The URL carries the NORMALIZED
// form, always "dim:value", so ?tag=group:Bay%20Area and the legacy bare
// ?tag=Bay%20Area both resolve to the same filter. The selected-filter map
// passed between these functions is {dim: configTag}.
window.PvmtSelectors = (function () {
    'use strict';

    // Mirrors config.DefaultTagDimension on the Go side.
    const DEFAULT_DIM = 'group';

    /**
     * normalizeTag returns the canonical "dim:value" form of a tag; a tag with
     * no colon belongs to the default dimension.
     * @param {string} tag
     */
    function normalizeTag(tag) {
        return tag.indexOf(':') === -1 ? DEFAULT_DIM + ':' + tag : tag;
    }

    /** @param {string} tag */
    function dimensionOf(tag) {
        const t = normalizeTag(tag);
        return t.slice(0, t.indexOf(':'));
    }

    /**
     * parseTags reads every ?tag= param into a {dim: configTag} map, keeping
     * only values that exist in dims — an unknown label is ignored rather than
     * filtering every city away, and so is a dimension with a single value
     * (it renders no filter control). Later params win within a dimension.
     * @param {URLSearchParams} params
     * @param {PvmtTagDimension[] | undefined} dims
     * @returns {Record<string, string>}
     */
    function parseTags(params, dims) {
        /** @type {Record<string, string>} */
        const out = {};
        params.getAll('tag').forEach(function (raw) {
            const want = normalizeTag(raw);
            (dims || []).forEach(function (d) {
                // A dimension with one value renders no (visible) filter, so a
                // tag in it could never be cleared: ignore it rather than apply
                // a filter the user cannot see.
                if (d.values.length < 2) return;
                d.values.forEach(function (v) {
                    if (normalizeTag(v.tag) === want) out[d.name] = v.tag;
                });
            });
        });
        return out;
    }

    /**
     * urlTags serializes a selected-filter map as normalized tags in dimension
     * order, ready for repeated ?tag= params.
     * @param {Record<string, string>} selected
     * @param {PvmtTagDimension[] | undefined} dims
     * @returns {string[]}
     */
    function urlTags(selected, dims) {
        /** @type {string[]} */
        const out = [];
        (dims || []).forEach(function (d) {
            if (selected[d.name]) out.push(normalizeTag(selected[d.name]));
        });
        return out;
    }

    /**
     * matches reports whether a city carrying cityTags passes every selected
     * filter (AND across dimensions). An empty selection matches everything.
     * @param {string[] | undefined} cityTags
     * @param {Record<string, string>} selected
     */
    function matches(cityTags, selected) {
        const tags = (cityTags || []).map(normalizeTag);
        return Object.keys(selected).every(function (dim) {
            return tags.indexOf(normalizeTag(selected[dim])) !== -1;
        });
    }

    /**
     * @template {{ tags?: string[] }} T
     * @param {T[]} cities
     * @param {Record<string, string>} selected
     * @returns {T[]}
     */
    function filterCities(cities, selected) {
        return cities.filter(function (c) { return matches(c.tags, selected); });
    }

    /**
     * readFilters collects the non-empty values of every .tag-filter select
     * under root into a {dim: configTag} map.
     * @param {ParentNode} [root]
     * @returns {Record<string, string>}
     */
    function readFilters(root) {
        /** @type {Record<string, string>} */
        const out = {};
        Array.from((root || document).querySelectorAll('select.tag-filter')).forEach(function (el) {
            const sel = /** @type {HTMLSelectElement} */ (el);
            if (sel.value) out[sel.dataset.dim] = sel.value;
        });
        return out;
    }

    /**
     * writeFilters sets each .tag-filter select to selected[dim], or to "All"
     * when the dimension is unselected or the value is not one of its options.
     * @param {Record<string, string>} selected
     * @param {ParentNode} [root]
     */
    function writeFilters(selected, root) {
        Array.from((root || document).querySelectorAll('select.tag-filter')).forEach(function (el) {
            const sel = /** @type {HTMLSelectElement} */ (el);
            const want = selected[sel.dataset.dim] || '';
            const known = Array.from(sel.options).some(function (o) { return o.value === want; });
            sel.value = known ? want : '';
        });
    }

    /**
     * sameFilters compares two selected-filter maps by normalized value.
     * @param {Record<string, string>} a
     * @param {Record<string, string>} b
     */
    function sameFilters(a, b) {
        const ka = Object.keys(a).filter(function (k) { return a[k]; });
        const kb = Object.keys(b).filter(function (k) { return b[k]; });
        if (ka.length !== kb.length) return false;
        return ka.every(function (k) { return b[k] && normalizeTag(a[k]) === normalizeTag(b[k]); });
    }

    /** @param {PvmtCity} a @param {PvmtCity} b */
    function byName(a, b) {
        const x = a.name.toLowerCase(), y = b.name.toLowerCase();
        return x < y ? -1 : x > y ? 1 : 0;
    }

    /**
     * rebuildCitySelect regenerates #city-select from the flat cities array
     * restricted to `selected`, laid out exactly as the Go template renders it
     * server-side: one <optgroup> per default-dimension value in dims[0] order
     * (a multi-tag city appears under each), then untagged cities as bare
     * <option>s, cities sorted case-insensitively by name within each. Options
     * carry data-bbox when the city has one, which the index page's selectCity
     * reads from the DOM. The previous selection (or
     * `prefer`) is kept when it survives the filter, otherwise the first visible
     * city is selected; with no match the select holds one disabled "No cities
     * match" option. Returns the selected slug, or '' when nothing matches.
     * @param {HTMLSelectElement} sel
     * @param {PvmtCity[]} cities
     * @param {PvmtTagDimension[] | undefined} dims
     * @param {Record<string, string>} selected
     * @param {string} [prefer]
     * @returns {string}
     */
    function rebuildCitySelect(sel, cities, dims, selected, prefer) {
        const prev = prefer || sel.value;
        const visible = filterCities(cities, selected);
        const doc = sel.ownerDocument;
        sel.textContent = '';
        if (visible.length === 0) {
            const o = doc.createElement('option');
            o.value = '';
            o.disabled = true;
            o.selected = true;
            o.textContent = 'No cities match';
            sel.appendChild(o);
            return '';
        }
        /** @param {PvmtCity} c */
        function makeOption(c) {
            const o = doc.createElement('option');
            o.value = c.slug;
            o.textContent = c.name;
            if (c.bbox) o.dataset.bbox = c.bbox.join(',');
            return o;
        }
        const groupDim = dims && dims.length > 0 && dims[0].name === DEFAULT_DIM ? dims[0] : null;
        const grouped = new Set();
        if (groupDim) {
            groupDim.values.forEach(function (v) {
                const members = visible.filter(function (c) {
                    return (c.tags || []).indexOf(v.tag) !== -1;
                }).sort(byName);
                if (members.length === 0) return;
                const og = doc.createElement('optgroup');
                og.label = v.label;
                members.forEach(function (c) { og.appendChild(makeOption(c)); grouped.add(c.slug); });
                sel.appendChild(og);
            });
        }
        visible.filter(function (c) { return !grouped.has(c.slug); }).sort(byName).forEach(function (c) {
            sel.appendChild(makeOption(c));
        });
        const keep = visible.some(function (c) { return c.slug === prev; }) ? prev : sel.options[0].value;
        sel.value = keep;
        return keep;
    }

    return {
        DEFAULT_DIM: DEFAULT_DIM,
        normalizeTag: normalizeTag,
        dimensionOf: dimensionOf,
        parseTags: parseTags,
        urlTags: urlTags,
        matches: matches,
        filterCities: filterCities,
        readFilters: readFilters,
        writeFilters: writeFilters,
        sameFilters: sameFilters,
        rebuildCitySelect: rebuildCitySelect,
    };
})();
