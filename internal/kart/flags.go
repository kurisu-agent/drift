// Package kart implements `kart.new` and its supporting pieces: flag
// composition, devcontainer source normalization, starter history strip,
// layer-1 dotfiles generation, and the orchestrator that ties them together
// with devpod.
package kart

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/seed"
)

// Tune is an alias for model.Tune — shared with internal/server to avoid
// a cycle (server imports kart).
type Tune = model.Tune

// TuneEnv is an alias for model.TuneEnv — carries chest:<name> references
// grouped by injection site (build/workspace/session).
type TuneEnv = model.TuneEnv

// ResolvedTuneEnv mirrors TuneEnv but holds literal values resolved from
// the chest. One map per injection site; stages stay independent
// downstream.
type ResolvedTuneEnv struct {
	Build     map[string]string
	Workspace map[string]string
	Session   map[string]string
}

// IsEmpty reports whether no env vars were resolved across any block.
func (e ResolvedTuneEnv) IsEmpty() bool {
	return len(e.Build) == 0 && len(e.Workspace) == 0 && len(e.Session) == 0
}

type Character struct {
	GitName    string
	GitEmail   string
	GithubUser string
	SSHKeyPath string
	// PAT is already de-chested by the server handler before Resolve().
	PAT string
	// DisplayName / Icon / Color carry presentation fields for the
	// in-kart UI (zellij topbar, claude-statusline). Icon is the
	// raw model value (catalog name, single grapheme, or emoji) — the
	// resolver hands it off to the seed which renders it via
	// icons.Resolve at info.json render time.
	DisplayName string
	Icon        string
	Color       string
}

// ServerDefaults is the narrow slice of internal/config.Server the resolver
// uses, so kart stays independent of the wider config schema.
type ServerDefaults struct {
	DefaultTune      string
	DefaultCharacter string
	// CircuitName is `Server.ResolveName()` snapshotted at handler entry —
	// the explicit `name:` from config.yaml, or hostname-derived. Threaded
	// into the resolver so kart-creation flows that populate seed Vars
	// don't have to re-derive (and so tests can stub it).
	CircuitName string
	// DenyLiteralsChest is the `chest:<name>` ref from server config's
	// `deny_literals` field. Empty when nothing is configured. The
	// resolver dechests it via ResolveChestRef and threads the literal
	// content into Resolved.DenyLiterals — the claudeCode seed
	// drops it at ~/.claude/deny-literals.txt and the always-
	// installed PreToolUse hook reads it from there. See
	// plans/20-kart-deny-literals.md.
	DenyLiteralsChest string
}

type Flags struct {
	Name         string
	Clone        string
	Starter      string
	Tune         string
	Features     string
	Devcontainer string
	Dotfiles     string
	Character    string
	Autostart    bool
	// PatSlug is the registered PAT this kart should use. The empty
	// string means "no kart-level override; fall through to the
	// character's pat_secret if any". The literal "none" means "do not
	// inject any PAT for this kart, even if the character has one" — it
	// is recorded as the absence of pat_slug on the kart YAML, not as
	// the literal string. Any other value names a slug that the
	// resolver dereferences via Resolver.LoadKartPAT.
	PatSlug string
	// Mounts carries --mount specs from `drift new`. Appended on top of
	// tune.MountDirs (flag-wins-on-target during the resolver merge).
	Mounts []model.Mount
	// MigratedFrom, when non-zero, is persisted on the kart config so
	// `drift migrate` can filter out already-adopted devpod workspaces on
	// subsequent runs. Never set by `drift new` — only by the migrate
	// path.
	MigratedFrom model.MigratedFrom
}

