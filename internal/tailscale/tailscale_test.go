package tailscale

import (
	"strings"
	"testing"
)

func TestParseStatus_OnlinePeersOnlySorted(t *testing.T) {
	raw := `{
		"Self": {"HostName":"me","Online":true},
		"Peer": {
			"pk-b": {"HostName":"box-b","DNSName":"box-b.ts.net.","TailscaleIPs":["100.1.2.2","fd00::2"],"Online":true},
			"pk-off": {"HostName":"gone","DNSName":"gone.ts.net.","TailscaleIPs":["100.1.2.3"],"Online":false},
			"pk-a": {"HostName":"box-a","DNSName":"box-a.ts.net.","TailscaleIPs":["100.1.2.1"],"Online":true}
		}
	}`
	peers, err := parseStatus([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("got %d peers, want 2 (offline excluded): %+v", len(peers), peers)
	}
	if peers[0].DisplayName() != "box-a.ts.net" {
		t.Errorf("first peer = %q, want box-a.ts.net (sorted)", peers[0].DisplayName())
	}
	if peers[0].PrimaryIP() != "100.1.2.1" {
		t.Errorf("PrimaryIP = %q, want 100.1.2.1", peers[0].PrimaryIP())
	}
	if peers[1].PrimaryIP() != "100.1.2.2" {
		t.Errorf("peers[1].PrimaryIP = %q, want IPv4", peers[1].PrimaryIP())
	}
}

func TestParseStatus_TrailingDotStripped(t *testing.T) {
	peers, err := parseStatus([]byte(`{"Peer":{"pk":{"HostName":"x","DNSName":"x.ts.net.","Online":true}}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("peers = %+v", peers)
	}
	if strings.HasSuffix(peers[0].DisplayName(), ".") {
		t.Errorf("DisplayName kept trailing dot: %q", peers[0].DisplayName())
	}
}

func TestParseStatus_NoDNSName_FallsBackToHostName(t *testing.T) {
	peers, err := parseStatus([]byte(`{"Peer":{"pk":{"HostName":"short","DNSName":"","TailscaleIPs":["100.0.0.1"],"Online":true}}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if peers[0].DisplayName() != "short" {
		t.Errorf("DisplayName = %q, want short", peers[0].DisplayName())
	}
}

func TestParseStatus_FiltersForeignTailnetPeers(t *testing.T) {
	// Suffix is derived from Self.DNSName. Mullvad exits and shared-in
	// peers from other tailnets should be dropped.
	raw := `{
		"Self": {"HostName":"leaf","DNSName":"leaf.tail-abc123.ts.net.","Online":true},
		"Peer": {
			"pk-own":  {"HostName":"devbox","DNSName":"devbox.tail-abc123.ts.net.","TailscaleIPs":["100.1.2.3"],"Online":true},
			"pk-mull": {"HostName":"al-tia-wg-003","DNSName":"al-tia-wg-003.mullvad.ts.net.","TailscaleIPs":["100.111.189.27"],"Online":true},
			"pk-share":{"HostName":"friend","DNSName":"friend.other-net.ts.net.","TailscaleIPs":["100.9.9.9"],"Online":true}
		}
	}`
	peers, err := parseStatus([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(peers) != 1 || peers[0].HostName != "devbox" {
		t.Fatalf("got %+v, want only devbox", peers)
	}
}

func TestParseStatus_NoPeers(t *testing.T) {
	peers, err := parseStatus([]byte(`{}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("expected empty slice, got %+v", peers)
	}
}
