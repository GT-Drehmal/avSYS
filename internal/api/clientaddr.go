package api

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ParseTrustedProxies reads a comma-separated list of addresses and ranges,
// as 127.0.0.1,100.64.0.0/10,fd7a:115c:a1e0::/48, of the reverse proxies the
// panel stands behind. A bare address is a range of that one address.
func ParseTrustedProxies(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for item := range strings.SplitSeq(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			p, err := netip.ParsePrefix(item)
			if err != nil {
				return nil, fmt.Errorf("trusted proxies: %q is not an address or a range", item)
			}
			if p.Addr().Is4In6() && p.Bits() >= 96 {
				p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("trusted proxies: %q is not an address or a range", item)
		}
		a = a.Unmap().WithZone("")
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// clientAddress is the address a request came from, without its port.
//
// It is the connection's own unless the connection comes from one of the
// trusted proxies. Then it is read from X-Forwarded-For, from the right,
// skipping the entries that are trusted proxies too: the first that is not
// is the client, as each proxy adds the address it was reached from at the
// end, and anything left of that was sent by the client and could say
// anything. Where every entry is a trusted proxy, the client is the
// left-most. An entry that is not an address ends the walk at the last one
// that was. With no X-Forwarded-For, X-Real-IP is read, from a trusted
// proxy only. A header from anyone else is never read.
func (s *Server) clientAddress(r *http.Request) string {
	return clientAddress(r, s.Config.TrustedProxies)
}

func clientAddress(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, ok := parseAddr(host)
	if !ok || !isTrusted(peer, trusted) {
		return host
	}
	if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
		// Several headers are one list, in order.
		entries := strings.Split(strings.Join(values, ","), ",")
		client := peer
		for i := len(entries) - 1; i >= 0; i-- {
			a, ok := parseAddr(strings.TrimSpace(entries[i]))
			if !ok {
				break
			}
			client = a
			if !isTrusted(a, trusted) {
				break
			}
		}
		return client.String()
	}
	if a, ok := parseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ok {
		return a.String()
	}
	return peer.String()
}

// parseAddr reads an address as proxies write it: bare, or with a port
// (1.2.3.4:5678, [2001:db8::1]:443), or bracketed. IPv4 written as IPv6
// (::ffff:1.2.3.4) is read as IPv4, and a zone is dropped.
func parseAddr(s string) (netip.Addr, bool) {
	if s == "" {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		ap, err2 := netip.ParseAddrPort(s)
		if err2 != nil {
			if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
				return netip.Addr{}, false
			}
			if a, err = netip.ParseAddr(s[1 : len(s)-1]); err != nil {
				return netip.Addr{}, false
			}
		} else {
			a = ap.Addr()
		}
	}
	return a.Unmap().WithZone(""), true
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