type Resolved struct {
	Name          string
	SourceMode    model.SourceMode // clone | starter | none
	SourceURL     string
	TuneName      string // empty when resolved to "none"
	Tune          *Tune
	CharacterName string
	Character     *Character
	// PATSlug is the registered PAT slug attached to this kart, persisted
	// into the kart YAML. Empty when no kart-level PAT was selected
	// (whether because the user passed --pat=none, no slug was matched,
	// or no flag was given at all).
	PATSlug      string
	Features     string // already merged
	Devcontainer string // raw; normalized later
	Dotfiles     string
	Autostart    bool
	// Env carries chest-resolved literal env vars per injection site. Held
	// only in memory; never persisted. EnvRefs holds the parallel
	// chest:<name> references for persistence and `kart info` rendering.
	Env     ResolvedTuneEnv
	EnvRefs TuneEnv
	// Mounts is tune.MountDirs + flag mounts, deduped by target with
	// flag-wins precedence. Both source and target retain `~/`-forms
	// from the tune spec so KartConfig.mount_dirs round-trips
	// verbatim (drift detection compares apples to apples); expansion
	// to absolute paths happens at overlay-splice time.
	Mounts []model.Mount
	// MigratedFrom threads through from Flags unchanged; the resolver has
	// nothing to decide about it.
	MigratedFrom model.MigratedFrom
	// Seeds are the resolved (but not yet rendered) templates listed in
	// the tune's `seed:` field. The post-`devpod up` finaliser renders
	// each against the kart's vars and emits one shell drop per file.
	Seeds []*seed.Template

	// PostCreateCommand, when non-empty, is spliced into the kart's
	// devcontainer.json overlay so devpod runs it once after the
	// container is up. Built by the resolver from `tune.flake_uri`
	// (a `nix profile install <uri>` call with --extra-substituters /
	// --extra-trusted-public-keys derived from NixCache when set).
	PostCreateCommand string

	// Icon / Color carry the kart's display identity for the in-kart
	// zellij topbar (and any other UI consumer of seed Vars). Defaulted
	// from the tune; per-kart override would come from a future
	// `kart set icon|color` flow.
	Icon  string
	Color string

	// CharacterDisplayName / CharacterIcon / CharacterColor carry the
	// presentation fields off the resolved character. Empty when no
	// character is selected. Threaded through to seed Vars so the
	// in-kart UI (zellij topbar, claude-statusline) can show the
	// friendly name + glyph instead of the bare YAML key.
	CharacterDisplayName string
	CharacterIcon        string
	CharacterColor       string

	// CircuitName is the snapshotted display name of this lakitu host
	// at resolve time. Mirrors ServerDefaults.CircuitName. Used as the
	// `{{ .Circuit }}` template var in seeds.
	CircuitName string

	// DenyLiterals is the dechested newline-separated deny-list rendered
	// into the kart at ~/.claude/deny-literals.txt by the claudeCode
	// seed. Empty when ServerDefaults.DenyLiteralsChest
	// is unset — the PreToolUse hook script still installs but
	// gracefully no-ops with no list present. See plan 20.
	DenyLiterals string
}

type Resolver struct {
	Defaults ServerDefaults
	// LoadTune / LoadCharacter: missing entries should return a NotFound
	// rpcerr. LoadCharacter must return a Character with PAT already
	// resolved — downstream code never touches the chest.
	LoadTune      func(name string) (*Tune, error)
	LoadCharacter func(name string) (*Character, error)
	// ResolveEnv turns a TuneEnv full of chest:<name> refs into literal
	// values per injection site. nil means "no env resolution" — callers
	// that don't wire a chest backend get an empty ResolvedTuneEnv and
	// skip injection. Errors must surface as rpcerr (e.g.
	// chest_entry_not_found with block + key in Data).
	ResolveEnv func(TuneEnv) (ResolvedTuneEnv, error)
	// ResolveChestRef dechests a single `chest:<name>` reference. Used to
	// inline secrets that ride on opaque values like dotfiles_repo (where
	// the chest entry stores e.g. an HTTPS URL with a PAT pre-embedded).
	// Caller has already verified the `chest:` prefix. nil means chest
	// refs in non-env fields will fail with internal_error — wire it
	// whenever ResolveEnv is wired.
	ResolveChestRef func(ref string) (string, error)
	// LoadSeed resolves a seed template by name — built-in registry first,
	// then on-disk garage/seeds. Missing entries should return a
	// `seed_not_found` rpcerr. nil means the tune's `seed:` list is
	// rejected with internal_error if non-empty (caller wiring bug).
	LoadSeed func(name string) (*seed.Template, error)
	// LoadKartPAT resolves a registered PAT slug to a literal token,
	// dechesting the underlying chest entry. Missing slugs should return
	// `pat_not_found` rpcerr. nil means f.PatSlug values other than "" /
	// "none" are rejected with internal_error (caller wiring bug). The
	// resolver only invokes this when f.PatSlug names a real slug.
	LoadKartPAT func(slug string) (token string, err error)
	// Verbose, if non-nil, receives a `[resolver] …` summary of the
	// effective resolved inputs (tune, character, source, devcontainer,
	// dotfiles, env block names) after each Resolve call. Wire to
	// os.Stderr in verbose mode.
	Verbose io.Writer
	// NixCache, if non-nil, drives auto-injection of substituters and
	// trusted-public-keys into any Nix devcontainer feature whose
	// extraNixConfig doesn't already set its own substituters line. See
	// plan 17 (phase 3) and InjectNixCache below.
	NixCache *NixCacheInfo
}

