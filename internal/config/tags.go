package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DefaultTagDimension is the dimension a tag with no "dimension:" prefix
// belongs to. Every tag that existed before dimensions were introduced is a
// plain label ("Bay Area", "Top 50"), so they all land here and configs written
// against the flat scheme keep working unchanged.
const DefaultTagDimension = "group"

// StateTagDimension is the dimension of the per-city state tag derived from
// the ", XX" suffix of the city name (see CityConfig.EffectiveTags). An
// explicit "state:XX" tag in the config takes precedence over the derivation.
const StateTagDimension = "state"

// tagDimRe is the shape a dimension name must have. Lowercase so "state:CA"
// and "State:CA" cannot silently become two dimensions, and no spaces so a
// legacy label that happens to contain a colon ("Metro: Boston") is rejected
// with a pointer to the fix rather than mis-parsed as a dimension.
var tagDimRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// stateSuffixRe matches the ", XX" two-letter postal suffix that every US city
// name in the shipped examples carries ("Livermore, CA", "Washington, DC").
var stateSuffixRe = regexp.MustCompile(`, ([A-Z]{2})$`)

// usPostalCodes is every code StateFromName accepts: the 50 states, DC and
// the inhabited territories. Any other ", XX" suffix ("Toronto, ON") is not a
// US state and derives no tag (solvent-streets-zcyy).
var usPostalCodes = func() map[string]bool {
	m := make(map[string]bool)
	for c := range strings.FieldsSeq(`
		AL AK AZ AR CA CO CT DE FL GA HI ID IL IN IA KS KY LA ME MD
		MA MI MN MS MO MT NE NV NH NJ NM NY NC ND OH OK OR PA RI SC
		SD TN TX UT VT VA WA WV WI WY
		DC PR GU VI AS MP`) {
		m[c] = true
	}
	return m
}()

// ParseTag splits a tag into its dimension and value. The split is on the
// FIRST colon only, so a value may itself contain colons. A tag with no colon
// belongs to DefaultTagDimension and is returned whole as the value.
func ParseTag(tag string) (dim, value string) {
	dim, value, ok := strings.Cut(tag, ":")
	if !ok {
		return DefaultTagDimension, tag
	}
	return dim, value
}

// TagLabel is the display form of a tag: the value without its dimension
// prefix. "state:CA" renders as "CA"; a default-dimension tag is unchanged.
func TagLabel(tag string) string {
	_, value := ParseTag(tag)
	return value
}

// validateTag rejects a blank tag and a malformed "dimension:value" prefix. A
// blank tag groups the city under the exporter's "untagged" bucket rather than
// a named group — a silent mis-grouping; a malformed prefix would either create
// a stray dimension or be mis-read as a plain label, so both fail loudly here
// at the config boundary.
func validateTag(tag string) error {
	if strings.TrimSpace(tag) == "" {
		return errors.New("is empty; remove it or give it a label")
	}
	if !strings.ContainsRune(tag, ':') {
		return nil
	}
	dim, value := ParseTag(tag)
	if !tagDimRe.MatchString(dim) {
		return fmt.Errorf("%q has dimension %q, which must match %s (e.g. \"state:CA\", \"metro:Bay Area\")",
			tag, dim, tagDimRe.String())
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%q has no value after the %q: dimension", tag, dim)
	}
	return nil
}

// StateFromName extracts the two-letter postal state from a ", XX" suffix on a
// city name. Returns false when the name has no such suffix or XX is not a US
// postal code (see usPostalCodes).
func StateFromName(name string) (string, bool) {
	m := stateSuffixRe.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil || !usPostalCodes[m[1]] {
		return "", false
	}
	return m[1], true
}

// EffectiveTags returns the city's explicit Tags plus a derived
// "state:XX" tag when the name carries a ", XX" US postal suffix and no explicit tag in
// the state dimension is present. It is the single place the derivation lives;
// the exporter calls it when building CityInfo so cities.json, the inline
// PVMT_CONFIG payload, the selector groups and /api/cities all agree, while
// CityConfig.Tags itself stays a faithful copy of what the file says (config show,
// ResolvedTOML and the include merge are unaffected). The returned slice is
// never the receiver's own backing array when a tag is added; when nothing is
// added the receiver's slice is returned as-is and must not be mutated.
func (c *CityConfig) EffectiveTags() []string {
	for _, t := range c.Tags {
		if dim, _ := ParseTag(t); dim == StateTagDimension {
			return c.Tags
		}
	}
	st, ok := StateFromName(c.Name)
	if !ok {
		return c.Tags
	}
	out := make([]string, 0, len(c.Tags)+1)
	out = append(out, c.Tags...)
	return append(out, StateTagDimension+":"+st)
}
