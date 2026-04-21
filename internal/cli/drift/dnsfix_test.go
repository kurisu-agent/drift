package drift

import (
	"net"
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

// TestInstallDNSFallback_OverridesDefaultResolver guards against a future
// refactor that accidentally drops the install or wires the wrong global.
// Doesn't exercise the network — that's covered by the address-detection
// test above plus the live behavior in CI.
func TestInstallDNSFallback_OverridesDefaultResolver(t *testing.T) {
	// Save and restore so this test doesn't bleed into others. NOT t.Parallel
	// because we mutate a package-level global.
	prev := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = prev })

	installDNSFallback()
	if net.DefaultResolver == prev {
		t.Errorf("DefaultResolver not replaced")
	}
	if net.DefaultResolver.Dial == nil {
		t.Errorf("DefaultResolver.Dial not set; pure-Go resolver path won't engage")
	}
	if !net.DefaultResolver.PreferGo {
		t.Errorf("PreferGo not set; cgo path may be selected and skip the fallback")
	}
}