// NixCacheInfo describes a circuit-local Nix substituter for the resolver
// to advertise into Nix-feature extraNixConfig blocks. Populated by the
// server from /run/lakitu/nix-cache.json (via internal/nixcache); the
// kart package itself does no I/O.
type NixCacheInfo struct {
	URL      string
	Pubkey   string
	Upstream []string
	// UpstreamPubkeys lists pubkeys for upstream substituters that the
	// caller wants surfaced alongside the local cache key. Optional —
	// for the common cache.nixos.org case the caller can include its
	// well-known pubkey here so kart users don't have to.
	UpstreamPubkeys []string
}

// Resolve applies: server defaults → tune → explicit flags, with --features
// additive on top of the tune's features.
func (r *Resolver) Resolve(f Flags) (*Resolved, error) {
	if f.Clone != "" && f.Starter != "" {
		return nil, rpcerr.UserError(rpcerr.TypeMutuallyExclusive,
			"kart.new: --clone and --starter are mutually exclusive").
			With("clone", f.Clone).With("starter", f.Starter)
	}

	tuneName := f.Tune
	tuneFromDefault := false
	if tuneName == "" {
		tuneName = r.Defaults.DefaultTune
		tuneFromDefault = true
	}
	var tune *Tune
	effectiveTune := tuneName
	if tuneName != "" && tuneName != "none" {
		t, err := r.LoadTune(tuneName)
		if err != nil {
			// default_tune is a hint — if the server config points at a tune
			// that doesn't exist (e.g. ships `default_tune: default` but no
			// `tunes/default.yaml` has been created), fall through to "no
			// tune" rather than erroring. Explicit --tune still errors.
			var rpcErr *rpcerr.Error
			if tuneFromDefault && errors.As(err, &rpcErr) && rpcErr.Type == "tune_not_found" {
				effectiveTune = ""
			} else {
				return nil, err
			}
		} else {
			tune = t
		}
	}
	if tuneName == "none" {
		effectiveTune = ""
	}

	characterName := f.Character
	if characterName == "" {
		characterName = r.Defaults.DefaultCharacter
	}
	var character *Character
	if characterName != "" {
		c, err := r.LoadCharacter(characterName)
		if err != nil {
			return nil, err
		}
		character = c
	}

	// Kart-level PAT override. Three branches:
	//   "":     no override; character's PAT (if any) stands.
	//   "none": explicit opt-out; clear any inherited PAT and don't
	//           record a slug on the kart record.
	//   <slug>: dechest via LoadKartPAT and override character.PAT.
	//           Synthesize a PAT-only character if no character was
	//           selected so dotfiles still emit gh_hosts / git_credentials.
	persistedPATSlug := ""
	switch f.PatSlug {
	case "":
		// no kart-level PAT directive
	case "none":
		if character != nil {
			character.PAT = ""
		}
	default:
		if r.LoadKartPAT == nil {
			return nil, rpcerr.Internal(
				"kart.new: pat_slug %q supplied but no PAT loader is configured", f.PatSlug)
		}
		token, err := r.LoadKartPAT(f.PatSlug)
		if err != nil {
			return nil, err
		}
		if character == nil {
			character = &Character{PAT: token}
		} else {
			character.PAT = token
		}
		persistedPATSlug = f.PatSlug
	}

	var (
		sourceMode model.SourceMode
		sourceURL  string
	)
	switch {
	case f.Clone != "":
		sourceMode = model.SourceModeClone
		sourceURL = f.Clone
	case f.Starter != "":
		sourceMode = model.SourceModeStarter
		sourceURL = f.Starter
	case tune != nil && tune.Starter != "":
		sourceMode = model.SourceModeStarter
		sourceURL = tune.Starter
	default:
		sourceMode = model.SourceModeNone
	}

	devcontainer := f.Devcontainer
	if devcontainer == "" && tune != nil {
		devcontainer = tune.Devcontainer
	}

	dotfiles := f.Dotfiles
	if dotfiles == "" && tune != nil {
		dotfiles = tune.DotfilesRepo
	}
	// dotfiles_repo accepts a `chest:<name>` ref so the auth token can stay
	// in the chest while the URL (with PAT embedded) flows through opaquely.
	if chestName, ok := chest.ParseRef(dotfiles); ok {
		if r.ResolveChestRef == nil {
			return nil, rpcerr.Internal(
				"kart.new: dotfiles_repo references chest but no chest resolver is configured")
		}
		val, err := r.ResolveChestRef(dotfiles)
		if err != nil {
			var rpcErr *rpcerr.Error
			if errors.As(err, &rpcErr) && rpcErr.Type == rpcerr.TypeChestEntryNotFound {
				return nil, rpcerr.New(rpcerr.CodeNotFound, rpcerr.TypeChestEntryNotFound,
					"kart.new: dotfiles_repo references missing chest entry %q", chestName).
					With("field", "dotfiles_repo").With("name", chestName)
			}
			return nil, err
		}
		dotfiles = val
	}

	features, err := mergeFeatures(tuneFeatures(tune), f.Features)
	if err != nil {
		return nil, err
	}
	flakeURI := ""
	if tune != nil {
		flakeURI = tune.FlakeURI
	}
	if flakeURI != "" {
		features, err = injectNixosOrgFeature(features, r.NixCache)
		if err != nil {
			return nil, err
		}
	}
	if character != nil && character.PAT != "" {
		features, err = injectGithubCLIFeature(features)
		if err != nil {
			return nil, err
		}
	}
	if r.NixCache != nil {
		features, err = InjectNixCache(features, *r.NixCache)
		if err != nil {
			return nil, err
		}
	}
	postCreateCommand := ""
	if flakeURI != "" {
		postCreateCommand, err = buildFlakeInstallPostCreate(flakeURI, r.NixCache)
		if err != nil {
			return nil, err
		}
	}

	var (
		envRefs     TuneEnv
		resolvedEnv ResolvedTuneEnv
	)
	if tune != nil {
		envRefs = tune.Env
		if !envRefs.IsEmpty() && r.ResolveEnv != nil {
			resolvedEnv, err = r.ResolveEnv(envRefs)
			if err != nil {
				return nil, err
			}
		}
	}

	mounts, err := mergeMounts(tuneMounts(tune), f.Mounts)
	if err != nil {
		return nil, err
	}

	// Dechest the circuit-level deny-literals reference once per
	// resolve. Mirrors the dotfiles_repo path above: chest.ParseRef
	// already validated the prefix at config-load time, but we re-check
	// here so a hand-edited config.yaml still surfaces a clean error
	// instead of an internal_error inside the chest call. Empty
	// DenyLiteralsChest leaves resolvedDenyLiterals = "" and the seed
	// template's {{ if .HasDenyLiterals }} guard skips the file drop.
	resolvedDenyLiterals := ""
	if r.Defaults.DenyLiteralsChest != "" {
		if r.ResolveChestRef == nil {
			return nil, rpcerr.Internal(
				"kart.new: deny_literals references chest but no chest resolver is configured")
		}
		chestName, ok := chest.ParseRef(r.Defaults.DenyLiteralsChest)
		if !ok {
			return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
				"kart.new: deny_literals must be a chest reference of the form %q", chest.RefPrefix+"<name>").
				With("field", "deny_literals")
		}
		val, err := r.ResolveChestRef(r.Defaults.DenyLiteralsChest)
		if err != nil {
			var rpcErr *rpcerr.Error
			if errors.As(err, &rpcErr) && rpcErr.Type == rpcerr.TypeChestEntryNotFound {
				return nil, rpcerr.New(rpcerr.CodeNotFound, rpcerr.TypeChestEntryNotFound,
					"kart.new: deny_literals references missing chest entry %q", chestName).
					With("field", "deny_literals").With("name", chestName)
			}
			return nil, err
		}
		resolvedDenyLiterals = val
	}

	var seeds []*seed.Template
	if tune != nil && len(tune.Seed) > 0 {
		if r.LoadSeed == nil {
			return nil, rpcerr.Internal(
				"kart.new: tune %q lists seed templates but no seed loader is configured", tuneName)
		}
		seeds = make([]*seed.Template, 0, len(tune.Seed))
		for _, n := range tune.Seed {
			t, err := r.LoadSeed(n)
			if err != nil {
				return nil, err
			}
			seeds = append(seeds, t)
		}
	}

	resolved := &Resolved{
		Name:                 f.Name,
		SourceMode:           sourceMode,
		SourceURL:            sourceURL,
		TuneName:             effectiveTune,
		Tune:                 tune,
		CharacterName:        characterName,
		Character:            character,
		PATSlug:              persistedPATSlug,
		Features:             features,
		Devcontainer:         devcontainer,
		Dotfiles:             dotfiles,
		Autostart:            f.Autostart,
		Env:                  resolvedEnv,
		EnvRefs:              envRefs,
		Mounts:               mounts,
		MigratedFrom:         f.MigratedFrom,
		Seeds:                seeds,
		PostCreateCommand:    postCreateCommand,
		Icon:                 tuneIcon(tune),
		Color:                tuneColor(tune),
		CharacterDisplayName: characterDisplayName(character, characterName),
		CharacterIcon:        characterIcon(character),
		CharacterColor:       characterColor(character),
		CircuitName:          r.Defaults.CircuitName,
		DenyLiterals:         resolvedDenyLiterals,
	}
	r.logResolved(resolved)
	return resolved, nil
}

