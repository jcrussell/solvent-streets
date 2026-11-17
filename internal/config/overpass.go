package config

import (
	"net/netip"
	"net/url"
	"strings"
)

// OverpassConfig picks the Overpass API instance every Overpass query goes to
// (roads, parking, sidewalks, facilities, the census step's Overpass fetches,
// OSM water and boundary relations).
type OverpassConfig struct {
	// URL is the instance's interpreter endpoint, such as
	// "https://overpass.private.coffee/api/interpreter". Empty uses the public
	// overpass-api.de instance (ingest.DefaultOverpassURL). PVMT_OVERPASS_URL
	// overrides it. It is read from the loaded config only; an [[include]]d
	// file's [overpass] is ignored. checkOverpassURL is the rule, applied at
	// load; the SSRF guard (ingest.ValidatePublicHTTPURL) runs before the
	// first fetch, since it needs DNS.
	URL string `toml:"url,omitempty"`
}

// checkOverpassURL returns "" when s is a usable Overpass endpoint, or a
// reason completing the sentence `overpass.url "s" ...`. It is the whole rule
// for both the file value and PVMT_OVERPASS_URL: https only, a DNS host (not
// an IP literal), no credentials, query or fragment (the endpoint is POSTed
// to, and its URL is stored in plaintext in the HTTP cache's metadata), and a
// path ending in /interpreter, which catches a URL pasted from an instance's
// web page instead of its API.
func checkOverpassURL(s string) string {
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return "is not a valid URL"
	case u.Scheme != "https":
		return "must be an https:// URL"
	case u.Opaque != "" || u.Host == "":
		return "has no host"
	case u.User != nil:
		return "must not carry credentials"
	case u.RawQuery != "" || u.ForceQuery:
		return "must not have a query string"
	case u.Fragment != "" || strings.Contains(s, "#"):
		return "must not have a fragment"
	}
	host := u.Hostname()
	if host == "" {
		return "has no host"
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return "must name its host by DNS name, not an IP address"
	}
	if !strings.HasSuffix(u.Path, "/interpreter") {
		return `must be the instance's API endpoint, ending in "/interpreter"`
	}
	return ""
}

// parseOverpassURLEnv is PVMT_OVERPASS_URL's rule (checkOverpassURL).
func parseOverpassURLEnv(s string) (string, string) {
	if reason := checkOverpassURL(s); reason != "" {
		return "", reason
	}
	return s, ""
}

// OverpassURL resolves the Overpass endpoint: a usable PVMT_OVERPASS_URL,
// else c's [overpass] url, else "" with SourceDefault, which callers map to
// ingest.DefaultOverpassURL. c may be nil (a command run with no pvmt.toml),
// in which case only the env layer applies. An unusable PVMT_OVERPASS_URL is
// dropped, and EnvProblems reports it.
func OverpassURL(c *Config) (string, Source) {
	if v, ok := envOverride(EnvOverpassURL, parseOverpassURLEnv); ok {
		return v, Source{Kind: SourceEnv, Detail: EnvOverpassURL}
	}
	if c != nil && c.Overpass.URL != "" {
		return c.Overpass.URL, Source{Kind: SourceFile, Detail: "overpass.url"}
	}
	return "", Source{Kind: SourceDefault}
}
