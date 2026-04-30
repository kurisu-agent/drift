package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/wire"
)

// writeSkill seeds a skill directory with SKILL.md content. Separated
// out so each test can build up its fixture tree without boilerplate.
func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

// TestSkillList_parsesFrontmatterAndSorts: the happy path — two well-
// formed skills come back in name order with their frontmatter name
// and description surfaced. This is what the client renders in the
// picker and `drift skill` table.
func TestSkillList_parsesFrontmatterAndSorts(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "security-review", "---\nname: security-review\ndescription: Audit pending changes.\n---\n\n# body\n")
	writeSkill(t, root, "scaffolder", "---\nname: scaffolder\ndescription: Scaffold a new kart.\n---\n\nbody")

	d := &server.Deps{SkillsDir: root}
	res, err := d.SkillListHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("SkillListHandler: %v", err)
	}
	out, ok := res.(wire.SkillListResult)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	if len(out.Skills) != 2 {
		t.Fatalf("skills = %d, want 2 (%+v)", len(out.Skills), out.Skills)
	}
	if out.Skills[0].Name != "scaffolder" || out.Skills[1].Name != "security-review" {
		t.Errorf("not sorted by name: %+v", out.Skills)
	}
	if out.Skills[0].Description != "Scaffold a new kart." {
		t.Errorf("description = %q", out.Skills[0].Description)
	}
}

// TestSkillList_missingDirReturnsEmpty: a circuit that has never run
// claude won't have ~/.claude/skills yet. List must succeed with zero
// entries so the client can render its "no skills configured" hint.
func TestSkillList_missingDirReturnsEmpty(t *testing.T) {
	d := &server.Deps{SkillsDir: filepath.Join(t.TempDir(), "does-not-exist")}
	res, err := d.SkillListHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("SkillListHandler: %v", err)
	}
	out := res.(wire.SkillListResult)
	if len(out.Skills) != 0 {
		t.Errorf("skills = %d, want 0", len(out.Skills))
	}
}

// TestSkillList_fallsBackToDirName: a skill whose SKILL.md has no
// frontmatter `name` field still appears in the list, keyed by its
// directory name. Drops rather than surfaces an error so one broken
// SKILL.md cannot hide the others.
func TestSkillList_fallsBackToDirName(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "bare", "no frontmatter here\n")
	writeSkill(t, root, "with-desc", "---\ndescription: desc only\n---\nbody\n")

	d := &server.Deps{SkillsDir: root}
	res, _ := d.SkillListHandler(context.Background(), nil)
	out := res.(wire.SkillListResult)
	if len(out.Skills) != 2 {
		t.Fatalf("skills = %+v", out.Skills)
	}
	if out.Skills[0].Name != "bare" || out.Skills[1].Name != "with-desc" {
		t.Errorf("names = %+v", out.Skills)
	}
	if out.Skills[1].Description != "desc only" {
		t.Errorf("description = %q", out.Skills[1].Description)
	}
}

// TestSkillList_skipsNonDirAndMissingSkillMd: stray files at the root
// level and directories without SKILL.md must not surface as skills.
func TestSkillList_skipsNonDirAndMissingSkillMd(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "real", "---\nname: real\n---\n")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "half-baked"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := &server.Deps{SkillsDir: root}
	res, _ := d.SkillListHandler(context.Background(), nil)
	out := res.(wire.SkillListResult)
	if len(out.Skills) != 1 || out.Skills[0].Name != "real" {
		t.Errorf("skills = %+v, want [real]", out.Skills)
	}
}

// TestSkillResolve_rendersCommandWithPrefix: the resolved command must
// cd to ~/.drift, clear the handoff sentinel, and exec claude with an
// auto-prefixed prompt that names the skill. Locks the shape in so a
// refactor of the prefix wording breaks this test before it breaks
// users.
func TestSkillResolve_rendersCommandWithPrefix(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "scaffolder", "---\nname: scaffolder\n---\n")

	d := &server.Deps{SkillsDir: root}
	raw, _ := json.Marshal(wire.SkillResolveParams{Name: "scaffolder", Prompt: "build me a go service"})
	res, err := d.SkillResolveHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("SkillResolveHandler: %v", err)
	}
	rr := res.(wire.SkillResolveResult)
	want := `cd "$HOME/.drift" && rm -f last-scaffold && exec claude --dangerously-skip-permissions 'Use the scaffolder skill. build me a go service'`
	if rr.Command != want {
		t.Errorf("Command = %q\nwant     = %q", rr.Command, want)
	}
	if rr.Post != wire.RunPostConnectLastScaffold {
		t.Errorf("Post = %q, want %q", rr.Post, wire.RunPostConnectLastScaffold)
	}
}

// TestSkillResolve_emptyPromptOmitsTrailer: no user prompt → the
// command carries just the skill-pick imperative. Lets users jump into
// a skill's default dialog (scaffolder asks the user what to build)
// without having to pre-type a prompt.
func TestSkillResolve_emptyPromptOmitsTrailer(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "scaffolder", "---\nname: scaffolder\n---\n")

	d := &server.Deps{SkillsDir: root}
	raw, _ := json.Marshal(wire.SkillResolveParams{Name: "scaffolder"})
	res, err := d.SkillResolveHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("SkillResolveHandler: %v", err)
	}
	rr := res.(wire.SkillResolveResult)
	want := `cd "$HOME/.drift" && rm -f last-scaffold && exec claude --dangerously-skip-permissions 'Use the scaffolder skill.'`
	if rr.Command != want {
		t.Errorf("Command = %q\nwant     = %q", rr.Command, want)
	}
}

// TestSkillResolve_quotesShellMetacharacters: the prompt is single-
// quoted with POSIX rules; embedded single quotes must be escaped via
// the `'\”` dance so the remote shell doesn't split on them.
func TestSkillResolve_quotesShellMetacharacters(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "review", "---\nname: review\n---\n")

	d := &server.Deps{SkillsDir: root}
	raw, _ := json.Marshal(wire.SkillResolveParams{Name: "review", Prompt: "it's broken"})
	res, err := d.SkillResolveHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("SkillResolveHandler: %v", err)
	}
	rr := res.(wire.SkillResolveResult)
	want := `cd "$HOME/.drift" && rm -f last-scaffold && exec claude --dangerously-skip-permissions 'Use the review skill. it'\''s broken'`
	if rr.Command != want {
		t.Errorf("Command = %q\nwant     = %q", rr.Command, want)
	}
}

// TestSkillResolve_notFound: an unknown skill name must produce a
// typed not-found error so the client can surface a user-readable
// message rather than a generic internal_error.
func TestSkillResolve_notFound(t *testing.T) {
	d := &server.Deps{SkillsDir: t.TempDir()}
	raw, _ := json.Marshal(wire.SkillResolveParams{Name: "ghost"})
	_, err := d.SkillResolveHandler(context.Background(), raw)
	var re *rpcerr.Error
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *rpcerr.Error", err)
	}
	if re.Code != rpcerr.CodeNotFound {
		t.Errorf("Code = %d, want %d", re.Code, rpcerr.CodeNotFound)
	}
}

// TestSkillResolve_requiresName: an empty name must fail validation
// before the filesystem lookup, mirroring run.resolve's contract.
func TestSkillResolve_requiresName(t *testing.T) {
	d := &server.Deps{SkillsDir: t.TempDir()}
	raw, _ := json.Marshal(wire.SkillResolveParams{})
	_, err := d.SkillResolveHandler(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}
