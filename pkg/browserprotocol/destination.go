package browserprotocol

import (
	"net"
	"net/netip"
	"net/url"
	"strings"
)

var blockedDestinationPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func validateNavigateURL(raw string) *Failure {
	if raw == "" || len(raw) > 4096 {
		return NewFailure(ErrorProtocolInvalid, "navigate URL is empty or too large", false)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return NewFailure(ErrorProtocolInvalid, "navigate URL must be an HTTP(S) URL without credentials", false)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return NewFailure(ErrorProtocolInvalid, "navigate URL address is not public", false)
		}
		return nil
	}
	if host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".localhost") ||
		host == "metadata.google.internal" || strings.ContainsAny(host, " /\\@") {
		return NewFailure(ErrorProtocolInvalid, "navigate URL host is not public", false)
	}
	return nil
}

func blockedIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range blockedDestinationPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
