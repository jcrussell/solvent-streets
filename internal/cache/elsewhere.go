package cache

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/warn"
)

// elsewhere notices a cache miss for a request the cache holds under another
// endpoint: the same method, query and body sent to a different scheme, host
// or path, as when an Overpass mirror is set for one run and not the next.
// The miss is still fetched live; elsewhere only warns, once per pair of
// endpoints, naming the endpoint the cache holds the response under. The
// endpoints are read from the cache's own entries (their .meta url), so no
// mirror is named in code.
type elsewhere struct {
	once      sync.Once
	endpoints []endpoint // every endpoint the cache held entries for at the first miss

	mu     sync.Mutex
	warned map[[2]string]bool // {requested, cached} pairs already reported
}

// endpoint is a URL without its query: scheme, host and path.
type endpoint struct{ scheme, host, path string }

func (e endpoint) String() string { return e.scheme + "://" + e.host + e.path }

func endpointOf(u *url.URL) endpoint { return endpoint{u.Scheme, u.Host, u.Path} }

// load lists the distinct endpoints of the entries in dir. It runs once per
// transport, on the first miss, so a run that hits the cache throughout
// never reads the directory.
func (e *elsewhere) load(dir string) {
	e.once.Do(func() {
		names, err := filepath.Glob(filepath.Join(dir, "*"+metaExt))
		if err != nil {
			return
		}
		seen := map[endpoint]bool{}
		for _, name := range names {
			data, err := os.ReadFile(name)
			if err != nil {
				continue
			}
			var m entryMeta
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			u, err := url.Parse(m.URL)
			if err != nil || u.Host == "" {
				continue
			}
			seen[endpointOf(u)] = true
		}
		for ep := range seen {
			e.endpoints = append(e.endpoints, ep)
		}
		slices.SortFunc(e.endpoints, func(a, b endpoint) int { return strings.Compare(a.String(), b.String()) })
	})
}

// checkElsewhere looks for a request with no cache entry under every other
// endpoint the cache holds and, on the first fresh entry found for a pair
// not yet reported, writes a warning to t.Warn. A request with an entry
// (haveCache, fresh or being revalidated), a disabled cache and a --force
// bypass are not misses to explain.
func (t *CachingTransport) checkElsewhere(req *http.Request, body []byte, haveCache bool) {
	if t.Warn == nil || haveCache || t.TTL <= 0 || bypassRequested(req.Context()) {
		return
	}
	method, reqURL := req.Method, req.URL
	e := &t.elsewhere
	e.load(t.Dir)
	want := endpointOf(reqURL)
	for _, ep := range e.endpoints {
		if ep == want || e.reported(want, ep) {
			continue
		}
		alt := *reqURL
		alt.Scheme, alt.Host, alt.Path, alt.RawPath = ep.scheme, ep.host, ep.path, ""
		if !t.freshEntry(cacheKey(method, alt.String(), body)) {
			continue
		}
		if !e.markReported(want, ep) {
			continue
		}
		warn.Fprint(t.Warn, hint.Withf(errElsewhere{want, ep},
			"point this source at %s to reuse the cached responses", ep))
	}
}

// freshEntry reports whether key has a cache entry younger than the TTL.
func (t *CachingTransport) freshEntry(key string) bool {
	data, err := os.ReadFile(filepath.Join(t.Dir, key+metaExt))
	if err != nil {
		return false
	}
	var m entryMeta
	return json.Unmarshal(data, &m) == nil && time.Since(m.Timestamp) < t.TTL
}

func (e *elsewhere) reported(want, ep endpoint) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.warned[[2]string{want.String(), ep.String()}]
}

// markReported records the pair and reports whether this call was the first
// to do so, so concurrent misses warn once.
func (e *elsewhere) markReported(want, ep endpoint) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	k := [2]string{want.String(), ep.String()}
	if e.warned[k] {
		return false
	}
	if e.warned == nil {
		e.warned = map[[2]string]bool{}
	}
	e.warned[k] = true
	return true
}

// errElsewhere is the warning's message: requests to one endpoint miss the
// cache, which holds them under another.
type errElsewhere struct{ requested, cached endpoint }

func (e errElsewhere) Error() string {
	return "fetching from " + e.requested.String() + " live, but the cache holds these requests under " + e.cached.String()
}