// logResolved emits a one-line `[resolver] …` summary of the effective
// inputs after merge — what the user actually got, not just what they
// passed. Skipped when Verbose is nil. Values like Devcontainer and
// Dotfiles flow through driftexec.RedactSecrets indirectly later, but
// the resolver sees raw chest-dechested URLs here, so any embedded PAT
// would land in this line — caller must pass a redacting writer if the
// destination is operator-visible (lakitu wraps appropriately).
func (r *Resolver) logResolved(resolved *Resolved) {
	if r == nil || r.Verbose == nil || resolved == nil {
		return
	}
	parts := []string{
		fmt.Sprintf("name=%s", resolved.Name),
		fmt.Sprintf("source=%s", resolved.SourceMode),
	}
	if resolved.SourceURL != "" {
		parts = append(parts, fmt.Sprintf("url=%s", resolved.SourceURL))
	}
	if resolved.TuneName != "" {
		parts = append(parts, fmt.Sprintf("tune=%s", resolved.TuneName))
	}
	if resolved.CharacterName != "" {
		parts = append(parts, fmt.Sprintf("character=%s", resolved.CharacterName))
	}
	if resolved.Devcontainer != "" {
		parts = append(parts, fmt.Sprintf("devcontainer=%s", resolved.Devcontainer))
	}
	if resolved.Dotfiles != "" {
		parts = append(parts, fmt.Sprintf("dotfiles=%s", resolved.Dotfiles))
	}
	if resolved.Autostart {
		parts = append(parts, "autostart=true")
	}
	// Env: just the block names + key counts so secrets don't surface.
	if n := len(resolved.EnvRefs.Build); n > 0 {
		parts = append(parts, fmt.Sprintf("env.build=%d", n))
	}
	if n := len(resolved.EnvRefs.Workspace); n > 0 {
		parts = append(parts, fmt.Sprintf("env.workspace=%d", n))
	}
	if n := len(resolved.EnvRefs.Session); n > 0 {
		parts = append(parts, fmt.Sprintf("env.session=%d", n))
	}
	fmt.Fprintf(r.Verbose, "[resolver] %s\n", strings.Join(parts, " "))
}

