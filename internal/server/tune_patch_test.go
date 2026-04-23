package server_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/server"
	"gopkg.in/yaml.v3"
)

// TestTunePatchDoesNotClobberUnmentionedFields is the regression test
// for the original bug: `tune set` used to full-replace the YAML, so
// editing one field wiped env/mount_dirs if the CLI didn't carry
// them. tune.patch must leave untouched fields alone.
func TestTunePatchDoesNotClobberUnmentionedFields(t *testing.T) {
	garage, d := newTuneTestDeps(t)

	// Seed a tune with env + mount_dirs already set.
	seed := model.Tune{
		Starter:      "https://example.org/starter.git",
		Devcontainer: "/etc/devcontainer.json",
		Env: model.TuneEnv{
			Build: map[string]string{"GITHUB_TOKEN": "chest:gh"},
		},
		MountDirs: []model.Mount{
			{Type: "bind", Source: "/host/src", Target: "/c/target"},
		},
	}
	writeSeedTune(t, garage, "mytune", seed)

	// Patch only the starter.
	params, _ := json.Marshal(server.TunePatchParams{
		Name: "mytune",
		Ops: []server.TunePatchOp{
			{Path: "starter", Op: "set", Value: "https://example.org/new.git"},
		},
	})
	if _, err := d.TunePatchHandler(context.Background(), params); err != nil {
		t.Fatalf("TunePatchHandler: %v", err)
	}

	// Reload — env and mount_dirs must survive.
	got := readTune(t, garage, "mytune")
	if got.Starter != "https://example.org/new.git" {
		t.Errorf("starter = %q, want new value", got.Starter)
	}
	if got.Env.Build["GITHUB_TOKEN"] != "chest:gh" {
		t.Errorf("env.build.GITHUB_TOKEN lost: %v", got.Env.Build)
	}
	if len(got.MountDirs) != 1 || got.MountDirs[0].Target != "/c/target" {
		t.Errorf("mount_dirs lost: %v", got.MountDirs)
	}
}

// TestTuneNewErrorsIfExists locks in the sharp create-vs-update
// boundary — tune.new refuses to touch an existing name.
func TestTuneNewErrorsIfExists(t *testing.T) {
	garage, d := newTuneTestDeps(t)
	writeSeedTune(t, garage, "exists", model.Tune{Starter: "x"})

	params, _ := json.Marshal(server.TuneNewParams{Name: "exists", Starter: "y"})
	_, err := d.TuneNewHandler(context.Background(), params)
	if err == nil {
		t.Fatal("TuneNewHandler on existing name: want error, got nil")
	}
}

// TestTunePatchUnsetMapEntryPrunes: the env.build map must disappear
// from the marshalled YAML when its last key is unset, matching the
// yamlpath pruning contract.
func TestTunePatchUnsetMapEntryPrunes(t *testing.T) {
	garage, d := newTuneTestDeps(t)
	seed := model.Tune{
		Env: model.TuneEnv{Build: map[string]string{"ONLY": "chest:foo"}},
	}
	writeSeedTune(t, garage, "t", seed)

	params, _ := json.Marshal(server.TunePatchParams{
		Name: "t",
		Ops:  []server.TunePatchOp{{Path: "env.build.ONLY", Op: "unset"}},
	})
	if _, err := d.TunePatchHandler(context.Background(), params); err != nil {
		t.Fatalf("TunePatchHandler: %v", err)
	}
	buf, err := os.ReadFile(filepath.Join(garage, "tunes", "t.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Empty tune should marshal to `{}\n` (or similar) — crucially,
	// no dangling `env:` key.
	if got := string(buf); got != "" && got != "{}\n" {
		// The yaml.Marshal of a zero Tune with all omitempty produces {}.
		// Guard against a `env:\n  build: {}` residue.
		if containsEnvKey(got) {
			t.Errorf("yaml still has env block after prune:\n%s", got)
		}
	}
}

// TestCharacterPatchPreservesGithubUser: patching git_email alone
// must leave github_user intact.
func TestCharacterPatchPreservesGithubUser(t *testing.T) {
	garage, d := newTuneTestDeps(t)
	if err := os.MkdirAll(filepath.Join(garage, "characters"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := server.Character{
		GitName:    "Alice",
		GitEmail:   "alice@old.example",
		GithubUser: "alice-gh",
	}
	buf, _ := yaml.Marshal(&seed)
	_ = os.WriteFile(filepath.Join(garage, "characters", "alice.yaml"), buf, 0o644)

	params, _ := json.Marshal(server.CharacterPatchParams{
		Name: "alice",
		Ops: []server.CharacterPatchOp{
			{Path: "git_email", Op: "set", Value: "alice@new.example"},
		},
	})
	if _, err := d.CharacterPatchHandler(context.Background(), params); err != nil {
		t.Fatalf("CharacterPatchHandler: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(garage, "characters", "alice.yaml"))
	var got server.Character
	_ = yaml.Unmarshal(raw, &got)
	if got.GitEmail != "alice@new.example" {
		t.Errorf("git_email = %q", got.GitEmail)
	}
	if got.GithubUser != "alice-gh" {
		t.Errorf("github_user lost: %q", got.GithubUser)
	}
}

// --- helpers ---

func newTuneTestDeps(t *testing.T) (string, *server.Deps) {
	t.Helper()
	garage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(garage, "tunes"), 0o755); err != nil {
		t.Fatal(err)
	}
	return garage, &server.Deps{GarageDir: garage}
}

func writeSeedTune(t *testing.T, garage, name string, tune model.Tune) {
	t.Helper()
	buf, err := yaml.Marshal(&tune)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(garage, "tunes", name+".yaml")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTune(t *testing.T, garage, name string) model.Tune {
	t.Helper()
	buf, err := os.ReadFile(filepath.Join(garage, "tunes", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var out model.Tune
	if err := yaml.Unmarshal(buf, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func containsEnvKey(s string) bool {
	// Loose check — the marshalled YAML should have no top-level
	// `env:` line when Env is the zero TuneEnv.
	for _, line := range splitLinesNoAlloc(s) {
		if len(line) >= 4 && line[:4] == "env:" {
			return true
		}
	}
	return false
}

func splitLinesNoAlloc(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
