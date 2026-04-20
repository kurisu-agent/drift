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

func TestResolveDefaultTuneMissingDegradesToNone(t *testing.T) {
	// When default_tune is set in server config but the tune file doesn't
	// exist yet, Resolve should treat it as "no tune" rather than erroring —
	// the default is a hint, not a hard requirement. Explicit --tune still
	// errors (covered by TestResolveExplicitTuneMissingErrors below).
	r := &Resolver{
		Defaults: ServerDefaults{DefaultTune: "default"},
		LoadTune: func(string) (*Tune, error) {
			return nil, rpcerr.NotFound("tune_not_found", "tune %q not found", "default")
		},
	}
	got, err := r.Resolve(Flags{Name: "k"})
	if err != nil {
		t.Fatalf("expected silent degrade, got %v", err)
	}
	if got.TuneName != "" {
		t.Fatalf("TuneName should be empty when default tune missing, got %q", got.TuneName)
	}
	if got.Tune != nil {
		t.Fatalf("Tune should be nil, got %+v", got.Tune)
	}
	if got.SourceMode != "none" {
		t.Fatalf("SourceMode should be none, got %q", got.SourceMode)
	}
}

func TestResolveExplicitTuneMissingErrors(t *testing.T) {
	// Explicit --tune on a non-existent tune must error; only the defaulted
	// path silently degrades.
	r := &Resolver{
		Defaults: ServerDefaults{DefaultTune: "default"},
		LoadTune: func(string) (*Tune, error) {
			return nil, rpcerr.NotFound("tune_not_found", "tune %q not found", "missing")
		},
	}
	_, err := r.Resolve(Flags{Name: "k", Tune: "missing"})
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != "tune_not_found" {
		t.Fatalf("expected tune_not_found, got %v", err)
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

// TestResolveDotfilesChestRef: a tune with `dotfiles_repo: chest:<name>`
// dechests through ResolveChestRef so the Resolved.Dotfiles is the literal
// URL (typically with PAT pre-embedded, opaque to drift).
func TestResolveDotfilesChestRef(t *testing.T) {
	const want = "https://ghp_xxx@github.com/example-org/private-dotfiles.git"
	r := &Resolver{
		Defaults: ServerDefaults{DefaultTune: "default"},
		LoadTune: func(string) (*Tune, error) {
			return &Tune{DotfilesRepo: "chest:dotfiles-url"}, nil
		},
		ResolveChestRef: func(ref string) (string, error) {
			if ref != "chest:dotfiles-url" {
				t.Fatalf("ResolveChestRef called with %q, want chest:dotfiles-url", ref)
			}
			return want, nil
		},
	}
	got, err := r.Resolve(Flags{Name: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Dotfiles != want {
		t.Errorf("dotfiles = %q, want %q", got.Dotfiles, want)
	}
}

// TestResolveDotfilesChestRefMissing: a chest miss surfaces as
// chest_entry_not_found with `dotfiles_repo` field context attached so the
// error envelope tells the user *where* the bad ref is, not just that the
// chest entry is missing.
func TestResolveDotfilesChestRefMissing(t *testing.T) {
	r := &Resolver{
		Defaults: ServerDefaults{DefaultTune: "default"},
		LoadTune: func(string) (*Tune, error) {
			return &Tune{DotfilesRepo: "chest:missing"}, nil
		},
		ResolveChestRef: func(string) (string, error) {
			return "", rpcerr.NotFound(rpcerr.TypeChestEntryNotFound,
				"chest entry %q not found", "missing")
		},
	}
	_, err := r.Resolve(Flags{Name: "k"})
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeChestEntryNotFound {
		t.Fatalf("want chest_entry_not_found, got %v", err)
	}
	if re.Data["field"] != "dotfiles_repo" {
		t.Errorf("missing field=dotfiles_repo in Data, got %v", re.Data)
	}
	if re.Data["name"] != "missing" {
		t.Errorf("missing name=missing in Data, got %v", re.Data)
	}
}

// TestResolveDotfilesChestRefRequiresResolver: a tune declares a chest
// ref but the resolver was wired without ResolveChestRef. Should error
// with internal_error rather than crashing or silently passing the
// `chest:` literal through to devpod.
func TestResolveDotfilesChestRefRequiresResolver(t *testing.T) {
	r := &Resolver{
		Defaults: ServerDefaults{DefaultTune: "default"},
		LoadTune: func(string) (*Tune, error) {
			return &Tune{DotfilesRepo: "chest:dotfiles-url"}, nil
		},
		// ResolveChestRef intentionally nil
	}
	_, err := r.Resolve(Flags{Name: "k"})
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Code != rpcerr.CodeInternal {
		t.Fatalf("want internal rpcerr, got %v", err)
	}
	if !strings.Contains(re.Message, "dotfiles_repo") {
		t.Errorf("error message missing dotfiles_repo context: %v", re.Message)
	}
}