func tuneFeatures(t *Tune) string {
	if t == nil {
		return ""
	}
	return t.Features
}

func tuneMounts(t *Tune) []model.Mount {
	if t == nil {
		return nil
	}
	return t.MountDirs
}

// mergeMounts concatenates tune + flag mounts, leaving `~/` forms on
// both source and target untouched. Source-side `~/` resolves to the
// lakitu process's literal $HOME at splice time (devpod v0.22 doesn't
// substitute `${localEnv:…}` in overlay mounts); target-side `~/`
// rewrites to /mnt/lakitu-host/... at splice time and then a
// post-`devpod up` helper symlinks the container's $HOME path back to
// the mounted location. Keeping both tildes on resolved.Mounts means
// KartConfig.mount_dirs round-trips the original tune spec verbatim,
// so plan-11's drift detection compares like with like.
//
// Flag mounts win on a matching target: a second entry with the same
// target overrides the first. Targets are required (a mount without a
// target is nonsensical on docker's side).
func mergeMounts(fromTune, fromFlag []model.Mount) ([]model.Mount, error) {
	if len(fromTune) == 0 && len(fromFlag) == 0 {
		return nil, nil
	}
	combined := make([]model.Mount, 0, len(fromTune)+len(fromFlag))
	combined = append(combined, fromTune...)
	combined = append(combined, fromFlag...)

	byTarget := make(map[string]int, len(combined))
	out := make([]model.Mount, 0, len(combined))
	for _, m := range combined {
		if strings.TrimSpace(m.Target) == "" {
			return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
				"kart.new: mount is missing a target (source=%q)", m.Source)
		}
		if idx, ok := byTarget[m.Target]; ok {
			out[idx] = m
			continue
		}
		byTarget[m.Target] = len(out)
		out = append(out, m)
	}
	return out, nil
}

// expandHomeTildeSource rewrites a leading `~/` (or bare `~`) to the
// lakitu process's literal $HOME. devpod v0.22 does not apply
// devcontainer variable substitution (`${localEnv:HOME}`) to mounts
// that come in via --extra-devcontainer-path — the string is passed to
// docker verbatim — so the tilde has to be expanded on this side.
// Migrating a kart to a different host therefore requires a mount
// source rewrite; that concern lives in the migrate path.
func expandHomeTildeSource(source string) string {
	switch {
	case source == "~":
		return homeOrTilde("~")
	case strings.HasPrefix(source, "~/"):
		return homeOrTilde("~") + "/" + source[2:]
	default:
		return source
	}
}

