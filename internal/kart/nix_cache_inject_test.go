package kart

import (
	"encoding/json"
	"strings"
	"testing"
)

// Plan 17, phase 3: auto-inject substituters into Nix-feature
// extraNixConfig when (a) the cache is enabled, (b) the tune carries the
// Nix devcontainer feature, and (c) the tune hasn't already declared its
// own substituters line.

func sampleNixCache() NixCacheInfo {
	return NixCacheInfo{
		URL:             "http://172.17.0.1:5000",
		Pubkey:          "circuit-1:abc123",
		Upstream:        []string{"https://cache.nixos.org"},
		UpstreamPubkeys: []string{"cache.nixos.org-1:upstream-key"},
	}
}

// Helper: pull the nix feature's extraNixConfig out of a features JSON
// string for assertions.
func extraNixConfigOf(t *testing.T, featuresJSON, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(featuresJSON), &m); err != nil {
		t.Fatalf("unmarshal features: %v", err)
	}
	opts, _ := m[key].(map[string]any)
	if opts == nil {
		t.Fatalf("feature %q not present in result", key)
	}
	v, _ := opts["extraNixConfig"].(string)
	return v
}

// (a) cache enabled + nix feature + no user substituters → injection happens.
func TestInjectNixCache_NixFeatureNoSubstituters_Injects(t *testing.T) {
	in := `{"ghcr.io/devcontainers/features/nix:1":{"version":"latest","extraNixConfig":"experimental-features = nix-command flakes"}}`
	out, err := InjectNixCache(in, sampleNixCache())
	if err != nil {
		t.Fatalf("InjectNixCache: %v", err)
	}
	got := extraNixConfigOf(t, out, "ghcr.io/devcontainers/features/nix:1")
	if !strings.Contains(got, "experimental-features = nix-command flakes") {
		t.Errorf("user's existing extraNixConfig dropped: %q", got)
	}
	if !strings.Contains(got, "substituters = http://172.17.0.1:5000 https://cache.nixos.org") {
		t.Errorf("substituters line missing: %q", got)
	}
	if !strings.Contains(got, "trusted-public-keys = circuit-1:abc123 cache.nixos.org-1:upstream-key") {
		t.Errorf("trusted-public-keys line missing: %q", got)
	}
	// The Nix devcontainer feature's install.sh splits EXTRANIXCONFIG on
	// commas; a newline between the user's content and our additions
	// would mask everything after the first line.
	if strings.Contains(got, "\n") {
		t.Errorf("EXTRANIXCONFIG must not contain newlines (feature install.sh comma-splits): %q", got)
	}
	if !strings.Contains(got, ",substituters") {
		t.Errorf("expected comma separator before substituters: %q", got)
	}
}

// (b) cache enabled + nix feature + user substituters present → no
// injection. Their list is trusted verbatim; we don't try to merge.
func TestInjectNixCache_UserSubstitutersPresent_BacksOff(t *testing.T) {
	original := `experimental-features = nix-command flakes
substituters = https://my.private.cache https://cache.nixos.org`
	in := `{"ghcr.io/devcontainers/features/nix:1":{"extraNixConfig":` + jsonString(original) + `}}`
	out, err := InjectNixCache(in, sampleNixCache())
	if err != nil {
		t.Fatalf("InjectNixCache: %v", err)
	}
	got := extraNixConfigOf(t, out, "ghcr.io/devcontainers/features/nix:1")
	if got != original {
		t.Errorf("user-supplied extraNixConfig was mutated.\nwant: %q\ngot:  %q", original, got)
	}
	if strings.Contains(got, "172.17.0.1") {
		t.Errorf("circuit cache URL leaked into a tune that opted out: %q", got)
	}
}

// (c) cache disabled (Resolver.NixCache is nil → callers don't even
// invoke InjectNixCache). The function itself short-circuits on a
// zero-value NixCacheInfo (empty URL) too — guard rail against
// misconfigured callers.
func TestInjectNixCache_EmptyInfo_PassesThrough(t *testing.T) {
	in := `{"ghcr.io/devcontainers/features/nix:1":{"extraNixConfig":"experimental-features = nix-command flakes"}}`
	out, err := InjectNixCache(in, NixCacheInfo{})
	if err != nil {
		t.Fatalf("InjectNixCache: %v", err)
	}
	if out != in {
		t.Errorf("zero NixCacheInfo should pass features through unchanged.\nwant: %q\ngot:  %q", in, out)
	}
}

// (d) tune has no Nix feature → no injection (the cache is irrelevant
// to a tune that doesn't even use Nix).
func TestInjectNixCache_NoNixFeature_PassesThrough(t *testing.T) {
	in := `{"ghcr.io/devcontainers/features/git:1":{},"ghcr.io/devcontainers/features/node:1":{"version":"20"}}`
	out, err := InjectNixCache(in, sampleNixCache())
	if err != nil {
		t.Fatalf("InjectNixCache: %v", err)
	}
	if out != in {
		t.Errorf("non-nix features should be untouched.\nwant: %q\ngot:  %q", in, out)
	}
}

// (e) bonus: feature has no existing extraNixConfig at all. The injected
// block is the entire content.
func TestInjectNixCache_NoExistingExtraNixConfig_StillInjects(t *testing.T) {
	in := `{"ghcr.io/devcontainers/features/nix:1":{"version":"latest"}}`
	out, err := InjectNixCache(in, sampleNixCache())
	if err != nil {
		t.Fatalf("InjectNixCache: %v", err)
	}
	got := extraNixConfigOf(t, out, "ghcr.io/devcontainers/features/nix:1")
	if !strings.HasPrefix(got, "substituters =") {
		t.Errorf("expected substituters line at start of new extraNixConfig: %q", got)
	}
	if !strings.Contains(got, "trusted-public-keys = circuit-1:abc123") {
		t.Errorf("trusted-public-keys missing: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("EXTRANIXCONFIG must be single-line comma-separated: %q", got)
	}
}

// (g) trailing newlines on the user's existing extraNixConfig must be
// trimmed, since the install.sh's `read -a` only consumes the first
// physical line — anything after a newline would be silently dropped.
func TestInjectNixCache_TrimsTrailingNewline(t *testing.T) {
	in := `{"ghcr.io/devcontainers/features/nix:1":{"extraNixConfig":"experimental-features = nix-command flakes\n"}}`
	out, err := InjectNixCache(in, sampleNixCache())
	if err != nil {
		t.Fatalf("InjectNixCache: %v", err)
	}
	got := extraNixConfigOf(t, out, "ghcr.io/devcontainers/features/nix:1")
	if strings.Contains(got, "\n") {
		t.Errorf("trailing newline should have been stripped: %q", got)
	}
	if !strings.HasPrefix(got, "experimental-features = nix-command flakes,substituters") {
		t.Errorf("expected user-line followed by comma + injection: %q", got)
	}
}

// (f) prefix matching — future major versions like nix:2 should still
// be detected. Guards against the tempting `== "ghcr.io/.../nix:1"`
// mistake.
func TestInjectNixCache_NixFeatureMajorBump_StillInjects(t *testing.T) {
	in := `{"ghcr.io/devcontainers/features/nix:2":{}}`
	out, err := InjectNixCache(in, sampleNixCache())
	if err != nil {
		t.Fatalf("InjectNixCache: %v", err)
	}
	got := extraNixConfigOf(t, out, "ghcr.io/devcontainers/features/nix:2")
	if !strings.Contains(got, "substituters") {
		t.Errorf("expected injection for nix:2 prefix match, got %q", got)
	}
}

// jsonString quotes a string for embedding into a JSON literal in tests.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
