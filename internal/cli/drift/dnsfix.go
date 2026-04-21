package drift

import (
	"context"
	"net"
	"os"
	"strings"
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

// resolvConfPath is the file probed to decide whether Go's loopback
// resolver target will be reachable. Package var so tests can point it
// at a fixture instead of touching the real system file.
var resolvConfPath = "/etc/resolv.conf"

// installDNSFallback overrides net.DefaultResolver so outbound HTTP
// calls in the drift binary resolve even on platforms where Go's
// pure-Go resolver can't find a working nameserver. Two modes:
//
//   - Eager (needsEagerFallback == true): /etc/resolv.conf is missing
//     or lists no nameserver. Go's resolver then dials its IANA-default
//     loopback target (127.0.0.1:53 / [::1]:53). On Termux nothing is
//     listening there, so the fallback IPs are substituted for the
//     loopback address before dialing. This is the path the previous
//     lazy-only fix missed: net.Dialer.DialContext on UDP always
//     "succeeds" at connect(2) time — the ECONNREFUSED only surfaces
//     when the resolver reads the response, and that read never flows
//     through this Dial callback. Preempting is the only way.
//
//   - Lazy (needsEagerFallback == false): resolv.conf is present with
//     real nameservers. On systemd-resolved systems that's 127.0.0.53,
//     which legitimately hosts the local stub — the stub knows about
//     VPN-overridden DNS, split-horizon, and per-link nameservers that
//     our public fallbacks don't. So the configured server is dialed
//     first and the fallback only fires on dial failure (TCP DNS
//     retries do fail visibly at dial time, which is why this path
//     still adds value on conventional Linux).
func installDNSFallback() {
	eager := needsEagerFallback(resolvConfPath)
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			if eager && isLoopbackDNS(address) {
				for _, fb := range fallbackDNS {
					if c, err := d.DialContext(ctx, network, fb); err == nil {
						return c, nil
					}
				}
				// All fallbacks unreachable — fall through to the original
				// address so the caller sees the real underlying error
				// rather than one synthesized here.
			}
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

// needsEagerFallback reports whether the given resolv.conf is unusable —
// missing, unreadable, or containing no "nameserver <addr>" line. In
// those states Go's resolver silently picks the IANA-default loopback
// and (on Termux/Android) nothing is listening there.
func needsEagerFallback(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			return false
		}
	}
	return true
}

func isLoopbackDNS(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
