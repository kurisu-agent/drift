package drift

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestIsLoopbackDNS covers the address-shape detection that gates the
// substitution. v4 and v6 loopback both qualify; real DNS server
// addresses must not.
func TestIsLoopbackDNS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr string
		want bool
	}{
		{"[::1]:53", true}, // typical Termux/Android Go-resolver fallback
		{"127.0.0.1:53", true},
		{"127.0.0.53:53", true}, // systemd-resolved stub on some Linux distros
		{"1.1.1.1:53", false},
		{"8.8.8.8:53", false},
		{"[2606:4700:4700::1111]:53", false},
		{"not-an-address", false},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isLoopbackDNS(tc.addr); got != tc.want {
				t.Errorf("isLoopbackDNS(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

// TestNeedsEagerFallback verifies the resolv.conf probe that decides
// between lazy (retry-on-dial-failure) and eager (substitute-before-dial)
// behavior. Eager is what saves Termux — a lazy-only fix lets UDP connect(2)
// "succeed" against nothing and the read failure bypasses our retry.
func TestNeedsEagerFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{
			// Termux: /etc/resolv.conf absent. Go picks [::1]:53, nobody home.
			name: "missing_file",
			path: filepath.Join(dir, "does-not-exist"),
			want: true,
		},
		{
			// Pathological: file present but zero nameservers. Same end result
			// as missing — Go falls back to IANA defaults.
			name: "empty_file",
			path: write("empty", ""),
			want: true,
		},
		{
			name: "comments_only",
			path: write("comments", "# managed by something\n; another comment\n"),
			want: true,
		},
		{
			// Options/search but no nameserver: also unusable.
			name: "no_nameserver_line",
			path: write("search_only", "search example.com\noptions timeout:1\n"),
			want: true,
		},
		{
			// systemd-resolved shape: loopback stub. Lazy mode is correct
			// here because the stub knows VPN/split-horizon routing our
			// public fallbacks don't.
			name: "systemd_resolved",
			path: write("systemd", "# stub\nnameserver 127.0.0.53\noptions edns0\n"),
			want: false,
		},
		{
			name: "public_nameserver",
			path: write("public", "nameserver 1.1.1.1\nnameserver 8.8.8.8\n"),
			want: false,
		},
		{
			// Leading whitespace is valid per resolv.conf(5).
			name: "indented_nameserver",
			path: write("indented", "  nameserver 1.1.1.1\n"),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsEagerFallback(tc.path); got != tc.want {
				t.Errorf("needsEagerFallback(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestInstallDNSFallback_OverridesDefaultResolver guards against a future
// refactor that accidentally drops the install or wires the wrong global.
// Doesn't exercise the network — that's covered by the address-detection
// test above plus the live behavior in CI.
func TestInstallDNSFallback_OverridesDefaultResolver(t *testing.T) {
	// Save and restore so this test doesn't bleed into others. NOT t.Parallel
	// because we mutate package-level globals.
	prevResolver := net.DefaultResolver
	prevPath := resolvConfPath
	t.Cleanup(func() {
		net.DefaultResolver = prevResolver
		resolvConfPath = prevPath
	})

	installDNSFallback()
	if net.DefaultResolver == prevResolver {
		t.Errorf("DefaultResolver not replaced")
	}
	if net.DefaultResolver.Dial == nil {
		t.Errorf("DefaultResolver.Dial not set; pure-Go resolver path won't engage")
	}
	if !net.DefaultResolver.PreferGo {
		t.Errorf("PreferGo not set; cgo path may be selected and skip the fallback")
	}
}

// TestInstallDNSFallback_EagerSubstitutesLoopback is the behavioral core:
// when resolv.conf is unusable, a Dial to a loopback DNS address must be
// redirected to fallbackDNS *before* touching the loopback socket. The
// previous lazy-only implementation would dial [::1]:53 first, see UDP
// connect(2) pretend-succeed, and hand back a doomed conn.
//
// Points Dial at a stub listener bound to a random loopback port so the
// "fallback" target is reachable and test-hermetic (no 1.1.1.1 traffic).
func TestInstallDNSFallback_EagerSubstitutesLoopback(t *testing.T) {
	prevResolver := net.DefaultResolver
	prevPath := resolvConfPath
	prevFallback := fallbackDNS
	t.Cleanup(func() {
		net.DefaultResolver = prevResolver
		resolvConfPath = prevPath
		fallbackDNS = prevFallback
	})

	// Stub UDP listener playing the role of our public DNS fallback.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen stub dns: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	stubAddr := pc.LocalAddr().String()

	// Point the installer at a bogus resolv.conf so it picks eager mode.
	dir := t.TempDir()
	bogus := filepath.Join(dir, "missing")
	resolvConfPath = bogus
	fallbackDNS = []string{stubAddr}

	installDNSFallback()

	// Simulate Go's resolver asking for [::1]:53 under eager mode. The Dial
	// must redirect to the stub and the resulting conn's remote address
	// must be the stub, proving no loopback-DNS packet was emitted.
	var dialedAddr atomic.Value
	origDial := net.DefaultResolver.Dial
	net.DefaultResolver.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialedAddr.Store(address)
		return origDial(ctx, network, address)
	}

	conn, err := net.DefaultResolver.Dial(context.Background(), "udp", "[::1]:53")
	if err != nil {
		t.Fatalf("eager dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if got := conn.RemoteAddr().String(); got != stubAddr {
		t.Errorf("RemoteAddr = %q, want stub %q (eager substitution did not fire)", got, stubAddr)
	}
}
