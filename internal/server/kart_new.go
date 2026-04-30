package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/kart"
	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/nixcache"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// cacheNixosOrgPubkey is the well-known signing key for the upstream
// public binary cache. We surface it as an upstream pubkey when the
// circuit advertises cache.nixos.org so karts get a fully paste-free
// trusted-public-keys list. Tunes pointing at private upstreams set
// their own substituters (auto-injection backs off entirely).
const cacheNixosOrgPubkey = "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="
const cacheNixosOrgURL = "https://cache.nixos.org"

// KartNewDeps is split from KartDeps so kart.new can evolve independently
// from the lifecycle handlers.
type KartNewDeps struct {
	Deps *Deps
	// Kart: the handler overrides Resolver and GarageDir at call time.
	// Tests pre-populate Devpod/Starter/Fetcher/Clock here.
	Kart kart.NewDeps
	// Verbose, if non-nil, receives `[chest] …` events for each chest
	// dechest performed during a kart.new (entries used for env blocks,
	// dotfiles_repo refs, character PATs). Names only — never values.
	// Also propagated into the Resolver and kart.NewDeps so their own
	// `[resolver] …` and `[kart] <phase>` lines surface on the same sink.
	Verbose io.Writer
}

// KartNewParams field names mirror `drift new` flags so drift and lakitu
// schemas align without translation.
type KartNewParams struct {
	Name         string `json:"name"`
	Clone        string `json:"clone,omitempty"`
	Starter      string `json:"starter,omitempty"`
	Tune         string `json:"tune,omitempty"`
	Features     string `json:"features,omitempty"`
	Devcontainer string `json:"devcontainer,omitempty"`
	Dotfiles     string `json:"dotfiles,omitempty"`
	Character    string `json:"character,omitempty"`
	// PatSlug is a registered PAT slug from the registry, or the literal
	// "none" to suppress any character-inherited PAT for this kart. Empty
	// means "no kart-level directive" — character.pat_secret (if any) is
	// the source of truth. Raw token strings are rejected at the CLI;
	// this field carries slugs only.
	PatSlug      string              `json:"pat_slug,omitempty"`
	Autostart    bool                `json:"autostart,omitempty"`
	Mounts       []model.Mount       `json:"mounts,omitempty"`
	MigratedFrom *model.MigratedFrom `json:"migrated_from,omitempty"`
}

func RegisterKartNew(reg *rpc.Registry, kd KartNewDeps) {
	reg.Register(wire.MethodKartNew, kd.kartNewHandler)
}

func (kd KartNewDeps) kartNewHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p KartNewParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "kart.new: name is required")
	}
	if kd.Deps == nil {
		return nil, rpcerr.Internal("kart.new: deps not configured")
	}

	srv, err := kd.Deps.loadServerConfig()
	if err != nil {
		return nil, err
	}

	resolver := &kart.Resolver{
		Defaults: kart.ServerDefaults{
			DefaultTune:       srv.DefaultTune,
			DefaultCharacter:  srv.DefaultCharacter,
			CircuitName:       srv.ResolveName(),
			DenyLiteralsChest: srv.DenyLiterals,
		},
		LoadTune: func(name string) (*model.Tune, error) {
			return kd.Deps.loadTune(name)
		},
		LoadCharacter: func(name string) (*kart.Character, error) {
			c, err := kd.Deps.loadCharacter(name)
			if err != nil {
				return nil, err
			}
			pat, err := kd.resolvePATSecret(c.PATSecret)
			if err != nil {
				return nil, err
			}
			return &kart.Character{
				GitName:     c.GitName,
				GitEmail:    c.GitEmail,
				GithubUser:  c.GithubUser,
				SSHKeyPath:  c.SSHKeyPath,
				PAT:         pat,
				DisplayName: c.DisplayName,
				Icon:        c.Icon,
				Color:       c.Color,
			}, nil
		},
		ResolveEnv:      kd.resolveTuneEnv,
		ResolveChestRef: kd.resolveChestRef,
		LoadSeed:        kd.Deps.loadSeed,
		LoadKartPAT:     kd.resolveKartPATSlug,
		Verbose:         kd.Verbose,
		NixCache:        loadNixCacheInfo(),
	}

	// Preserve caller-pre-populated fields (devpod, starter, fetcher, clock)
	// while overriding the garage-dependent ones.
	kd.Kart.Resolver = resolver
	kd.Kart.Verbose = kd.Verbose
	if kd.Kart.GarageDir == "" {
		garage, derr := kd.Deps.garageDir()
		if derr != nil {
			return nil, rpcerr.Internal("kart.new: %v", derr).Wrap(derr)
		}
		kd.Kart.GarageDir = garage
	}

	f := kart.Flags{
		Name:         p.Name,
		Clone:        p.Clone,
		Starter:      p.Starter,
		Tune:         p.Tune,
		Features:     p.Features,
		Devcontainer: p.Devcontainer,
		Dotfiles:     p.Dotfiles,
		Character:    p.Character,
		PatSlug:      p.PatSlug,
		Autostart:    p.Autostart,
		Mounts:       p.Mounts,
	}
	if p.MigratedFrom != nil {
		f.MigratedFrom = *p.MigratedFrom
	}
	return kart.New(ctx, kd.Kart, f)
}