// homeOrTilde returns $HOME when set, otherwise falls back to
// os.UserHomeDir — and finally back to the literal tilde so the error
// surfaces at devpod-up time rather than silently expanding to an
// empty string that'd bind-mount the filesystem root.
func homeOrTilde(fallback string) string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return fallback
}

// mergeFeatures composes tune + user features JSON, user-wins-on-overlap.
// The merge is shallow: top-level feature IDs, matching devpod's own
// interpretation of --additional-features.
func mergeFeatures(tuneJSON, userJSON string) (string, error) {
	tuneJSON = strings.TrimSpace(tuneJSON)
	userJSON = strings.TrimSpace(userJSON)
	if tuneJSON == "" && userJSON == "" {
		return "", nil
	}
	if userJSON == "" {
		// Validate tune JSON so a broken tune surfaces at kart.new time
		// rather than deep inside devpod.
		if _, err := decodeFeaturesMap(tuneJSON, "tune features"); err != nil {
			return "", err
		}
		return tuneJSON, nil
	}
	if tuneJSON == "" {
		if _, err := decodeFeaturesMap(userJSON, "--features"); err != nil {
			return "", err
		}
		return userJSON, nil
	}
	tm, err := decodeFeaturesMap(tuneJSON, "tune features")
	if err != nil {
		return "", err
	}
	um, err := decodeFeaturesMap(userJSON, "--features")
	if err != nil {
		return "", err
	}
	for k, v := range um {
		tm[k] = v
	}
	return encodeFeaturesMap(tm)
}

func decodeFeaturesMap(raw, label string) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"kart.new: %s is not valid JSON: %v", label, err)
	}
	if m == nil {
		m = make(map[string]any)
	}
	return m, nil
}

func encodeFeaturesMap(m map[string]any) (string, error) {
	buf, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("kart: re-marshal features: %w", err)
	}
	return string(buf), nil
}

// nixFeaturePrefix matches the canonical devcontainer-features Nix feature
// IDs. We deliberately match by prefix rather than exact ID so future
// major versions (`nix:2`, `nix:3`) and version-pinned forms
// (`nix@sha256:…`) continue to be detected without code changes.
const nixFeaturePrefix = "ghcr.io/devcontainers/features/nix"

// substitutersLineRE detects whether the user has already declared their
// own substituter list inside extraNixConfig. Anchored multiline so a
// `substituters =` *anywhere* in the block (not necessarily the first
// line) opts the tune out of auto-injection.
var substitutersLineRE = regexp.MustCompile(`(?m)^\s*substituters\s*=`)

// InjectNixCache appends substituters and trusted-public-keys lines into
// any Nix devcontainer feature's extraNixConfig that doesn't already set
// its own substituters list. Tunes that opt out (by writing their own
// substituters line) are left untouched — we trust their list verbatim
// rather than trying to merge.
//
// featuresJSON is the JSON-encoded devcontainer features map (post
// tune+--features merge). Returns the same map shape with the relevant
// extraNixConfig fields rewritten. An empty input passes through.
func InjectNixCache(featuresJSON string, info NixCacheInfo) (string, error) {
	featuresJSON = strings.TrimSpace(featuresJSON)
	if featuresJSON == "" || info.URL == "" {
		return featuresJSON, nil
	}
	m, err := decodeFeaturesMap(featuresJSON, "tune features")
	if err != nil {
		return "", err
	}
	mutated := false
	for key, raw := range m {
		if !strings.HasPrefix(key, nixFeaturePrefix) {
			continue
		}
		opts, ok := raw.(map[string]any)
		if !ok {
			// Non-object feature value (e.g. `"nix": {}` already coerced,
			// or a malformed `"nix": true`). decodeFeaturesMap would have
			// already validated JSON shape; treat unexpected types as
			// "leave alone".
			continue
		}
		extra, _ := opts["extraNixConfig"].(string)
		if substitutersLineRE.MatchString(extra) {
			continue
		}
		opts["extraNixConfig"] = appendNixCacheLines(extra, info)
		m[key] = opts
		mutated = true
	}
	if !mutated {
		return featuresJSON, nil
	}
	return encodeFeaturesMap(m)
}

func appendNixCacheLines(existing string, info NixCacheInfo) string {
	subs := append([]string{info.URL}, info.Upstream...)
	keys := append([]string{info.Pubkey}, info.UpstreamPubkeys...)
	add := fmt.Sprintf("substituters = %s,trusted-public-keys = %s",
		strings.Join(subs, " "),
		strings.Join(keys, " "))
	if existing == "" {
		return add
	}
	// The Nix devcontainer feature's install.sh splits EXTRANIXCONFIG on
	// commas (`IFS=, read -a …`), reading only the first physical line.
	// Newline-separated additions get silently dropped at install time, so
	// we use commas here to make sure our substituters/trusted-public-keys
	// reach create_or_update_file. Trailing newlines on the user's
	// existing block also break the comma-split, so we trim them.
	existing = strings.TrimRight(existing, "\n")
	return existing + "," + add
}

