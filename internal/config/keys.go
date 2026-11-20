package config

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// renamedKeys maps a key that pvmt.toml no longer takes to the key that
// replaced it, both as generic paths (array elements unindexed: a per-city key
// is "cities.<key>"). A file that sets an old key fails to load with a message
// naming the new one; there are no silent aliases.
var renamedKeys = map[string]string{}

// CityPath is how a message addresses one [[cities]] entry: cities[<slug>],
// or cities[<index>] when the name gives no slug.
func CityPath(i int, city *CityConfig) string {
	if slug := city.Slug(); slug != "" {
		return "cities[" + slug + "]"
	}
	return fmt.Sprintf("cities[%d]", i)
}

// serviceOverridePath addresses one [[services]] entry by its name, or by
// index when it has none.
func serviceOverridePath(i int, s ServiceOverride) string {
	if name := strings.TrimSpace(s.Name); name != "" {
		return "services[" + name + "]"
	}
	return fmt.Sprintf("services[%d]", i)
}

// unknownKeysError reports the keys the decoder did not consume: a renamed
// key names its replacement, and any other key gets the nearest known key at
// the same level as a suggestion. Keys inside [[cities]] and [[services]] are
// addressed by the entry that holds them, as cities[<slug>].key.
func unknownKeysError(undecoded []toml.Key, data []byte) error {
	var raw map[string]any
	_, _ = toml.Decode(string(data), &raw) // the same bytes already decoded once
	known := knownKeys()

	// An unknown table is reported once, not once more for each key inside it.
	set := make(map[string]bool, len(undecoded))
	for _, k := range undecoded {
		set[k.String()] = true
	}
	// The decoder lists a key once per array element that sets it, and
	// locateKey already finds every such element, so each generic key is
	// reported once.
	reported := make(map[string]bool, len(undecoded))
	var renamed, unknown []string
	for _, k := range undecoded {
		generic := k.String()
		if hasUndecodedParent(k, set) || reported[generic] {
			continue
		}
		reported[generic] = true
		where := locateKey(raw, k)
		if to, ok := renamedKeys[generic]; ok {
			for _, w := range where {
				renamed = append(renamed, fmt.Sprintf("%s was renamed to %s", w, renameTarget(w, generic, to)))
			}
			continue
		}
		hint := ""
		if s := suggestKey(k, known); s != "" {
			hint = fmt.Sprintf(" (did you mean %s?)", s)
		}
		for _, w := range where {
			unknown = append(unknown, w+hint)
		}
	}
	var errs []error
	if len(renamed) > 0 {
		errs = append(errs, errors.New(strings.Join(renamed, "; ")))
	}
	if len(unknown) > 0 {
		errs = append(errs, fmt.Errorf("unknown config key(s): %s", strings.Join(unknown, ", ")))
	}
	return errors.Join(append([]error{ErrInvalidConfig}, errs...)...)
}

func hasUndecodedParent(k toml.Key, set map[string]bool) bool {
	for n := len(k) - 1; n > 0; n-- {
		if set[k[:n].String()] {
			return true
		}
	}
	return false
}

// renameTarget spells the replacement of a renamed key at the address where
// the old one was found: the generic prefix the two share is swapped for that
// address's own spelling, so cities[oakland-ca].min_hex_area maps onto the
// same city.
func renameTarget(where, generic, to string) string {
	// Find the longest leading run of path elements the old and new key share.
	g, t := strings.Split(generic, "."), strings.Split(to, ".")
	n := 0
	for n < len(g)-1 && n < len(t)-1 && g[n] == t[n] {
		n++
	}
	if n == 0 {
		return to
	}
	// where has the same number of elements as generic, with indices added.
	w := strings.Split(where, ".")
	return strings.Join(append(w[:n:n], t[n:]...), ".")
}

