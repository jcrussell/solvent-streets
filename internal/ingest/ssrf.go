package ingest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"syscall"

	"github.com/jcrussell/solvent-streets/internal/redact"
)

// SSRF-guard rule for outbound URLs in package ingest
// =====================================================
//
// Any outbound URL whose host is even partially operator- or
// user-controlled MUST pass through validatePublicHTTPURL before the
// HTTP client touches it. The guard rejects loopback, link-local,
// private (RFC1918), unspecified, and multicast addresses so a hostile
// pvmt.toml cannot steer ingest at internal services
// (localhost, 169.254.169.254, RFC1918 ranges).
//
// It is wired into arcgis.go (per city), and into the Overpass endpoint
// ([overpass] url / PVMT_OVERPASS_URL), which the factory's
// cmdutil.Factory.OverpassURL validates once, after config load and before
// the first Overpass fetch; the default overpass-api.de endpoint skips it.
// Nominatim (nominatim.go) uses a hardcoded endpoint that isn't
// operator-controlled. If it — or any new source — becomes configurable,
// route it through this guard at the same layer (post-config-load,
// pre-fetch) so the policy is not silently lost.
//
// Dial layer: the factory's transport dials through PublicDialer, which
// applies the same address ranges to the IP each connection is made to, so a
// DNS answer that changes between the URL check and the dial (rebinding) is
// refused too. It honors the same AllowPrivate context opt-in, and leaves the
// HTTP proxy's own address unchecked.
//
// Redirect layer: the per-call guard above only validates the INITIAL
// URL. A 302 to 169.254.169.254/127.0.0.1 from a hostile or MITM'd
// upstream would otherwise defeat it, since the shared client follows
// redirects to any host. The factory's client.CheckRedirect re-runs
// ValidatePublicHTTPURL on every hop's req.URL (skipping the check only
// when the request context carries the AllowPrivate opt-in, set by
// ArcGISSource.Fetch and propagated by the stdlib across redirects).

// allowPrivateKey carries the AllowPrivate opt-in on a request context so
// the client-level CheckRedirect can honor self-hosted/staging endpoints
// across redirect hops. ArcGISSource.Fetch derives the request context
// with this value; the stdlib propagates it onto each redirect request.
type allowPrivateKey struct{}

// WithAllowPrivate marks ctx so the redirect-layer SSRF guard
// (CheckRedirect) skips public-URL validation — the redirect-hop analog
// of ArcGISSource.AllowPrivate, for self-hosted endpoints on internal
// networks.
func WithAllowPrivate(ctx context.Context) context.Context {
	return context.WithValue(ctx, allowPrivateKey{}, true)
}

// AllowPrivateFromContext reports whether ctx opted out of the SSRF guard
// via WithAllowPrivate.
func AllowPrivateFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(allowPrivateKey{}).(bool)
	return v
}

// ValidatePublicHTTPURL is the exported wrapper around the per-call SSRF
// guard, used by the factory's client.CheckRedirect to re-validate each
// redirect hop's destination. See the package design comment above.
func ValidatePublicHTTPURL(ctx context.Context, urlStr string) error {
	return validatePublicHTTPURL(ctx, urlStr)
}

// validatePublicHTTPURL rejects URLs whose resolved host is loopback,
// link-local, private, unspecified, or multicast. Defense-in-depth
// against a shared hostile pvmt.toml steering ingest at internal
// services (localhost, 169.254.169.254, RFC1918 ranges) —
//
// Resolution happens at call time; PublicDialer re-checks the address each
// connection is actually made to, which closes the DNS-rebinding gap.
func validatePublicHTTPURL(ctx context.Context, urlStr string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("parse url: %w", redact.Error(err))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme %q is not http or https", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("url has no host")
	}

	// Hostname() may itself be an IP literal — handle both forms.
	if addr, err := netip.ParseAddr(host); err == nil {
		return checkPublicAddr(addr, host)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("resolve %q: no addresses returned", host)
	}
	for _, a := range addrs {
		if err := checkPublicAddr(a, host); err != nil {
			return err
		}
	}
	return nil
}

// blockedPrefixes are IANA special-use / non-routable ranges the stdlib
// IsPrivate/IsLoopback/etc. predicates do NOT cover but which must still be
// refused as SSRF targets: CGNAT space (a hostile config could route these at
// carrier-internal hosts), the TEST-NET documentation ranges, the benchmarking
// range, the reserved 240/4 block, the IPv4 broadcast address, 0.0.0.0/8 and
// the deprecated IPv6 site-local range. Parsed once at package init; every
// candidate address is checked (after Unmap, and after translating a NAT64 or
// 6to4 address to the IPv4 address it embeds: embeddedIPv4) against this list
// on top of the stdlib predicates.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),      // RFC 6598 CGNAT
	netip.MustParsePrefix("192.0.2.0/24"),       // RFC 5737 TEST-NET-1
	netip.MustParsePrefix("198.51.100.0/24"),    // RFC 5737 TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),     // RFC 5737 TEST-NET-3
	netip.MustParsePrefix("198.18.0.0/15"),      // RFC 2544 benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),        // RFC 1112 reserved (future use)
	netip.MustParsePrefix("255.255.255.255/32"), // limited broadcast
	netip.MustParsePrefix("0.0.0.0/8"),          // RFC 1122 "this network"; 0.0.0.0/32 alone is IsUnspecified
	netip.MustParsePrefix("fec0::/10"),          // RFC 3879 deprecated site-local
}

