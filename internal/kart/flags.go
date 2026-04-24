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
	"strings"

	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/rpcerr"
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
}

// ServerDefaults is the narrow slice of internal/config.Server the resolver
// uses, so kart stays independent of the wider config schema.
type ServerDefaults struct {
	DefaultTune      string
	DefaultCharacter string
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
	// Mounts carries --mount specs from `drift new`. Appended on top of
	// tune.MountDirs (flag-wins-on-target during the resolver merge).
	Mounts []model.Mount
	// NormaliseUser, when non-nil, overrides the tune's normalise_user
	// setting for this kart. Nil leaves the tune value in effect (which
	// itself defaults to true).
	NormaliseUser *bool
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
	Features      string // already merged
	Devcontainer  string // raw; normalized later
	Dotfiles      string
	Autostart     bool
	// Env carries chest-resolved literal env vars per injection site. Held
	// only in memory; never persisted. EnvRefs holds the parallel
	// chest:<name> references for persistence and `kart info` rendering.
	Env     ResolvedTuneEnv
	EnvRefs TuneEnv
	// Mounts is tune.MountDirs + flag mounts, deduped by target with
	// flag-wins precedence. Bind sources have `~/` expanded to the
	// lakitu process's literal $HOME; `~/` on the target side is
	// rewritten to /home/<character>/ when a character is resolved.
	// Source-side expansion happens here (not via devcontainer
	// variable substitution) because devpod v0.22 does not substitute
	// `${localEnv:…}` in mounts from --extra-devcontainer-path.
	Mounts []model.Mount
	// NormaliseUser is the effective setting after flag-beats-tune
	// precedence: flag value wins when non-nil, else tune value (which
	// itself defaults to true). False skips the remoteUser override and
	// the onCreateCommand rename — the kart runs as the image's
	// default user. Always false when no character is resolved (nothing
	// to rename to).
	NormaliseUser bool
	// NormaliseUserRef records the explicit override the kart was
	// created with, if any. Persisted on KartConfig so drift detection
	// can tell the difference between "inherited from tune" and
	// "explicitly set on this kart".
	NormaliseUserRef *bool
	// MigratedFrom threads through from Flags unchanged; the resolver has
	// nothing to decide about it.
	MigratedFrom model.MigratedFrom
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
	// Verbose, if non-nil, receives a `[resolver] …` summary of the
	// effective resolved inputs (tune, character, source, devcontainer,
	// dotfiles, env block names) after each Resolve call. Wire to
	// os.Stderr in verbose mode.
	Verbose io.Writer
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

	mounts, err := mergeMounts(tuneMounts(tune), f.Mounts, characterName)
	if err != nil {
		return nil, err
	}

	normaliseUser := resolveNormaliseUser(tune, f.NormaliseUser, characterName)

	resolved := &Resolved{
		Name:             f.Name,
		SourceMode:       sourceMode,
		SourceURL:        sourceURL,
		TuneName:         effectiveTune,
		Tune:             tune,
		CharacterName:    characterName,
		Character:        character,
		Features:         features,
		Devcontainer:     devcontainer,
		Dotfiles:         dotfiles,
		Autostart:        f.Autostart,
		Env:              resolvedEnv,
		EnvRefs:          envRefs,
		Mounts:           mounts,
		NormaliseUser:    normaliseUser,
		NormaliseUserRef: f.NormaliseUser,
		MigratedFrom:     f.MigratedFrom,
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

// mergeMounts concatenates tune + flag mounts and rewrites `~/` in
// bind sources to lakitu's literal $HOME and `~/` in bind targets to
// `/home/<character>/`. Both expansions happen here (not via
// devcontainer variable substitution) — devpod v0.22 does not
// substitute `${localEnv:…}` for mounts that come in via
// --extra-devcontainer-path, and the container user's home has no
// devcontainer substitution at all. Flag mounts win on a matching
// target: a second entry with the same target overrides the first.
// Targets are required (mount without a target is nonsensical on
// docker's side).
func mergeMounts(fromTune, fromFlag []model.Mount, character string) ([]model.Mount, error) {
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
		m.Source = expandHomeTildeSource(m.Source)
		target, err := expandHomeTildeTarget(m.Target, character)
		if err != nil {
			return nil, err
		}
		m.Target = target
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

// expandHomeTildeTarget rewrites a leading `~/` (or bare `~`) on the
// container side to /home/<character>/ — requires a resolved character.
// Errors if the mount uses `~/` but the kart has no character
// (nothing to expand against).
func expandHomeTildeTarget(target, character string) (string, error) {
	if target != "~" && !strings.HasPrefix(target, "~/") {
		return target, nil
	}
	if character == "" {
		return "", rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"kart.new: mount target %q uses ~/ but the kart has no character to expand against", target)
	}
	home := "/home/" + character
	if target == "~" {
		return home, nil
	}
	return home + "/" + target[2:], nil
}

// resolveNormaliseUser applies the precedence: explicit flag > tune >
// default(true). When no character is resolved, normalisation is forced
// off — there's nothing to rename the user to.
func resolveNormaliseUser(tune *Tune, flag *bool, character string) bool {
	if character == "" {
		return false
	}
	if flag != nil {
		return *flag
	}
	if tune == nil {
		return true
	}
	return tune.NormaliseUserEnabled()
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
