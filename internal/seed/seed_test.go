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
	// CLAUDE.md, .claude.json, .claude/settings.json, hooks/block-literals.sh,
	// deny-literals.txt — see plan 20 for the latter two.
	if len(tmpl.Files) != 5 {
		t.Fatalf("Files = %d, want 5", len(tmpl.Files))
	}
	settings := tmpl.Files[2]
	if settings.Path != "~/.claude/settings.json" || settings.OnConflict != "" {
		t.Errorf("settings.json entry = %+v, want path=~/.claude/settings.json on_conflict=overwrite (zero)", settings)
	}
	if !strings.Contains(settings.Content, "nix-env-claude-status") {
		t.Errorf("settings.json must set statusLine.command to 'nix-env-claude-status'; got %q", settings.Content)
	}
	if !strings.Contains(settings.Content, `"effortLevel": "xhigh"`) {
		t.Errorf("settings.json must persist effortLevel=xhigh (max-effort that survives sessions); got %q", settings.Content)
	}
	if !strings.Contains(settings.Content, `"alwaysThinkingEnabled": true`) {
		t.Errorf("settings.json must enable alwaysThinkingEnabled; got %q", settings.Content)
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

// TestKartInfoBuiltinSchema locks the nested layout consumed by the
// flake-side topbar / claude-statusline. The same shape is rendered in
// every kart so flake consumers can `jq` a stable path; a regression
// to flat keys would silently break every running kart's identity row.
func TestKartInfoBuiltinSchema(t *testing.T) {
	tmpl, err := Load("kartInfo", "")
	if err != nil {
		t.Fatalf("Load kartInfo: %v", err)
	}
	if len(tmpl.Files) != 1 {
		t.Fatalf("kartInfo files = %d, want 1", len(tmpl.Files))
	}
	files, err := Render(tmpl, Vars{
		"Kart":                 "demo",
		"Character":            "alice",
		"CharacterDisplayName": "Alice the Great",
		"CharacterIcon":        "★",
		"CharacterColor":       "mauve",
		"Circuit":              "den",
		"Icon":                 "",
		"Color":                "green",
		"Timezone":             "UTC",
	})
	if err != nil {
		t.Fatalf("Render kartInfo: %v", err)
	}
	body := string(files[0].Content)
	for _, want := range []string{
		`"name": "demo"`,         // kart.name
		`"display_name": "Alice`, // character.display_name
		`"icon": "★"`,            // character.icon
		`"color": "mauve"`,       // character.color
		`"name": "den"`,          // circuit.name
		`"timezone": "UTC"`,      // top-level
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered info.json missing %q; got:\n%s", want, body)
		}
	}
	// The nested structure itself: kart/character/circuit must be
	// objects, not bare strings, so flake jq paths like .character.icon
	// don't hit a string-indexing error.
	for _, prefix := range []string{`"kart": {`, `"character": {`, `"circuit": {`} {
		if !strings.Contains(body, prefix) {
			t.Errorf("rendered info.json missing nested %q; got:\n%s", prefix, body)
		}
	}
}

func TestIsBuiltin(t *testing.T) {
	if !IsBuiltin("claudeCode") {
		t.Error("IsBuiltin(claudeCode) = false")
	}
	if !IsBuiltin("driftShell") {
		t.Error("IsBuiltin(driftShell) = false")
	}
	if IsBuiltin("nope") {
		t.Error("IsBuiltin(nope) = true")
	}
}

