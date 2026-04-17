package kart

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/rpcerr"
)

func TestNormalizeDevcontainerEmpty(t *testing.T) {
	p, cleanup, err := NormalizeDevcontainer(context.Background(), "", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if p != "" {
		t.Fatalf("expected empty path, got %q", p)
	}
}

func TestNormalizeDevcontainerFilePath(t *testing.T) {
	p, cleanup, err := NormalizeDevcontainer(context.Background(), "/etc/passwd", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if p != "/etc/passwd" {
		t.Fatalf("expected passthrough, got %q", p)
	}
}

func TestNormalizeDevcontainerJSON(t *testing.T) {
	dir := t.TempDir()
	p, cleanup, err := NormalizeDevcontainer(context.Background(), `{"image":"ubuntu"}`, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if filepath.Dir(p) != dir {
		t.Fatalf("expected file under %s, got %s", dir, p)
	}
	buf, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf), "ubuntu") {
		t.Fatalf("unexpected body: %s", buf)
	}
}

func TestNormalizeDevcontainerJSONInvalid(t *testing.T) {
	_, _, err := NormalizeDevcontainer(context.Background(), `{broken`, t.TempDir(), nil)
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeInvalidFlag {
		t.Fatalf("expected invalid_flag, got %v", err)
	}
}

func TestNormalizeDevcontainerURL(t *testing.T) {
	dir := t.TempDir()
	fake := DevcontainerFetcher(func(ctx context.Context, url string) ([]byte, error) {
		if url != "https://example.com/dc.json" {
			t.Fatalf("unexpected URL %q", url)
		}
		return []byte(`{"image":"debian"}`), nil
	})
	p, cleanup, err := NormalizeDevcontainer(context.Background(), "https://example.com/dc.json", dir, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	buf, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf), "debian") {
		t.Fatalf("unexpected body: %s", buf)
	}
}
