package drift

import (
	"context"
	"net"
	"time"
)

// fallbackDNS is the ordered list of resolvers tried when Go's pure-Go
// resolver picks a loopback DNS server (the symptom of /etc/resolv.conf
// being missing or empty — typical on Termux/Android, where Android's
// own resolver lives behind a binder service the Go runtime can't see).
// Picked for ubiquity and pinned to numeric IPs so this path itself
// doesn't recurse through the resolver we're trying to fix.
var fallbackDNS = []string{
	"1.1.1.1:53",
	"8.8.8.8:53",
}

// installDNSFallback overrides net.DefaultResolver with a wrapper that
// retries against fallbackDNS when the native resolver would query a
// loopback address AND that dial fails. Process-wide so every http/net
// call in the drift binary inherits the fix — not just `drift update`.
//
// Try-first ordering matters: on standard Linux with systemd-resolved,
// the resolver legitimately points at 127.0.0.53 — the local stub is
// what knows about VPN-overridden DNS, split-horizon, and per-link
// nameservers. Substituting eagerly would break those setups. The
// fallback only fires when the loopback dial itself fails (typical
// Termux/Android case where /etc/resolv.conf is missing and Go's
// resolver picks IANA-default [::1]:53 with nothing listening).
func installDNSFallback() {
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			conn, err := d.DialContext(ctx, network, address)
			if err == nil || !isLoopbackDNS(address) {
				return conn, err
			}
			for _, fb := range fallbackDNS {
				if c, ferr := d.DialContext(ctx, network, fb); ferr == nil {
					return c, nil
				}
			}
			return conn, err
		},
	}
}

func isLoopbackDNS(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
