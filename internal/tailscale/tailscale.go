// Package tailscale is a thin wrapper over `tailscale status --json` for
// drift's peer-picker flow. No tsnet, no direct API — we only need a
// snapshot of online peers so the init wizard can offer a dropdown.
package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sort"
	"strings"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

// ErrNotInstalled surfaces when `tailscale` is missing from PATH. Callers
// check for this sentinel before reporting "couldn't list peers" so the
// UX can say "install tailscale" vs. "tailscale is broken."
var ErrNotInstalled = errors.New("tailscale: binary not found on PATH")

// Peer is a flattened view of one entry under `.Peer` in `tailscale
// status --json`. We only expose the fields drift's picker renders; the
// rest of the tailscale shape is ignored intentionally.
type Peer struct {
	HostName string   // short hostname (`devbox-home`)
	DNSName  string   // MagicDNS name without trailing dot (`devbox-home.tail-scale.ts.net`)
	IPs      []string // TailscaleIPs, IPv4 first
	Online   bool
}

// DisplayName returns the shortest unambiguous identifier: DNSName when
// present (trimmed of the trailing dot), falling back to HostName.
func (p Peer) DisplayName() string {
	if p.DNSName != "" {
		return p.DNSName
	}
	return p.HostName
}

// PrimaryIP returns the first TailscaleIP, preferring IPv4 (colon-free)
// so the picker shows a familiar 100.x.y.z address.
func (p Peer) PrimaryIP() string {
	for _, ip := range p.IPs {
		if !strings.Contains(ip, ":") {
			return ip
		}
	}
	if len(p.IPs) > 0 {
		return p.IPs[0]
	}
	return ""
}

// Available reports whether the `tailscale` binary is on PATH. Cheap
// LookPath lookup — the daemon itself may still be down.
func Available() bool {
	_, err := exec.LookPath("tailscale")
	return err == nil
}

// Peers shells `tailscale status --json`, returns online peers (excluding
// self) sorted by DisplayName for stable picker ordering. Returns
// ErrNotInstalled when tailscale is missing.
func Peers(ctx context.Context) ([]Peer, error) {
	if !Available() {
		return nil, ErrNotInstalled
	}
	res, err := driftexec.Run(ctx, driftexec.Cmd{
		Name: "tailscale",
		Args: []string{"status", "--json"},
	})
	if err != nil {
		return nil, err
	}
	return parseStatus(res.Stdout)
}

// rawStatus pulls exactly the fields we use out of `tailscale status
// --json`. Unknown fields are silently dropped so a tailscale version
// bump that adds fields is additive.
type rawStatus struct {
	Self *rawPeer           `json:"Self"`
	Peer map[string]rawPeer `json:"Peer"`
}

type rawPeer struct {
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
}

func parseStatus(buf []byte) ([]Peer, error) {
	var raw rawStatus
	if err := json.Unmarshal(buf, &raw); err != nil {
		return nil, err
	}
	var suffix string
	if raw.Self != nil {
		self := strings.TrimSuffix(raw.Self.DNSName, ".")
		if i := strings.IndexByte(self, '.'); i >= 0 {
			suffix = self[i+1:]
		}
	}
	peers := make([]Peer, 0, len(raw.Peer))
	for _, p := range raw.Peer {
		if !p.Online {
			continue
		}
		dns := strings.TrimSuffix(p.DNSName, ".")
		// Drop peers outside our tailnet (Mullvad exits, shared-in nodes).
		if suffix != "" && !strings.HasSuffix(dns, "."+suffix) {
			continue
		}
		peers = append(peers, Peer{
			HostName: p.HostName,
			DNSName:  dns,
			IPs:      append([]string(nil), p.TailscaleIPs...),
			Online:   p.Online,
		})
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].DisplayName() < peers[j].DisplayName()
	})
	return peers, nil
}
