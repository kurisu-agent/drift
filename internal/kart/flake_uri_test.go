package kart

import (
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/model"
)

// Plan 17, phase 4: tune.flake_uri transparently injects the community
// nixos.org Nix devcontainer feature and a postCreateCommand that does
// `nix profile install` with substituters carried as CLI flags. The
// substituters live on the install command line (not in nix.conf)
// because the daemon honours --extra-substituters from trusted users
// regardless of when the daemon was started — the workaround for the
// legacy feature's daemon-config-staleness bug.

func resolverWithCache() *Resolver {
	return &Resolver{
		LoadTune: func(string) (*Tune, error) { return nil, nil },
		NixCache: &NixCacheInfo{
			URL:    "http://172.17.0.1:5000",
			Pubkey: "circuit-1:abc",
		},
	}
}

func TestResolveFlakeURI_InjectsCommunityFeatureAndPCC(t *testing.T) {
	r := resolverWithCache()
	r.LoadTune = func(string) (*Tune, error) {
		return &model.Tune{FlakeURI: "github:owner/repo#pkg"}, nil
	}
	resolved, err := r.Resolve(Flags{Name: "k", Tune: "t", Clone: "https://example.org/repo.git"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.Contains(resolved.Features, nixosOrgFeatureID) {
		t.Errorf("features should include the community nixos.org feature, got %q", resolved.Features)
	}
	if resolved.PostCreateCommand == "" {
		t.Fatal("PostCreateCommand should be non-empty when flake_uri is set")
	}
	pcc := resolved.PostCreateCommand
	if !strings.Contains(pcc, "nix profile install") {
		t.Errorf("PCC should run nix profile install: %q", pcc)
	}
	if !strings.Contains(pcc, "pidof nix-daemon") {
		t.Errorf("PCC should ensure nix-daemon is running first: %q", pcc)
	}
	if !strings.Contains(pcc, "'github:owner/repo#pkg'") {
		t.Errorf("PCC should single-quote the flake URI: %q", pcc)
	}
	if !strings.Contains(resolved.Features, "extra-substituters = http://172.17.0.1:5000") {
		t.Errorf("nixos.org feature extra_options should carry circuit cache via --extra-conf: %q", resolved.Features)
	}
	if !strings.Contains(resolved.Features, "extra-trusted-substituters = http://172.17.0.1:5000") {
		t.Errorf("nixos.org feature extra_options should mark circuit cache as trusted-substituter: %q", resolved.Features)
	}
	if !strings.Contains(resolved.Features, "extra-trusted-public-keys = circuit-1:abc") {
		t.Errorf("nixos.org feature extra_options should carry circuit pubkey via --extra-conf: %q", resolved.Features)
	}
	if !strings.Contains(pcc, "--option substituters 'http://172.17.0.1:5000 https://cache.nixos.org'") {
		t.Errorf("PCC should override substituters at install time to bypass install.determinate.systems: %q", pcc)
	}
	if !strings.Contains(pcc, "--option trusted-public-keys 'circuit-1:abc cache.nixos.org-1:") {
		t.Errorf("PCC should pass trusted-public-keys override paired with substituters: %q", pcc)
	}
}

func TestBuildFlakeInstallPostCreate_NoCacheFallsBackToUpstream(t *testing.T) {
	pcc, err := buildFlakeInstallPostCreate("github:owner/repo#pkg", nil)
	if err != nil {
		t.Fatalf("buildFlakeInstallPostCreate: %v", err)
	}
	if !strings.Contains(pcc, "--option substituters 'https://cache.nixos.org'") {
		t.Errorf("PCC without cache should still set substituters to cache.nixos.org so install.determinate.systems is bypassed: %q", pcc)
	}
	if strings.Contains(pcc, "install.determinate.systems") {
		t.Errorf("PCC must never reference install.determinate.systems: %q", pcc)
	}
}

// Determinate Nix's profile script self-guards via __ETC_PROFILE_NIX_SOURCED.
// devpod's lifecycle-hook chain can leak the flag into the hook env while
// resetting PATH, which makes the source a no-op and leaves nix off PATH.
// The PCC must clear the flag before sourcing.
func TestBuildFlakeInstallPostCreate_UnsetsProfileSourcedFlag(t *testing.T) {
	pcc, err := buildFlakeInstallPostCreate("github:owner/repo#pkg", nil)
	if err != nil {
		t.Fatalf("buildFlakeInstallPostCreate: %v", err)
	}
	idxUnset := strings.Index(pcc, "unset __ETC_PROFILE_NIX_SOURCED")
	idxSource := strings.Index(pcc, ". /nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh")
	if idxUnset < 0 {
		t.Errorf("PCC must unset __ETC_PROFILE_NIX_SOURCED before sourcing the profile: %q", pcc)
	}
	if idxSource < 0 {
		t.Fatalf("PCC must source nix-daemon.sh: %q", pcc)
	}
	if idxUnset > idxSource {
		t.Errorf("unset must come before the source, got unset@%d source@%d: %q", idxUnset, idxSource, pcc)
	}
}

func TestResolveFlakeURI_NoCache_StillWorks(t *testing.T) {
	r := &Resolver{
		LoadTune: func(string) (*Tune, error) {
			return &model.Tune{FlakeURI: "github:owner/repo#pkg"}, nil
		},
	}
	resolved, err := r.Resolve(Flags{Name: "k", Tune: "t", Clone: "https://example.org/repo.git"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.PostCreateCommand == "" {
		t.Fatal("PCC should still be emitted when cache is disabled (just no extra_options cache lines)")
	}
	if strings.Contains(resolved.Features, "extra-substituters") {
		t.Errorf("nixos.org feature should not get extra-conf substituters when NixCache is nil: %q", resolved.Features)
	}
}

func TestResolveFlakeURI_PreservesExistingFeatureKey(t *testing.T) {
	// When the tune already names the community feature with custom
	// extra_options, the resolver leaves that entry alone.
	r := resolverWithCache()
	r.LoadTune = func(string) (*Tune, error) {
		return &model.Tune{
			FlakeURI: "github:owner/repo#pkg",
			Features: `{"ghcr.io/devcontainer-community/devcontainer-features/nixos.org:1":{"extra_options":"--init none --diagnostic-endpoint ''"}}`,
		}, nil
	}
	resolved, err := r.Resolve(Flags{Name: "k", Tune: "t", Clone: "https://example.org/repo.git"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.Contains(resolved.Features, "diagnostic-endpoint") {
		t.Errorf("user-set extra_options should be preserved when feature already declared: %q", resolved.Features)
	}
}

func TestResolveFlakeURI_NoFlakeURI_NoInjection(t *testing.T) {
	r := resolverWithCache()
	r.LoadTune = func(string) (*Tune, error) { return &model.Tune{}, nil }
	resolved, err := r.Resolve(Flags{Name: "k", Tune: "t", Clone: "https://example.org/repo.git"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.PostCreateCommand != "" {
		t.Errorf("PCC should be empty when flake_uri is unset: %q", resolved.PostCreateCommand)
	}
	if strings.Contains(resolved.Features, "nixos.org") {
		t.Errorf("nixos.org feature should not be auto-injected without flake_uri: %q", resolved.Features)
	}
}

func TestResolveFlakeURI_RejectsSingleQuoteInURI(t *testing.T) {
	r := resolverWithCache()
	r.LoadTune = func(string) (*Tune, error) {
		return &model.Tune{FlakeURI: "github:owner/repo'evil#pkg"}, nil
	}
	if _, err := r.Resolve(Flags{Name: "k", Tune: "t", Clone: "https://example.org/repo.git"}); err == nil {
		t.Error("expected single-quote in flake_uri to be rejected")
	}
}

func TestSplicePostCreateCommand_AppendsToExistingString(t *testing.T) {
	root := map[string]any{"postCreateCommand": "npm install"}
	splicePostCreateCommandInto(root, "echo our-command")
	got, _ := root["postCreateCommand"].(string)
	if got != "npm install && echo our-command" {
		t.Errorf("expected sequenced commands, got %q", got)
	}
}

func TestSplicePostCreateCommand_SetsWhenAbsent(t *testing.T) {
	root := map[string]any{}
	splicePostCreateCommandInto(root, "echo ours")
	got, _ := root["postCreateCommand"].(string)
	if got != "echo ours" {
		t.Errorf("expected our command verbatim, got %q", got)
	}
}

func TestSplicePostCreateCommand_PromotesArrayToObject(t *testing.T) {
	root := map[string]any{"postCreateCommand": []any{"npm", "install"}}
	splicePostCreateCommandInto(root, "echo ours")
	obj, ok := root["postCreateCommand"].(map[string]any)
	if !ok {
		t.Fatalf("expected object form, got %T: %v", root["postCreateCommand"], root["postCreateCommand"])
	}
	if obj["drift-flake-install"] != "echo ours" {
		t.Errorf("our key missing or wrong: %v", obj)
	}
	if _, ok := obj["project"]; !ok {
		t.Errorf("project array should be preserved under 'project' key: %v", obj)
	}
}
