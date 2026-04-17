package kart

import (
	"errors"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/rpcerr"
)

func TestResolveMutuallyExclusiveSources(t *testing.T) {
	r := &Resolver{}
	_, err := r.Resolve(Flags{Name: "k", Clone: "x", Starter: "y"})
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeMutuallyExclusive {
		t.Fatalf("expected mutually_exclusive_flags, got %v", err)
	}
}

func TestResolveSourcePriority(t *testing.T) {
	cases := []struct {
		name    string
		flags   Flags
		tune    *Tune
		wantMd  string
		wantURL string
	}{
		{"clone wins", Flags{Clone: "c"}, &Tune{Starter: "t"}, "clone", "c"},
		{"starter wins over tune", Flags{Starter: "s"}, &Tune{Starter: "t"}, "starter", "s"},
		{"tune starter", Flags{}, &Tune{Starter: "t"}, "starter", "t"},
		{"none", Flags{}, nil, "none", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Resolver{
				LoadTune: func(string) (*Tune, error) { return tc.tune, nil },
			}
			f := tc.flags
			if tc.tune != nil {
				f.Tune = "default"
			}
			got, err := r.Resolve(f)
			if err != nil {
				t.Fatal(err)
			}
			if got.SourceMode != tc.wantMd || got.SourceURL != tc.wantURL {
				t.Fatalf("got %s/%s, want %s/%s", got.SourceMode, got.SourceURL, tc.wantMd, tc.wantURL)
			}
		})
	}
}

func TestResolveTuneNone(t *testing.T) {
	r := &Resolver{
		Defaults: ServerDefaults{DefaultTune: "default"},
		LoadTune: func(string) (*Tune, error) {
			t.Fatalf("LoadTune should not be called when --tune=none")
			return nil, nil
		},
	}
	got, err := r.Resolve(Flags{Name: "k", Tune: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if got.TuneName != "" {
		t.Fatalf("tune should be empty for --tune=none, got %q", got.TuneName)
	}
	if got.SourceMode != "none" {
		t.Fatalf("source mode should be none, got %q", got.SourceMode)
	}
}

func TestResolveFeaturesAdditive(t *testing.T) {
	r := &Resolver{
		Defaults: ServerDefaults{DefaultTune: "default"},
		LoadTune: func(string) (*Tune, error) {
			return &Tune{Features: `{"ghcr.io/a":{"version":"1"},"ghcr.io/b":{"version":"1"}}`}, nil
		},
	}
	got, err := r.Resolve(Flags{Name: "k", Features: `{"ghcr.io/b":{"version":"2"},"ghcr.io/c":{"version":"1"}}`})
	if err != nil {
		t.Fatal(err)
	}
	// User wins on the overlap key, tune's other keys survive.
	if !strings.Contains(got.Features, `"ghcr.io/a"`) {
		t.Fatalf("tune feature a missing: %s", got.Features)
	}
	if !strings.Contains(got.Features, `"ghcr.io/c"`) {
		t.Fatalf("user feature c missing: %s", got.Features)
	}
	if !strings.Contains(got.Features, `"version":"2"`) {
		t.Fatalf("user-side version for b missing: %s", got.Features)
	}
}

func TestResolveFeaturesInvalidJSON(t *testing.T) {
	r := &Resolver{
		Defaults: ServerDefaults{DefaultTune: "default"},
		LoadTune: func(string) (*Tune, error) { return &Tune{}, nil },
	}
	_, err := r.Resolve(Flags{Name: "k", Features: `not json`})
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeInvalidFlag {
		t.Fatalf("expected invalid_flag, got %v", err)
	}
}

func TestResolveExplicitOverrides(t *testing.T) {
	r := &Resolver{
		Defaults: ServerDefaults{DefaultTune: "default", DefaultCharacter: "kurisu"},
		LoadTune: func(string) (*Tune, error) {
			return &Tune{
				Devcontainer: "tune-dc.json",
				DotfilesRepo: "tune-dotfiles",
			}, nil
		},
		LoadCharacter: func(string) (*Character, error) {
			return &Character{GitName: "k", GitEmail: "k@x"}, nil
		},
	}
	got, err := r.Resolve(Flags{
		Name:         "k",
		Devcontainer: "explicit.json",
		Dotfiles:     "explicit-dotfiles",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Devcontainer != "explicit.json" {
		t.Fatalf("devcontainer: got %q", got.Devcontainer)
	}
	if got.Dotfiles != "explicit-dotfiles" {
		t.Fatalf("dotfiles: got %q", got.Dotfiles)
	}
	if got.CharacterName != "kurisu" {
		t.Fatalf("default character should kick in: got %q", got.CharacterName)
	}
}
