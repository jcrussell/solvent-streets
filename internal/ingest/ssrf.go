package ingest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
)

// validatePublicHTTPURL rejects URLs whose resolved host is loopback,
// link-local, private, unspecified, or multicast. Defense-in-depth
// against a shared hostile pvmt.toml steering ingest at internal
// services (localhost, 169.254.169.254, RFC1918 ranges) —
// solvent-streets-di49.
//
// Resolution happens at call time, so DNS rebinding after this check
// can still slip past in principle. The mitigation here is per-request
// rather than per-connection because pvmt's HTTP client is shared
// across all ingest sources and the policy is per-city (only the
// arcgis_url path is operator-supplied; Overpass/Nominatim are fixed).
// Wiring a transport-level dial check would force the same policy on
// every source.
func validatePublicHTTPURL(ctx context.Context, urlStr string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
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

func checkPublicAddr(addr netip.Addr, host string) error {
	switch {
	case addr.IsLoopback():
		return fmt.Errorf("refusing %q: resolves to loopback %s (set allow_private_arcgis = true to override)", host, addr)
	case addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast():
		return fmt.Errorf("refusing %q: resolves to link-local %s (set allow_private_arcgis = true to override)", host, addr)
	case addr.IsPrivate():
		return fmt.Errorf("refusing %q: resolves to private %s (set allow_private_arcgis = true to override)", host, addr)
	case addr.IsUnspecified():
		return fmt.Errorf("refusing %q: resolves to unspecified %s (set allow_private_arcgis = true to override)", host, addr)
	case addr.IsMulticast():
		return fmt.Errorf("refusing %q: resolves to multicast %s (set allow_private_arcgis = true to override)", host, addr)
	}
	return nil
}