// nixosOrgFeatureID is the community devcontainer feature drift wires
// up automatically when a tune declares `flake_uri`. It uses the
// Determinate Systems installer with `--init none`, which avoids the
// daemon-config-staleness bug in the legacy ghcr.io/devcontainers/
// features/nix:1 feature. Cache substituters are passed as CLI flags
// to `nix profile install` at runtime via PostCreateCommand, not via
// nix.conf — so the daemon honours them through the trusted-user path
// regardless of when the daemon was started.
const nixosOrgFeatureID = "ghcr.io/devcontainer-community/devcontainer-features/nixos.org:1"

// injectNixosOrgFeature adds the community Nix feature to the merged
// features map if it isn't already present. The feature's
// `extra_options` is populated with `--extra-conf` flags carrying the
// circuit's substituters and trusted-public-keys when a NixCacheInfo
// is provided — this writes them into /etc/nix/nix.conf during the DS
// installer's run, *before* nix-daemon ever starts. Substituters set
// this way are trusted by the daemon system-wide (no per-user trust
// negotiation), so `nix profile install` from the unprivileged remote
// user will use the cache without needing --extra-substituters flags.
//
// If a tune author has explicitly declared the feature with their own
// `extra_options`, the resolver leaves their settings untouched.
func injectNixosOrgFeature(featuresJSON string, info *NixCacheInfo) (string, error) {
	featuresJSON = strings.TrimSpace(featuresJSON)
	var m map[string]any
	if featuresJSON == "" {
		m = map[string]any{}
	} else {
		var err error
		m, err = decodeFeaturesMap(featuresJSON, "tune features")
		if err != nil {
			return "", err
		}
	}
	if _, ok := m[nixosOrgFeatureID]; ok {
		return encodeFeaturesMap(m)
	}
	m[nixosOrgFeatureID] = map[string]any{
		"extra_options": nixosOrgExtraOptions(info),
	}
	return encodeFeaturesMap(m)
}

// githubCLIFeatureID is the canonical github-cli devcontainer feature
// drift wires up automatically when the resolved character carries a
// PAT. Layer-1 dotfiles drives `gh auth login --with-token` against
// it so the kart's git operations and GitHub API access flow through
// gh's credential helper. Without this feature `gh` isn't on PATH and
// the layer-1 install would have to skip the auth setup, regressing
// per-kart PAT scoping back to whatever's in the lakitu host's git
// credential store.
const githubCLIFeatureID = "ghcr.io/devcontainers/features/github-cli:1"

// injectGithubCLIFeature adds the github-cli feature to the merged
// features map if no `:1`-versioned entry is already present. The
// feature's default options are sufficient (latest stable gh on PATH);
// drift doesn't override anything. If a tune author already declared
// the feature with their own options, the resolver leaves their entry
// untouched.
func injectGithubCLIFeature(featuresJSON string) (string, error) {
	featuresJSON = strings.TrimSpace(featuresJSON)
	var m map[string]any
	if featuresJSON == "" {
		m = map[string]any{}
	} else {
		var err error
		m, err = decodeFeaturesMap(featuresJSON, "tune features")
		if err != nil {
			return "", err
		}
	}
	if _, ok := m[githubCLIFeatureID]; ok {
		return encodeFeaturesMap(m)
	}
	m[githubCLIFeatureID] = map[string]any{}
	return encodeFeaturesMap(m)
}

// nixosOrgExtraOptions builds the DS-installer flag string drift
// passes via the community feature's `extra_options`. Always includes
// `--init none` (skip systemd integration; devcontainers don't run
// systemd anyway). When a circuit cache is wired, adds
// `--extra-conf 'extra-substituters = …'`,
// `--extra-conf 'extra-trusted-substituters = …'`, and
// `--extra-conf 'extra-trusted-public-keys = …'` so the daemon picks
// them up at startup AND the workspace user (non-root) can reference
// them at runtime via `--option substituters` — required because
// Determinate Nix's bundled nix.conf appends
// `extra-substituters = https://install.determinate.systems` that we
// can't remove from the additive list, only override at the call site.
func nixosOrgExtraOptions(info *NixCacheInfo) string {
	parts := []string{"--init none"}
	if info == nil || info.URL == "" {
		return strings.Join(parts, " ")
	}
	subs := append([]string{info.URL}, flakeInstallUpstreamSubstituter)
	keys := []string{info.Pubkey, flakeInstallUpstreamPubkey}
	parts = append(parts,
		fmt.Sprintf("--extra-conf 'extra-substituters = %s'", strings.Join(subs, " ")),
		fmt.Sprintf("--extra-conf 'extra-trusted-substituters = %s'", strings.Join(subs, " ")),
		fmt.Sprintf("--extra-conf 'extra-trusted-public-keys = %s'", strings.Join(keys, " ")),
	)
	return strings.Join(parts, " ")
}