func TestClaudeCodeMD_GHAuthBlock_ConditionalOnHasPAT(t *testing.T) {
	tmpl, err := Load("claudeCode", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	withPAT, err := Render(tmpl, Vars{
		"Image":            "node:22",
		"DevcontainerPath": "/workspaces/k/.devcontainer/devcontainer.json",
		"Character":        "alice",
		"HasPAT":           "true",
	})
	if err != nil {
		t.Fatalf("Render with PAT: %v", err)
	}
	got := string(withPAT[0].Content)
	for _, want := range []string{"gh pr create", "credential helper", "character `alice`"} {
		if !strings.Contains(got, want) {
			t.Errorf("CLAUDE.md with PAT missing %q\ngot:\n%s", want, got)
		}
	}

	withoutPAT, err := Render(tmpl, Vars{
		"Image":            "node:22",
		"DevcontainerPath": "/workspaces/k/.devcontainer/devcontainer.json",
		"Character":        "alice",
		// HasPAT deliberately empty.
	})
	if err != nil {
		t.Fatalf("Render without PAT: %v", err)
	}
	got = string(withoutPAT[0].Content)
	for _, forbid := range []string{"gh pr create", "credential helper"} {
		if strings.Contains(got, forbid) {
			t.Errorf("CLAUDE.md without PAT must not advertise gh auth (%q)\ngot:\n%s", forbid, got)
		}
	}
}

func TestDriftShellBuiltin(t *testing.T) {
	tmpl, err := Load("driftShell", "")
	if err != nil {
		t.Fatalf("Load driftShell: %v", err)
	}
	if len(tmpl.Files) != 1 {
		t.Fatalf("Files = %d, want 1 (.bashrc stub only — zsh ships ZDOTDIR via the flake)", len(tmpl.Files))
	}
	bashrc := tmpl.Files[0]
	if bashrc.Path != "~/.bashrc" || bashrc.OnConflict != ConflictAppend {
		t.Errorf("bashrc = %+v, want path=~/.bashrc, on_conflict=append", bashrc)
	}
	// The stub must source the flake-provided bootstrap; without that line the
	// seed becomes a no-op and the kart loses zellij auto-attach + zsh fallback.
	if !strings.Contains(bashrc.Content, ".nix-profile/share/nix-env/bashrc-bootstrap") {
		t.Errorf("bashrc stub missing source line for flake bootstrap; got %q", bashrc.Content)
	}
	// Stub must keep its `[ -f … ] &&` guard so a kart without the flake
	// installed still gets a clean ~/.bashrc instead of a "file not found" error.
	if !strings.Contains(bashrc.Content, "[ -f ") {
		t.Errorf("bashrc stub lost its file-exists guard; got %q", bashrc.Content)
	}
}

// TestClaudeCodeBlockLiteralsHook verifies plan 20: every kart with the
// claudeCode seed gets the PreToolUse hook script + deny-list file
// installed, and settings.json wires the hook unconditionally.
func TestClaudeCodeBlockLiteralsHook(t *testing.T) {
	tmpl, err := Load("claudeCode", "")
	if err != nil {
		t.Fatalf("Load claudeCode: %v", err)
	}

	var hook, deny *File
	for i := range tmpl.Files {
		switch tmpl.Files[i].Path {
		case "~/.claude/hooks/block-literals.sh":
			hook = &tmpl.Files[i]
		case "~/.claude/deny-literals.txt":
			deny = &tmpl.Files[i]
		}
	}
	if hook == nil {
		t.Fatalf("claudeCode missing block-literals.sh entry")
	}
	if deny == nil {
		t.Fatalf("claudeCode missing deny-literals.txt entry")
	}
	for _, want := range []string{
		"PreToolUse",
		"deny-literals.txt",
		"permissionDecision",
		"grep -F -i",
		"jq -r",
	} {
		if !strings.Contains(hook.Content, want) {
			t.Errorf("hook script missing %q", want)
		}
	}
	if deny.Content != "{{ .DenyLiterals }}" {
		t.Errorf("deny-literals.txt template = %q, want %q", deny.Content, "{{ .DenyLiterals }}")
	}

	// settings.json must always wire the PreToolUse hook (empty list = no-op).
	var settings *File
	for i := range tmpl.Files {
		if tmpl.Files[i].Path == "~/.claude/settings.json" {
			settings = &tmpl.Files[i]
		}
	}
	if settings == nil {
		t.Fatalf("settings.json entry missing")
	}
	for _, want := range []string{
		`"hooks":`,
		`"PreToolUse"`,
		`"matcher": "Bash|Edit|Write|MultiEdit"`,
		`bash $HOME/.claude/hooks/block-literals.sh`,
	} {
		if !strings.Contains(settings.Content, want) {
			t.Errorf("settings.json missing %q", want)
		}
	}
}

// TestBlockLiteralsRenderEmptyAndPopulated verifies the deny-list file
// renders empty when DenyLiterals is unset and carries the literal
// content (verbatim) when set.
func TestBlockLiteralsRenderEmptyAndPopulated(t *testing.T) {
	tmpl, err := Load("claudeCode", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	empty, err := Render(tmpl, Vars{})
	if err != nil {
		t.Fatalf("Render empty: %v", err)
	}
	for _, f := range empty {
		if f.Path == "~/.claude/deny-literals.txt" && len(f.Content) != 0 {
			t.Errorf("deny-literals.txt with no DenyLiterals var = %q, want empty", f.Content)
		}
	}

	want := "alpha\nbeta gamma\n# comment\n"
	populated, err := Render(tmpl, Vars{"DenyLiterals": want})
	if err != nil {
		t.Fatalf("Render populated: %v", err)
	}
	got := ""
	for _, f := range populated {
		if f.Path == "~/.claude/deny-literals.txt" {
			got = string(f.Content)
		}
	}
	if got != want {
		t.Errorf("deny-literals.txt content = %q, want %q", got, want)
	}
}