// translatedPrefixes are the IPv6 ranges that embed an IPv4 address the
// packet is translated to, with the byte offset of those four bytes: the
// NAT64 well-known prefix (RFC 6052), the local-use NAT64 prefix (RFC 8215,
// as deployed with a /96 inside it) and 6to4 (RFC 3056). An address in one
// is judged by the IPv4 address it reaches, so a DNS64 network's public
// upstreams pass while 64:ff9b::10.0.0.1 is still refused.
var translatedPrefixes = []struct {
	prefix netip.Prefix
	offset int
}{
	{netip.MustParsePrefix("64:ff9b::/96"), 12},
	{netip.MustParsePrefix("64:ff9b:1::/48"), 12},
	{netip.MustParsePrefix("2002::/16"), 2},
}

// embeddedIPv4 returns the IPv4 address a NAT64 or 6to4 address reaches.
func embeddedIPv4(addr netip.Addr) (netip.Addr, netip.Prefix, bool) {
	for _, tp := range translatedPrefixes {
		if tp.prefix.Contains(addr) {
			b := addr.As16()
			return netip.AddrFrom4([4]byte(b[tp.offset : tp.offset+4])), tp.prefix, true
		}
	}
	return netip.Addr{}, netip.Prefix{}, false
}

// BlockedAddressError is the SSRF guard's refusal: Host resolves to (or
// connects to) Addr, which is not a public address. Kind names the range
// ("loopback", "private", "special-use"), and Prefix is the blocked prefix
// for a special-use address. The caller decides
// whether an opt-out exists and hints at it: only ArcGIS has one
// (allow_private_arcgis).
type BlockedAddressError struct {
	Host   string
	Addr   netip.Addr
	Kind   string
	Prefix netip.Prefix
}

func (e *BlockedAddressError) Error() string {
	if e.Prefix.IsValid() {
		return fmt.Sprintf("refusing %q: resolves to %s %s in %s", e.Host, e.Kind, e.Addr, e.Prefix)
	}
	return fmt.Sprintf("refusing %q: resolves to %s %s", e.Host, e.Kind, e.Addr)
}

// blockedKind names the non-public range addr is in, and its prefix for a
// special-use one; "" for a public address.
func blockedKind(addr netip.Addr) (string, netip.Prefix) {
	// Collapse IPv4-mapped IPv6 (e.g. ::ffff:10.0.0.1) to its IPv4 form FIRST
	// so the stdlib predicates and the prefix blocklist below see the real
	// address family — otherwise a mapped private/loopback address slips
	// through every IsPrivate/IsLoopback check.
	addr = addr.Unmap()
	if v4, via, ok := embeddedIPv4(addr); ok {
		if kind, _ := blockedKind(v4); kind != "" {
			return kind + " " + v4.String() + " through", via
		}
		return "", netip.Prefix{}
	}

	switch {
	case addr.IsLoopback():
		return "loopback", netip.Prefix{}
	case addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast():
		return "link-local", netip.Prefix{}
	case addr.IsPrivate():
		return "private", netip.Prefix{}
	case addr.IsUnspecified():
		return "unspecified", netip.Prefix{}
	case addr.IsMulticast():
		return "multicast", netip.Prefix{}
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return "special-use", p
		}
	}
	return "", netip.Prefix{}
}

func checkPublicAddr(addr netip.Addr, host string) error {
	if kind, prefix := blockedKind(addr); kind != "" {
		return &BlockedAddressError{Host: host, Addr: addr.Unmap(), Kind: kind, Prefix: prefix}
	}
	return nil
}

// PublicDialer opens connections only to public addresses, checking the
// address each connection is actually made to: the per-URL guard
// (validatePublicHTTPURL) resolves the host before the fetch, and a DNS
// answer that changes in between (rebinding) would otherwise reach whatever
// the second answer names. It applies blockedKind's ranges to the dialed IP
// and refuses with a BlockedAddressError. A request whose context carries
// WithAllowPrivate is not checked, nor is a dial to the HTTP proxy the
// transport's Proxy (wrapped with PublicDialer.Proxy) chose, which may well
// sit on a private network.
type PublicDialer struct {
	Dialer *net.Dialer
	// Exempt, when set, lets through an address the guard would refuse.
	// Tests use it to reach a loopback httptest server.
	Exempt  func(netip.Addr) bool
	proxies sync.Map // canonical "host:port" of every proxy Proxy returned
}

// Proxy wraps a Transport.Proxy func so the dialer knows the proxies it
// returns and dials them unchecked.
func (p *PublicDialer) Proxy(next func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		u, err := next(req)
		if err == nil && u != nil {
			p.proxies.Store(proxyAddr(u), true)
		}
		return u, err
	}
}

// proxyAddr is the address net/http dials for proxy u: its host and port,
// with the scheme's default port when it names none.
func proxyAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// DialContext is a Transport.DialContext that refuses a non-public
// destination (see PublicDialer).
func (p *PublicDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if AllowPrivateFromContext(ctx) {
		return p.Dialer.DialContext(ctx, network, address)
	}
	if _, ok := p.proxies.Load(address); ok {
		return p.Dialer.DialContext(ctx, network, address)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	d := *p.Dialer
	d.ControlContext = func(_ context.Context, _, addr string, _ syscall.RawConn) error {
		return p.checkDialed(host, addr)
	}
	return d.DialContext(ctx, network, address)
}

// checkDialed refuses the resolved "ip:port" addr a dial to host is about to
// connect to, unless it is public or Exempt.
func (p *PublicDialer) checkDialed(host, addr string) error {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return fmt.Errorf("parse dial address %q: %w", addr, err)
	}
	if p.Exempt != nil && p.Exempt(ap.Addr()) {
		return nil
	}
	return checkPublicAddr(ap.Addr(), host)
}
