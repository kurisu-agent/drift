package seed

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadBuiltin(t *testing.T) {
	tmpl, err := Load("claudeCode", "")
	if err != nil {
		t.Fatalf("Load claudeCode: %v", err)
	}
	if tmpl.Name != "claudeCode" {
		t.Fatalf("Name = %q, want claudeCode", tmpl.Name)
	}
	if len(tmpl.Files) != 2 {
		t.Fatalf("Files = %d, want 2", len(tmpl.Files))
	}
	// Confirm we can't mutate the registry through the returned pointer.
	tmpl.Files[0].Path = "mutated"
	again, _ := Load("claudeCode", "")
	if again.Files[0].Path == "mutated" {
		t.Fatalf("registry mutated through Load result")
	}
}

func TestLoadFromDisk(t *testing.T) {
	dir := t.TempDir()
	body := `files:
  - path: ~/notes.md
    content: |
      hello {{ .Kart }}
`
	if err := os.WriteFile(filepath.Join(dir, "myDocs.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tmpl, err := Load("myDocs", dir)
	if err != nil {
		t.Fatalf("Load myDocs: %v", err)
	}
	if tmpl.Name != "myDocs" {
		t.Fatalf("Name = %q", tmpl.Name)
	}
	if got := tmpl.Files[0].Path; got != "~/notes.md" {
		t.Fatalf("Path = %q", got)
	}
}

func TestLoadNotFound(t *testing.T) {
	_, err := Load("doesNotExist", t.TempDir())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestLoadValidatesDiskTemplate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("files: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("bad", dir); err == nil {
		t.Fatalf("expected validation error for empty files")
	}
}

func TestRender(t *testing.T) {
	tmpl := &Template{
		Name: "t",
		Files: []File{
			{Path: "~/a/{{ .Kart }}.txt", Content: "hi {{ .Workspace }}\n", BreakSymlinks: true},
			{Path: "~/b.json", Content: `{"image":"{{ .Image }}"}`, OnConflict: ConflictSkip},
		},
	}
	files, err := Render(tmpl, Vars{
		"Kart":      "foo",
		"Workspace": "/workspaces/foo",
		"Image":     "ubuntu",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("len = %d", len(files))
	}
	if files[0].Path != "~/a/foo.txt" {
		t.Errorf("file[0].Path = %q", files[0].Path)
	}
	if !strings.Contains(string(files[0].Content), "hi /workspaces/foo") {
		t.Errorf("file[0].Content = %q", files[0].Content)
	}
	if !files[0].BreakSymlinks {
		t.Errorf("file[0].BreakSymlinks lost")
	}
	if files[0].OnConflict != ConflictOverwrite {
		t.Errorf("file[0].OnConflict = %q, want %q (default)", files[0].OnConflict, ConflictOverwrite)
	}
	if files[1].OnConflict != ConflictSkip {
		t.Errorf("file[1].OnConflict = %q, want %q", files[1].OnConflict, ConflictSkip)
	}
	if string(files[1].Content) != `{"image":"ubuntu"}` {
		t.Errorf("file[1].Content = %q", files[1].Content)
	}
}

func TestRenderMergeRequiresStructuredExt(t *testing.T) {
	tmpl := &Template{
		Name: "t",
		Files: []File{
			{Path: "~/notes.txt", Content: "hi", OnConflict: ConflictMerge},
		},
	}
	if _, err := Render(tmpl, Vars{}); err == nil {
		t.Fatalf("expected merge to reject non-json/yaml extension")
	}
}

func TestValidateRejectsUnknownConflictMode(t *testing.T) {
	dir := t.TempDir()
	body := `files:
  - path: ~/x.json
    content: "{}"
    on_conflict: nopes
`
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("bad", dir); err == nil {
		t.Fatalf("expected unknown on_conflict to fail validation")
	}
}

func TestRenderMissingVarIsZero(t *testing.T) {
	tmpl := &Template{Files: []File{{Path: "~/x", Content: "img={{ .Image }}\n"}}}
	files, err := Render(tmpl, Vars{}) // no Image
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := string(files[0].Content); got != "img=\n" {
		t.Errorf("Content = %q", got)
	}
}

func TestIsBuiltin(t *testing.T) {
	if !IsBuiltin("claudeCode") {
		t.Error("IsBuiltin(claudeCode) = false")
	}
	if IsBuiltin("nope") {
		t.Error("IsBuiltin(nope) = true")
	}
}