// locateKey spells an undecoded key once per place it occurs, addressing
// [[cities]] and [[services]] entries by slug or name. The decoder's key
// carries no array index, so the entries are found in the raw decode.
func locateKey(raw map[string]any, k toml.Key) []string {
	if len(k) < 2 || (k[0] != "cities" && k[0] != "services") {
		return []string{k.String()}
	}
	entries, _ := raw[k[0]].([]map[string]any)
	var out []string
	for i, e := range entries {
		if !hasPath(e, k[1:]) {
			continue
		}
		var addr string
		if k[0] == "cities" {
			name, _ := e["name"].(string)
			addr = CityPath(i, &CityConfig{Name: name})
		} else {
			name, _ := e["name"].(string)
			addr = serviceOverridePath(i, ServiceOverride{Name: name})
		}
		out = append(out, addr+"."+strings.Join(k[1:], "."))
	}
	if len(out) == 0 {
		return []string{k.String()}
	}
	return out
}

// hasPath reports whether path is set in m, looking through arrays of tables.
func hasPath(m map[string]any, path []string) bool {
	v, ok := m[path[0]]
	if !ok {
		return false
	}
	if len(path) == 1 {
		return true
	}
	switch t := v.(type) {
	case map[string]any:
		return hasPath(t, path[1:])
	case []map[string]any:
		for _, e := range t {
			if hasPath(e, path[1:]) {
				return true
			}
		}
	}
	return false
}

// knownKeys lists every key a pvmt.toml may set, as generic paths.
func knownKeys() map[string]bool {
	out := map[string]bool{}
	collectKeys(reflect.TypeFor[Config](), "", out)
	return out
}

func collectKeys(t reflect.Type, prefix string, out map[string]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if name == "" || name == "-" || !f.IsExported() {
			continue
		}
		path := prefix + name
		out[path] = true
		ft := f.Type
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" {
			collectKeys(ft, path+".", out)
		}
	}
}

// suggestKey returns the known key at the same level nearest to k's last
// element: within an edit distance of a third of the name (at least 2), or a
// known key that extends the name by a word ("budget" for "current_budget").
// It returns "" when none is that close or two are equally near.
func suggestKey(k toml.Key, known map[string]bool) string {
	parent := strings.Join(k[:len(k)-1], ".")
	if parent != "" {
		parent += "."
	}
	leaf := k[len(k)-1]
	limit := max(2, len(leaf)/3)
	suggest := suggestCandidates(parent, known)
	names := make([]string, 0, len(suggest))
	for n := range suggest {
		names = append(names, n)
	}
	sort.Strings(names)
	// A tie is ambiguous ("rate" is as near decay_rate as growth_rate), so it
	// suggests nothing.
	var best []string
	bestDist := limit + 1
	for _, c := range names {
		d := editDistance(leaf, c)
		if strings.HasSuffix(c, "_"+leaf) || strings.HasPrefix(c, leaf+"_") {
			d = min(d, limit)
		}
		switch {
		case d < bestDist:
			best, bestDist = []string{suggest[c]}, d
		case d == bestDist && !slices.Contains(best, suggest[c]):
			best = append(best, suggest[c])
		}
	}
	if len(best) != 1 {
		return ""
	}
	return best[0]
}

// suggestCandidates maps each key name at the level parent (a generic path
// ending in "." or "") to what to suggest for it: the name itself, or for a
// renamed key its replacement ("yeers" suggests horizon_years, not the old
// years).
func suggestCandidates(parent string, known map[string]bool) map[string]string {
	leafOf := func(path string) (string, bool) {
		rest, ok := strings.CutPrefix(path, parent)
		return rest, ok && !strings.Contains(rest, ".")
	}
	suggest := map[string]string{}
	for path := range known {
		if name, ok := leafOf(path); ok {
			suggest[name] = name
		}
	}
	for from, to := range renamedKeys {
		name, ok := leafOf(from)
		if !ok {
			continue
		}
		if toName, same := leafOf(to); same {
			suggest[name] = toName
		} else {
			suggest[name] = to
		}
	}
	return suggest
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
