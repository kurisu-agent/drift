package lakitu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/nixcache"
)

// Hard-coded so the printed snippet is paste-ready when the default
// upstream (cache.nixos.org) is in use. Tunes pointing at private
// upstreams add their own pubkeys themselves.
const cacheNixosOrgPubkey = "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="

const cacheNixosOrgURL = "https://cache.nixos.org"

type nixCacheCmd struct {
	Info nixCacheInfoCmd `cmd:"" help:"Show this circuit's Nix binary cache details and a paste-ready extraNixConfig snippet."`
}

type nixCacheInfoCmd struct {
	Output string `enum:"text,json" default:"text" help:"Output format."`
}

func runNixCacheInfo(_ context.Context, io IO, cmd nixCacheInfoCmd) int {
	marker, err := nixcache.Read(nixcache.DefaultMarkerPath)
	if err != nil {
		if errors.Is(err, nixcache.ErrNotConfigured) {
			return errfmt.Emit(io.Stderr, fmt.Errorf("nix cache not enabled on this circuit (set services.lakitu.nixCache.enable = true and rebuild)"))
		}
		return errfmt.Emit(io.Stderr, err)
	}
	switch cmd.Output {
	case "json":
		buf, err := json.MarshalIndent(marker, "", "  ")
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		fmt.Fprintln(io.Stdout, string(buf))
	default:
		fmt.Fprint(io.Stdout, formatNixCacheInfo(marker))
	}
	return 0
}

func formatNixCacheInfo(m nixcache.Marker) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Substituter: %s\n", m.URL)
	fmt.Fprintf(&b, "Public key:  %s\n", m.Pubkey)
	if len(m.Upstream) > 0 {
		fmt.Fprintf(&b, "Upstreams:   %s\n", strings.Join(m.Upstream, " "))
	}
	b.WriteString("\nTo use in a tune, add to extraNixConfig:\n\n")
	fmt.Fprintf(&b, "  substituters = %s\n", strings.Join(append([]string{m.URL}, m.Upstream...), " "))
	fmt.Fprintf(&b, "  trusted-public-keys = %s\n", strings.Join(trustedPubkeysFor(m), " "))
	return b.String()
}

// trustedPubkeysFor returns the local cache pubkey followed by any
// well-known upstream pubkeys we can fill in automatically. Operators
// using private upstreams add their own pubkey to whatever this prints.
func trustedPubkeysFor(m nixcache.Marker) []string {
	keys := []string{m.Pubkey}
	for _, up := range m.Upstream {
		if up == cacheNixosOrgURL {
			keys = append(keys, cacheNixosOrgPubkey)
		}
	}
	return keys
}
