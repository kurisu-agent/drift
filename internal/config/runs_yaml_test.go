package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/run"
)

func TestEnsureRunsYAML_seedsParsesToKnownEntries(t *testing.T) {
	home := t.TempDir()
	created, err := config.EnsureRunsYAML(home)
	if err != nil {
		t.Fatalf("EnsureRunsYAML: %v", err)
	}
	if !created {
		t.Fatalf("expected to create runs.yaml on a fresh dir")
	}
	buf, err := os.ReadFile(filepath.Join(home, "runs.yaml"))
	if err != nil {
		t.Fatalf("read runs.yaml: %v", err)
	}
	reg, err := run.Parse(buf)
	if err != nil {
		t.Fatalf("embedded runs.yaml failed to parse: %v", err)
	}
	// Spot-check built-ins. `ai` and `scaffolder` have moved to the
	// dedicated `drift ai` / `drift skill` commands, so the embedded
	// seed only carries the generic utilities now.
	for _, name := range []string{"ping", "uptime", "speedtest"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("built-in %q missing from embedded runs.yaml", name)
		}
	}
	if _, ok := reg.Get("ai"); ok {
		t.Errorf("built-in `ai` should have been removed (use drift ai instead)")
	}
	if _, ok := reg.Get("scaffolder"); ok {
		t.Errorf("built-in `scaffolder` should have been removed (use drift skill scaffolder)")
	}
	ping, _ := reg.Get("ping")
	if len(ping.Args) != 1 || ping.Args[0].Default != "1.1.1.1" {
		t.Errorf("ping.args = %+v, want one input with default 1.1.1.1", ping.Args)
	}
}

func TestEnsureRunsYAML_preservesUserEdits(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "runs.yaml")
	if err := os.WriteFile(path, []byte("runs: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := config.EnsureRunsYAML(home)
	if err != nil {
		t.Fatalf("EnsureRunsYAML: %v", err)
	}
	if created {
		t.Fatal("ensure should not have clobbered user file")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "runs: {}\n" {
		t.Errorf("user file was overwritten: %q", got)
	}
}
