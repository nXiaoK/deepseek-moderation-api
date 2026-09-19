package audit

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func parseTrustedProxies(values []string) ([]netip.Prefix, error) {
	if len(values) > 64 {
		return nil, fmt.Errorf("AUDIT_TRUSTED_PROXIES supports at most 64 IP addresses or CIDRs")
	}
	trusted := make([]netip.Prefix, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			addr, addrErr := netip.ParseAddr(value)
			if addrErr != nil || addr.Zone() != "" {
				return nil, fmt.Errorf("AUDIT_TRUSTED_PROXIES requires IP addresses or CIDRs")
			}
			addr = addr.Unmap()
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		if prefix.Bits() == 0 {
			return nil, fmt.Errorf("AUDIT_TRUSTED_PROXIES must not trust all addresses")
		}
		if prefix.Addr().Is4In6() && prefix.Bits() >= 96 {
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
			if prefix.Bits() == 0 {
				return nil, fmt.Errorf("AUDIT_TRUSTED_PROXIES must not trust all IPv4 addresses")
			}
		}
		trusted = append(trusted, prefix.Masked())
	}
	return trusted, nil
}

func trustedAddress(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Trust X-Forwarded-For only when the direct peer is explicitly configured.
// Walk right to left, stopping at the first untrusted hop; a client-supplied
// leftmost address can never override that hop. Malformed chains use the peer.
func requestClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	peer = peer.Unmap()
	if !trustedAddress(peer, trusted) {
		return peer.String()
	}
	forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if forwarded == "" || len(forwarded) > 4096 {
		return peer.String()
	}
	parts := strings.Split(forwarded, ",")
	if len(parts) > 32 {
		return peer.String()
	}
	addresses := make([]netip.Addr, len(parts))
	for i, raw := range parts {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil || addr.Zone() != "" {
			return peer.String()
		}
		addresses[i] = addr.Unmap()
	}
	client := peer
	for i := len(addresses) - 1; i >= 0 && trustedAddress(client, trusted); i-- {
		client = addresses[i]
	}
	return client.String()
}
