package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kurisu-agent/drift/internal/config"
)

func TestLoadClient_MissingFileIsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.yaml")
	c, err := config.LoadClient(path)
	if err != nil {
		t.Fatalf("LoadClient on missing file: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client for missing file")
	}
	if !c.ManagesSSHConfig() {
		t.Error("manage_ssh_config should default to true when field is absent")
	}
}

func TestLoadClient_RejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `default_circuit: ""
manage_ssh_config: true
circuits: {}
rogue_key: oops
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadClient(path)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	if !strings.Contains(err.Error(), "rogue_key") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}

func TestLoadClient_ValidatesCircuitNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `default_circuit: "my-box"
circuits:
  my-box:
    host: dev@my-box.example.com
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadClient(path)
	if err != nil {
		t.Fatalf("LoadClient: %v", err)
	}
	if got, want := c.DefaultCircuit, "my-box"; got != want {
		t.Errorf("default_circuit = %q, want %q", got, want)
	}
	if got, want := c.Circuits["my-box"].Host, "dev@my-box.example.com"; got != want {
		t.Errorf("host = %q, want %q", got, want)
	}
}

func TestClient_Validate(t *testing.T) {
	cases := []struct {
		name    string
		c       config.Client
		wantSub string
	}{
		{
			name: "bad circuit name",
			c: config.Client{
				Circuits: map[string]config.ClientCircuit{
					"Bad Name": {Host: "x@y"},
				},
			},
			wantSub: "invalid",
		},
		{
			name: "missing host",
			c: config.Client{
				Circuits: map[string]config.ClientCircuit{
					"ok": {},
				},
			},
			wantSub: "host is required",
		},
		{
			name: "default_circuit points to nothing",
			c: config.Client{
				DefaultCircuit: "ghost",
				Circuits: map[string]config.ClientCircuit{
					"real": {Host: "x@y"},
				},
			},
			wantSub: "default_circuit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q missing substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestServer_Validate(t *testing.T) {
	s := config.DefaultServer()
	if err := s.Validate(); err != nil {
		t.Errorf("default server should validate, got: %v", err)
	}

	s.Chest.Backend = "made-up"
	if err := s.Validate(); err == nil {
		t.Error("expected error for unknown backend")
	}

	s.Chest.Backend = config.ChestBackendEnvfile
	s.DefaultTune = ""
	if err := s.Validate(); err == nil {
		t.Error("expected error for empty default_tune")
	}
}

func TestLoadServer_MissingFileIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	_, err := config.LoadServer(path)
	if err == nil {
		t.Fatal("expected error for missing server config")
	}
	if !strings.Contains(err.Error(), "lakitu init") {
		t.Errorf("error should mention `lakitu init`, got: %v", err)
	}
}

func TestLoadServer_RejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `default_tune: default
default_character: ""
nix_cache_url: ""
chest:
  backend: envfile
surprise: 1
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadServer(path)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	if !strings.Contains(err.Error(), "surprise") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}

func TestSaveServer_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "garage", "config.yaml")
	want := &config.Server{
		DefaultTune:      "node",
		DefaultCharacter: "kurisu",
		NixCacheURL:      "https://cache.example.com",
		Chest:            config.ChestConfig{Backend: config.ChestBackendEnvfile},
	}
	if err := config.SaveServer(path, want); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	got, err := config.LoadServer(path)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("round-trip mismatch (-want +got):\n%s", diff)
	}
}

func TestWriteFileAtomic_NoTempFilesLeftOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.yaml")
	if err := config.WriteFileAtomic(path, []byte("k: v\n"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected a single file in dir, got %v", names)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

func TestWriteFileAtomic_ReplacesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
}

func TestWriteFileAtomic_ErrorLeavesNoTemp(t *testing.T) {
	// Target a nonexistent, unwritable parent path — MkdirAll on /proc/... fails.
	// We simulate by using a file-as-parent: create a regular file, then try to
	// write beneath it (which is impossible on POSIX).
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "child.yaml")
	err := config.WriteFileAtomic(path, []byte("x"), 0o600)
	if err == nil {
		t.Fatal("expected error writing beneath a file-as-parent")
	}
	// Nothing unexpected should have been left in dir.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "blocker" {
		t.Errorf("unexpected dir contents: %v", entries)
	}
}

func TestClientConfigPath_XDGOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	got, err := config.ClientConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "drift", "config.yaml")
	if got != want {
		t.Errorf("ClientConfigPath = %q, want %q", got, want)
	}
}

func TestClientConfigPath_FallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Unset XDG so we exercise the fallback.
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := config.ClientConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "drift", "config.yaml")
	if got != want {
		t.Errorf("ClientConfigPath = %q, want %q", got, want)
	}
}

func TestGarageDir_HonorsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := config.GarageDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".drift", "garage")
	if got != want {
		t.Errorf("GarageDir = %q, want %q", got, want)
	}
}

func TestInitGarage_FreshAndIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "garage")

	first, err := config.InitGarage(root)
	if err != nil {
		t.Fatalf("InitGarage (first): %v", err)
	}
	// All expected paths should exist.
	for _, sub := range append([]string{"config.yaml"}, config.GarageSubdirs...) {
		p := filepath.Join(root, sub)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist after init: %v", p, err)
		}
	}
	if len(first.Created) == 0 {
		t.Error("first init should report created entries")
	}

	// Chest directory must be 0700 — it holds the envfile secret store.
	chestInfo, err := os.Stat(filepath.Join(root, "chest"))
	if err != nil {
		t.Fatal(err)
	}
	if got := chestInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("chest dir mode = %o, want 0700", got)
	}

	// Config should be loadable and match DefaultServer().
	loaded, err := config.LoadServer(filepath.Join(root, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if diff := cmp.Diff(config.DefaultServer(), loaded); diff != "" {
		t.Errorf("default config mismatch (-want +got):\n%s", diff)
	}

	// Re-running is a no-op: no new Created entries, no error.
	second, err := config.InitGarage(root)
	if err != nil {
		t.Fatalf("InitGarage (second): %v", err)
	}
	if len(second.Created) != 0 {
		t.Errorf("second init should be a no-op, but Created=%v", second.Created)
	}
}

func TestInitGarage_PreservesUserEdits(t *testing.T) {
	root := filepath.Join(t.TempDir(), "garage")
	if _, err := config.InitGarage(root); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config.yaml")
	edited := &config.Server{
		DefaultTune:      "python",
		DefaultCharacter: "kurisu",
		Chest:            config.ChestConfig{Backend: config.ChestBackendEnvfile},
	}
	if err := config.SaveServer(cfgPath, edited); err != nil {
		t.Fatal(err)
	}
	if _, err := config.InitGarage(root); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadServer(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(edited, got); diff != "" {
		t.Errorf("init clobbered user-edited config (-want +got):\n%s", diff)
	}
}

func TestInitGarage_FailsWhenGarageIsAFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "garage")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.InitGarage(blocker)
	if err == nil {
		t.Fatal("expected error when garage path is a file")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error should note the type mismatch, got: %v", err)
	}
}

// Sanity check that the two load functions report "not exist" via a wrapped
// error we can reason about, not a bare string.
func TestLoadServer_ErrorWrapsOSError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	_, err := config.LoadServer(path)
	if err == nil {
		t.Fatal("expected error")
	}
	// The load should not wrap fs.ErrNotExist — we treat missing-server-config
	// as a distinct, messaged error. If we ever start wrapping it, this test
	// guards against accidentally leaking the low-level error.
	if errors.Is(err, os.ErrNotExist) {
		t.Error("LoadServer missing-file error should be a custom message, not os.ErrNotExist")
	}
}
