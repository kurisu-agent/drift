// Package kart implements `kart.new` and its supporting pieces: flag
// composition, devcontainer source normalization, starter history strip,
// layer-1 dotfiles generation, and the orchestrator that ties them together
// with devpod.
package kart

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// Tune duplicates internal/server.Tune to keep kart below server in the
// dep graph (server imports kart).
type Tune struct {
	Starter      string
	Devcontainer string
	DotfilesRepo string
	Features     string
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
}

type Resolved struct {
	Name          string
	SourceMode    string // "clone" | "starter" | "none"
	SourceURL     string
	TuneName      string // empty when resolved to "none"
	Tune          *Tune
	CharacterName string
	Character     *Character
	Features      string // already merged
	Devcontainer  string // raw; normalized later
	Dotfiles      string
	Autostart     bool
}

type Resolver struct {
	Defaults ServerDefaults
	// LoadTune / LoadCharacter: missing entries should return a NotFound
	// rpcerr. LoadCharacter must return a Character with PAT already
	// resolved — downstream code never touches the chest.
	LoadTune      func(name string) (*Tune, error)
	LoadCharacter func(name string) (*Character, error)
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