// resolvePATSecret turns a `chest:<name>` reference into the literal token
// the layer-1 dotfiles generator embeds. Empty input returns empty output.
// Non-chest-prefixed values are rejected — character.add enforces the shape.
func (kd KartNewDeps) resolvePATSecret(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	backend, err := kd.openChestBackend()
	if err != nil {
		return "", err
	}
	val, err := dechestRef(backend, "character", "pat_secret", ref)
	if err != nil {
		return "", err
	}
	if key, ok := chest.ParseRef(ref); ok {
		kd.verboseChest("dechested chest:%s (character pat, %d bytes)", key, len(val))
	}
	return val, nil
}

// resolveKartPATSlug looks up a registered PAT by slug and returns the
// dechested token. Missing slugs surface as `pat_not_found` (the same
// shape `drift pat update <missing>` produces). Used by the kart resolver
// to honor `drift new --pat=<slug>`.
func (kd KartNewDeps) resolveKartPATSlug(slug string) (string, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "", rpcerr.UserError(rpcerr.TypeInvalidFlag, "kart.new: pat_slug is empty")
	}
	if kd.Deps == nil {
		return "", rpcerr.Internal("kart.new: deps not configured")
	}
	rec, err := kd.Deps.loadPat(slug)
	if err != nil {
		return "", err
	}
	token, err := kd.resolveChestRef(rec.ChestRef)
	if err != nil {
		return "", err
	}
	if key, ok := chest.ParseRef(rec.ChestRef); ok {
		kd.verboseChest("dechested chest:%s (kart pat %q, %d bytes)", key, slug, len(token))
	}
	return token, nil
}

// resolveChestRef dechests a single `chest:<name>` value. Caller has already
// verified the prefix; passes through the chest backend's own
// chest_entry_not_found rpcerr so the resolver can wrap it with field
// context.
func (kd KartNewDeps) resolveChestRef(ref string) (string, error) {
	key := strings.TrimPrefix(strings.TrimSpace(ref), chest.RefPrefix)
	backend, err := kd.openChestBackend()
	if err != nil {
		return "", err
	}
	val, err := backend.Get(key)
	if err != nil {
		return "", err
	}
	kd.verboseChest("dechested chest:%s (%d bytes)", key, len(val))
	return string(val), nil
}

// verboseChest emits a `[chest] …` event when verbose mode is on. Only
// the chest entry name appears — never the value — so the line is safe
// to surface to the operator under --debug.
func (kd KartNewDeps) verboseChest(format string, args ...any) {
	if kd.Verbose == nil {
		return
	}
	fmt.Fprintf(kd.Verbose, "[chest] "+format+"\n", args...)
}

// openChestBackend exists separately from Deps.openChest to avoid colliding
// with the private method already defined in server.go.
func (kd KartNewDeps) openChestBackend() (chest.Backend, error) {
	if kd.Deps == nil {
		return nil, rpcerr.Internal("kart.new: deps not configured")
	}
	return kd.Deps.openChest()
}

// resolveTuneEnv turns every chest:<name> reference in the tune's env map
// into its literal value, grouped by injection site. Values never leave
// this handler's memory; persistence stores only the original chest
// references (see writeKartConfig).
func (kd KartNewDeps) resolveTuneEnv(refs kart.TuneEnv) (kart.ResolvedTuneEnv, error) {
	if refs.IsEmpty() {
		return kart.ResolvedTuneEnv{}, nil
	}
	// Open the backend once so a big env map doesn't pay per-key
	// file-load overhead.
	var backend chest.Backend
	var out kart.ResolvedTuneEnv
	blocks := []struct {
		name string
		src  map[string]string
		dst  *map[string]string
	}{
		{"build", refs.Build, &out.Build},
		{"workspace", refs.Workspace, &out.Workspace},
		{"session", refs.Session, &out.Session},
	}
	for _, b := range blocks {
		if len(b.src) == 0 {
			continue
		}
		resolved := make(map[string]string, len(b.src))
		field := "env." + b.name
		for k, ref := range b.src {
			if backend == nil {
				var err error
				backend, err = kd.openChestBackend()
				if err != nil {
					return kart.ResolvedTuneEnv{}, err
				}
			}
			val, err := dechestRef(backend, field, k, ref)
			if err != nil {
				return kart.ResolvedTuneEnv{}, err
			}
			if name, ok := chest.ParseRef(ref); ok {
				kd.verboseChest("dechested chest:%s (env.%s.%s, %d bytes)", name, b.name, k, len(val))
			}
			resolved[k] = val
		}
		*b.dst = resolved
	}
	return out, nil
}

// loadNixCacheInfo reads the marker file the lakitu NixOS module's
// services.lakitu.nixCache activation script writes (see plan 17). A
// missing or unreadable marker means "no circuit-local cache configured"
// — we return nil so kart.Resolver skips auto-injection without an
// error. Real read errors (permissions, malformed JSON) are also
// swallowed: logging them here would noise up every kart.new on
// circuits that just don't run the cache. Operators wanting visibility
// run `lakitu nix-cache info` directly.
func loadNixCacheInfo() *kart.NixCacheInfo {
	m, err := nixcache.Read(nixcache.DefaultMarkerPath)
	if err != nil {
		if errors.Is(err, nixcache.ErrNotConfigured) {
			return nil
		}
		// Don't fail kart.new on a malformed marker — fall through to
		// no-injection so karts can still be created.
		return nil
	}
	info := &kart.NixCacheInfo{
		URL:      m.URL,
		Pubkey:   m.Pubkey,
		Upstream: m.Upstream,
	}
	for _, up := range m.Upstream {
		if up == cacheNixosOrgURL {
			info.UpstreamPubkeys = append(info.UpstreamPubkeys, cacheNixosOrgPubkey)
		}
	}
	return info
}
