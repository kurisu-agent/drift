// Package kart implements `kart.new` and its supporting pieces: flag
// composition, devcontainer source normalization, starter history strip,
// layer-1 dotfiles generation, and the orchestrator that ties them together
// with devpod. See plans/PLAN.md § Flag composition and resolution and
// § Kart creation modes.
package kart

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// Tune mirrors the tune-profile fields the resolver reads. Duplicated from
// internal/server.Tune so internal/kart stays below internal/server in the
// dependency graph — the server package imports this one. plans/PLAN.md
// § Tune profile fields.
type Tune struct {
	Starter      string
	Devcontainer string
	DotfilesRepo string
	Features     string
}

// Character mirrors the character fields kart.new consumes: git identity
// plus optional GitHub/PAT/SSH data used by the layer-1 dotfiles generator.
// plans/PLAN.md § Character file.
type Character struct {
	GitName    string
	GitEmail   string
	GithubUser string
	SSHKeyPath string
	// PAT is the *resolved* token value (already looked up via chest) or
	// empty when no PAT is attached. The resolver layer never sees the
	// literal `chest:<name>` reference — the server handler resolves it
	// before calling Resolve().
	PAT string
}

// ServerDefaults is the narrow slice of internal/config.Server the resolver
// uses. Passing this struct (rather than *config.Server) keeps the kart
// package independent of the wider config schema.
type ServerDefaults struct {
	DefaultTune      string
	DefaultCharacter string
}

// Flags is the parsed-but-unresolved input to `kart.new`. All fields map 1:1
// to the `drift new` flags documented in plans/PLAN.md § drift new flags.
// Empty means "not set" so Resolve() can layer tune defaults underneath.
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
}

// Resolved is the composed view produced by Resolve(). It carries the tune
// and character records alongside the final flag values so downstream code
// never has to re-open the garage.
type Resolved struct {
	Name          string
	SourceMode    string // "clone" | "starter" | "none"
	SourceURL     string
	TuneName      string // empty when resolved to "none"
	Tune          *Tune
	CharacterName string
	Character     *Character
	Features      string // already merged
	Devcontainer  string // raw value: path/JSON/URL — normalization happens later
	Dotfiles      string
	Autostart     bool
}

// Resolver composes the flag layers. Instances bind to a garage so the
// handler layer can construct one per call without repeating filesystem
// lookups.
type Resolver struct {
	Defaults ServerDefaults
	// LoadTune returns a tune by name. Missing tunes should yield a
	// user-facing rpcerr; the caller decides what NotFound means for their
	// domain.
	LoadTune func(name string) (*Tune, error)
	// LoadCharacter returns a character by name. Missing characters yield
	// a NotFound rpcerr. The returned Character must carry the resolved
	// PAT (already de-chested) so downstream code never touches the chest.
	LoadCharacter func(name string) (*Character, error)
}

// Resolve applies plans/PLAN.md § Flag composition:
//  1. server defaults (default_tune, default_character)
//  2. tune preset (starter, features, devcontainer, dotfiles_repo)
//  3. explicit flags always override tune values
//  4. --features is ADDITIVE — merged on top of the tune's features
//  5. --devcontainer passes through; normalization to a file happens later.
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
			// default_tune is a hint, not a hard requirement. When it points
			// at a tune that doesn't exist (e.g. the server config ships
			// `default_tune: default` but no `tunes/default.yaml` has been
			// created yet), fall through to "no tune" rather than erroring.
			// Explicit --tune still errors — only the defaulted path degrades.
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

	var sourceMode, sourceURL string
	switch {
	case f.Clone != "":
		sourceMode = "clone"
		sourceURL = f.Clone
	case f.Starter != "":
		sourceMode = "starter"
		sourceURL = f.Starter
	case tune != nil && tune.Starter != "":
		sourceMode = "starter"
		sourceURL = tune.Starter
	default:
		sourceMode = "none"
	}

	devcontainer := f.Devcontainer
	if devcontainer == "" && tune != nil {
		devcontainer = tune.Devcontainer
	}

	dotfiles := f.Dotfiles
	if dotfiles == "" && tune != nil {
		dotfiles = tune.DotfilesRepo
	}

	features, err := mergeFeatures(tuneFeatures(tune), f.Features)
	if err != nil {
		return nil, err
	}

	return &Resolved{
		Name:          f.Name,
		SourceMode:    sourceMode,
		SourceURL:     sourceURL,
		TuneName:      effectiveTune,
		Tune:          tune,
		CharacterName: characterName,
		Character:     character,
		Features:      features,
		Devcontainer:  devcontainer,
		Dotfiles:      dotfiles,
		Autostart:     f.Autostart,
	}, nil
}

func tuneFeatures(t *Tune) string {
	if t == nil {
		return ""
	}
	return t.Features
}

// mergeFeatures composes the tune's features JSON and the user's --features
// JSON, user-wins-on-overlap. Both sides are optional. The return value is
// JSON text suitable for devpod's --additional-features.
//
// The merge is shallow: devpod features are top-level feature-ID keys so a
// user specifying the same feature ID as the tune replaces the whole record.
// That matches devpod's own interpretation of --additional-features.
func mergeFeatures(tuneJSON, userJSON string) (string, error) {
	tuneJSON = strings.TrimSpace(tuneJSON)
	userJSON = strings.TrimSpace(userJSON)
	if tuneJSON == "" && userJSON == "" {
		return "", nil
	}
	if userJSON == "" {
		// Still validate tune JSON so a broken tune surfaces at kart.new
		// time rather than deep inside devpod.
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