// flakeInstallSubstitutersFallback is used when no NixCache is wired
// up (the marker file isn't present on the circuit). The flake install
// still works, just hits cache.nixos.org directly with the well-known
// upstream pubkey — no acceleration, no failure either.
const flakeInstallUpstreamSubstituter = "https://cache.nixos.org"
const flakeInstallUpstreamPubkey = "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="

// buildFlakeInstallPostCreate emits the one-shot postCreateCommand
// drift splices into the kart's devcontainer overlay when
// `tune.flake_uri` is set. Single-line bash that:
//
//  1. Starts nix-daemon under passwordless sudo if it isn't already
//     running. Multi-user install owns /nix as root and the community
//     feature ships no entrypoint, so on first PCC run nothing has
//     bootstrapped the daemon yet. The non-root user can't access
//     /nix/var/nix/db without it.
//  2. Sources the daemon profile so $PATH and $NIX_REMOTE point at the
//     daemon socket.
//  3. Runs `nix profile install <flake_uri>` with `--option substituters`
//     and `--option trusted-public-keys` set to the circuit-local cache
//     plus cache.nixos.org. The override is necessary because
//     Determinate Nix's bundled `/etc/nix/nix.conf` appends
//     `extra-substituters = https://install.determinate.systems` which
//     can't be removed from the additive list, only overridden at the
//     call site. Without this, every flake-install runs queries against
//     install.determinate.systems and fails on hosts where DNS returns
//     a v6-only address that the container can't reach (Determinate
//     Nix's libcurl doesn't fall back v6 → v4 reliably).
//
// Rejects flake URIs containing a literal single quote — we wrap the
// URI in single quotes for shell safety, and supporting embedded
// single quotes would mean shell-escape acrobatics for a corner case
// flake URIs don't realistically use.
func buildFlakeInstallPostCreate(flakeURI string, info *NixCacheInfo) (string, error) {
	if strings.ContainsRune(flakeURI, '\'') {
		return "", rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"tune.flake_uri must not contain single quotes: %q", flakeURI)
	}
	const ensureDaemon = `if ! pidof nix-daemon >/dev/null 2>&1; then ` +
		`sudo -n /nix/var/nix/profiles/default/bin/nix-daemon >/tmp/nix-daemon.log 2>&1 & ` +
		`for i in $(seq 1 30); do [ -S /nix/var/nix/daemon-socket/socket ] && break; sleep 1; done; ` +
		`fi`
	// Determinate Nix's nix-daemon.sh self-guards via __ETC_PROFILE_NIX_SOURCED
	// and `return`s early if the flag is already set. devpod runs the lifecycle
	// hook through a chain where a parent shell has already sourced the profile,
	// then spawns the hook in a context that resets PATH but inherits the
	// exported flag — sourcing becomes a no-op, PATH stays system-default,
	// `nix` is not found. Clearing the flag forces the re-export.
	const sourceProfile = "unset __ETC_PROFILE_NIX_SOURCED; . /nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh 2>/dev/null || true"
	subs, keys := flakeInstallSubstituters(info)
	install := fmt.Sprintf(
		"nix profile install --extra-experimental-features 'nix-command flakes' "+
			"--option substituters '%s' --option trusted-public-keys '%s' '%s'",
		strings.Join(subs, " "),
		strings.Join(keys, " "),
		flakeURI,
	)
	return ensureDaemon + " && " + sourceProfile + " && " + install, nil
}

// flakeInstallSubstituters composes the substituters / trusted-public-keys
// pair to pass on the `nix profile install` command line. Always includes
// the well-known cache.nixos.org as a fallback so the install still
// completes when no circuit-local harmonia is configured. When a
// NixCacheInfo is wired, the circuit cache is listed first so it gets
// queried before falling through to the upstream.
func flakeInstallSubstituters(info *NixCacheInfo) (subs, keys []string) {
	if info != nil && info.URL != "" {
		subs = append(subs, info.URL)
		if info.Pubkey != "" {
			keys = append(keys, info.Pubkey)
		}
	}
	subs = append(subs, flakeInstallUpstreamSubstituter)
	keys = append(keys, flakeInstallUpstreamPubkey)
	return subs, keys
}
