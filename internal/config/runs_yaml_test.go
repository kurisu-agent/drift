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
	// Spot-check built-ins the scaffolder feature depends on.
	for _, name := range []string{"ai", "scaffolder", "ping", "uptime"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("built-in %q missing from embedded runs.yaml", name)
		}
	}
	scaf, _ := reg.Get("scaffolder")
	if scaf.Post != run.PostConnectLastScaffold {
		t.Errorf("scaffolder.post = %q, want connect-last-scaffold", scaf.Post)
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

func TestEnsureScaffolderRecipe_seedsInRecipesDir(t *testing.T) {
	home := t.TempDir()
	created, err := config.EnsureScaffolderRecipe(home)
	if err != nil {
		t.Fatalf("EnsureScaffolderRecipe: %v", err)
	}
	if !created {
		t.Fatal("expected seed on fresh dir")
	}
	if _, err := os.Stat(filepath.Join(home, "recipes", "scaffolder.md")); err != nil {
		t.Errorf("recipes/scaffolder.md not created: %v", err)
	}
}
